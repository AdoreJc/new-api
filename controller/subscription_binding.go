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

// GetSubscriptionSelfUsage returns the rate-limit windows of the OAuth
// account bound to one of the caller's own active subscriptions. Reset
// credits are not exposed here; redemption stays an administrator action.
func GetSubscriptionSelfUsage(c *gin.Context) {
	userId := c.GetInt("id")
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorT(c, "Invalid subscription ID")
		return
	}
	sub, err := model.GetUserSubscriptionById(subId)
	if err != nil || sub.UserId != userId {
		// Same answer for a foreign and a missing subscription.
		common.ApiErrorT(c, "Subscription not found")
		return
	}
	now := model.GetDBTimestamp()
	if sub.Status != "active" || sub.EndTime <= now {
		common.ApiErrorT(c, "This subscription is no longer active")
		return
	}
	if sub.BoundChannelId <= 0 {
		common.ApiErrorT(c, "This subscription is not bound to a channel")
		return
	}
	ch, err := model.GetChannelById(sub.BoundChannelId, true)
	if err != nil || ch == nil || !model.IsSubscriptionBindableChannelType(ch.Type) {
		common.ApiErrorT(c, "The bound channel is no longer available")
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels are not supported")
		return
	}

	result := SubscriptionUsageResponse{
		SubscriptionId: sub.Id,
		ChannelType:    ch.Type,
		Windows:        map[string]SubscriptionUsageWindow{},
		FetchedAt:      common.GetTimestamp(),
	}
	var statusCode int
	switch ch.Type {
	case constant.ChannelTypeClaudeOAuth:
		result.Provider = "claude"
		var payload any
		var ok bool
		statusCode, payload, ok = claudeChannelOAuthFetch(
			c, ch,
			func(ctx context.Context, client *http.Client, baseURL string, accessToken string) (int, any, error) {
				status, body, err := service.FetchClaudeOAuthUsage(ctx, client, baseURL, accessToken)
				if err != nil {
					return status, nil, err
				}
				var doc any
				if common.Unmarshal(body, &doc) != nil {
					doc = nil
				}
				return status, doc, nil
			},
			claudeUsageRequestTimeout,
			common.LogText("failed to fetch claude usage for subscription"),
			"Failed to fetch usage. Please try again later",
		)
		if !ok {
			return
		}
		if statusCode >= 200 && statusCode < 300 {
			result.Windows = projectClaudeSubscriptionUsage(payload)
			for _, window := range result.Windows {
				if window.Utilization >= 100 {
					result.LimitReached = true
				}
			}
		}
	case constant.ChannelTypeCodex:
		result.Provider = "codex"
		var body []byte
		var ok bool
		statusCode, body, ok = codexChannelWhamFetch(
			c, ch,
			service.FetchCodexWhamUsage,
			common.LogText("failed to fetch codex usage for subscription"),
			"Failed to fetch usage. Please try again later",
		)
		if !ok {
			return
		}
		if statusCode >= 200 && statusCode < 300 {
			result.Windows, result.PlanType, result.LimitReached = projectCodexSubscriptionUsage(body, time.Now())
		}
	}

	if statusCode < 200 || statusCode >= 300 {
		common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Upstream returned status {{status}}", map[string]any{"status": statusCode}), gin.H{
			"upstream_status": statusCode,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"message":         "",
		"upstream_status": statusCode,
		"data":            result,
	})
}
