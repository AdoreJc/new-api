// Package promptaudit checks relay prompts with a Qwen3Guard model.
//
// Which requests are checked, and what a finding does, is decided by the
// audit policy rules (setting/audit_policy_setting) through the coordinator
// in service/auditpolicy. Two paths exist:
//
//   - Enqueue copies the request body into a bounded in-memory queue and
//     returns; background workers scan it and store the result. The queue
//     lives only in memory, so pending items are lost on restart, and a full
//     queue drops new items.
//   - Check scans the request on the request path, before billing, and
//     returns the verdict. Calls in flight are bounded globally and per Guard
//     endpoint; a full bulkhead, a timeout or a broken answer is reported as
//     unavailable and the caller applies the rule's failure policy.
package promptaudit

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
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

	// Event modes.
	ModeAsync    = prompt_audit_setting.ModeAsync
	ModeBlocking = prompt_audit_setting.ModeBlocking
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
	// bulkheadFull counts synchronous calls refused by a full bulkhead,
	// globally or at one endpoint.
	bulkheadFull atomic.Int64
	// Synchronous checks and their outcomes.
	syncChecks      atomic.Int64
	syncUnavailable atomic.Int64
	scanLatency     latencyWindow
	syncLatency     latencyWindow
}

// latencyWindow keeps the latest samples for percentile estimates.
type latencyWindow struct {
	mu      sync.Mutex
	samples [1024]int64
	next    int
	count   int
}

func (w *latencyWindow) add(ms int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples[w.next] = ms
	w.next = (w.next + 1) % len(w.samples)
	w.count = min(w.count+1, len(w.samples))
}

// percentiles returns p50, p95 and p99 of the kept samples.
func (w *latencyWindow) percentiles() (int64, int64, int64) {
	w.mu.Lock()
	sorted := slices.Clone(w.samples[:w.count])
	w.mu.Unlock()
	if len(sorted) == 0 {
		return 0, 0, 0
	}
	slices.Sort(sorted)
	at := func(p int) int64 { return sorted[min(len(sorted)-1, len(sorted)*p/100)] }
	return at(50), at(95), at(99)
}

// Meta describes a relay request for its audit event.
type Meta struct {
	RequestID   string
	UserID      int
	Username    string
	UserEmail   string
	TokenID     int
	TokenName   string
	Group       string
	ChannelID   int
	RelayFormat string
	Endpoint    string
	Model       string
	Protocol    string
}

func (m Meta) event(mode string) model.PromptAuditEvent {
	endpoint, _, _ := strings.Cut(m.Endpoint, "?")
	return model.PromptAuditEvent{
		RequestId: m.RequestID, UserId: m.UserID, Username: m.Username, TokenId: m.TokenID,
		TokenName: m.TokenName, UsingGroup: m.Group, ChannelId: m.ChannelID, RelayFormat: m.RelayFormat,
		Endpoint: endpoint, ModelName: m.Model, Mode: mode,
	}
}

type job struct {
	protocol   string
	size       int64
	body       []byte
	event      model.PromptAuditEvent
	email      string
	rule       audit_policy_setting.PromptAudit
	enqueuedAt time.Time
	// client is the client class of the request; it is only set while
	// request structure statistics are on.
	client string
}

// DisposalHook receives triggered findings of rules that count toward
// disposal. It is set by the policy coordinator.
type DisposalHook func(event *model.PromptAuditEvent, email string)

// Service owns the queue, workers and event writer.
type Service struct {
	queue       chan *job
	depth       atomic.Int64
	queuedBytes atomic.Int64
	active      atomic.Int64
	workerSlots atomic.Pointer[chan struct{}]
	// syncSlots bound synchronous checks in flight across all endpoints.
	syncSlots chan struct{}
	writes    chan *model.PromptAuditEvent
	guard     guardClient
	metrics   Metrics
	startOnce sync.Once
	// lastCleanup is the unix time of the last retention cleanup.
	lastCleanup atomic.Int64
	disposal    atomic.Pointer[DisposalHook]
	// lastFailureLog and suppressedFailures throttle scan failure logging so an
	// unreachable Guard does not flood the log.
	lastFailureLog     atomic.Int64
	suppressedFailures atomic.Int64
	// retryDelays are the waits before each retry of a retryable Guard failure
	// in the background; synchronous checks never wait to retry.
	retryDelays []time.Duration
	now         func() time.Time
	// structure collects request structure statistics.
	structure *structureStats
}

func newService() *Service {
	return &Service{
		queue:       make(chan *job, prompt_audit_setting.MaxQueueCapacity),
		syncSlots:   make(chan struct{}, prompt_audit_setting.GlobalConcurrency),
		writes:      make(chan *model.PromptAuditEvent, writeBufferSize),
		retryDelays: []time.Duration{200 * time.Millisecond, time.Second},
		now:         time.Now,
		structure:   newStructureStats(),
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
		go s.structureLoop(ctx)
	})
}

// SetDisposalHook installs the function that counts triggered findings.
func SetDisposalHook(hook DisposalHook) { defaultService.disposal.Store(&hook) }

// RelayProtocol maps a relay format to the extraction protocol. Formats that
// carry no free-form prompt text (embeddings, rerank, audio, realtime, search
// and tool calls) are exempt.
func RelayProtocol(format types.RelayFormat, path string) (string, bool) {
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

// Enabled reports whether the engine runs at all.
func Enabled() bool { return prompt_audit_setting.Current().Config.Enabled }

// CountSkipped counts a request the policy kept out of prompt audit.
func CountSkipped() { defaultService.metrics.skipped.Add(1) }

// Enqueue queues the request for background auditing with the rule's
// settings. It never blocks and never fails the request.
func Enqueue(c *gin.Context, meta Meta, ruleID string, rule audit_policy_setting.PromptAudit) {
	defaultService.enqueue(c, meta, ruleID, rule)
}

func (s *Service) enqueue(c *gin.Context, meta Meta, ruleID string, rule audit_policy_setting.PromptAudit) {
	snapshot := prompt_audit_setting.Current()
	if !snapshot.Config.Enabled {
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
	event := meta.event(ModeAsync)
	event.RuleId = ruleID
	item := &job{protocol: meta.Protocol, size: size, body: raw, event: event, email: meta.UserEmail, rule: rule, enqueuedAt: s.now()}
	if snapshot.Config.StructureStatsEnabled {
		item.client = ClientClassOf(c.Request.UserAgent())
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
	var subject *structureSubject
	if snapshot.Config.StructureStatsEnabled {
		subject = &structureSubject{userID: item.event.UserId, protocol: item.protocol, client: item.client}
	}
	prompt, err := s.extract(item.protocol, item.body, snapshot, &item.rule, subject)
	if err != nil {
		return
	}
	s.metrics.scanned.Add(1)
	// A failed scan can still return the findings of chunks scanned before
	// the failure; those are recorded and marked incomplete.
	request := scanRequest{snapshot: snapshot, rule: &item.rule, retryDelays: s.retryDelays}
	result, err := s.guard.evaluate(ctx, request, prompt.Latest, prompt.Rest, &s.metrics)
	if err != nil {
		s.countFailure(item.event.RequestId, err)
		if result == nil {
			return
		}
	}
	event := s.finish(item.event, snapshot, prompt, result)
	if event == nil {
		return
	}
	event.PolicyAction = audit_policy_setting.ActionObserve
	if result.Decision == DecisionCritical && item.rule.ActionFor(result.Triggered) == audit_policy_setting.ActionIgnore {
		event.PolicyAction = audit_policy_setting.ActionIgnore
	}
	if result.Decision == DecisionCritical && item.rule.CountDisposal && event.PolicyAction != audit_policy_setting.ActionIgnore {
		if hook := s.disposal.Load(); hook != nil {
			(*hook)(event, item.email)
		}
	}
	s.Record(event)
}

// extract reads the text the rule's scan scope sends to the Guard and counts
// requests without any. With a subject, every extracted text of the request,
// whatever the scope, is also handed to request structure statistics.
func (s *Service) extract(protocol string, body []byte, snapshot *prompt_audit_setting.Snapshot, rule *audit_policy_setting.PromptAudit, subject *structureSubject) (*promptSnapshot, error) {
	scope := rule.ScanScope
	if scope == "" {
		scope = snapshot.Config.ScanScope
	}
	var prompt *promptSnapshot
	segments, turns, err := requestSegments(protocol, body)
	if err == nil {
		if subject != nil && len(segments) > 0 {
			s.structure.submit(*subject, segments, turns)
		}
		prompt, err = scopedSnapshot(segments, scope)
	}
	if errors.Is(err, errAgentContextOnly) {
		s.metrics.agentContextOnly.Add(1)
	} else if err != nil {
		s.metrics.noText.Add(1)
	}
	return prompt, err
}

func (s *Service) countFailure(requestID string, err error) {
	s.metrics.failed.Add(1)
	var guardErr *guardError
	if errors.As(err, &guardErr) && guardErr.Timeout || errors.Is(err, context.DeadlineExceeded) {
		s.metrics.timeouts.Add(1)
	}
	s.logScanFailure(requestID, err)
}

// finish counts the verdict and builds its event. It returns nil for a pass
// that is not stored.
func (s *Service) finish(base model.PromptAuditEvent, snapshot *prompt_audit_setting.Snapshot, prompt *promptSnapshot, result *guardResult) *model.PromptAuditEvent {
	s.metrics.latencyTotalMS.Add(int64(result.LatencyMS))
	s.metrics.scanLatency.add(int64(result.LatencyMS))
	for current := s.metrics.latencyMaxMS.Load(); int64(result.LatencyMS) > current &&
		!s.metrics.latencyMaxMS.CompareAndSwap(current, int64(result.LatencyMS)); current = s.metrics.latencyMaxMS.Load() {
	}
	switch result.Decision {
	case DecisionPass:
		s.metrics.passed.Add(1)
		if !snapshot.Config.StorePassEvents {
			return nil
		}
	case DecisionCritical:
		s.metrics.critical.Add(1)
	default:
		s.metrics.flagged.Add(1)
	}
	event := base
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
	if snapshot.Config.StoreFullPrompt && result.Decision != DecisionPass {
		event.FullPrompt = model.LongText(prompt.FullPrompt)
	}
	return &event
}

// Record queues an event for storage.
func Record(event *model.PromptAuditEvent) { defaultService.Record(event) }

func (s *Service) Record(event *model.PromptAuditEvent) {
	if event == nil {
		return
	}
	select {
	case s.writes <- event:
	default:
		s.metrics.recordFailed.Add(1)
	}
}

// Outcome is the result of a synchronous check.
type Outcome int

const (
	// OutcomeSkipped: nothing was checked (engine off, no text, too large).
	OutcomeSkipped Outcome = iota
	OutcomePass
	// OutcomeFlag is a finding below the rule's trigger level.
	OutcomeFlag
	// OutcomeTriggered is a finding at or above the trigger level.
	OutcomeTriggered
	// OutcomeUnavailable: the Guard could not answer in time or correctly, a
	// bulkhead was full, or the stored settings could not be read.
	OutcomeUnavailable
)

// Verdict is a synchronous check's answer. Event, when set, is not stored
// yet: the caller sets the policy action and passes it to Record.
type Verdict struct {
	Outcome   Outcome
	Triggered []string
	Event     *model.PromptAuditEvent
	LatencyMS int64
}

// Check scans the request synchronously with the rule's settings.
func Check(ctx context.Context, c *gin.Context, meta Meta, ruleID string, rule audit_policy_setting.PromptAudit) Verdict {
	return defaultService.check(ctx, c, meta, ruleID, rule)
}

func (s *Service) check(ctx context.Context, c *gin.Context, meta Meta, ruleID string, rule audit_policy_setting.PromptAudit) Verdict {
	snapshot := prompt_audit_setting.Current()
	started := s.now()
	verdict := s.checkSnapshot(ctx, c, meta, ruleID, &rule, snapshot)
	if verdict.Outcome != OutcomeSkipped {
		verdict.LatencyMS = s.now().Sub(started).Milliseconds()
		s.metrics.syncChecks.Add(1)
		s.metrics.syncLatency.add(verdict.LatencyMS)
	}
	if verdict.Outcome == OutcomeUnavailable {
		s.metrics.syncUnavailable.Add(1)
	}
	return verdict
}

func (s *Service) checkSnapshot(ctx context.Context, c *gin.Context, meta Meta, ruleID string, rule *audit_policy_setting.PromptAudit, snapshot *prompt_audit_setting.Snapshot) Verdict {
	if snapshot.Degraded {
		return Verdict{Outcome: OutcomeUnavailable}
	}
	if !snapshot.Config.Enabled {
		s.metrics.skipped.Add(1)
		return Verdict{Outcome: OutcomeSkipped}
	}
	endpoints := snapshot.Config.EnabledEndpoints()
	if len(endpoints) == 0 {
		return Verdict{Outcome: OutcomeUnavailable}
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		s.metrics.skipped.Add(1)
		return Verdict{Outcome: OutcomeSkipped}
	}
	if storage.Size() > maxBodyBytes {
		s.metrics.tooLarge.Add(1)
		return Verdict{Outcome: OutcomeSkipped}
	}
	body, err := storage.Bytes()
	if err != nil {
		s.metrics.skipped.Add(1)
		return Verdict{Outcome: OutcomeSkipped}
	}
	var subject *structureSubject
	if snapshot.Config.StructureStatsEnabled {
		subject = &structureSubject{userID: meta.UserID, protocol: meta.Protocol, client: ClientClassOf(c.Request.UserAgent())}
	}
	prompt, err := s.extract(meta.Protocol, body, snapshot, rule, subject)
	if err != nil {
		return Verdict{Outcome: OutcomeSkipped}
	}
	select {
	case s.syncSlots <- struct{}{}:
		defer func() { <-s.syncSlots }()
	default:
		s.metrics.bulkheadFull.Add(1)
		return Verdict{Outcome: OutcomeUnavailable}
	}
	s.metrics.scanned.Add(1)
	// Each call is bounded by its endpoint's own timeout (its HTTP client),
	// so a slow first endpoint still leaves time to fail over. The whole
	// check is bounded by the sum of those timeouts, at most MaxTimeoutMS.
	budget := 0
	for _, endpoint := range endpoints {
		budget += endpoint.TimeoutMS
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(min(budget, prompt_audit_setting.MaxTimeoutMS))*time.Millisecond)
	defer cancel()
	result, err := s.guard.evaluate(ctx, scanRequest{snapshot: snapshot, rule: rule, synchronous: true}, prompt.Latest, prompt.Rest, &s.metrics)
	if err != nil {
		s.countFailure(meta.RequestID, err)
		// A critical finding before the failure still decides; anything less
		// cannot vouch for the unscanned rest.
		if result == nil || result.Decision != DecisionCritical {
			return Verdict{Outcome: OutcomeUnavailable}
		}
	}
	base := meta.event(ModeBlocking)
	base.RuleId = ruleID
	verdict := Verdict{Event: s.finish(base, snapshot, prompt, result), Triggered: result.Triggered}
	switch result.Decision {
	case DecisionPass:
		verdict.Outcome = OutcomePass
	case DecisionCritical:
		verdict.Outcome = OutcomeTriggered
	default:
		verdict.Outcome = OutcomeFlag
	}
	return verdict
}

// logScanFailure logs at most one scan failure per minute and reports how
// many were suppressed in between. The message never contains prompt text.
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

// writer stores events in small batches so SQLite takes its write lock once
// per batch instead of once per event.
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

// cleanup deletes events older than the retention period in batches, and
// request structure statistics older than their own retention period. It also
// runs while auditing is off so stored prompts still expire.
func (s *Service) cleanup() (int64, error) {
	config := prompt_audit_setting.Current().Config
	statsCutoff := s.now().Add(-time.Duration(config.StructureStatsRetentionDays) * 24 * time.Hour)
	_, statsErr := model.CleanupPromptAuditStructureStats(statsCutoff.Unix(), structureDay(statsCutoff))
	deleted, err := model.CleanupPromptAuditEvents(s.now().Add(-time.Duration(config.RetentionDays) * 24 * time.Hour).Unix())
	return deleted, errors.Join(err, statsErr)
}

// RuntimeStatus is the operational view shown to administrators.
type RuntimeStatus struct {
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"`
	Degraded         bool   `json:"degraded"`
	FailPolicy       string `json:"fail_policy"`
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
	BulkheadFull     int64  `json:"bulkhead_full"`
	Recorded         int64  `json:"recorded"`
	RecordFailed     int64  `json:"record_failed"`
	ChunksCached     int64  `json:"chunks_cached"`
	ChunksOverBudget int64  `json:"chunks_over_budget"`
	LatencyAvgMS     int64  `json:"latency_avg_ms"`
	LatencyMaxMS     int64  `json:"latency_max_ms"`
	LatencyP50MS     int64  `json:"latency_p50_ms"`
	LatencyP95MS     int64  `json:"latency_p95_ms"`
	LatencyP99MS     int64  `json:"latency_p99_ms"`
	// Sync* describe checks on the request path; their latency is the extra
	// time those requests waited.
	SyncChecks      int64 `json:"sync_checks"`
	SyncUnavailable int64 `json:"sync_unavailable"`
	SyncInFlight    int   `json:"sync_in_flight"`
	SyncConcurrency int   `json:"sync_concurrency"`
	SyncP50MS       int64 `json:"sync_p50_ms"`
	SyncP95MS       int64 `json:"sync_p95_ms"`
	SyncP99MS       int64 `json:"sync_p99_ms"`
	// Structure* describe request structure statistics. Observed: requests
	// measured. Dropped: requests not measured because the hand-over queue
	// was full. FingerprintsDropped: fingerprint updates left out of measured
	// requests (too many distinct fingerprints at once, or a full table).
	// WriteFailed: batch writes that failed and lost their counters.
	StructureStatsEnabled        bool  `json:"structure_stats_enabled"`
	StructureObserved            int64 `json:"structure_observed"`
	StructureDropped             int64 `json:"structure_dropped"`
	StructureFingerprintsDropped int64 `json:"structure_fingerprints_dropped"`
	StructureWriteFailed         int64 `json:"structure_write_failed"`
}

func Runtime() RuntimeStatus { return defaultService.runtime() }

func (s *Service) runtime() RuntimeStatus {
	snapshot := prompt_audit_setting.Current()
	config := snapshot.Config
	m := &s.metrics
	status := RuntimeStatus{
		Enabled: config.Enabled, Mode: config.Mode, Degraded: snapshot.Degraded, FailPolicy: config.FailPolicy,
		ConfigVersion: config.ConfigVersion, WorkerCount: config.WorkerCount, ActiveWorkers: s.active.Load(),
		QueueDepth: s.depth.Load(), QueueCapacity: config.QueueCapacity,
		QueuedBytes: s.queuedBytes.Load(), QueuedBytesLimit: maxQueuedBytes, QueueInMemory: true,
		Enqueued: m.enqueued.Load(), Dropped: m.dropped.Load(), Skipped: m.skipped.Load(),
		SkippedTooLarge: m.tooLarge.Load(), SkippedNoText: m.noText.Load(), SkippedAgentOnly: m.agentContextOnly.Load(), Scanned: m.scanned.Load(),
		Passed: m.passed.Load(), Flagged: m.flagged.Load(), Critical: m.critical.Load(),
		Failed: m.failed.Load(), Timeouts: m.timeouts.Load(), Failovers: m.failovers.Load(),
		Retries: m.retries.Load(), BulkheadFull: m.bulkheadFull.Load(), Recorded: m.recorded.Load(), RecordFailed: m.recordFailed.Load(),
		ChunksCached: m.chunksCached.Load(), ChunksOverBudget: m.chunksOverBudget.Load(),
		LatencyMaxMS: m.latencyMaxMS.Load(), SyncChecks: m.syncChecks.Load(), SyncUnavailable: m.syncUnavailable.Load(),
		SyncInFlight: len(s.syncSlots), SyncConcurrency: cap(s.syncSlots),
		StructureStatsEnabled: config.StructureStatsEnabled, StructureObserved: s.structure.observed.Load(),
		StructureDropped: s.structure.dropped.Load(), StructureFingerprintsDropped: s.structure.fingerprintsDropped.Load(),
		StructureWriteFailed: s.structure.writeFailed.Load(),
	}
	if completed := status.Passed + status.Flagged + status.Critical; completed > 0 {
		status.LatencyAvgMS = m.latencyTotalMS.Load() / completed
	}
	status.LatencyP50MS, status.LatencyP95MS, status.LatencyP99MS = m.scanLatency.percentiles()
	status.SyncP50MS, status.SyncP95MS, status.SyncP99MS = m.syncLatency.percentiles()
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
	rule := audit_policy_setting.PromptAudit{MinLevel: audit_policy_setting.LevelUnsafe}
	result, err := scanWith(ctx, client, endpoint, token, "Hello", &rule)
	probe := ProbeResult{LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.OK, probe.Safety = true, result.Safety
	return probe
}
