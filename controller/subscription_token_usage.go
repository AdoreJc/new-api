package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// ---- API key: read-only usage of the caller's bound subscriptions ----
//
// GET /api/usage/subscription answers clients that only hold an sk- key, such
// as a CC Switch usage script. It never exposes reset credits, the channel id
// or account details, and offers no way to redeem a reset.

// TokenSubscriptionUsage is one bound subscription in the API-key answer: the
// self usage shape without can_reset.
type TokenSubscriptionUsage struct {
	SubscriptionId int    `json:"subscription_id"`
	Provider       string `json:"provider"`
	ChannelType    int    `json:"channel_type"`
	// Same keys as SubscriptionUsageResponse.Windows; empty when Error is set
	Windows      map[string]SubscriptionUsageWindow `json:"windows"`
	PlanType     string                             `json:"plan_type,omitempty"`
	LimitReached bool                               `json:"limit_reached"`
	FetchedAt    int64                              `json:"fetched_at,omitempty"`
	// Set when the usage of this subscription could not be fetched
	Error string `json:"error,omitempty"`
}

// TokenSubscriptionUsageItem is one usage window in the CC Switch usage
// script format, so a script can return the items unchanged.
type TokenSubscriptionUsageItem struct {
	PlanName string `json:"planName"`
	// Percent of the window used, 0-100
	Used      float64 `json:"used"`
	Total     float64 `json:"total"`
	Remaining float64 `json:"remaining"`
	Unit      string  `json:"unit"`
	// RFC 3339; empty when upstream reported no reset time
	ResetsAt string `json:"resetsAt,omitempty"`
	// Notes such as a window filled from sampled response headers
	Extra string `json:"extra,omitempty"`
}

type TokenSubscriptionUsageResponse struct {
	Subscriptions []TokenSubscriptionUsage     `json:"subscriptions"`
	Items         []TokenSubscriptionUsageItem `json:"items"`
}

type boundSubscriptionTarget struct {
	sub *model.UserSubscription
	ch  *model.Channel
}

// subscriptionUsageProvider names the provider of a bindable channel type.
func subscriptionUsageProvider(channelType int) string {
	if channelType == constant.ChannelTypeCodex {
		return "codex"
	}
	return "claude"
}

// subscriptionUsageWindowLabel names a usage window for clients without the
// web console, in the language the reader stated.
func subscriptionUsageWindowLabel(c *gin.Context, provider string, key string, window SubscriptionUsageWindow) string {
	if provider == "claude" {
		switch key {
		case "five_hour":
			return i18n.T(c, i18n.MsgSubscriptionUsageClaudeFiveHour)
		case "seven_day":
			return i18n.T(c, i18n.MsgSubscriptionUsageClaudeSevenDay)
		case "seven_day_sonnet":
			return i18n.T(c, i18n.MsgSubscriptionUsageClaudeSevenDaySonnet)
		default:
			return i18n.T(c, i18n.MsgSubscriptionUsageClaudeSevenDayFable)
		}
	}
	const fiveHours, oneWeek = 5 * 60 * 60, 7 * 24 * 60 * 60
	switch {
	case window.WindowSeconds == fiveHours || (window.WindowSeconds == 0 && key == "primary"):
		return i18n.T(c, i18n.MsgSubscriptionUsageCodexFiveHour)
	case window.WindowSeconds == oneWeek || (window.WindowSeconds == 0 && key == "secondary"):
		return i18n.T(c, i18n.MsgSubscriptionUsageCodexWeekly)
	default:
		return i18n.T(c, i18n.MsgSubscriptionUsageCodexWindow, map[string]any{
			"Hours": strconv.FormatFloat(float64(window.WindowSeconds)/3600, 'f', -1, 64),
		})
	}
}

// subscriptionUsageWindowOrder lists the window keys of each provider in
// display order.
var subscriptionUsageWindowOrder = map[string][]string{
	"claude": claudeSubscriptionUsageWindows,
	"codex":  {"primary", "secondary"},
}

// GetTokenSubscriptionUsage returns the usage windows of the caller's bound
// subscriptions for an API key. Query parameters: provider (claude|codex) and
// subscription_id, both optional. A token whose group is the dedicated group
// of one of the caller's bindings sees that subscription first and alone;
// otherwise every active bound subscription is listed.
func GetTokenSubscriptionUsage(c *gin.Context) {
	userId := c.GetInt("id")
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider != "" && provider != "claude" && provider != "codex" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}

	var targets []boundSubscriptionTarget
	if raw := strings.TrimSpace(c.Query("subscription_id")); raw != "" {
		subId, _ := strconv.Atoi(raw)
		sub, ch, err := resolveBoundSubscription(userId, subId)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		targets = append(targets, boundSubscriptionTarget{sub: sub, ch: ch})
	} else {
		subs, err := model.ListActiveBoundUserSubscriptions(userId)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		tokenGroup := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		var dedicated []boundSubscriptionTarget
		for i := range subs {
			sub, ch, err := resolveBoundSubscription(userId, subs[i].Id)
			if err != nil {
				continue
			}
			target := boundSubscriptionTarget{sub: sub, ch: ch}
			targets = append(targets, target)
			if model.IsSubscriptionBoundGroup(tokenGroup) && sub.HasBoundGroup(tokenGroup) {
				dedicated = append(dedicated, target)
			}
		}
		if len(dedicated) > 0 {
			targets = dedicated
		}
	}
	if provider != "" {
		filtered := targets[:0]
		for _, target := range targets {
			if subscriptionUsageProvider(target.ch.Type) == provider {
				filtered = append(filtered, target)
			}
		}
		targets = filtered
	}

	response := TokenSubscriptionUsageResponse{
		Subscriptions: []TokenSubscriptionUsage{},
		Items:         []TokenSubscriptionUsageItem{},
	}
	if len(targets) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "no bound subscription",
			"data":    response,
		})
		return
	}

	perProvider := map[string]int{}
	for _, target := range targets {
		perProvider[subscriptionUsageProvider(target.ch.Type)]++
	}
	var firstErr error
	succeeded := 0
	for _, target := range targets {
		entry := TokenSubscriptionUsage{
			SubscriptionId: target.sub.Id,
			Provider:       subscriptionUsageProvider(target.ch.Type),
			ChannelType:    target.ch.Type,
			Windows:        map[string]SubscriptionUsageWindow{},
		}
		usage, err := loadSubscriptionAccountUsage(c.Request.Context(), target.ch)
		if err == nil && !usage.upstreamSucceeded() {
			err = upstreamStatusMessage(usage.UpstreamStatus)
		}
		if err != nil {
			entry.Error = err.Error()
			if firstErr == nil {
				firstErr = err
			}
			response.Subscriptions = append(response.Subscriptions, entry)
			continue
		}
		succeeded++
		entry.Windows = usage.Windows
		entry.PlanType = usage.PlanType
		entry.LimitReached = usage.LimitReached
		entry.FetchedAt = usage.FetchedAt
		response.Subscriptions = append(response.Subscriptions, entry)

		for _, key := range subscriptionUsageWindowOrder[entry.Provider] {
			window, ok := usage.Windows[key]
			if !ok {
				continue
			}
			item := TokenSubscriptionUsageItem{
				PlanName:  subscriptionUsageWindowLabel(c, entry.Provider, key, window),
				Used:      window.Utilization,
				Total:     100,
				Remaining: 100 - window.Utilization,
				Unit:      "%",
				ResetsAt:  window.ResetsAt,
			}
			// Tell apart two subscriptions of the same provider.
			if perProvider[entry.Provider] > 1 {
				item.PlanName += " #" + strconv.Itoa(entry.SubscriptionId)
			}
			if window.Source == service.ClaudePassiveUsageSource {
				item.Extra = i18n.T(c, i18n.MsgSubscriptionUsagePassiveSample, map[string]any{"SampledAt": window.SampledAt})
			}
			response.Items = append(response.Items, item)
		}
	}

	if succeeded == 0 {
		common.ApiErrorStatus(c, http.StatusOK, firstErr, gin.H{"data": response})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    response,
	})
}
