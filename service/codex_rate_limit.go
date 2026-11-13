package service

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Usage limits of Codex (ChatGPT OAuth) channels. Every answer of the ChatGPT
// backend carries the account's two usage windows:
//
//	x-codex-{primary,secondary}-used-percent         0-100
//	x-codex-{primary,secondary}-reset-after-seconds  seconds until the reset
//	x-codex-{primary,secondary}-window-minutes       300 (5h) or 10080 (7d)
//
// Which of primary and secondary is the 5-hour window is decided by the window
// length, as sub2api OpenAICodexUsageSnapshot.Normalize does. A usage-limit
// 429 also names the reset in its body: {"error":{"type":"usage_limit_reached",
// "resets_at":..., "resets_in_seconds":...}}.

const (
	// CodexPassiveUsageKey is the channel OtherInfo key holding the windows
	// sampled from relayed answers, in the shape of the Claude samples
	// ({"five_hour": {...}, "seven_day": {...}}).
	CodexPassiveUsageKey = "codex_passive_usage"
	// codexUsageLimitFallbackCooldown applies when a usage-limit answer does
	// not say when the limit resets (sub2api's default 429 cooldown).
	codexUsageLimitFallbackCooldown = 60 * time.Second
	codexPassiveUsageWriteInterval  = 30 * time.Second
	// codexFiveHourWindowMaxMinutes separates the 5-hour window from the
	// weekly one when only one window length is known.
	codexFiveHourWindowMaxMinutes = 360
	// ginKeyCodexUsageLimitResetAt holds the reset time (unix seconds) a
	// usage-limit body of the current attempt named, 0 when none.
	ginKeyCodexUsageLimitResetAt = "codex_usage_limit_reset_at"
)

type codexHeaderWindow struct {
	usedPercent   float64
	hasUsed       bool
	resetAt       int64
	windowMinutes int
	hasMinutes    bool
}

func parseCodexHeaderWindow(headers http.Header, prefix string, now time.Time) (codexHeaderWindow, bool) {
	var w codexHeaderWindow
	found := false
	if used, err := strconv.ParseFloat(strings.TrimSpace(headers.Get(prefix+"-used-percent")), 64); err == nil && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 0 {
		w.usedPercent, w.hasUsed, found = used, true, true
	}
	if after, err := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"-reset-after-seconds")), 10, 64); err == nil && after >= 0 {
		w.resetAt, found = now.Unix()+after, true
	} else if at, err := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"-reset-at")), 10, 64); err == nil && at > 0 {
		w.resetAt, found = at, true
	}
	if minutes, err := strconv.Atoi(strings.TrimSpace(headers.Get(prefix + "-window-minutes"))); err == nil && minutes > 0 {
		w.windowMinutes, w.hasMinutes, found = minutes, true, true
	}
	return w, found
}

// ParseCodexUsageWindows reads the x-codex-* usage headers into the 5-hour
// ("five_hour") and weekly ("seven_day") windows. A window needs a used
// percentage.
func ParseCodexUsageWindows(headers http.Header, now time.Time) map[string]ClaudePassiveUsageWindow {
	windows := map[string]ClaudePassiveUsageWindow{}
	primary, hasPrimary := parseCodexHeaderWindow(headers, "x-codex-primary", now)
	secondary, hasSecondary := parseCodexHeaderWindow(headers, "x-codex-secondary", now)
	if !hasPrimary && !hasSecondary {
		return windows
	}
	// Without window lengths the legacy layout applies: primary is weekly.
	primaryIsFiveHour := false
	switch {
	case primary.hasMinutes && secondary.hasMinutes:
		primaryIsFiveHour = primary.windowMinutes < secondary.windowMinutes
	case primary.hasMinutes:
		primaryIsFiveHour = primary.windowMinutes <= codexFiveHourWindowMaxMinutes
	case secondary.hasMinutes:
		primaryIsFiveHour = secondary.windowMinutes > codexFiveHourWindowMaxMinutes
	}
	keys := map[bool]string{true: "five_hour", false: "seven_day"}
	for _, entry := range []struct {
		window   codexHeaderWindow
		ok       bool
		fiveHour bool
	}{
		{window: primary, ok: hasPrimary, fiveHour: primaryIsFiveHour},
		{window: secondary, ok: hasSecondary, fiveHour: !primaryIsFiveHour},
	} {
		if !entry.ok || !entry.window.hasUsed {
			continue
		}
		windows[keys[entry.fiveHour]] = ClaudePassiveUsageWindow{
			Utilization: entry.window.usedPercent,
			ResetsAt:    entry.window.resetAt,
			SampledAt:   now.Unix(),
		}
	}
	return windows
}

// codexExhaustedWindow returns the exhausted window (used >= 100) that stays
// locked longest, as "5h" or "7d", with its reset time. A 429 whose windows
// are below 100% does not prove the account is exhausted.
func codexExhaustedWindow(headers http.Header, now time.Time) (string, int64, bool) {
	windows := ParseCodexUsageWindows(headers, now)
	for _, candidate := range []struct{ key, label string }{{"seven_day", "7d"}, {"five_hour", "5h"}} {
		w, ok := windows[candidate.key]
		if !ok || w.Utilization < 100 || w.ResetsAt <= now.Unix() || time.Unix(w.ResetsAt, 0).Sub(now) > subscriptionLimitResetMaxAhead {
			continue
		}
		return candidate.label, w.ResetsAt, true
	}
	return "", 0, false
}

// parseCodexUsageLimitBodyReset reads the reset time of a usage-limit error
// body, 0 when the body names none.
func parseCodexUsageLimitBodyReset(body []byte, now time.Time) int64 {
	var payload struct {
		Error struct {
			Type            string `json:"type"`
			Code            any    `json:"code"`
			ResetsAt        any    `json:"resets_at"`
			ResetsInSeconds any    `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if common.Unmarshal(body, &payload) != nil {
		return 0
	}
	kind := payload.Error.Type
	if code, ok := payload.Error.Code.(string); ok && kind == "" {
		kind = code
	}
	if kind != "usage_limit_reached" && kind != "rate_limit_exceeded" {
		return 0
	}
	if at, ok := codexNumber(payload.Error.ResetsAt); ok && at > now.Unix() {
		return at
	}
	if in, ok := codexNumber(payload.Error.ResetsInSeconds); ok && in > 0 {
		return now.Unix() + in
	}
	return 0
}

func codexNumber(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// RecordCodexUsageLimitBody keeps the reset time named by the error body of
// the current attempt (nil body clears it) for MarkCodexChannelCooldown.
func RecordCodexUsageLimitBody(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	var resetAt int64
	if len(body) > 0 {
		resetAt = parseCodexUsageLimitBodyReset(body, time.Now())
	}
	c.Set(ginKeyCodexUsageLimitResetAt, resetAt)
}

// IsCodexUsageLimitError reports a usage-limit failure of a Codex account: a
// 429, or a usage_limit_reached / rate_limit_exceeded error (which a bare
// stream error event carries with any status).
func IsCodexUsageLimitError(apiErr *types.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	if apiErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	switch apiErr.ToOpenAIError().Type {
	case "usage_limit_reached", "rate_limit_exceeded":
		return true
	}
	return false
}

// MarkCodexChannelCooldown keeps a single-account Codex channel whose
// attempt failed on its usage limit out of channel selection until the
// limit resets: the exhausted window of the x-codex-* headers first, then
// the reset named by the error body, otherwise a short fallback.
func MarkCodexChannelCooldown(c *gin.Context, channelError types.ChannelError, apiErr *types.NewAPIError) {
	if channelError.ChannelType != constant.ChannelTypeCodex || channelError.IsMultiKey || !IsCodexUsageLimitError(apiErr) {
		return
	}
	now := time.Now()
	headers, _ := common.GetContextKeyType[http.Header](c, constant.ContextKeyUpstreamRateLimitHeaders)
	window, resetAt, ok := codexExhaustedWindow(headers, now)
	if !ok {
		window = ""
		resetAt = c.GetInt64(ginKeyCodexUsageLimitResetAt)
	}
	if resetAt <= now.Unix() || time.Unix(resetAt, 0).Sub(now) > subscriptionLimitResetMaxAhead {
		resetAt = now.Add(codexUsageLimitFallbackCooldown).Unix()
	}
	model.CoolDownCodexChannel(channelError.ChannelId, time.Unix(resetAt, 0))
	logger.LogWarn(c, common.LogText("codex channel #%d hit its usage limit (window=%q); it is skipped until %s", channelError.ChannelId, window, time.Unix(resetAt, 0).Format(time.RFC3339)))
}

// EndCodexChannelCooldownAfterSuccess ends the cooldown of a channel that
// answered successfully and whose usage headers show no exhausted window.
func EndCodexChannelCooldownAfterSuccess(channelID int, headers http.Header) {
	if _, _, exhausted := codexExhaustedWindow(headers, time.Now()); exhausted {
		return
	}
	model.ClearCodexChannelCooldown(channelID)
}

var codexPassiveUsageThrottle = struct {
	sync.Mutex
	lastWrite map[int]time.Time
}{lastWrite: map[int]time.Time{}}

// RecordCodexPassiveUsage stores the usage windows of a relayed answer in the
// channel's OtherInfo, at most once per channel every 30 seconds, in the
// background.
func RecordCodexPassiveUsage(channelID int, headers http.Header) {
	if channelID <= 0 || headers == nil {
		return
	}
	now := time.Now()
	windows := ParseCodexUsageWindows(headers, now)
	if len(windows) == 0 {
		return
	}
	codexPassiveUsageThrottle.Lock()
	if last, ok := codexPassiveUsageThrottle.lastWrite[channelID]; ok && now.Sub(last) < codexPassiveUsageWriteInterval {
		codexPassiveUsageThrottle.Unlock()
		return
	}
	codexPassiveUsageThrottle.lastWrite[channelID] = now
	codexPassiveUsageThrottle.Unlock()
	gopool.Go(func() {
		if err := model.UpdateChannelOtherInfoEntry(channelID, CodexPassiveUsageKey, windows); err != nil {
			common.SysLog(common.LogText("failed to store codex passive usage: channel_id=%d, error=%v", channelID, err))
			return
		}
		NotifyChannelAutoResetUsageSample(channelID)
	})
}

// LoadCodexPassiveUsage reads the stored usage windows of a Codex channel.
func LoadCodexPassiveUsage(ch *model.Channel) map[string]ClaudePassiveUsageWindow {
	windows := map[string]ClaudePassiveUsageWindow{}
	if ch == nil {
		return windows
	}
	raw, ok := ch.GetOtherInfo()[CodexPassiveUsageKey]
	if !ok {
		return windows
	}
	encoded, err := common.Marshal(raw)
	if err != nil || common.Unmarshal(encoded, &windows) != nil {
		return map[string]ClaudePassiveUsageWindow{}
	}
	return windows
}
