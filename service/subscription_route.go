package service

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// ErrorCodeSubscriptionAccountLimited is returned when the bound account of a
// steered request hit its own usage limit and the request did not fall back.
const ErrorCodeSubscriptionAccountLimited types.ErrorCode = "subscription_account_limited"

// subscriptionLimitResetMaxAhead bounds a reset time read from upstream or a
// sample; anything further away (or in the past) is not shown.
const subscriptionLimitResetMaxAhead = 8 * 24 * time.Hour

// Usage windows of a Claude subscription account, by rate-limit header name,
// with the key the passive samples use for the same window.
var subscriptionLimitWindows = []struct {
	header    string
	sampleKey string
	labelKey  string
}{
	{header: "5h", sampleKey: "five_hour", labelKey: i18n.MsgRelaySubscriptionWindow5h},
	{header: "7d", sampleKey: "seven_day", labelKey: i18n.MsgRelaySubscriptionWindow7d},
	{header: "7d_oi", sampleKey: "seven_day_overage_included", labelKey: i18n.MsgRelaySubscriptionWindow7dOi},
}

// SubscriptionAccountLimit is an upstream answer saying the bound account ran
// out of one of its own usage windows.
type SubscriptionAccountLimit struct {
	// Window is "5h", "7d", "7d_oi", or "" when the answer did not say which.
	Window string
	// ResetAt is the unix time the window resets, 0 when unknown.
	ResetAt int64
}

// RelayRetryGroup is the group a relay retry selects channels from. A request
// served by a subscription binding stays in the binding's dedicated group: the
// subscription pays only for its own account, never for the shared pool.
func RelayRetryGroup(info *relaycommon.RelayInfo) string {
	if info.SubscriptionRoute.Active() {
		return info.SubscriptionRoute.Group
	}
	return info.TokenGroup
}

// RecordSubscriptionLimitHeaders keeps the usage-limit headers of the latest
// upstream answer of a subscription OAuth channel for the retry loop, which
// only sees the converted error. A nil header clears what an earlier attempt
// left.
func RecordSubscriptionLimitHeaders(c *gin.Context, headers http.Header) {
	if c == nil {
		return
	}
	kept := http.Header{}
	for name, values := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "anthropic-ratelimit-unified-") || strings.HasPrefix(lower, "x-codex-") || lower == "retry-after" {
			kept[name] = values
		}
	}
	common.SetContextKey(c, constant.ContextKeyUpstreamRateLimitHeaders, kept)
}

// DetectSubscriptionAccountLimit reports whether a failed attempt on a bound
// subscription channel was the account's own usage limit: HTTP 429, an
// Anthropic rate_limit_error, a window whose unified status is "rejected", or
// a Codex usage_limit_reached / rate_limit_exceeded error. The window and reset
// time come from the response headers first, then from the channel's passive
// usage samples.
func DetectSubscriptionAccountLimit(apiErr *types.NewAPIError, headers http.Header, passive map[string]ClaudePassiveUsageWindow, now time.Time) (SubscriptionAccountLimit, bool) {
	if apiErr == nil {
		return SubscriptionAccountLimit{}, false
	}
	limited := apiErr.StatusCode == http.StatusTooManyRequests
	switch apiErr.ToOpenAIError().Type {
	case "rate_limit_error", "usage_limit_reached", "rate_limit_exceeded":
		limited = true
	}
	if strings.EqualFold(strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-status")), "rejected") {
		limited = true
	}

	// The window that keeps the account locked longest is the one to report.
	var limit SubscriptionAccountLimit
	found := false
	for _, w := range subscriptionLimitWindows {
		prefix := "anthropic-ratelimit-unified-" + w.header + "-"
		rejected := strings.EqualFold(strings.TrimSpace(headers.Get(prefix+"status")), "rejected")
		if !rejected {
			utilization, err := strconv.ParseFloat(strings.TrimSpace(headers.Get(prefix+"utilization")), 64)
			rejected = err == nil && utilization >= 1
		}
		if !rejected {
			continue
		}
		limited = true
		resetAt := parseSubscriptionLimitReset(headers.Get(prefix+"reset"), now)
		if !found || resetAt > limit.ResetAt {
			limit = SubscriptionAccountLimit{Window: w.header, ResetAt: resetAt}
			found = true
		}
	}
	if !limited {
		return SubscriptionAccountLimit{}, false
	}
	if limit.ResetAt == 0 {
		limit.ResetAt = parseSubscriptionLimitReset(headers.Get("anthropic-ratelimit-unified-reset"), now)
	}
	if found || limit.ResetAt > 0 {
		return limit, true
	}
	for _, w := range subscriptionLimitWindows {
		sample, ok := passive[w.sampleKey]
		if !ok || sample.Utilization < 100 || sample.ResetsAt <= now.Unix() || time.Unix(sample.ResetsAt, 0).Sub(now) > subscriptionLimitResetMaxAhead {
			continue
		}
		if sample.ResetsAt > limit.ResetAt {
			limit = SubscriptionAccountLimit{Window: w.header, ResetAt: sample.ResetsAt}
		}
	}
	return limit, true
}

func parseSubscriptionLimitReset(raw string, now time.Time) int64 {
	reset, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || reset <= 0 {
		return 0
	}
	// Anthropic sends unix seconds; tolerate milliseconds.
	if reset > 1e11 {
		reset /= 1000
	}
	if reset <= now.Unix() || time.Unix(reset, 0).Sub(now) > subscriptionLimitResetMaxAhead {
		return 0
	}
	return reset
}

// HandleSubscriptionRouteFailure runs after a failed attempt of a request the
// distributor steered to a subscription binding. When the bound account
// answered with its own usage limit:
//   - if the plan has fallback enabled and nothing reached the client yet, the
//     bound subscription's reservation is refunded, the wallet reserves the
//     request instead, and the retry continues in the request's original group
//     (Auto without the dedicated group); it returns true;
//   - otherwise it returns a 429 that names the exhausted window and its reset
//     time, which ends the request.
//
// Any other failure is returned unchanged.
func HandleSubscriptionRouteFailure(c *gin.Context, info *relaycommon.RelayInfo, retry *RetryParam, apiErr *types.NewAPIError, responseStarted bool) (*types.NewAPIError, bool) {
	route := info.SubscriptionRoute
	if !route.Active() || apiErr == nil || common.GetContextKeyInt(c, constant.ContextKeyChannelId) != route.ChannelId {
		return apiErr, false
	}
	headers, _ := common.GetContextKeyType[http.Header](c, constant.ContextKeyUpstreamRateLimitHeaders)
	var passive map[string]ClaudePassiveUsageWindow
	if channel, err := model.CacheGetChannel(route.ChannelId); err == nil {
		passive = LoadClaudePassiveUsage(channel)
	}
	limit, limited := DetectSubscriptionAccountLimit(apiErr, headers, passive, time.Now())
	if !limited {
		return apiErr, false
	}

	fallBack := false
	if !responseStarted && route.OriginalGroup != "" && route.OriginalGroup != route.Group {
		enabled, err := model.SubscriptionFallsBackToSharedPool(route.SubscriptionId)
		if err != nil {
			logger.LogError(c, common.LogText("failed to read the shared pool fallback setting of subscription %d: %v", route.SubscriptionId, err))
		}
		fallBack = enabled
	}
	if !fallBack {
		logger.LogInfo(c, common.LogText("subscription %d bound account (channel #%d) hit its usage limit (window=%q, reset_at=%d); the request ends without falling back", route.SubscriptionId, route.ChannelId, limit.Window, limit.ResetAt))
		return subscriptionAccountLimitError(c, limit), false
	}

	logger.LogWarn(c, common.LogText("subscription %d bound account (channel #%d) hit its usage limit (window=%q); falling back to the shared pool group %s, billed to the wallet", route.SubscriptionId, route.ChannelId, limit.Window, route.OriginalGroup))
	route.FellBack = true
	route.LimitWindow = limit.Window
	if session, ok := info.Billing.(*BillingSession); ok {
		session.refundNow()
	}
	info.Billing = nil
	if !info.PriceData.FreeModel {
		if billingErr := PreConsumeBilling(c, info.PriceData.QuotaToPreConsume, info); billingErr != nil {
			return billingErr, false
		}
	}
	common.SetContextKey(c, constant.ContextKeyUsingGroup, route.OriginalGroup)
	common.SetContextKey(c, constant.ContextKeyAutoGroupIndex, 0)
	info.UsingGroup = route.OriginalGroup
	retry.TokenGroup = route.OriginalGroup
	// The shared pool gets the full attempt budget, starting at its highest
	// priority.
	retry.SetRetry(0)
	retry.ResetRetryNextTry()
	c.Set(ginKeyChannelAffinityRetryOffset, 0)
	return apiErr, true
}

func subscriptionAccountLimitError(c *gin.Context, limit SubscriptionAccountLimit) *types.NewAPIError {
	resetAt := ""
	if limit.ResetAt > 0 {
		resetAt = time.Unix(limit.ResetAt, 0).Format("2006-01-02 15:04 MST")
	}
	window := ""
	for _, w := range subscriptionLimitWindows {
		if w.header == limit.Window {
			window = i18n.T(c, w.labelKey)
		}
	}
	var message string
	switch {
	case window != "" && resetAt != "":
		message = i18n.T(c, i18n.MsgRelaySubscriptionAccountLimited, map[string]any{"Window": window, "ResetAt": resetAt})
	case window != "":
		message = i18n.T(c, i18n.MsgRelaySubscriptionAccountLimitedNoReset, map[string]any{"Window": window})
	case resetAt != "":
		message = i18n.T(c, i18n.MsgRelaySubscriptionAccountLimitedGenericReset, map[string]any{"ResetAt": resetAt})
	default:
		message = i18n.T(c, i18n.MsgRelaySubscriptionAccountLimitedGeneric)
	}
	return types.NewErrorWithStatusCode(errors.New(message), ErrorCodeSubscriptionAccountLimited, http.StatusTooManyRequests, types.ErrOptionWithSkipRetry())
}
