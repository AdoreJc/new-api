package promptaudit

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Request structure statistics measure how requests sent to prompt audit are
// built: which kinds of text they carry, a few structural signals, and a hash
// of the normalized system prompt. They are off by default, store no prompt
// text and never take part in an audit decision.
//
// The request path only hands over texts the audit already extracted. A
// background loop analyses them, adds the result to in-memory counters and
// writes those to the database in batches. A full hand-over queue or a failed
// write drops statistics; it never affects the request or its audit.

const (
	structureQueueSize = 256
	// structureQueuedLimit bounds the text held by the hand-over queue.
	structureQueuedLimit   = 32 << 20
	structureFlushInterval = 15 * time.Second
	// structurePendingFingerprints bounds the distinct fingerprints kept in
	// memory between two flushes.
	structurePendingFingerprints = 1000
)

// structureSubject names who sent a request and how, for its statistics.
type structureSubject struct {
	userID   int
	protocol string
	client   string
}

type structureJob struct {
	subject  structureSubject
	segments []promptSegment
	turns    int
	size     int64
}

type structureDailyKey struct {
	day      int
	protocol string
	client   string
}

type structureStats struct {
	jobs        chan structureJob
	queuedBytes atomic.Int64
	observed    atomic.Int64
	dropped     atomic.Int64
	writeFailed atomic.Int64

	mu           sync.Mutex
	fingerprints map[string]*model.PromptAuditFingerprintDelta
	daily        map[structureDailyKey]*model.PromptAuditStructureDaily
}

func newStructureStats() *structureStats {
	return &structureStats{
		jobs:         make(chan structureJob, structureQueueSize),
		fingerprints: map[string]*model.PromptAuditFingerprintDelta{},
		daily:        map[structureDailyKey]*model.PromptAuditStructureDaily{},
	}
}

// submit hands the extracted texts of a request to the background loop. It
// never blocks: when the queue is full the request is not measured.
func (s *structureStats) submit(subject structureSubject, segments []promptSegment, turns int) {
	var size int64
	for _, segment := range segments {
		size += int64(len(segment.text))
	}
	if s.queuedBytes.Add(size) > structureQueuedLimit {
		s.queuedBytes.Add(-size)
		s.dropped.Add(1)
		return
	}
	select {
	case s.jobs <- structureJob{subject: subject, segments: segments, turns: turns, size: size}:
	default:
		s.queuedBytes.Add(-size)
		s.dropped.Add(1)
	}
}

// record analyses one request and adds it to the in-memory counters.
func (s *structureStats) record(item structureJob, now time.Time) {
	s.queuedBytes.Add(-item.size)
	report, ok := analyzeStructure(item.segments, item.turns)
	if !ok {
		return
	}
	client := item.subject.client
	if client == "" {
		client = ClientOther
	}
	key := structureDailyKey{day: structureDay(now), protocol: item.subject.protocol, client: client}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed.Add(1)
	day := s.daily[key]
	if day == nil {
		day = &model.PromptAuditStructureDaily{StatDay: key.day, RequestFormat: key.protocol, ClientClass: key.client}
		s.daily[key] = day
	}
	day.Requests++
	for _, counter := range []struct {
		set   bool
		count *int64
	}{
		{report.HasSystem, &day.WithSystem}, {report.HasDeveloper, &day.WithDeveloper},
		{report.HasHistoryUser, &day.WithHistoryUser}, {report.HasHistoryAssistant, &day.WithHistoryAssistant},
		{report.TrailingAssistant, &day.TrailingAssistant}, {report.HasAgentContext, &day.WithAgentContext},
		{report.HasTool, &day.WithTool}, {report.AssistantTurns >= manyAssistantTurns, &day.AssistantTurns10Plus},
		{report.MidSystem, &day.MidSystem}, {report.ControlTokens, &day.ControlTokens}, {report.RoleMarkers, &day.RoleMarkers},
		{report.InvisibleChars > 0, &day.InvisibleRequests}, {report.EncodedRuns > 0, &day.EncodedRequests},
	} {
		if counter.set {
			*counter.count++
		}
	}
	day.AssistantTurns += int64(report.AssistantTurns)
	day.UserTurns += int64(report.UserTurns)
	day.InvisibleChars += int64(report.InvisibleChars)
	day.EncodedRuns += int64(report.EncodedRuns)
	if report.HasSystem || report.HasDeveloper {
		switch {
		case report.SystemLength < 1000:
			day.SystemUnder1k++
		case report.SystemLength < 4000:
			day.SystemUnder4k++
		case report.SystemLength < 16000:
			day.SystemUnder16k++
		case report.SystemLength < 64000:
			day.SystemUnder64k++
		default:
			day.System64kPlus++
		}
	}

	fingerprint := s.fingerprints[report.Fingerprint]
	if fingerprint == nil {
		if len(s.fingerprints) >= structurePendingFingerprints {
			s.dropped.Add(1)
			return
		}
		fingerprint = &model.PromptAuditFingerprintDelta{Fingerprint: report.Fingerprint, PrefixHash: report.PrefixHash, FirstSeenAt: now.Unix()}
		s.fingerprints[report.Fingerprint] = fingerprint
	}
	fingerprint.Requests++
	fingerprint.LastSeenAt = now.Unix()
	fingerprint.SystemLength = report.SystemLength
	fingerprint.RequestFormat, fingerprint.ClientClass = key.protocol, key.client
	if len(fingerprint.UserIds) < model.PromptAuditFingerprintUserCap && !slices.Contains(fingerprint.UserIds, item.subject.userID) {
		fingerprint.UserIds = append(fingerprint.UserIds, item.subject.userID)
	}
}

// structureDay is the server-local date as yyyymmdd.
func structureDay(moment time.Time) int {
	return moment.Year()*10000 + int(moment.Month())*100 + moment.Day()
}

// flush writes the in-memory counters to the database and clears them. A
// failed write loses that batch, so memory stays bounded.
func (s *structureStats) flush() {
	s.mu.Lock()
	fingerprints := make([]*model.PromptAuditFingerprintDelta, 0, len(s.fingerprints))
	for _, fingerprint := range s.fingerprints {
		fingerprints = append(fingerprints, fingerprint)
	}
	daily := make([]*model.PromptAuditStructureDaily, 0, len(s.daily))
	for _, day := range s.daily {
		daily = append(daily, day)
	}
	clear(s.fingerprints)
	clear(s.daily)
	s.mu.Unlock()
	if len(fingerprints) == 0 && len(daily) == 0 {
		return
	}
	skipped, err := model.ApplyPromptAuditStructureStats(fingerprints, daily)
	s.dropped.Add(int64(skipped))
	if err != nil {
		s.writeFailed.Add(1)
		common.SysError(common.LogText("failed to store prompt audit structure statistics: %s", err.Error()))
	}
}

// drain analyses every queued request without waiting for more.
func (s *structureStats) drain(now func() time.Time) {
	for {
		select {
		case item := <-s.jobs:
			s.record(item, now())
		default:
			return
		}
	}
}

func (s *Service) structureLoop(ctx context.Context) {
	ticker := time.NewTicker(structureFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flushStructure()
			return
		case item := <-s.structure.jobs:
			s.structure.record(item, s.now())
		case <-ticker.C:
			s.structure.flush()
		}
	}
}

// flushStructure stores what is queued and counted so far.
func (s *Service) flushStructure() {
	s.structure.drain(s.now)
	s.structure.flush()
}

// Shutdown stores the request structure statistics still held in memory. It
// is called once when the process exits.
func Shutdown() { defaultService.flushStructure() }
