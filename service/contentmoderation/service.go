// Package contentmoderation checks the latest user message of relay requests
// with an OpenAI-compatible /v1/moderations engine.
//
// Modes: off (nothing runs), observe (the input is queued in memory and
// checked in the background; requests are never delayed or blocked) and
// pre_block (the input is checked before billing and a flagged request is
// answered with 403). Engine failures always let the request through
// (fail-open). Allowlisted users are checked in the background and never
// blocked, mailed or disabled. Which requests are moderated, and how, is
// decided by a ScopePolicy (policy.go); the stages that follow are the hash
// blocklist, the keyword lists and the engine.
package contentmoderation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"

	"github.com/gin-gonic/gin"
)

// Error codes returned to clients whose request was blocked, or rejected
// because the engine failed under a fail-closed rule.
const (
	ErrorCodeBlocked     = types.ErrorCode("content_moderation_blocked")
	ErrorCodeUnavailable = types.ErrorCode("content_moderation_unavailable")
)

const (
	// maxBodyBytes skips larger request bodies; they are media uploads.
	maxBodyBytes = 32 << 20
	// maxQueuedBytes bounds the memory held by queued inputs.
	maxQueuedBytes = 64 << 20

	recordBufferSize    = 1024
	recordBatchSize     = 50
	recordFlushInterval = time.Second
	hashRefreshInterval = time.Minute
	hashFullReloadEvery = 10 * time.Minute
	cleanupInterval     = time.Hour
	cleanupEvery        = 24 * time.Hour
	hitEmailInterval    = time.Hour
	// disposalQueueWait may run on the request path, so it stays short.
	disposalQueueWait = 50 * time.Millisecond
	// maxFlaggedHashes bounds the hash blocklist; the oldest go first.
	maxFlaggedHashes = 100000
	maxHitEmailUsers = 10000
)

// Metrics are process-local counters reset on restart.
type Metrics struct {
	checked      atomic.Int64
	skipped      atomic.Int64
	tooLarge     atomic.Int64
	empty        atomic.Int64
	sampledOut   atomic.Int64
	noKeys       atomic.Int64
	enqueued     atomic.Int64
	dropped      atomic.Int64
	passed       atomic.Int64
	flagged      atomic.Int64
	blocked      atomic.Int64
	hashBlocked  atomic.Int64
	keywordHits  atomic.Int64
	failed       atomic.Int64
	timeouts     atomic.Int64
	calls        atomic.Int64
	autoDisabled atomic.Int64
	emailsSent   atomic.Int64
	emailsFailed atomic.Int64
	recorded     atomic.Int64
	recordFailed atomic.Int64
	// disposalDropped counts flagged events lost before disposal, so their
	// violations were not counted.
	disposalDropped atomic.Int64
	latencyTotalMS  atomic.Int64
	latencyMaxMS    atomic.Int64
	syncTotalMS     atomic.Int64
	syncChecks      atomic.Int64
	rerouted        atomic.Int64
	// unavailableBlocked counts requests rejected by a fail-closed rule.
	unavailableBlocked atomic.Int64
}

// request is one input with the relay metadata needed for its event.
type request struct {
	input      Input
	hash       string
	decision   Decision
	size       int64
	event      model.ContentModerationEvent
	enqueuedAt time.Time
}

// record is an event waiting to be stored. Flagged events by users who are
// not allowlisted also go through disposal (violation count, automatic
// disable, mail) before they are stored.
type record struct {
	event   *model.ContentModerationEvent
	email   string
	dispose bool
	// rememberHash adds the input hash to the blocklist; only inputs the
	// engine flagged are remembered, never keyword hits.
	rememberHash bool
	config       content_moderation_setting.Config
}

// Service owns the check queue, workers, recorder and the hash blocklist.
type Service struct {
	engine      engine
	metrics     Metrics
	queue       chan *request
	depth       atomic.Int64
	queuedBytes atomic.Int64
	active      atomic.Int64
	workerSlots atomic.Pointer[chan struct{}]
	records     chan *record
	startOnce   sync.Once
	lastCleanup atomic.Int64

	keywords  atomic.Pointer[keywordIndex]
	keywordMu sync.Mutex

	hashMu       sync.RWMutex
	hashes       map[string]struct{}
	hashLoaded   atomic.Bool
	hashVersion  atomic.Value
	hashLoadedAt atomic.Int64

	emailMu   sync.Mutex
	lastEmail map[int]time.Time

	lastFailureLog     atomic.Int64
	suppressedFailures atomic.Int64

	// disposalWait is how long an event that needs disposal waits for room
	// in a full recorder buffer.
	disposalWait time.Duration

	now func() time.Time
}

func newService() *Service {
	s := &Service{
		queue:     make(chan *request, content_moderation_setting.MaxQueueSize),
		records:   make(chan *record, recordBufferSize),
		hashes:    map[string]struct{}{},
		lastEmail: map[int]time.Time{},
		now:       time.Now,

		disposalWait: disposalQueueWait,
	}
	s.engine.now = s.now
	return s
}

var defaultService = newService()

// Start launches the workers, the recorder, the hash blocklist refresh and
// the daily retention cleanup. They idle while moderation is off.
func Start() { defaultService.start(context.Background()) }

func (s *Service) start(ctx context.Context) {
	s.startOnce.Do(func() {
		for range content_moderation_setting.MaxWorkerCount {
			go s.worker(ctx)
		}
		go s.recorder(ctx)
		go s.maintenanceLoop(ctx)
	})
}

// RelayProtocol maps a relay request to the extraction protocol. Requests
// without a user-written prompt (embeddings, rerank, audio, realtime, search,
// tools and moderation calls) are not checked.
func RelayProtocol(format types.RelayFormat, path string) (string, bool) {
	path, _, _ = strings.Cut(path, "?")
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
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			return ProtocolChat, true
		case strings.HasSuffix(path, "/completions"):
			return ProtocolCompletions, true
		}
	}
	return "", false
}

// Outcome is the result of a moderation check for the request path.
type Outcome int

const (
	// OutcomeContinue lets the request go on: out of scope, not flagged,
	// checked in the background, or failed open.
	OutcomeContinue Outcome = iota
	// OutcomeBlock rejects the request (flagged, or the hash blocklist).
	OutcomeBlock
	// OutcomeReroute sends the request to the rule's reroute target.
	OutcomeReroute
	// OutcomeUnavailable rejects the request because the engine failed and
	// the decision fails closed.
	OutcomeUnavailable
)

// SubjectOf builds the policy subject of a relay request. For an "auto"
// token the group is the one the distributor chose, and "auto" stays the
// requested group.
func SubjectOf(c *gin.Context, info *relaycommon.RelayInfo) Subject {
	subject := Subject{UserID: info.UserId, TokenID: info.TokenId, Group: info.UsingGroup, Model: info.OriginModelName, Format: string(info.RelayFormat)}
	if subject.Group == "auto" {
		if selected := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selected != "" {
			subject.Group, subject.RequestedGroup = selected, "auto"
		}
	}
	return subject
}

// CheckRelay moderates the latest user message of a relay request with the
// current scope policy. It returns an error when the request is blocked; a
// reroute outcome is reported as a block because only the audit policy
// coordinator can reroute.
func CheckRelay(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	return defaultService.checkRelay(c, info)
}

func (s *Service) checkRelay(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info == nil {
		return nil
	}
	subject := SubjectOf(c, info)
	switch s.check(c, info, subject, CurrentScopePolicy().Decide(subject)) {
	case OutcomeBlock, OutcomeReroute:
		return BlockError(c)
	case OutcomeUnavailable:
		return UnavailableError(c)
	}
	return nil
}

// Check moderates the latest user message of a relay request as the decision
// says. It runs once, before billing and channel retries. Only a synchronous
// decision (block or reroute) can return anything but OutcomeContinue. When
// the decision is out of scope it costs nothing.
func Check(c *gin.Context, info *relaycommon.RelayInfo, subject Subject, decision Decision) Outcome {
	if info == nil {
		return OutcomeContinue
	}
	return defaultService.check(c, info, subject, decision)
}

func (s *Service) check(c *gin.Context, info *relaycommon.RelayInfo, subject Subject, decision Decision) Outcome {
	if !decision.Moderate {
		if decision.Mode != "" {
			s.metrics.skipped.Add(1)
		}
		return OutcomeContinue
	}
	protocol, covered := RelayProtocol(info.RelayFormat, info.RequestURLPath)
	if !covered {
		s.metrics.skipped.Add(1)
		return OutcomeContinue
	}
	snapshot := content_moderation_setting.Current()
	config := &snapshot.Config
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		s.metrics.skipped.Add(1)
		return OutcomeContinue
	}
	keywordMode, keywordLists := config.KeywordMode, config.KeywordLists
	if decision.KeywordMode != "" {
		keywordMode = decision.KeywordMode
	}
	if decision.KeywordLists != nil {
		keywordLists = decision.KeywordLists
	}
	var input Input
	keywordText := ""
	useKeywords := keywordMode != content_moderation_setting.KeywordModeAPIOnly
	contentType := c.Request.Header.Get("Content-Type")
	if protocol == ProtocolImages && isMultipartForm(contentType) {
		// Image edits send their prompt as multipart form fields. The form is
		// streamed so file parts are skipped without loading them, whatever
		// the body size, and every prompt field the upstream receives is read.
		reader, err := storage.NewReader()
		if err != nil {
			s.metrics.skipped.Add(1)
			return OutcomeContinue
		}
		prompts, tooLarge := MultipartPrompts(contentType, reader)
		_ = reader.Close()
		if tooLarge {
			// Like a JSON body over maxBodyBytes: counted and not checked.
			s.metrics.tooLarge.Add(1)
			return OutcomeContinue
		}
		keywordText = joinParts(prompts)
		input.Text = prefixRunes(keywordText, maxInputRunes)
	} else {
		if storage.Size() > maxBodyBytes {
			s.metrics.tooLarge.Add(1)
			return OutcomeContinue
		}
		body, err := storage.Bytes()
		if err != nil {
			s.metrics.skipped.Add(1)
			return OutcomeContinue
		}
		input = ExtractInput(protocol, body)
		if useKeywords {
			keywordText = ExtractKeywordText(protocol, body)
		}
	}
	if input.Empty() && keywordText == "" {
		s.metrics.empty.Add(1)
		return OutcomeContinue
	}
	s.metrics.checked.Add(1)
	endpoint, _, _ := strings.Cut(info.RequestURLPath, "?")
	hash := input.Hash()
	item := &request{
		input:      input,
		hash:       hash,
		decision:   decision,
		size:       int64(len(input.Text)),
		enqueuedAt: s.now(),
		event: model.ContentModerationEvent{
			RequestId:     c.GetString(common.RequestIdKey),
			UserId:        info.UserId,
			Username:      common.GetContextKeyString(c, constant.ContextKeyUserName),
			TokenId:       info.TokenId,
			TokenName:     c.GetString("token_name"),
			UsingGroup:    subject.Group,
			ChannelId:     common.GetContextKeyInt(c, constant.ContextKeyChannelId),
			RelayFormat:   string(info.RelayFormat),
			Endpoint:      endpoint,
			ModelName:     info.OriginModelName,
			Mode:          decision.Mode,
			InputHash:     hash,
			InputLength:   len([]rune(input.Text)),
			ImageCount:    len(input.Images),
			ConfigVersion: config.ConfigVersion,
			// Inputs that are not disposed are marked allowlisted so they
			// never count as violations later.
			Allowlisted: !decision.Dispose,
			RuleId:      decision.RuleID,
		},
	}
	for _, image := range input.Images {
		item.size += int64(len(image))
	}
	synchronous := decision.synchronous()
	// flaggedOutcome is what a flagged synchronous check returns.
	flaggedOutcome, flaggedAction := OutcomeBlock, ""
	if decision.Action == ActionReroute {
		flaggedOutcome, flaggedAction = OutcomeReroute, model.ContentModerationActionReroute
	}

	// Keyword stage, matched against the reminder-preserving text. In
	// keyword_only and keyword_and_api a hit is decided here without an
	// engine call (blocked in block mode, recorded otherwise); keyword_only
	// stops for every other input, keyword_and_api sends it on as api_only
	// would. keyword_gated_api sends only hits on, whatever the sample rate.
	// A keyword hit never enters the hash blocklist, so editing a list takes
	// effect for inputs it matched before.
	keywordGated := false
	if useKeywords {
		list, hit := s.matchKeyword(keywordText, keywordLists)
		if hit {
			s.metrics.keywordHits.Add(1)
			item.event.KeywordList = list
		}
		gated := keywordMode == content_moderation_setting.KeywordModeKeywordGated
		if !hit && (gated || keywordMode == content_moderation_setting.KeywordModeKeywordOnly) {
			return OutcomeContinue
		}
		keywordGated = gated
		if hit && !gated {
			s.metrics.flagged.Add(1)
			event := item.event
			event.CreatedAt = s.now().Unix()
			event.Action, event.Flagged, event.HighestCategory, event.HighestScore = model.ContentModerationActionAllow, true, model.ContentModerationKeywordCategory, 1
			event.InputExcerpt = Excerpt(keywordExcerptSource(input.Text, keywordText))
			if synchronous {
				event.Action = cmp.Or(flaggedAction, model.ContentModerationActionKeywordBlock)
			}
			s.enqueueRecord(&record{event: &event, email: info.UserEmail, dispose: decision.Dispose, config: config.Clone()})
			if synchronous {
				return s.flagged(flaggedOutcome)
			}
			return OutcomeContinue
		}
	}
	if input.Empty() {
		s.metrics.empty.Add(1)
		return OutcomeContinue
	}

	if config.HashBlocklistEnabled && s.hashListed(item.hash) {
		s.metrics.hashBlocked.Add(1)
		event := item.event
		event.CreatedAt = s.now().Unix()
		event.Action, event.Flagged, event.HighestCategory, event.HighestScore = model.ContentModerationActionAllow, true, model.ContentModerationHashCategory, 1
		event.InputExcerpt = Excerpt(input.Text)
		if synchronous {
			event.Action = cmp.Or(flaggedAction, model.ContentModerationActionHashBlock)
		}
		s.enqueueRecord(&record{event: &event})
		if synchronous {
			return s.flagged(flaggedOutcome)
		}
		return OutcomeContinue
	}
	if !keywordGated && !sampled(item.hash, decision.SampleRate) {
		s.metrics.sampledOut.Add(1)
		return OutcomeContinue
	}
	if len(snapshot.Keys) == 0 {
		s.metrics.noKeys.Add(1)
		if synchronous && decision.FailClosed {
			return s.unavailable(item)
		}
		return OutcomeContinue
	}
	if !synchronous {
		s.enqueue(item, config)
		return OutcomeContinue
	}

	started := s.now()
	flagged, failed := s.moderate(c.Request.Context(), snapshot, item, info.UserEmail, 0, true)
	s.metrics.syncChecks.Add(1)
	s.metrics.syncTotalMS.Add(s.now().Sub(started).Milliseconds())
	switch {
	case flagged:
		return s.flagged(flaggedOutcome)
	case failed && decision.FailClosed:
		return s.unavailable(item)
	}
	return OutcomeContinue
}

func (s *Service) flagged(outcome Outcome) Outcome {
	if outcome == OutcomeBlock {
		s.metrics.blocked.Add(1)
	} else {
		s.metrics.rerouted.Add(1)
	}
	return outcome
}

// unavailable records a request rejected because the engine could not
// answer and the decision fails closed.
func (s *Service) unavailable(item *request) Outcome {
	s.metrics.unavailableBlocked.Add(1)
	event := item.event
	event.CreatedAt = s.now().Unix()
	event.Action = model.ContentModerationActionUnavailable
	s.enqueueRecord(&record{event: &event})
	return OutcomeUnavailable
}

// BlockError is the 403 answer for a blocked request. It names no category
// so the reply does not help tune a prompt around the engine.
func BlockError(c *gin.Context) *types.NewAPIError {
	service.RequestPolicy(c).AddEvent(service.PolicyEvent{ErrorCode: string(ErrorCodeBlocked), ErrorSource: "local",
		Decision: service.PolicyDecision{Action: "stop", Reason: "local_rejection", Source: "global"}, Health: "unchanged"})
	logger.LogWarn(c, "content moderation blocked the request")
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgContentModerationBlocked)), ErrorCodeBlocked,
		http.StatusForbidden, types.ErrOptionWithSkipRetry())
}

// UnavailableError is the 503 answer when the engine failed and the rule
// fails closed.
func UnavailableError(c *gin.Context) *types.NewAPIError {
	service.RequestPolicy(c).AddEvent(service.PolicyEvent{ErrorCode: string(ErrorCodeUnavailable), ErrorSource: "local",
		Decision: service.PolicyDecision{Action: "stop", Reason: "local_rejection", Source: "global"}, Health: "unchanged"})
	logger.LogWarn(c, "content moderation was unavailable and the rule fails closed")
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgAuditUnavailable)), ErrorCodeUnavailable,
		http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

// sampled keeps SampleRate percent of inputs, chosen by the input hash.
func sampled(hash string, rate int) bool {
	if rate >= 100 {
		return true
	}
	if rate <= 0 || len(hash) < 4 {
		return rate > 0
	}
	value, err := strconv.ParseUint(hash[:4], 16, 16)
	if err != nil {
		return true
	}
	return int(value%100) < rate
}

func (s *Service) enqueue(item *request, config *content_moderation_setting.Config) {
	// Reserve capacity first and roll back, so concurrent requests cannot
	// overshoot the item or byte limits.
	if s.depth.Add(1) > int64(config.QueueSize) {
		s.depth.Add(-1)
		s.metrics.dropped.Add(1)
		return
	}
	if s.queuedBytes.Add(item.size) > maxQueuedBytes {
		s.queuedBytes.Add(-item.size)
		s.depth.Add(-1)
		s.metrics.dropped.Add(1)
		return
	}
	select {
	case s.queue <- item:
		s.metrics.enqueued.Add(1)
	default:
		s.release(item)
		s.metrics.dropped.Add(1)
	}
}

func (s *Service) release(item *request) {
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
			snapshot := content_moderation_setting.Current()
			slots := s.slots(snapshot.Config.WorkerCount)
			slots <- struct{}{}
			s.active.Add(1)
			// Items were admitted by the scope policy; they are checked
			// even if the settings changed while they waited.
			if len(snapshot.Keys) > 0 {
				delay := int(s.now().Sub(item.enqueuedAt).Milliseconds())
				s.moderate(ctx, snapshot, item, "", delay, false)
			}
			s.release(item)
			s.active.Add(-1)
			<-slots
		}
	}
}

// moderate calls the engine for one input and queues its event. It reports
// whether a synchronous check flagged the input, and whether the call failed.
func (s *Service) moderate(ctx context.Context, snapshot *content_moderation_setting.Snapshot, item *request, email string, queueDelayMS int, synchronous bool) (flagged bool, failed bool) {
	config := &snapshot.Config
	started := s.now()
	result, attempts, err := s.engine.moderate(ctx, snapshot, item.input)
	latency := int(s.now().Sub(started).Milliseconds())
	s.metrics.calls.Add(int64(attempts))
	event := item.event
	event.CreatedAt = s.now().Unix()
	event.LatencyMs, event.QueueDelayMs = latency, queueDelayMS
	event.ConfigVersion = config.ConfigVersion
	if err != nil {
		s.metrics.failed.Add(1)
		var engineErr *engineError
		if errors.As(err, &engineErr) && engineErr.Timeout {
			s.metrics.timeouts.Add(1)
		}
		s.logFailure(event.RequestId, err)
		if config.RecordNonHits {
			event.Action, event.Error = model.ContentModerationActionError, err.Error()
			s.enqueueRecord(&record{event: &event})
		}
		return false, true
	}
	s.metrics.latencyTotalMS.Add(int64(latency))
	for current := s.metrics.latencyMaxMS.Load(); int64(latency) > current &&
		!s.metrics.latencyMaxMS.CompareAndSwap(current, int64(latency)); current = s.metrics.latencyMaxMS.Load() {
	}
	thresholds := config.Thresholds
	if len(item.decision.Thresholds) > 0 {
		thresholds = maps.Clone(config.Thresholds)
		maps.Copy(thresholds, item.decision.Thresholds)
	}
	flagged, highest, highestScore := evaluate(result.Scores, thresholds)
	event.Flagged, event.HighestCategory, event.HighestScore = flagged, highest, highestScore
	event.EngineModel = result.Model
	event.CategoryScores = encodeJSON(result.Scores)
	event.Thresholds = encodeJSON(thresholds)
	event.Action = model.ContentModerationActionAllow
	if !flagged {
		s.metrics.passed.Add(1)
		if config.RecordNonHits {
			s.enqueueRecord(&record{event: &event})
		}
		return false, false
	}
	s.metrics.flagged.Add(1)
	event.InputExcerpt = Excerpt(item.input.Text)
	dispose := item.decision.Dispose
	if synchronous {
		switch item.decision.Action {
		case ActionBlock:
			event.Action = model.ContentModerationActionBlock
		case ActionReroute:
			event.Action = model.ContentModerationActionReroute
		}
	}
	if config.HashBlocklistEnabled && dispose {
		s.addHash(item.hash)
	}
	s.enqueueRecord(&record{event: &event, email: email, dispose: dispose, rememberHash: dispose, config: config.Clone()})
	return synchronous, false
}

// RecordViolation counts a finding of another engine as a violation of the
// user: it goes through disposal (violation count, automatic disabling,
// mail) with the current content moderation settings and is stored as a
// content moderation event.
func RecordViolation(event model.ContentModerationEvent, email string) {
	config := content_moderation_setting.Current().Config
	event.Flagged = true
	event.ConfigVersion = config.ConfigVersion
	defaultService.enqueueRecord(&record{event: &event, email: email, dispose: true, config: config.Clone()})
}

func encodeJSON(value any) string {
	encoded, err := common.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// logFailure logs at most one engine failure per minute and reports how many
// were suppressed in between. The message never contains the key.
func (s *Service) logFailure(requestID string, err error) {
	now := s.now().Unix()
	last := s.lastFailureLog.Load()
	if now-last < 60 || !s.lastFailureLog.CompareAndSwap(last, now) {
		s.suppressedFailures.Add(1)
		return
	}
	common.SysError(common.LogText("content moderation check failed for request %s, request allowed: %s (%d similar failures not logged)",
		requestID, err.Error(), s.suppressedFailures.Swap(0)))
}

// enqueueRecord hands an event to the recorder. A full buffer drops plain
// events at once; an event that needs disposal waits briefly first, and if
// it still cannot be queued the loss is counted as disposal_dropped, because
// it means a violation was not counted.
func (s *Service) enqueueRecord(item *record) {
	select {
	case s.records <- item:
		return
	default:
	}
	if item.dispose {
		timer := time.NewTimer(s.disposalWait)
		defer timer.Stop()
		select {
		case s.records <- item:
			return
		case <-timer.C:
		}
		s.metrics.disposalDropped.Add(1)
	}
	s.metrics.recordFailed.Add(1)
}

// recorder stores events. Plain events are written in batches; events that
// need disposal are handled one at a time, in order, so each violation count
// includes the events stored before it.
func (s *Service) recorder(ctx context.Context) {
	ticker := time.NewTicker(recordFlushInterval)
	defer ticker.Stop()
	batch := make([]*model.ContentModerationEvent, 0, recordBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		stored, err := model.CreateContentModerationEvents(batch)
		s.metrics.recorded.Add(int64(stored))
		if err != nil {
			s.metrics.recordFailed.Add(int64(len(batch) - stored))
			common.SysError(common.LogText("failed to record %d content moderation events: %s", len(batch)-stored, err.Error()))
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case item := <-s.records:
			if !item.dispose {
				batch = append(batch, item.event)
				if len(batch) >= recordBatchSize {
					flush()
				}
				continue
			}
			flush()
			s.dispose(item)
		case <-ticker.C:
			flush()
		}
	}
}

// dispose counts the user's violations, disables the user at the threshold,
// mails the user when configured, and stores the event.
func (s *Service) dispose(item *record) {
	event, config := item.event, &item.config
	if item.rememberHash && event.InputHash != "" && config.HashBlocklistEnabled {
		if err := model.AddContentModerationFlaggedHash(event.InputHash, event.CreatedAt); err != nil {
			common.SysError(common.LogText("failed to store a content moderation hash: %s", err.Error()))
		}
	}
	disabledNow := false
	if event.UserId > 0 {
		since := event.CreatedAt - int64(config.ViolationWindowHours)*3600
		if count, err := model.CountContentModerationViolations(event.UserId, since); err == nil {
			event.ViolationCount = int(count) + 1
		} else {
			event.ViolationCount = 1
			common.SysError(common.LogText("failed to count content moderation violations: %s", err.Error()))
		}
		if config.AutoDisableEnabled && event.ViolationCount >= config.DisableThreshold {
			disabledNow = s.disableUser(event, config)
		}
	}
	if config.EmailOnHit || disabledNow {
		event.EmailSent = s.notify(event, item.email, config, disabledNow)
	}
	if err := model.CreateContentModerationEvent(event); err != nil {
		s.metrics.recordFailed.Add(1)
		common.SysError(common.LogText("failed to record a content moderation event: %s", err.Error()))
		return
	}
	s.metrics.recorded.Add(1)
}

// disableUser disables a common user and records an audit log entry.
// Administrators are never disabled automatically. It reports whether the
// user was enabled before; the event is marked either way so the count
// restarts after it.
func (s *Service) disableUser(event *model.ContentModerationEvent, config *content_moderation_setting.Config) bool {
	user, err := model.GetUserById(event.UserId, false)
	if err != nil {
		common.SysError(common.LogText("content moderation could not load user %d: %s", event.UserId, err.Error()))
		return false
	}
	if user.Role >= common.RoleAdminUser {
		common.SysLog(common.LogText("content moderation skipped disabling administrator %d after %d violations", user.Id, event.ViolationCount))
		return false
	}
	event.AutoDisabled = true
	if user.Status == common.UserStatusDisabled {
		return false
	}
	update := model.User{Id: user.Id, Status: common.UserStatusDisabled}
	if err := update.Update(false); err != nil {
		// Update can fail after the status was committed (cache or session
		// revocation); only a status that did not change is a failure.
		common.SysError(common.LogText("content moderation failed to disable user %d: %s", user.Id, err.Error()))
		if reloaded, loadErr := model.GetUserById(user.Id, false); loadErr != nil || reloaded.Status != common.UserStatusDisabled {
			event.AutoDisabled = false
			return false
		}
	}
	if err := model.InvalidateUserTokensCache(user.Id); err != nil {
		common.SysError(common.LogText("failed to invalidate tokens cache for user %d: %s", user.Id, err.Error()))
	}
	s.metrics.autoDisabled.Add(1)
	params := map[string]any{
		"target_user_id": user.Id, "username": user.Username, "violations": event.ViolationCount,
		"threshold": config.DisableThreshold, "window_hours": config.ViolationWindowHours, "request_id": event.RequestId,
	}
	content := fmt.Sprintf("Content moderation disabled user %s (ID: %d) after %d flagged requests", user.Username, user.Id, event.ViolationCount)
	model.RecordAuditLog(nil, model.AuditLog{
		UserId: user.Id, Username: user.Username, ActorRole: common.RoleRootUser, Category: model.AuditCategoryOperation,
		Action: "content_moderation.auto_disable", Content: content, Success: true,
		Other: model.AuditOther{
			Op:        &model.AuditOperation{Action: "content_moderation.auto_disable", Params: params},
			AdminInfo: &model.AuditAdminInfo{AdminUsername: "content_moderation", AuthMethod: "system"},
		},
	})
	return true
}

// notify mails the user about a flagged input (at most once per hour per
// user) or about the account being disabled (always). The mail names no
// category or content.
func (s *Service) notify(event *model.ContentModerationEvent, email string, config *content_moderation_setting.Config, disabledNow bool) bool {
	if email == "" {
		if user, err := model.GetUserById(event.UserId, false); err == nil {
			email = user.Email
		}
	}
	if email == "" {
		return false
	}
	if !disabledNow {
		now := s.now()
		s.emailMu.Lock()
		last, seen := s.lastEmail[event.UserId]
		throttled := seen && now.Sub(last) < hitEmailInterval
		if !throttled {
			if len(s.lastEmail) >= maxHitEmailUsers {
				clear(s.lastEmail)
			}
			s.lastEmail[event.UserId] = now
		}
		s.emailMu.Unlock()
		if throttled {
			return false
		}
	}
	lang := model.GetUserLanguage(event.UserId)
	data := map[string]any{
		"SystemName": common.SystemName,
		"Count":      event.ViolationCount,
		"Threshold":  config.DisableThreshold,
		"Hours":      config.ViolationWindowHours,
	}
	subjectKey, bodyKey := i18n.MsgEmailContentModerationHitSubject, i18n.MsgEmailContentModerationHitBody
	if disabledNow {
		subjectKey, bodyKey = i18n.MsgEmailContentModerationDisabledSubject, i18n.MsgEmailContentModerationDisabledBody
	}
	content := "<p>" + html.EscapeString(i18n.Translate(lang, bodyKey, data)) + "</p>"
	if err := common.SendEmail(i18n.Translate(lang, subjectKey, data), email, content); err != nil {
		s.metrics.emailsFailed.Add(1)
		common.SysError(common.LogText("failed to send a content moderation notice to user %d: %s", event.UserId, err.Error()))
		return false
	}
	s.metrics.emailsSent.Add(1)
	return true
}

func (s *Service) hashListed(hash string) bool {
	s.hashMu.RLock()
	defer s.hashMu.RUnlock()
	_, listed := s.hashes[hash]
	return listed
}

func (s *Service) addHash(hash string) {
	s.hashMu.Lock()
	defer s.hashMu.Unlock()
	s.hashes[hash] = struct{}{}
}

// refreshHashes reloads the blocklist from the database, which holds the
// entries of every node and admin deletions. It reads the whole table only
// when a one-row summary shows the table changed since the last load, and
// at least every hashFullReloadEvery in case a change kept the summary.
func (s *Service) refreshHashes() error {
	version, err := model.ContentModerationFlaggedHashesVersion()
	if err != nil {
		return err
	}
	fresh := s.now().Unix()-s.hashLoadedAt.Load() < int64(hashFullReloadEvery/time.Second)
	if s.hashLoaded.Load() && fresh && s.hashVersion.Load() == version {
		return nil
	}
	hashes, err := model.ListContentModerationFlaggedHashes()
	if err != nil {
		return err
	}
	loaded := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		loaded[hash] = struct{}{}
	}
	s.hashMu.Lock()
	s.hashes = loaded
	s.hashMu.Unlock()
	s.hashVersion.Store(version)
	s.hashLoadedAt.Store(s.now().Unix())
	s.hashLoaded.Store(true)
	return nil
}

// hashBlocklistInUse reports whether any request can consult the blocklist:
// it is enabled and either the global mode is on or a replaced ScopePolicy
// may moderate requests while the global mode is off.
func hashBlocklistInUse(config *content_moderation_setting.Config) bool {
	if !config.HashBlocklistEnabled {
		return false
	}
	_, defaultPolicy := CurrentScopePolicy().(ConfigScopePolicy)
	return config.Mode != content_moderation_setting.ModeOff || !defaultPolicy
}

// ForgetHash removes a hash from this node's cache after an admin deletion.
func ForgetHash(hash string) {
	defaultService.hashMu.Lock()
	defer defaultService.hashMu.Unlock()
	delete(defaultService.hashes, hash)
}

// ForgetAllHashes empties this node's cache after the blocklist was cleared.
func ForgetAllHashes() {
	defaultService.hashMu.Lock()
	defer defaultService.hashMu.Unlock()
	defaultService.hashes = map[string]struct{}{}
}

// maintenanceLoop refreshes the hash blocklist while it is enabled, drops the
// health of removed keys, and runs the daily retention cleanup.
func (s *Service) maintenanceLoop(ctx context.Context) {
	hashTicker := time.NewTicker(hashRefreshInterval)
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer hashTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		// Lists and the blocklist are loaded whenever the settings use them,
		// independent of the global mode, because a replaced ScopePolicy may
		// moderate requests while the global mode is off.
		snapshot := content_moderation_setting.Current()
		// A replaced ScopePolicy (the audit policy rules) may use keyword
		// lists of its own.
		keywordPolicy, ok := CurrentScopePolicy().(KeywordListUser)
		if snapshot.Config.KeywordMode != content_moderation_setting.KeywordModeAPIOnly || len(snapshot.Config.KeywordLists) > 0 ||
			ok && keywordPolicy.UsesKeywordLists() {
			if err := s.reloadKeywords(false); err != nil {
				common.SysError(common.LogText("failed to load content moderation keyword lists: %s", err.Error()))
			}
		}
		if hashBlocklistInUse(&snapshot.Config) && !s.hashLoaded.Load() {
			if err := s.refreshHashes(); err != nil {
				common.SysError(common.LogText("failed to load the content moderation hash blocklist: %s", err.Error()))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-hashTicker.C:
			s.engine.forget(snapshot.Keys)
			if !hashBlocklistInUse(&snapshot.Config) {
				s.hashLoaded.Store(false)
				continue
			}
			if err := s.refreshHashes(); err != nil {
				common.SysError(common.LogText("failed to refresh the content moderation hash blocklist: %s", err.Error()))
			}
		case <-cleanupTicker.C:
			if !common.IsMasterNode || s.now().Unix()-s.lastCleanup.Load() < int64(cleanupEvery/time.Second) {
				continue
			}
			s.lastCleanup.Store(s.now().Unix())
			if _, err := s.cleanup(); err != nil {
				common.SysError(common.LogText("failed to clean up content moderation events: %s", err.Error()))
			}
		}
	}
}

// cleanup deletes events past their retention, and blocklisted hashes older
// than the flagged-event retention or beyond the blocklist size limit. It
// also runs while moderation is off so stored excerpts still expire.
func (s *Service) cleanup() (int64, error) {
	config := content_moderation_setting.Current().Config
	now := s.now()
	hitBefore := now.Add(-time.Duration(config.HitRetentionDays) * 24 * time.Hour).Unix()
	deleted, err := model.CleanupContentModerationEvents(hitBefore,
		now.Add(-time.Duration(config.NonHitRetentionDays)*24*time.Hour).Unix())
	if err != nil {
		return deleted, err
	}
	hashes, err := model.CleanupContentModerationFlaggedHashes(hitBefore, maxFlaggedHashes)
	return deleted + hashes, err
}

// RuntimeStatus is the operational view shown to administrators.
type RuntimeStatus struct {
	Mode             string      `json:"mode"`
	ConfigVersion    int64       `json:"config_version"`
	WorkerCount      int         `json:"worker_count"`
	ActiveWorkers    int64       `json:"active_workers"`
	QueueDepth       int64       `json:"queue_depth"`
	QueueSize        int         `json:"queue_size"`
	QueuedBytes      int64       `json:"queued_bytes"`
	QueuedBytesLimit int64       `json:"queued_bytes_limit"`
	HashBlocklist    int         `json:"hash_blocklist"`
	Checked          int64       `json:"checked"`
	Skipped          int64       `json:"skipped"`
	SkippedTooLarge  int64       `json:"skipped_too_large"`
	SkippedEmpty     int64       `json:"skipped_empty"`
	SampledOut       int64       `json:"sampled_out"`
	NoKeys           int64       `json:"no_keys"`
	Enqueued         int64       `json:"enqueued"`
	Dropped          int64       `json:"dropped"`
	Passed           int64       `json:"passed"`
	Flagged          int64       `json:"flagged"`
	Blocked          int64       `json:"blocked"`
	HashBlocked      int64       `json:"hash_blocked"`
	KeywordHits      int64       `json:"keyword_hits"`
	Failed           int64       `json:"failed"`
	Timeouts         int64       `json:"timeouts"`
	Calls            int64       `json:"calls"`
	AutoDisabled     int64       `json:"auto_disabled"`
	EmailsSent       int64       `json:"emails_sent"`
	EmailsFailed     int64       `json:"emails_failed"`
	Recorded         int64       `json:"recorded"`
	RecordFailed     int64       `json:"record_failed"`
	DisposalDropped  int64       `json:"disposal_dropped"`
	LatencyAvgMS     int64       `json:"latency_avg_ms"`
	LatencyMaxMS     int64       `json:"latency_max_ms"`
	SyncAvgMS        int64       `json:"sync_avg_ms"`
	Rerouted         int64       `json:"rerouted"`
	UnavailableBlock int64       `json:"unavailable_blocked"`
	Keys             []KeyStatus `json:"keys"`
}

func Runtime() RuntimeStatus { return defaultService.runtime() }

func (s *Service) runtime() RuntimeStatus {
	snapshot := content_moderation_setting.Current()
	config := snapshot.Config
	m := &s.metrics
	s.hashMu.RLock()
	hashCount := len(s.hashes)
	s.hashMu.RUnlock()
	status := RuntimeStatus{
		Mode: config.Mode, ConfigVersion: config.ConfigVersion, WorkerCount: config.WorkerCount,
		ActiveWorkers: s.active.Load(), QueueDepth: s.depth.Load(), QueueSize: config.QueueSize,
		QueuedBytes: s.queuedBytes.Load(), QueuedBytesLimit: maxQueuedBytes, HashBlocklist: hashCount,
		Checked: m.checked.Load(), Skipped: m.skipped.Load(), SkippedTooLarge: m.tooLarge.Load(),
		SkippedEmpty: m.empty.Load(), SampledOut: m.sampledOut.Load(), NoKeys: m.noKeys.Load(),
		Enqueued: m.enqueued.Load(), Dropped: m.dropped.Load(), Passed: m.passed.Load(), Flagged: m.flagged.Load(),
		Blocked: m.blocked.Load(), HashBlocked: m.hashBlocked.Load(), KeywordHits: m.keywordHits.Load(), Failed: m.failed.Load(), Timeouts: m.timeouts.Load(),
		Calls: m.calls.Load(), AutoDisabled: m.autoDisabled.Load(), EmailsSent: m.emailsSent.Load(),
		EmailsFailed: m.emailsFailed.Load(), Recorded: m.recorded.Load(), RecordFailed: m.recordFailed.Load(), DisposalDropped: m.disposalDropped.Load(),
		LatencyMaxMS: m.latencyMaxMS.Load(), Keys: s.engine.keyStatuses(snapshot.Keys),
		Rerouted: m.rerouted.Load(), UnavailableBlock: m.unavailableBlocked.Load(),
	}
	if completed := status.Passed + status.Flagged; completed > 0 {
		status.LatencyAvgMS = m.latencyTotalMS.Load() / completed
	}
	if checks := m.syncChecks.Load(); checks > 0 {
		status.SyncAvgMS = m.syncTotalMS.Load() / checks
	}
	return status
}

// ProbeKeyResult reports one test call with one key.
type ProbeKeyResult struct {
	ID         string `json:"id"`
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
}

// ProbeResult reports a test of the engine settings.
type ProbeResult struct {
	Keys            []ProbeKeyResult   `json:"keys"`
	Flagged         bool               `json:"flagged"`
	HighestCategory string             `json:"highest_category,omitempty"`
	HighestScore    float64            `json:"highest_score"`
	Scores          map[string]float64 `json:"scores,omitempty"`
}

// Probe sends the text once with every key, using a throwaway client so
// probing an address does not touch the shared client or key health.
func Probe(ctx context.Context, config content_moderation_setting.Config, keys []string, text string) ProbeResult {
	client := newHTTPClient(time.Duration(config.TimeoutMS) * time.Millisecond)
	defer client.CloseIdleConnections()
	result := ProbeResult{Keys: make([]ProbeKeyResult, 0, len(keys))}
	for _, key := range keys {
		started := time.Now()
		moderation, err := callOnce(ctx, client, &config, key, text)
		probe := ProbeKeyResult{ID: content_moderation_setting.KeyID(key), LatencyMS: time.Since(started).Milliseconds()}
		if err != nil {
			probe.Error = err.Error()
			var engineErr *engineError
			if errors.As(err, &engineErr) {
				probe.HTTPStatus = engineErr.HTTPStatus
			}
			result.Keys = append(result.Keys, probe)
			continue
		}
		probe.OK = true
		result.Keys = append(result.Keys, probe)
		if result.Scores == nil {
			result.Scores = moderation.Scores
			result.Flagged, result.HighestCategory, result.HighestScore = evaluate(moderation.Scores, config.Thresholds)
		}
	}
	return result
}
