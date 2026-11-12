package contentaudit

import (
	"slices"
	"sync/atomic"
	"time"
)

const latencySampleSize = 4096

// latencyWindow keeps the most recent samples in a lock-free ring so the
// request path never waits on a mutex.
type latencyWindow struct {
	next    atomic.Uint64
	samples [latencySampleSize]atomic.Int64
}

func (w *latencyWindow) record(duration time.Duration) {
	index := w.next.Add(1) - 1
	// Store nanoseconds + 1 so an empty slot (0) is distinguishable.
	w.samples[index%latencySampleSize].Store(int64(duration) + 1)
}

type LatencySummary struct {
	P50     float64 `json:"p50"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
	Max     float64 `json:"max"`
	Samples int     `json:"samples"`
}

// summary reports the recent percentiles in the given unit.
func (w *latencyWindow) summary(unit time.Duration) LatencySummary {
	values := make([]int64, 0, latencySampleSize)
	for index := range w.samples {
		if value := w.samples[index].Load(); value > 0 {
			values = append(values, value-1)
		}
	}
	if len(values) == 0 {
		return LatencySummary{}
	}
	slices.Sort(values)
	at := func(quantile float64) float64 {
		position := min(int(quantile*float64(len(values))), len(values)-1)
		return float64(values[position]) / float64(unit)
	}
	return LatencySummary{P50: at(0.50), P95: at(0.95), P99: at(0.99), Max: float64(values[len(values)-1]) / float64(unit), Samples: len(values)}
}

type counters struct {
	enqueued                 atomic.Int64
	droppedQueueFull         atomic.Int64
	droppedQueueBytes        atomic.Int64
	skippedOutOfScope        atomic.Int64
	skippedBodyTooLarge      atomic.Int64
	skippedUnsupportedFormat atomic.Int64
	skippedInvalidJSON       atomic.Int64
	skippedNoText            atomic.Int64
	scanned                  atomic.Int64
	allowed                  atomic.Int64
	warned                   atomic.Int64
	blocked                  atomic.Int64
	failed                   atomic.Int64
	retries                  atomic.Int64
	failovers                atomic.Int64
	credentialPatternHits    atomic.Int64
	eventsWritten            atomic.Int64
	eventWriteFailed         atomic.Int64
}

func (c *counters) addRetry()    { c.retries.Add(1) }
func (c *counters) addFailover() { c.failovers.Add(1) }

type CounterSnapshot struct {
	Enqueued                 int64 `json:"enqueued"`
	DroppedQueueFull         int64 `json:"dropped_queue_full"`
	DroppedQueueBytes        int64 `json:"dropped_queue_bytes"`
	SkippedOutOfScope        int64 `json:"skipped_out_of_scope"`
	SkippedBodyTooLarge      int64 `json:"skipped_body_too_large"`
	SkippedUnsupportedFormat int64 `json:"skipped_unsupported_format"`
	SkippedInvalidJSON       int64 `json:"skipped_invalid_json"`
	SkippedNoText            int64 `json:"skipped_no_text"`
	Scanned                  int64 `json:"scanned"`
	Allowed                  int64 `json:"allowed"`
	Warned                   int64 `json:"warned"`
	Blocked                  int64 `json:"blocked"`
	Failed                   int64 `json:"failed"`
	Retries                  int64 `json:"retries"`
	Failovers                int64 `json:"failovers"`
	CredentialPatternHits    int64 `json:"credential_pattern_hits"`
	EventsWritten            int64 `json:"events_written"`
	EventWriteFailed         int64 `json:"event_write_failed"`
}

func (c *counters) snapshot() CounterSnapshot {
	return CounterSnapshot{
		Enqueued:                 c.enqueued.Load(),
		DroppedQueueFull:         c.droppedQueueFull.Load(),
		DroppedQueueBytes:        c.droppedQueueBytes.Load(),
		SkippedOutOfScope:        c.skippedOutOfScope.Load(),
		SkippedBodyTooLarge:      c.skippedBodyTooLarge.Load(),
		SkippedUnsupportedFormat: c.skippedUnsupportedFormat.Load(),
		SkippedInvalidJSON:       c.skippedInvalidJSON.Load(),
		SkippedNoText:            c.skippedNoText.Load(),
		Scanned:                  c.scanned.Load(),
		Allowed:                  c.allowed.Load(),
		Warned:                   c.warned.Load(),
		Blocked:                  c.blocked.Load(),
		Failed:                   c.failed.Load(),
		Retries:                  c.retries.Load(),
		Failovers:                c.failovers.Load(),
		CredentialPatternHits:    c.credentialPatternHits.Load(),
		EventsWritten:            c.eventsWritten.Load(),
		EventWriteFailed:         c.eventWriteFailed.Load(),
	}
}
