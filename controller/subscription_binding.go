package controller

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// ---- Admin: bind an OAuth channel account to a user subscription ----

func AdminListSubscriptionBindableChannels(c *gin.Context) {
	channels, err := model.ListSubscriptionBindableChannels()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, channels)
}

type AdminBindUserSubscriptionChannelRequest struct {
	ChannelId int `json:"channel_id"`
}

func AdminBindUserSubscriptionChannel(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorT(c, "Invalid subscription ID")
		return
	}
	var req AdminBindUserSubscriptionChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.ChannelId <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	sub, err := model.GetUserSubscriptionById(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	msg, err := model.AdminBindSubscriptionChannel(subId, req.ChannelId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, sub.UserId, "subscription.channel_bind", map[string]any{
		"target_user_id":  sub.UserId,
		"subscription_id": subId,
		"channel_id":      req.ChannelId,
	})
	subscriptionChangeSuccess(c, msg)
}

func AdminUnbindUserSubscriptionChannel(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorT(c, "Invalid subscription ID")
		return
	}
	sub, err := model.GetUserSubscriptionById(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	msg, err := model.AdminUnbindSubscriptionChannel(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, sub.UserId, "subscription.channel_unbind", map[string]any{
		"target_user_id":  sub.UserId,
		"subscription_id": subId,
		"channel_id":      sub.BoundChannelId,
	})
	subscriptionChangeSuccess(c, msg)
}

// ---- User: usage windows of the bound account ----

// SubscriptionUsageWindow is one rate-limit window of the bound account,
// trimmed to what the subscriber may see.
type SubscriptionUsageWindow struct {
	// 0-100
	Utilization float64 `json:"utilization"`
	// RFC 3339, empty when upstream omitted it
	ResetsAt string `json:"resets_at,omitempty"`
	// Window length when upstream reports it (Codex)
	WindowSeconds int64 `json:"window_seconds,omitempty"`
	// "passive" when the window was filled from sampled rate-limit headers
	Source string `json:"source,omitempty"`
	// RFC 3339 time of the sampled response, with Source
	SampledAt string `json:"sampled_at,omitempty"`
}

type SubscriptionUsageResponse struct {
	SubscriptionId int `json:"subscription_id"`
	// claude | codex
	Provider    string `json:"provider"`
	ChannelType int    `json:"channel_type"`
	// Claude: five_hour, seven_day, seven_day_sonnet, seven_day_overage_included.
	// Codex: primary, secondary.
	Windows map[string]SubscriptionUsageWindow `json:"windows"`
	// Codex plan type when upstream reports it
	PlanType     string `json:"plan_type,omitempty"`
	LimitReached bool   `json:"limit_reached"`
	FetchedAt    int64  `json:"fetched_at"`
	// CanReset reports the administrator switch that lets this user view and
	// redeem the reset credits of the bound account.
	CanReset bool `json:"can_reset"`
}

var claudeSubscriptionUsageWindows = []string{"five_hour", "seven_day", "seven_day_sonnet", "seven_day_overage_included"}

func clampUtilization(value float64) float64 {
	return max(0, min(100, value))
}

// projectClaudeSubscriptionUsage keeps only the window utilization and reset
// times from Anthropic's /api/oauth/usage document.
func projectClaudeSubscriptionUsage(payload any) map[string]SubscriptionUsageWindow {
	windows := make(map[string]SubscriptionUsageWindow)
	doc, ok := payload.(map[string]any)
	if !ok {
		return windows
	}
	for _, key := range claudeSubscriptionUsageWindows {
		block, ok := doc[key].(map[string]any)
		if !ok {
			continue
		}
		window := SubscriptionUsageWindow{}
		if utilization, ok := block["utilization"].(float64); ok {
			window.Utilization = clampUtilization(utilization)
		}
		if resetsAt, ok := block["resets_at"].(string); ok {
			window.ResetsAt = strings.TrimSpace(resetsAt)
		}
		if source, ok := block["source"].(string); ok && source == service.ClaudePassiveUsageSource {
			window.Source = source
			if sampledAt, ok := block["sampled_at"].(string); ok {
				window.SampledAt = strings.TrimSpace(sampledAt)
			}
		}
		windows[key] = window
	}
	return windows
}

type codexWhamUsageWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	ResetAt            *int64   `json:"reset_at"`
	ResetAfterSeconds  *int64   `json:"reset_after_seconds"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
}

type codexWhamUsageDocument struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		LimitReached    bool                  `json:"limit_reached"`
		PrimaryWindow   *codexWhamUsageWindow `json:"primary_window"`
		SecondaryWindow *codexWhamUsageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

func projectCodexWindow(w *codexWhamUsageWindow, now time.Time) (SubscriptionUsageWindow, bool) {
	if w == nil {
		return SubscriptionUsageWindow{}, false
	}
	window := SubscriptionUsageWindow{}
	if w.UsedPercent != nil {
		window.Utilization = clampUtilization(*w.UsedPercent)
	}
	switch {
	case w.ResetAt != nil && *w.ResetAt > 0:
		window.ResetsAt = time.Unix(*w.ResetAt, 0).UTC().Format(time.RFC3339)
	case w.ResetAfterSeconds != nil && *w.ResetAfterSeconds > 0:
		window.ResetsAt = now.Add(time.Duration(*w.ResetAfterSeconds) * time.Second).UTC().Format(time.RFC3339)
	}
	if w.LimitWindowSeconds != nil && *w.LimitWindowSeconds > 0 {
		window.WindowSeconds = *w.LimitWindowSeconds
	}
	return window, true
}

// projectCodexSubscriptionUsage keeps only the two rate-limit windows and the
// plan type from the wham usage document (no email, user id or credits).
func projectCodexSubscriptionUsage(body []byte, now time.Time) (map[string]SubscriptionUsageWindow, string, bool) {
	windows := make(map[string]SubscriptionUsageWindow)
	var doc codexWhamUsageDocument
	if err := common.Unmarshal(body, &doc); err != nil || doc.RateLimit == nil {
		return windows, strings.TrimSpace(doc.PlanType), false
	}
	if window, ok := projectCodexWindow(doc.RateLimit.PrimaryWindow, now); ok {
		windows["primary"] = window
	}
	if window, ok := projectCodexWindow(doc.RateLimit.SecondaryWindow, now); ok {
		windows["secondary"] = window
	}
	return windows, strings.TrimSpace(doc.PlanType), doc.RateLimit.LimitReached
}

// resolveBoundSubscription loads one of userId's own active subscriptions and
// the single-key OAuth channel bound to it. Errors are web console messages
// that never name the channel or its dedicated group.
func resolveBoundSubscription(userId int, subId int) (*model.UserSubscription, *model.Channel, error) {
	if subId <= 0 {
		return nil, nil, common.NewMessage("Invalid subscription ID")
	}
	sub, err := model.GetUserSubscriptionById(subId)
	if err != nil || sub.UserId != userId {
		// Same answer for a foreign and a missing subscription.
		return nil, nil, common.NewMessage("Subscription not found")
	}
	now := model.GetDBTimestamp()
	if sub.Status != "active" || sub.EndTime <= now {
		return nil, nil, common.NewMessage("This subscription is no longer active")
	}
	if sub.BoundChannelId <= 0 {
		return nil, nil, common.NewMessage("This subscription is not bound to a channel")
	}
	ch, err := model.GetChannelById(sub.BoundChannelId, true)
	if err != nil || ch == nil || !model.IsSubscriptionBindableChannelType(ch.Type) {
		return nil, nil, common.NewMessage("The bound channel is no longer available")
	}
	if ch.ChannelInfo.IsMultiKey {
		return nil, nil, common.NewMessage("Multi-key channels are not supported")
	}
	return sub, ch, nil
}

// resolveSelfBoundSubscription is resolveBoundSubscription for the caller and
// the :id route parameter. On failure the error response has already been
// written and ok is false.
func resolveSelfBoundSubscription(c *gin.Context) (sub *model.UserSubscription, ch *model.Channel, ok bool) {
	subId, _ := strconv.Atoi(c.Param("id"))
	sub, ch, err := resolveBoundSubscription(c.GetInt("id"), subId)
	if err != nil {
		common.ApiError(c, err)
		return nil, nil, false
	}
	return sub, ch, true
}

// subscriptionAccountUsage is the usage of one bound OAuth account. It is
// shared through the service cache by every subscriber of the channel and
// both usage endpoints, so it holds nothing specific to a user.
type subscriptionAccountUsage struct {
	Provider       string
	UpstreamStatus int
	// Read-only once cached
	Windows      map[string]SubscriptionUsageWindow
	PlanType     string
	LimitReached bool
	FetchedAt    int64
}

func (u subscriptionAccountUsage) upstreamSucceeded() bool {
	return u.UpstreamStatus >= 200 && u.UpstreamStatus < 300
}

// loadSubscriptionAccountUsage returns the usage windows of the account
// behind ch, asking upstream at most once per service.SubscriptionUsageCacheTTL.
// A non-nil error means upstream gave no answer; an upstream failure status
// is reported by UpstreamStatus instead.
func loadSubscriptionAccountUsage(ctx context.Context, ch *model.Channel) (subscriptionAccountUsage, error) {
	// The answer is shared with concurrent callers and the cache, so one
	// caller going away must not cancel it; the fetch timeouts still apply.
	ctx = context.WithoutCancel(ctx)
	return service.LoadSubscriptionUsage(ch.Id, time.Now(), func() (subscriptionAccountUsage, bool, error) {
		usage := subscriptionAccountUsage{
			Windows:   map[string]SubscriptionUsageWindow{},
			FetchedAt: common.GetTimestamp(),
		}
		switch ch.Type {
		case constant.ChannelTypeClaudeOAuth:
			usage.Provider = "claude"
			statusCode, payload, err := claudeChannelOAuthFetch(
				ctx, ch,
				func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
					status, body, err := service.FetchClaudeOAuthUsage(ctx, client, ch.GetBaseURL(), accessToken)
					if err != nil {
						return status, nil, err
					}
					var doc any
					if common.Unmarshal(body, &doc) != nil {
						doc = nil
					}
					if windows, ok := doc.(map[string]any); ok && status >= 200 && status < 300 {
						service.BackfillClaudeUsageWindows(windows, service.LoadClaudePassiveUsage(ch), time.Now())
					}
					return status, doc, nil
				},
				claudeUsageRequestTimeout,
				common.LogText("failed to fetch claude usage for subscription"),
				"Failed to fetch usage. Please try again later",
			)
			if err != nil {
				return usage, false, err
			}
			usage.UpstreamStatus = statusCode
			if usage.upstreamSucceeded() {
				usage.Windows = projectClaudeSubscriptionUsage(payload)
				for _, window := range usage.Windows {
					if window.Utilization >= 100 {
						usage.LimitReached = true
					}
				}
			}
		case constant.ChannelTypeCodex:
			usage.Provider = "codex"
			statusCode, body, err := codexChannelWhamFetch(
				ctx, ch,
				service.FetchCodexWhamUsage,
				common.LogText("failed to fetch codex usage for subscription"),
				"Failed to fetch usage. Please try again later",
			)
			if err != nil {
				return usage, false, err
			}
			usage.UpstreamStatus = statusCode
			if usage.upstreamSucceeded() {
				usage.Windows, usage.PlanType, usage.LimitReached = projectCodexSubscriptionUsage(body, time.Now())
			}
		}
		return usage, usage.upstreamSucceeded(), nil
	})
}

func upstreamStatusMessage(statusCode int) *common.Message {
	return common.NewMessage("Upstream returned status {{status}}", map[string]any{"status": statusCode})
}

// GetSubscriptionSelfUsage returns the rate-limit windows of the OAuth
// account bound to one of the caller's own active subscriptions. Reset
// credits are served by the separate self reset-credit endpoints, which an
// administrator enables per user; can_reset reports that switch.
func GetSubscriptionSelfUsage(c *gin.Context) {
	sub, ch, ok := resolveSelfBoundSubscription(c)
	if !ok {
		return
	}
	usage, err := loadSubscriptionAccountUsage(c.Request.Context(), ch)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !usage.upstreamSucceeded() {
		common.ApiErrorStatus(c, http.StatusOK, upstreamStatusMessage(usage.UpstreamStatus), gin.H{
			"upstream_status": usage.UpstreamStatus,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"message":         "",
		"upstream_status": usage.UpstreamStatus,
		"data": SubscriptionUsageResponse{
			SubscriptionId: sub.Id,
			Provider:       usage.Provider,
			ChannelType:    ch.Type,
			Windows:        usage.Windows,
			PlanType:       usage.PlanType,
			LimitReached:   usage.LimitReached,
			FetchedAt:      usage.FetchedAt,
			CanReset:       userAllowsSubscriptionResetCredit(c.GetInt("id")),
		},
	})
}
