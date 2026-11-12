// Package contentaudit records Guard classifications of relay prompts in shadow
// mode: requests are only observed, never delayed by a Guard call or blocked.
//
// The relay entry copies a reference to the request body into a bounded
// in-process queue; a small fixed worker pool extracts the prompt text,
// classifies it with the configured Guard endpoints and writes only warn and
// block findings, in batches. Prompt text stays in memory and is never stored.
// Queued items are lost when the process restarts.
package contentaudit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/content_audit_setting"
	"github.com/gin-gonic/gin"
)

const (
	eventBatchSize     = 50
	eventFlushInterval = time.Second
	eventBufferSize    = 1024
	eventWriteAttempts = 3
	cleanupInterval    = 24 * time.Hour
	cleanupFirstDelay  = 5 * time.Minute
	maxEndpointRunes   = 128
)

type job struct {
	body   []byte
	format types.RelayFormat
	path   string
	event  model.ContentAuditEvent
}

type workerPool struct {
	queue    chan *job
	bytes    atomic.Int64
	maxBytes atomic.Int64
	workers  int
	stop     chan struct{}
}

func (p *workerPool) release(item *job) {
	p.bytes.Add(-int64(len(item.body)))
}

type auditor struct {
	pool         atomic.Pointer[workerPool]
	reconfigure  sync.Mutex
	counters     counters
	entryLatency latencyWindow
	guardLatency latencyWindow
	guard        guardClient
	events       chan *model.ContentAuditEvent
	writerOnce   sync.Once
	startOnce    sync.Once
	startedAt    atomic.Int64
}

var defaultAuditor = &auditor{events: make(chan *model.ContentAuditEvent, eventBufferSize)}

// Start applies the current configuration and follows later changes. The
// retention cleanup runs on the master node only.
func Start() {
	defaultAuditor.startOnce.Do(func() {
		content_audit_setting.OnChange(defaultAuditor.apply)
		defaultAuditor.apply(content_audit_setting.Current())
		if common.IsMasterNode {
			go defaultAuditor.cleanupLoop()
		}
	})
}

// CheckRelay is the relay entry. In shadow mode it only enqueues the request
// for asynchronous auditing and always returns nil; a later blocking mode
// returns its decision here, before any quota is reserved. When auditing is
// off it returns before touching the request.
func CheckRelay(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	snapshot := content_audit_setting.Current()
	if !snapshot.Config.Enabled || info == nil {
		return nil
	}
	start := time.Now()
	defaultAuditor.observe(c, info, snapshot)
	defaultAuditor.entryLatency.record(time.Since(start))
	return nil
}

func (a *auditor) observe(c *gin.Context, info *relaycommon.RelayInfo, snapshot *content_audit_setting.Snapshot) {
	defer func() {
		if recovered := recover(); recovered != nil {
			common.SysError(common.LogText("content audit entry panic: %v", recovered))
		}
	}()
	path := c.Request.URL.Path
	if !IsAuditedFormat(info.RelayFormat) || info.RelayFormat == types.RelayFormatGemini && strings.Contains(path, "embed") {
		a.counters.skippedUnsupportedFormat.Add(1)
		return
	}
	if !snapshot.Config.GroupInScope(info.UsingGroup) {
		a.counters.skippedOutOfScope.Add(1)
		return
	}
	pool := a.pool.Load()
	if pool == nil {
		a.counters.droppedQueueFull.Add(1)
		return
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		a.counters.skippedInvalidJSON.Add(1)
		return
	}
	size := storage.Size()
	if size > int64(snapshot.Config.MaxBodyMB)<<20 {
		a.counters.skippedBodyTooLarge.Add(1)
		return
	}
	if pool.bytes.Add(size) > pool.maxBytes.Load() {
		pool.bytes.Add(-size)
		a.counters.droppedQueueBytes.Add(1)
		return
	}
	// Memory-backed bodies share their immutable buffer, so this does not copy.
	body, err := storage.Bytes()
	if err != nil {
		pool.bytes.Add(-size)
		a.counters.skippedInvalidJSON.Add(1)
		return
	}
	pool.bytes.Add(int64(len(body)) - size)
	createdAt := info.StartTime.Unix()
	if info.StartTime.IsZero() {
		createdAt = time.Now().Unix()
	}
	item := &job{
		body:   body,
		format: info.RelayFormat,
		path:   path,
		event: model.ContentAuditEvent{
			CreatedAt:   createdAt,
			RequestId:   c.GetString(common.RequestIdKey),
			UserId:      info.UserId,
			Username:    c.GetString(string(constant.ContextKeyUserName)),
			TokenId:     info.TokenId,
			TokenName:   c.GetString("token_name"),
			UsingGroup:  info.UsingGroup,
			ChannelId:   common.GetContextKeyInt(c, constant.ContextKeyChannelId),
			RelayFormat: string(info.RelayFormat),
			Endpoint:    trimRunes(path, maxEndpointRunes),
			ModelName:   info.OriginModelName,
		},
	}
	select {
	case pool.queue <- item:
		a.counters.enqueued.Add(1)
	default:
		pool.release(item)
		a.counters.droppedQueueFull.Add(1)
	}
}

// apply resizes the worker pool for a new snapshot. A replaced pool finishes
// the items already queued and then exits.
func (a *auditor) apply(snapshot *content_audit_setting.Snapshot) {
	a.reconfigure.Lock()
	defer a.reconfigure.Unlock()
	cfg := snapshot.Config
	current := a.pool.Load()
	if !cfg.Enabled {
		if current != nil {
			a.pool.Store(nil)
			close(current.stop)
		}
		return
	}
	a.writerOnce.Do(func() { go a.writeEvents() })
	maxBytes := int64(cfg.QueueMaxMB) << 20
	if current != nil && current.workers == cfg.WorkerCount && cap(current.queue) == cfg.QueueCapacity {
		current.maxBytes.Store(maxBytes)
		return
	}
	next := &workerPool{
		queue:   make(chan *job, cfg.QueueCapacity),
		workers: cfg.WorkerCount,
		stop:    make(chan struct{}),
	}
	next.maxBytes.Store(maxBytes)
	for range next.workers {
		go a.work(next)
	}
	a.pool.Store(next)
	if current == nil {
		a.startedAt.Store(time.Now().Unix())
	} else {
		close(current.stop)
	}
}

func (a *auditor) work(pool *workerPool) {
	for {
		select {
		case item := <-pool.queue:
			pool.release(item)
			a.process(item)
		case <-pool.stop:
			for {
				select {
				case item := <-pool.queue:
					pool.release(item)
					a.process(item)
				default:
					return
				}
			}
		}
	}
}

func (a *auditor) process(item *job) {
	defer func() {
		if recovered := recover(); recovered != nil {
			a.counters.failed.Add(1)
			common.SysError(common.LogText("content audit worker panic: %v", recovered))
		}
	}()
	snapshot := content_audit_setting.Current()
	prompt, err := extractPromptSnapshot(item.format, item.path, item.body)
	item.body = nil
	if errors.Is(err, errNoText) {
		a.counters.skippedNoText.Add(1)
		return
	}
	if err != nil {
		a.counters.skippedInvalidJSON.Add(1)
		return
	}
	if containsCredentialPattern(prompt.ScanText) {
		a.counters.credentialPatternHits.Add(1)
	}
	endpoints := resolveEndpoints(snapshot)
	if len(endpoints) == 0 {
		a.counters.failed.Add(1)
		return
	}
	chunks := splitChunks(prompt.ScanText, snapshot.Config.MaxChunkRunes)
	if len(chunks) > maxChunksPerRequest {
		chunks = chunks[:maxChunksPerRequest]
	}
	started := time.Now()
	results := make([]*guardResult, 0, len(chunks))
	for _, chunk := range chunks {
		result, err := a.guard.scanChunk(context.Background(), endpoints, chunk, snapshot.Config.Scanners, &a.counters)
		if err != nil {
			a.guardLatency.record(time.Since(started))
			a.counters.failed.Add(1)
			return
		}
		results = append(results, result)
		if result.Decision == model.ContentAuditDecisionBlock {
			break
		}
	}
	latency := time.Since(started)
	a.guardLatency.record(latency)
	a.counters.scanned.Add(1)
	verdict := aggregateResults(results)
	switch verdict.Decision {
	case model.ContentAuditDecisionWarn:
		a.counters.warned.Add(1)
	case model.ContentAuditDecisionBlock:
		a.counters.blocked.Add(1)
	default:
		a.counters.allowed.Add(1)
		return
	}
	event := item.event
	event.Decision = verdict.Decision
	event.Safety = verdict.Safety
	event.RiskLevel = verdict.RiskLevel
	event.Score = verdict.Score
	event.Categories = verdict.Categories
	event.MatchedScanners = verdict.MatchedScanners
	event.UnknownCategories = verdict.UnknownCategories
	event.GuardEndpointId = verdict.EndpointID
	event.ScannerVersion = verdict.ScannerVersion
	event.ConfigVersion = snapshot.Config.ConfigVersion
	event.ChunkTotal = len(chunks)
	event.LatencyMs = int(latency.Milliseconds())
	event.PromptHash = prompt.Hash
	event.RedactedPreview = prompt.Preview
	event.PromptLength = prompt.PromptLength
	event.MessageCount = prompt.MessageCount
	select {
	case a.events <- &event:
	default:
		a.counters.eventWriteFailed.Add(1)
	}
}

// writeEvents stores findings in batches of eventBatchSize or once per
// eventFlushInterval, whichever comes first, to keep database writes short.
func (a *auditor) writeEvents() {
	ticker := time.NewTicker(eventFlushInterval)
	defer ticker.Stop()
	batch := make([]*model.ContentAuditEvent, 0, eventBatchSize)
	for {
		select {
		case event := <-a.events:
			batch = append(batch, event)
			if len(batch) < eventBatchSize {
				continue
			}
		case <-ticker.C:
			if len(batch) == 0 {
				continue
			}
		}
		a.flush(batch)
		batch = make([]*model.ContentAuditEvent, 0, eventBatchSize)
	}
}

func (a *auditor) flush(batch []*model.ContentAuditEvent) {
	var err error
	for attempt := range eventWriteAttempts {
		if attempt > 0 {
			time.Sleep(time.Duration(200<<attempt) * time.Millisecond)
		}
		if err = model.CreateContentAuditEvents(batch); err == nil {
			a.counters.eventsWritten.Add(int64(len(batch)))
			return
		}
	}
	a.counters.eventWriteFailed.Add(int64(len(batch)))
	common.SysError(common.LogText("content audit event write failed (%d events): %v", len(batch), err))
}

func (a *auditor) cleanupLoop() {
	time.Sleep(cleanupFirstDelay)
	for {
		retentionDays := content_audit_setting.Current().Config.RetentionDays
		cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
		deleted, err := model.DeleteContentAuditEventsBefore(cutoff)
		if err != nil {
			common.SysError(common.LogText("content audit event cleanup failed: %v", err))
		} else if deleted > 0 {
			common.SysLog(common.LogText("content audit event cleanup removed %d events", deleted))
		}
		time.Sleep(cleanupInterval)
	}
}

type Runtime struct {
	Enabled        bool            `json:"enabled"`
	ConfigVersion  int64           `json:"config_version"`
	LoadError      string          `json:"load_error,omitempty"`
	Workers        int             `json:"workers"`
	StartedAt      int64           `json:"started_at"`
	QueueDepth     int             `json:"queue_depth"`
	QueueCapacity  int             `json:"queue_capacity"`
	QueueBytes     int64           `json:"queue_bytes"`
	QueueMaxBytes  int64           `json:"queue_max_bytes"`
	Counters       CounterSnapshot `json:"counters"`
	GuardLatencyMs LatencySummary  `json:"guard_latency_ms"`
	EntryLatencyUs LatencySummary  `json:"entry_latency_us"`
}

func GetRuntime() Runtime {
	snapshot := content_audit_setting.Current()
	runtime := Runtime{
		Enabled:        snapshot.Config.Enabled,
		ConfigVersion:  snapshot.Config.ConfigVersion,
		LoadError:      snapshot.LoadError,
		StartedAt:      defaultAuditor.startedAt.Load(),
		Counters:       defaultAuditor.counters.snapshot(),
		GuardLatencyMs: defaultAuditor.guardLatency.summary(time.Millisecond),
		EntryLatencyUs: defaultAuditor.entryLatency.summary(time.Microsecond),
	}
	if pool := defaultAuditor.pool.Load(); pool != nil {
		runtime.Workers = pool.workers
		runtime.QueueDepth = len(pool.queue)
		runtime.QueueCapacity = cap(pool.queue)
		runtime.QueueBytes = max(pool.bytes.Load(), 0)
		runtime.QueueMaxBytes = pool.maxBytes.Load()
	}
	return runtime
}

type ProbeResult struct {
	OK          bool   `json:"ok"`
	LatencyMs   int64  `json:"latency_ms"`
	ModelListed bool   `json:"model_listed"`
	Safety      string `json:"safety"`
	Error       string `json:"error"`
}

// Probe checks an endpoint: whether its model is listed and whether a short
// benign prompt gets a well-formed Guard verdict.
func Probe(ctx context.Context, endpoint content_audit_setting.Endpoint, token string) ProbeResult {
	timeout := time.Duration(content_audit_setting.Current().Config.TimeoutMS) * time.Millisecond
	target := guardEndpoint{Endpoint: endpoint, Token: token, Timeout: timeout}
	result := ProbeResult{}
	if body, err := defaultAuditor.guard.do(ctx, target, "GET", "/v1/models", nil); err == nil {
		var models struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if common.Unmarshal(body, &models) == nil {
			for _, item := range models.Data {
				if item.ID == endpoint.Model {
					result.ModelListed = true
				}
			}
		}
	}
	started := time.Now()
	verdict, err := defaultAuditor.guard.scan(ctx, target, "Hello", content_audit_setting.ScannerIDs)
	result.LatencyMs = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = errorCode(err)
		return result
	}
	result.OK = true
	result.Safety = verdict.Safety
	return result
}
