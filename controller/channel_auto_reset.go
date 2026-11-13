package controller

import (
	"context"
	"errors"
	"fmt"
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

// channelAutoResetPassiveFresh is how recent the passive samples of both the
// 5-hour and the weekly window must be for an evaluation to use them alone,
// without asking the usage API.
const channelAutoResetPassiveFresh = 5 * time.Minute

// channelAutoResetUpstream runs the automatic redemption with the same
// upstream calls as the usage dialog and the subscriber reset.
type channelAutoResetUpstream struct{}

// StartChannelAutoResetCreditTask installs the automatic reset-credit engine.
func StartChannelAutoResetCreditTask() {
	service.StartChannelAutoResetTask(channelAutoResetUpstream{})
}

func (channelAutoResetUpstream) LoadUsage(ctx context.Context, ch *model.Channel, allowUpstream bool) (*service.ChannelAutoResetUsage, error) {
	now := time.Now()
	usage := &service.ChannelAutoResetUsage{Windows: map[string]service.ChannelAutoResetWindow{}, FetchedAt: now}
	passive := service.LoadClaudePassiveUsage(ch)
	if ch.Type == constant.ChannelTypeCodex {
		passive = service.LoadCodexPassiveUsage(ch)
	}
	addPassive := func(maxAge time.Duration) {
		for key, sample := range passive {
			if _, ok := usage.Windows[key]; ok || sample.SampledAt <= 0 || now.Sub(time.Unix(sample.SampledAt, 0)) > maxAge {
				continue
			}
			window := service.ChannelAutoResetWindow{Utilization: sample.Utilization}
			if sample.ResetsAt > 0 {
				window.ResetsAt = time.Unix(sample.ResetsAt, 0)
			}
			usage.Windows[key] = window
		}
	}
	fiveHour, weekly := passive["five_hour"], passive["seven_day"]
	if fiveHour.SampledAt > 0 && weekly.SampledAt > 0 &&
		now.Sub(time.Unix(fiveHour.SampledAt, 0)) <= channelAutoResetPassiveFresh &&
		now.Sub(time.Unix(weekly.SampledAt, 0)) <= channelAutoResetPassiveFresh {
		addPassive(channelAutoResetPassiveFresh)
		return usage, nil
	}
	if !allowUpstream {
		addPassive(service.ClaudePassiveUsageMaxAge)
		if len(usage.Windows) == 0 {
			return nil, common.NewMessage("No recent usage data. It will be fetched again shortly")
		}
		return usage, nil
	}
	usage.FromUpstream = true

	account, err := loadSubscriptionAccountUsage(ctx, ch)
	if err != nil {
		return nil, err
	}
	if !account.upstreamSucceeded() {
		return nil, upstreamStatusMessage(account.UpstreamStatus)
	}
	for key, window := range account.Windows {
		if ch.Type == constant.ChannelTypeCodex {
			// The wham document names its windows primary / secondary; the
			// window length decides which one is the 5-hour window.
			switch {
			case window.WindowSeconds > 0 && window.WindowSeconds <= 6*3600:
				key = "five_hour"
			case window.WindowSeconds > 0:
				key = "seven_day"
			case key == "primary":
				key = "five_hour"
			default:
				key = "seven_day"
			}
		}
		converted := service.ChannelAutoResetWindow{Utilization: window.Utilization}
		if at, err := time.Parse(time.RFC3339, window.ResetsAt); err == nil {
			converted.ResetsAt = at
		}
		usage.Windows[key] = converted
	}
	addPassive(service.ClaudePassiveUsageMaxAge)
	return usage, nil
}

func (channelAutoResetUpstream) LoadCredits(ctx context.Context, ch *model.Channel) (*service.ChannelAutoResetCredits, error) {
	credits := &service.ChannelAutoResetCredits{FetchedAt: time.Now()}
	keepEarliest := func(at time.Time) {
		if credits.EarliestExpiry == nil || at.Before(*credits.EarliestExpiry) {
			credits.EarliestExpiry = &at
		}
	}
	switch ch.Type {
	case constant.ChannelTypeClaudeOAuth:
		status, payload, err := claudeChannelOAuthFetch(ctx, ch,
			func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
				status, credits, err := service.FetchClaudeResetCredits(ctx, client, ch.GetBaseURL(), accessToken)
				if err != nil || credits == nil {
					return status, nil, err
				}
				return status, credits, nil
			},
			claudeUsageRequestTimeout,
			common.LogText("failed to fetch claude reset credits for auto reset"),
			"Failed to fetch reset credit details. Please try again later",
			true,
		)
		if err != nil {
			return nil, err
		}
		upstream, ok := payload.(*service.ClaudeResetCredits)
		if status < 200 || status >= 300 || !ok {
			return nil, upstreamStatusMessage(status)
		}
		credits.Available = upstream.AvailableCount
		for _, credit := range upstream.Credits {
			if credit.ExpiresAt != nil {
				keepEarliest(*credit.ExpiresAt)
			}
		}
	case constant.ChannelTypeCodex:
		status, body, err := codexChannelWhamFetch(ctx, ch,
			service.FetchCodexWhamRateLimitResetCredits,
			common.LogText("failed to fetch codex reset credits for auto reset"),
			"Failed to fetch reset credit details. Please try again later",
		)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, upstreamStatusMessage(status)
		}
		var upstream SubscriptionCodexResetCredits
		if err := common.Unmarshal(body, &upstream); err != nil {
			return nil, common.NewMessage("Failed to fetch reset credit details. Please try again later")
		}
		available := 0
		for _, credit := range upstream.Credits {
			if !strings.EqualFold(strings.TrimSpace(credit.Status), "available") {
				continue
			}
			available++
			if credit.ExpiresAt == nil {
				continue
			}
			if at, err := time.Parse(time.RFC3339, strings.TrimSpace(*credit.ExpiresAt)); err == nil {
				keepEarliest(at)
			}
		}
		credits.Available = available
		if upstream.AvailableCount != nil {
			credits.Available = *upstream.AvailableCount
		}
	default:
		return nil, common.NewMessage("This channel type does not support reset credits")
	}
	return credits, nil
}

func (channelAutoResetUpstream) Redeem(ctx context.Context, ch *model.Channel) service.ChannelAutoResetRedeemResult {
	switch ch.Type {
	case constant.ChannelTypeClaudeOAuth:
		var redeemErr error
		status, payload, err := claudeChannelOAuthFetch(ctx, ch,
			func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
				status, outcome, err := service.RedeemClaudeResetCredit(ctx, client, ch.GetBaseURL(), accessToken)
				redeemErr = err
				if err != nil || outcome == nil {
					return status, nil, err
				}
				return status, outcome, nil
			},
			claudeResetRequestTimeout,
			common.LogText("failed to auto reset claude usage"),
			"Failed to reset usage. Please try again later",
			false,
		)
		outcome, _ := payload.(*service.ClaudeResetOutcome)
		switch {
		case errors.Is(redeemErr, service.ErrClaudeResetNotAvailable):
			return service.ChannelAutoResetRedeemResult{Result: "not_available"}
		case errors.Is(redeemErr, service.ErrClaudeResetOutcomeUnknown):
			return service.ChannelAutoResetRedeemResult{Result: "outcome_unknown", MaybeConsumed: true}
		case err != nil:
			return service.ChannelAutoResetRedeemResult{Result: "error"}
		case status < 200 || status >= 300:
			return service.ChannelAutoResetRedeemResult{Result: fmt.Sprintf("upstream_status_%d", status)}
		case outcome == nil:
			return service.ChannelAutoResetRedeemResult{Result: "outcome_unknown", MaybeConsumed: true}
		}
		result := service.ChannelAutoResetRedeemResult{Result: outcome.Outcome, Success: outcome.Outcome == "reset"}
		if outcome.Outcome == "unknown" {
			result.MaybeConsumed = true
		}
		if outcome.CooldownUntil != nil {
			result.RetryAfter = *outcome.CooldownUntil
		}
		return result
	case constant.ChannelTypeCodex:
		status, _, err := codexChannelWhamFetch(ctx, ch,
			service.ConsumeCodexWhamRateLimitResetCredit,
			common.LogText("failed to auto reset codex usage"),
			"Failed to reset usage. Please try again later",
		)
		switch {
		case err != nil:
			// The consume request may have reached upstream.
			return service.ChannelAutoResetRedeemResult{Result: "outcome_unknown", MaybeConsumed: true}
		case status >= 200 && status < 300:
			model.ClearCodexChannelCooldown(ch.Id)
			return service.ChannelAutoResetRedeemResult{Result: "consumed", Success: true}
		case status >= 500:
			return service.ChannelAutoResetRedeemResult{Result: fmt.Sprintf("upstream_status_%d", status), MaybeConsumed: true}
		default:
			return service.ChannelAutoResetRedeemResult{Result: fmt.Sprintf("upstream_status_%d", status)}
		}
	}
	return service.ChannelAutoResetRedeemResult{Result: "unsupported_channel"}
}

// ---- Admin API ----

// resolveChannelAutoResetChannel loads the :id channel and checks that it
// supports reset credits. On failure the error response has been written.
func resolveChannelAutoResetChannel(c *gin.Context) (*model.Channel, bool) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid channel ID")
		return nil, false
	}
	ch, err := model.GetChannelById(channelId, true)
	if err != nil || ch == nil {
		common.ApiErrorT(c, "Channel not found")
		return nil, false
	}
	if !service.IsChannelAutoResetSupported(ch.Type) {
		common.ApiErrorT(c, "This channel type does not support reset credits")
		return nil, false
	}
	if ch.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels are not supported")
		return nil, false
	}
	return ch, true
}

type channelAutoResetView struct {
	Config            service.ChannelAutoResetConfig    `json:"config"`
	State             service.ChannelAutoResetState     `json:"state"`
	LastDecision      *service.ChannelAutoResetDecision `json:"last_decision,omitempty"`
	NextAllowedAt     int64                             `json:"next_allowed_at,omitempty"`
	NextAllowedReason string                            `json:"next_allowed_reason,omitempty"`
	NextAlarmAt       string                            `json:"next_alarm_at,omitempty"`
	AlarmGraceMinutes int                               `json:"alarm_grace_minutes"`
	WeeklyWindows     []string                          `json:"weekly_windows"`
}

func buildChannelAutoResetView(ch *model.Channel) channelAutoResetView {
	now := time.Now()
	cfg := service.LoadChannelAutoResetConfig(ch)
	view := channelAutoResetView{
		Config:            cfg,
		State:             service.LoadChannelAutoResetState(ch),
		AlarmGraceMinutes: int(service.ChannelAutoResetAlarmGrace / time.Minute),
		WeeklyWindows:     service.ChannelAutoResetWeeklyWindows(ch.Type),
	}
	next, reason := service.ChannelAutoResetNextAllowed(cfg, view.State, now)
	if reason != "" {
		view.NextAllowedAt, view.NextAllowedReason = next.Unix(), reason
	}
	view.NextAlarmAt = service.ChannelAutoResetNextAlarm(cfg, view.State, now)
	if engine := service.DefaultChannelAutoResetEngine(); engine != nil {
		if decision, ok := engine.LastDecision(ch.Id); ok {
			view.LastDecision = &decision
		}
	}
	return view
}

func GetChannelAutoResetCredit(c *gin.Context) {
	ch, ok := resolveChannelAutoResetChannel(c)
	if !ok {
		return
	}
	common.ApiSuccess(c, buildChannelAutoResetView(ch))
}

func UpdateChannelAutoResetCredit(c *gin.Context) {
	ch, ok := resolveChannelAutoResetChannel(c)
	if !ok {
		return
	}
	var cfg service.ChannelAutoResetConfig
	if err := common.DecodeJson(c.Request.Body, &cfg); err != nil {
		common.ApiErrorT(c, "Invalid request body.")
		return
	}
	cfg, err := service.NormalizeChannelAutoResetConfig(cfg, ch.Type, time.Now())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	cfg.UpdatedBy = c.GetInt("id")
	cfg.UpdatedByRole = c.GetInt("role")
	cfg.UpdatedAt = common.GetTimestamp()
	if err := model.UpdateChannelOtherInfoEntry(ch.Id, service.ChannelAutoResetConfigKey, cfg); err != nil {
		common.ApiError(c, err)
		return
	}
	service.SetChannelAutoResetEnabled(ch.Id, cfg.Enabled)
	recordManageAudit(c, "channel.auto_reset_credit_update", map[string]any{
		"id":      ch.Id,
		"name":    ch.Name,
		"enabled": cfg.Enabled,
	})
	fresh, err := model.GetChannelById(ch.Id, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, buildChannelAutoResetView(fresh))
}

// DryRunChannelAutoResetCredit evaluates the stored setting, or the draft in
// the body, against the current usage and reset credits without redeeming.
func DryRunChannelAutoResetCredit(c *gin.Context) {
	ch, ok := resolveChannelAutoResetChannel(c)
	if !ok {
		return
	}
	engine := service.DefaultChannelAutoResetEngine()
	if engine == nil {
		common.ApiError(c, service.ErrChannelAutoResetUnavailable)
		return
	}
	var req struct {
		Config *service.ChannelAutoResetConfig `json:"config"`
	}
	if c.Request.ContentLength != 0 {
		if err := common.DecodeJson(c.Request.Body, &req); err != nil {
			common.ApiErrorT(c, "Invalid request body.")
			return
		}
	}
	var draft *service.ChannelAutoResetConfig
	if req.Config != nil {
		// A draft is tried even when its switch is off.
		normalized, err := service.NormalizeChannelAutoResetConfig(*req.Config, ch.Type, time.Now())
		if err != nil {
			common.ApiError(c, err)
			return
		}
		draft = &normalized
	}
	common.ApiSuccess(c, engine.Evaluate(c.Request.Context(), ch, time.Now(), true, draft))
}
