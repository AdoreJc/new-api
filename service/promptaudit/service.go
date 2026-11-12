// Package promptaudit records Guard-model findings for relay prompts.
//
// Phase one only records: EnqueueRelay copies the request body into a bounded
// in-memory queue and returns. Background workers extract the text, scan it
// with Qwen3Guard, and store flagged results. The queue lives only in memory,
// so pending items are lost on restart, and a full queue drops new items.
package promptaudit

import (
	"bytes"
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
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/gin-gonic/gin"
)

const (
	// maxBodyBytes skips larger requests; they are almost always media uploads.
	maxBodyBytes = 8 << 20
	// maxQueuedBytes bounds the memory held by queued request bodies.
	maxQueuedBytes = 64 << 20

	writeBatchSize     = 50
	writeFlushInterval = time.Second
	writeBufferSize    = 512
	cleanupInterval    = time.Hour
	cleanupEvery       = 24 * time.Hour
)

// Metrics are process-local counters reset on restart.
type Metrics struct {
	enqueued atomic.Int64
	dropped  atomic.Int64
	skipped  atomic.Int64
	tooLarge atomic.Int64
	noText   atomic.Int64
	// agentContextOnly counts requests whose in-scope user text was only
	// agent wrappers, so nothing was sent to the Guard.
	agentContextOnly atomic.Int64
	scanned          atomic.Int64
	passed           atomic.Int64
	flagged          atomic.Int64
	critical         atomic.Int64
	failed           atomic.Int64
	timeouts         atomic.Int64
	failovers        atomic.Int64
	retries          atomic.Int64
	recorded         atomic.Int64
	recordFailed     atomic.Int64
	latencyTotalMS   atomic.Int64
	latencyMaxMS     atomic.Int64
	// chunksCached counts chunks skipped because they were recently found safe;
	// chunksOverBudget counts chunks left unscanned by the per-request limit.
	chunksCached     atomic.Int64
	chunksOverBudget atomic.Int64
}

type job struct {
	protocol   string
	size       int64
	body       []byte
	event      model.PromptAuditEvent
	enqueuedAt time.Time
}

// Service owns the queue, workers and event writer.
type Service struct {
	queue       chan *job
	depth       atomic.Int64
	queuedBytes atomic.Int64
	active      atomic.Int64
	workerSlots atomic.Pointer[chan struct{}]
	writes      chan *model.PromptAuditEvent
	guard       guardClient
	metrics     Metrics
	startOnce   sync.Once
	lastCleanup atomic.Int64
	// lastFailureLog and suppressedFailures throttle scan failure logging so an
	// unreachable Guard does not flood the log.
	lastFailureLog     atomic.Int64
	suppressedFailures atomic.Int64
	// retryDelays are the waits before each retry of a retryable Guard failure.
	retryDelays []time.Duration
	now         func() time.Time
}

func newService() *Service {
	return &Service{
		queue:       make(chan *job, prompt_audit_setting.MaxQueueCapacity),
		writes:      make(chan *model.PromptAuditEvent, writeBufferSize),
		retryDelays: []time.Duration{200 * time.Millisecond, time.Second},
		now:         time.Now,
	}
}

var defaultService = newService()

// Start launches the workers, the event writer and the daily retention
// cleanup. Workers idle until auditing is enabled.
func Start() { defaultService.start(context.Background()) }

func (s *Service) start(ctx context.Context) {
	s.startOnce.Do(func() {
		for range prompt_audit_setting.MaxWorkerCount {
			go s.worker(ctx)
		}
		go s.writer(ctx)
		go s.cleanupLoop(ctx)
	})
}

// EnqueueRelay queues the request for auditing when prompt audit is on and the
// request is in scope. It never blocks, never fails the request, and does
// nothing beyond one atomic load when auditing is off.
func EnqueueRelay(c *gin.Context, info *relaycommon.RelayInfo) {
	defaultService.enqueueRelay(c, info)
}

func (s *Service) enqueueRelay(c *gin.Context, info *relaycommon.RelayInfo) {
	snapshot := prompt_audit_setting.Current()
	if !snapshot.Config.Enabled || info == nil {
		return
	}
	protocol, audited := relayProtocol(info.RelayFormat, info.RequestURLPath)
	group := info.UsingGroup
	inScope := snapshot.Config.IncludesGroup(group)
	// An "auto" token routes to a concrete group chosen by the distributor;
	// scope rules and the event use that group, and "auto" still matches.
	if group == "auto" {
		if selected := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selected != "" {
			group = selected
			inScope = inScope || snapshot.Config.IncludesGroup(selected)
		}
	}
	if !audited || !inScope {
		s.metrics.skipped.Add(1)
		return
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		s.metrics.skipped.Add(1)
		return
	}
	size := storage.Size()
	if size > maxBodyBytes {
		s.metrics.tooLarge.Add(1)
		return
	}
	// Reserve capacity first and roll back, so concurrent requests cannot
	// overshoot the item or byte limits.
	if s.depth.Add(1) > int64(snapshot.Config.QueueCapacity) {
		s.depth.Add(-1)
		s.metrics.dropped.Add(1)
		return
	}
	if s.queuedBytes.Add(size) > maxQueuedBytes {
		s.queuedBytes.Add(-size)
		s.depth.Add(-1)
		s.metrics.dropped.Add(1)
		return
	}
	raw, err := storage.Bytes()
	if err != nil {
		s.depth.Add(-1)
		s.queuedBytes.Add(-size)
		s.metrics.skipped.Add(1)
		return
	}
	// Disk-backed storage already returns a fresh copy; memory storage returns
	// the buffer the relay itself reads, which must not be shared.
	if !storage.IsDisk() {
		raw = bytes.Clone(raw)
	}
	endpoint, _, _ := strings.Cut(info.RequestURLPath, "?")
	item := &job{
		protocol: protocol,
		size:     size,
		body:     raw,
		event: model.PromptAuditEvent{
			RequestId:   c.GetString(common.RequestIdKey),
			UserId:      info.UserId,
			Username:    common.GetContextKeyString(c, constant.ContextKeyUserName),
			TokenId:     info.TokenId,
			TokenName:   c.GetString("token_name"),
			UsingGroup:  group,
			ChannelId:   common.GetContextKeyInt(c, constant.ContextKeyChannelId),
			RelayFormat: string(info.RelayFormat),
			Endpoint:    endpoint,
			ModelName:   info.OriginModelName,
			Mode:        prompt_audit_setting.ModeAsync,
		},
		enqueuedAt: s.now(),
	}
	select {
	case s.queue <- item:
		s.metrics.enqueued.Add(1)
	default:
		s.release(item)
		s.metrics.dropped.Add(1)
	}
}

func (s *Service) release(item *job) {
	s.depth.Add(-1)
	s.queuedBytes.Add(-item.size)
}

// relayProtocol maps a relay format to the extraction protocol. Formats that
// carry no free-form prompt text (embeddings, rerank, audio, realtime, search
// and tool calls) are exempt in this phase.
func relayProtocol(format types.RelayFormat, path string) (string, bool) {
	switch format {
	case types.RelayFormatClaude:
		return ProtocolClaude, true
	case types.RelayFormatGemini:
		if strings.Contains(path, "embed") {
			return "", false
		}
		return ProtocolGemini, true
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		return ProtocolResponses, true
	case types.RelayFormatOpenAIImage:
		return ProtocolImages, true
	case types.RelayFormatOpenAI:
		if strings.Contains(path, "/chat/completions") {
			return ProtocolChat, true
		}
		return ProtocolGeneric, true
	}
	return "", false
}

// slots returns the worker semaphore, resized when the configured count changes.
func (s *Service) slots(count int) chan struct{} {
	current := s.workerSlots.Load()
	if current != nil && cap(*current) == count {
		return *current
	}
	next := make(chan struct{}, count)
	if s.workerSlots.CompareAndSwap(current, &next) {
		return next
	}
	return *s.workerSlots.Load()
}

func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.queue:
			// Hold a slot of the semaphore that was current when the job started,
			// so a resize never over-releases the new one.
			snapshot := prompt_audit_setting.Current()
			slots := s.slots(snapshot.Config.WorkerCount)
			slots <- struct{}{}
			s.active.Add(1)
			s.process(ctx, item)
			s.active.Add(-1)
			<-slots
		}
	}
}

func (s *Service) process(ctx context.Context, item *job) {
	defer s.release(item)
	snapshot := prompt_audit_setting.Current()
	if !snapshot.Config.Enabled {
		s.metrics.skipped.Add(1)
		return
	}
	prompt, err := extractPromptSnapshot(item.protocol, item.body, snapshot.Config.ScanScope)
	if errors.Is(err, errAgentContextOnly) {
		s.metrics.agentContextOnly.Add(1)
		return
	}
	if err != nil {
		s.metrics.noText.Add(1)
		return
	}
	s.metrics.scanned.Add(1)
	// A failed scan can still return the findings of chunks scanned before
	// the failure; those are recorded and marked incomplete.
	result, err := s.guard.evaluate(ctx, snapshot, prompt.Latest, prompt.Rest, s.retryDelays, &s.metrics)
	if err != nil {
		s.metrics.failed.Add(1)
		var guardErr *guardError
		if errors.As(err, &guardErr) && guardErr.Timeout {
			s.metrics.timeouts.Add(1)
		}
		s.logScanFailure(item.event.RequestId, err)
		if result == nil {
			return
		}
	}
	s.metrics.latencyTotalMS.Add(int64(result.LatencyMS))
	for current := s.metrics.latencyMaxMS.Load(); int64(result.LatencyMS) > current &&
		!s.metrics.latencyMaxMS.CompareAndSwap(current, int64(result.LatencyMS)); current = s.metrics.latencyMaxMS.Load() {
	}
	switch result.Decision {
	case DecisionPass:
		s.metrics.passed.Add(1)
		return
	case DecisionCritical:
		s.metrics.critical.Add(1)
	default:
		s.metrics.flagged.Add(1)
	}
	event := item.event
	event.CreatedAt = s.now().Unix()
	event.Decision, event.RiskLevel, event.Action, event.Safety = result.Decision, result.RiskLevel, result.Action, result.Safety
	event.Categories = encodeList(result.Categories)
	event.MatchedScanners = encodeList(result.MatchedScanners)
	event.UnknownCategories = encodeList(result.UnknownCategories)
	event.GuardEndpointId, event.ScannerVersion = result.GuardEndpointID, result.ScannerVersion
	event.ConfigVersion = snapshot.Config.ConfigVersion
	event.ChunkTotal, event.ChunksScanned, event.LatencyMs = result.ChunkTotal, result.ChunksScanned, result.LatencyMS
	event.ScanIncomplete = result.Incomplete
	event.HitChunks = encodeHits(result.Hits)
	event.PromptHash, event.RedactedPreview = prompt.Hash, prompt.Preview
	event.PromptLength, event.MessageCount = prompt.Length, prompt.MessageCount
	if snapshot.Config.StoreFullPrompt {
		event.FullPrompt = model.LongText(prompt.FullPrompt)
	}
	select {
	case s.writes <- &event:
	default:
		s.metrics.recordFailed.Add(1)
	}
}

// logScanFailure logs at most one scan failure per minute and reports how
// many were suppressed in between.
func (s *Service) logScanFailure(requestID string, err error) {
	now := s.now().Unix()
	last := s.lastFailureLog.Load()
	if now-last < 60 || !s.lastFailureLog.CompareAndSwap(last, now) {
		s.suppressedFailures.Add(1)
		return
	}
	common.SysError(common.LogText("prompt audit scan failed for request %s: %s (%d similar failures not logged)",
		requestID, err.Error(), s.suppressedFailures.Swap(0)))
}

func encodeList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := common.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func encodeHits(hits []chunkHit) string {
	if len(hits) == 0 {
		return "[]"
	}
	encoded, err := common.Marshal(hits)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// writer stores flagged events in small batches so SQLite takes its write
// lock once per batch instead of once per event.
func (s *Service) writer(ctx context.Context) {
	ticker := time.NewTicker(writeFlushInterval)
	defer ticker.Stop()
	batch := make([]*model.PromptAuditEvent, 0, writeBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		stored, err := model.CreatePromptAuditEvents(batch)
		s.metrics.recorded.Add(int64(stored))
		if err != nil {
			s.metrics.recordFailed.Add(int64(len(batch) - stored))
			common.SysError(common.LogText("failed to record %d prompt audit events: %s", len(batch)-stored, err.Error()))
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case event := <-s.writes:
			batch = append(batch, event)
			if len(batch) >= writeBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Service) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !common.IsMasterNode || s.now().Unix()-s.lastCleanup.Load() < int64(cleanupEvery/time.Second) {
				continue
			}
			s.lastCleanup.Store(s.now().Unix())
			if _, err := s.cleanup(); err != nil {
				common.SysError(common.LogText("failed to clean up prompt audit events: %s", err.Error()))
			}
		}
	}
}

// cleanup deletes events older than the retention period. It also runs while
// auditing is off so stored prompts still expire.
func (s *Service) cleanup() (int64, error) {
	days := prompt_audit_setting.Current().Config.RetentionDays
	return model.CleanupPromptAuditEvents(s.now().Add(-time.Duration(days) * 24 * time.Hour).Unix())
}

// RuntimeStatus is the operational view shown to administrators.
type RuntimeStatus struct {
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"`
	ConfigVersion    int64  `json:"config_version"`
	WorkerCount      int    `json:"worker_count"`
	ActiveWorkers    int64  `json:"active_workers"`
	QueueDepth       int64  `json:"queue_depth"`
	QueueCapacity    int    `json:"queue_capacity"`
	QueuedBytes      int64  `json:"queued_bytes"`
	QueuedBytesLimit int64  `json:"queued_bytes_limit"`
	QueueInMemory    bool   `json:"queue_in_memory"`
	Enqueued         int64  `json:"enqueued"`
	Dropped          int64  `json:"dropped"`
	Skipped          int64  `json:"skipped"`
	SkippedTooLarge  int64  `json:"skipped_too_large"`
	SkippedNoText    int64  `json:"skipped_no_text"`
	SkippedAgentOnly int64  `json:"skipped_agent_context_only"`
	Scanned          int64  `json:"scanned"`
	Passed           int64  `json:"passed"`
	Flagged          int64  `json:"flagged"`
	Critical         int64  `json:"critical"`
	Failed           int64  `json:"failed"`
	Timeouts         int64  `json:"timeouts"`
	Failovers        int64  `json:"failovers"`
	Retries          int64  `json:"retries"`
	Recorded         int64  `json:"recorded"`
	RecordFailed     int64  `json:"record_failed"`
	ChunksCached     int64  `json:"chunks_cached"`
	ChunksOverBudget int64  `json:"chunks_over_budget"`
	LatencyAvgMS     int64  `json:"latency_avg_ms"`
	LatencyMaxMS     int64  `json:"latency_max_ms"`
}

func Runtime() RuntimeStatus { return defaultService.runtime() }

func (s *Service) runtime() RuntimeStatus {
	config := prompt_audit_setting.Current().Config
	m := &s.metrics
	status := RuntimeStatus{
		Enabled: config.Enabled, Mode: config.Mode, ConfigVersion: config.ConfigVersion,
		WorkerCount: config.WorkerCount, ActiveWorkers: s.active.Load(),
		QueueDepth: s.depth.Load(), QueueCapacity: config.QueueCapacity,
		QueuedBytes: s.queuedBytes.Load(), QueuedBytesLimit: maxQueuedBytes, QueueInMemory: true,
		Enqueued: m.enqueued.Load(), Dropped: m.dropped.Load(), Skipped: m.skipped.Load(),
		SkippedTooLarge: m.tooLarge.Load(), SkippedNoText: m.noText.Load(), SkippedAgentOnly: m.agentContextOnly.Load(), Scanned: m.scanned.Load(),
		Passed: m.passed.Load(), Flagged: m.flagged.Load(), Critical: m.critical.Load(),
		Failed: m.failed.Load(), Timeouts: m.timeouts.Load(), Failovers: m.failovers.Load(),
		Retries: m.retries.Load(), Recorded: m.recorded.Load(), RecordFailed: m.recordFailed.Load(),
		ChunksCached: m.chunksCached.Load(), ChunksOverBudget: m.chunksOverBudget.Load(),
		LatencyMaxMS: m.latencyMaxMS.Load(),
	}
	if completed := status.Passed + status.Flagged + status.Critical; completed > 0 {
		status.LatencyAvgMS = m.latencyTotalMS.Load() / completed
	}
	return status
}

// ProbeResult reports one test scan against an endpoint.
type ProbeResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Safety    string `json:"safety,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Probe sends a harmless test prompt to one endpoint with the given token.
func Probe(ctx context.Context, endpoint prompt_audit_setting.Endpoint, token string) ProbeResult {
	started := time.Now()
	// A throwaway client: probing arbitrary addresses must not grow the cache.
	client := newHTTPClient(endpoint)
	defer client.CloseIdleConnections()
	result, err := scanWith(ctx, client, endpoint, token, "Hello", prompt_audit_setting.ScannerIDs, false)
	probe := ProbeResult{LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.OK, probe.Safety = true, result.Safety
	return probe
}
