package service

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

// Passive usage sampling for Claude Subscription (OAuth) channels.
//
// Every Messages response from Anthropic carries rate-limit headers for each
// usage window:
//
//	anthropic-ratelimit-unified-5h-utilization     -> five_hour
//	anthropic-ratelimit-unified-7d-utilization     -> seven_day
//	anthropic-ratelimit-unified-7d_oi-utilization  -> seven_day_overage_included (the Fable 7-day window)
//	anthropic-ratelimit-unified-<window>-reset     -> resets_at (unix seconds)
//
// The utilization header is a 0-1 fraction; GET /api/oauth/usage reports the
// same windows as 0-100 percentages. Samples are stored as 0-100 so they can
// be dropped into the usage document unchanged. The usage API does not always
// return seven_day_overage_included, so the sampled value is used to fill in
// the windows the API left out.

const (
	// ClaudePassiveUsageKey is the channel OtherInfo key holding the samples:
	// {"five_hour": {"utilization": 12.5, "resets_at": 1762300000, "sampled_at": 1762290000}, ...}.
	ClaudePassiveUsageKey = "claude_passive_usage"
	// ClaudePassiveUsageSource marks a window that came from sampled headers
	// rather than from the usage API.
	ClaudePassiveUsageSource = "passive"
	// ClaudePassiveUsageMaxAge is how long a sample stays usable for backfill
	// when the channel sees no further traffic.
	ClaudePassiveUsageMaxAge = 24 * time.Hour
	// claudePassiveUsageWriteInterval bounds the database writes per channel;
	// samples arriving sooner after a write are dropped.
	claudePassiveUsageWriteInterval = 30 * time.Second
)

// ClaudePassiveUsageWindow is one sampled window.
type ClaudePassiveUsageWindow struct {
	// 0-100, same unit as the usage API (header fraction x 100)
	Utilization float64 `json:"utilization"`
	// Unix seconds; 0 when the reset header was absent
	ResetsAt int64 `json:"resets_at,omitempty"`
	// Unix seconds of the response that carried the headers
	SampledAt int64 `json:"sampled_at"`
}

var claudePassiveUsageHeaderWindows = []struct {
	header string
	key    string
}{
	{header: "5h", key: "five_hour"},
	{header: "7d", key: "seven_day"},
	{header: "7d_oi", key: "seven_day_overage_included"},
}

// ParseClaudePassiveUsageHeaders reads the rate-limit headers of one upstream
// response. A window needs a well-formed utilization header; the reset header
// is optional. Malformed values are ignored.
func ParseClaudePassiveUsageHeaders(headers http.Header, now time.Time) map[string]ClaudePassiveUsageWindow {
	windows := map[string]ClaudePassiveUsageWindow{}
	for _, w := range claudePassiveUsageHeaderWindows {
		prefix := "anthropic-ratelimit-unified-" + w.header + "-"
		fraction, err := strconv.ParseFloat(strings.TrimSpace(headers.Get(prefix+"utilization")), 64)
		if err != nil || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0 {
			continue
		}
		window := ClaudePassiveUsageWindow{Utilization: fraction * 100, SampledAt: now.Unix()}
		if reset, err := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"reset")), 10, 64); err == nil && reset > 0 {
			// Anthropic sends unix seconds; tolerate milliseconds.
			if reset > 1e11 {
				reset /= 1000
			}
			window.ResetsAt = reset
		}
		windows[w.key] = window
	}
	return windows
}

// ClaudePassiveUsageSampler throttles the per-channel persistence of sampled
// headers.
type ClaudePassiveUsageSampler struct {
	interval  time.Duration
	mu        sync.Mutex
	lastWrite map[int]time.Time
}

func NewClaudePassiveUsageSampler(interval time.Duration) *ClaudePassiveUsageSampler {
	return &ClaudePassiveUsageSampler{interval: interval, lastWrite: map[int]time.Time{}}
}

// Sample parses the headers and reports whether the channel is due for a
// write. A response without usable headers never consumes the write slot.
func (s *ClaudePassiveUsageSampler) Sample(channelId int, headers http.Header, now time.Time) (map[string]ClaudePassiveUsageWindow, bool) {
	windows := ParseClaudePassiveUsageHeaders(headers, now)
	if len(windows) == 0 {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastWrite[channelId]; ok && now.Sub(last) < s.interval {
		return windows, false
	}
	s.lastWrite[channelId] = now
	return windows, true
}

var claudePassiveUsageSampler = NewClaudePassiveUsageSampler(claudePassiveUsageWriteInterval)

// RecordClaudePassiveUsage stores the rate-limit headers of a relayed
// response in the channel's OtherInfo. The write is throttled per channel
// and runs in the background so the relay is never delayed.
func RecordClaudePassiveUsage(channelId int, headers http.Header) {
	if channelId <= 0 || headers == nil {
		return
	}
	windows, due := claudePassiveUsageSampler.Sample(channelId, headers, time.Now())
	if !due {
		return
	}
	gopool.Go(func() {
		if err := model.UpdateChannelOtherInfoEntry(channelId, ClaudePassiveUsageKey, windows); err != nil {
			common.SysLog(common.LogText("failed to store claude passive usage: channel_id=%d, error=%v", channelId, err))
			return
		}
		NotifyChannelAutoResetUsageSample(channelId)
	})
}

// LoadClaudePassiveUsage reads the stored samples of a channel; malformed
// entries yield an empty map.
func LoadClaudePassiveUsage(ch *model.Channel) map[string]ClaudePassiveUsageWindow {
	windows := map[string]ClaudePassiveUsageWindow{}
	if ch == nil {
		return windows
	}
	raw, ok := ch.GetOtherInfo()[ClaudePassiveUsageKey]
	if !ok {
		return windows
	}
	encoded, err := common.Marshal(raw)
	if err != nil || common.Unmarshal(encoded, &windows) != nil {
		return map[string]ClaudePassiveUsageWindow{}
	}
	return windows
}

// BackfillClaudeUsageWindows adds the sampled windows the usage document
// lacks. A window the API returned is left untouched; a sample past its
// reset time or older than ClaudePassiveUsageMaxAge is not used. Backfilled
// windows carry source "passive" and the sample time.
func BackfillClaudeUsageWindows(doc map[string]any, passive map[string]ClaudePassiveUsageWindow, now time.Time) {
	if doc == nil {
		return
	}
	for key, window := range passive {
		if existing, ok := doc[key].(map[string]any); ok && existing != nil {
			continue
		}
		if window.SampledAt <= 0 || now.Sub(time.Unix(window.SampledAt, 0)) > ClaudePassiveUsageMaxAge {
			continue
		}
		if window.ResetsAt > 0 && !now.Before(time.Unix(window.ResetsAt, 0)) {
			continue
		}
		entry := map[string]any{
			"utilization": window.Utilization,
			"source":      ClaudePassiveUsageSource,
			"sampled_at":  time.Unix(window.SampledAt, 0).UTC().Format(time.RFC3339),
		}
		if window.ResetsAt > 0 {
			entry["resets_at"] = time.Unix(window.ResetsAt, 0).UTC().Format(time.RFC3339)
		}
		doc[key] = entry
	}
}
