package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// ---- User: reset credits of the bound account (administrator switch) ----

// userAllowsSubscriptionResetCredit reads the per-user administrator switch
// from the database so a revoked permission takes effect at once.
func userAllowsSubscriptionResetCredit(userId int) bool {
	setting, err := model.GetUserSetting(userId, true)
	return err == nil && setting.AllowSubscriptionResetCredit
}

// resolveSelfResetCreditSubscription applies the per-user switch and then the
// same checks as the self usage endpoint. On failure the error response has
// already been written and ok is false.
func resolveSelfResetCreditSubscription(c *gin.Context) (*model.UserSubscription, *model.Channel, bool) {
	if !userAllowsSubscriptionResetCredit(c.GetInt("id")) {
		common.ApiErrorT(c, "Reset credits are not enabled for your account")
		return nil, nil, false
	}
	return resolveSelfBoundSubscription(c)
}

// writeSubscriptionUpstreamResult answers with the projected payload, or with
// the upstream status alone when upstream did not succeed.
func writeSubscriptionUpstreamResult(c *gin.Context, statusCode int, payload any) {
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
		"data":            payload,
	})
}

// SubscriptionCodexResetCredit is one Codex reset credit trimmed for the
// subscriber: no credit id and no profile of the account owner.
type SubscriptionCodexResetCredit struct {
	ResetType       string  `json:"reset_type,omitempty"`
	Status          string  `json:"status,omitempty"`
	GrantedAt       *string `json:"granted_at,omitempty"`
	ExpiresAt       *string `json:"expires_at,omitempty"`
	RedeemStartedAt *string `json:"redeem_started_at,omitempty"`
	RedeemedAt      *string `json:"redeemed_at,omitempty"`
	Title           string  `json:"title,omitempty"`
	Description     string  `json:"description,omitempty"`
}

type SubscriptionCodexResetCredits struct {
	Credits          []SubscriptionCodexResetCredit `json:"credits"`
	AvailableCount   *int                           `json:"available_count,omitempty"`
	TotalEarnedCount *int                           `json:"total_earned_count,omitempty"`
}

// SubscriptionCodexResetOutcome is the trimmed answer of a Codex consume.
type SubscriptionCodexResetOutcome struct {
	WindowsReset *int   `json:"windows_reset,omitempty"`
	Code         string `json:"code,omitempty"`
}

func GetSubscriptionSelfResetCredits(c *gin.Context) {
	_, ch, ok := resolveSelfResetCreditSubscription(c)
	if !ok {
		return
	}
	switch ch.Type {
	case constant.ChannelTypeClaudeOAuth:
		statusCode, payload, err := claudeChannelOAuthFetch(
			c.Request.Context(), ch,
			func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
				status, credits, err := service.FetchClaudeResetCredits(ctx, client, ch.GetBaseURL(), accessToken)
				if err != nil || credits == nil {
					return status, nil, err
				}
				return status, credits, nil
			},
			claudeUsageRequestTimeout,
			common.LogText("failed to fetch claude reset credits for subscription"),
			"Failed to fetch reset credit details. Please try again later",
			false,
		)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		writeSubscriptionUpstreamResult(c, statusCode, payload)
	case constant.ChannelTypeCodex:
		statusCode, body, err := codexChannelWhamFetch(
			c.Request.Context(), ch,
			service.FetchCodexWhamRateLimitResetCredits,
			common.LogText("failed to fetch codex reset credits for subscription"),
			"Failed to fetch reset credit details. Please try again later",
		)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		credits := SubscriptionCodexResetCredits{Credits: []SubscriptionCodexResetCredit{}}
		_ = common.Unmarshal(body, &credits)
		if credits.Credits == nil {
			credits.Credits = []SubscriptionCodexResetCredit{}
		}
		writeSubscriptionUpstreamResult(c, statusCode, credits)
	}
}

// ResetSubscriptionSelfUsage redeems one reset credit of the account bound to
// the caller's own subscription. Eligibility is checked again here, only one
// redemption per bound account runs at a time, and every attempt is written
// to the caller's audit log.
func ResetSubscriptionSelfUsage(c *gin.Context) {
	sub, ch, ok := resolveSelfResetCreditSubscription(c)
	if !ok {
		return
	}
	release, acquired, err := service.AcquireSubscriptionResetLock(ch.Id)
	if err != nil {
		common.SysError(common.LogText("failed to lock subscription reset: %s", err.Error()))
		common.ApiErrorT(c, "Failed to reset usage. Please try again later")
		return
	}
	if !acquired {
		common.ApiErrorT(c, "Another reset for this account is still in progress. Refresh the reset credits after it finishes")
		return
	}
	defer release()

	var provider, result string
	switch ch.Type {
	case constant.ChannelTypeClaudeOAuth:
		provider = "claude"
		var redeemErr error
		statusCode, payload, fetchErr := claudeChannelOAuthFetch(
			c.Request.Context(), ch,
			func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
				status, outcome, err := service.RedeemClaudeResetCredit(ctx, client, ch.GetBaseURL(), accessToken)
				redeemErr = err
				if err != nil || outcome == nil {
					return status, nil, err
				}
				return status, outcome, nil
			},
			claudeResetRequestTimeout,
			common.LogText("failed to reset claude usage for subscription"),
			"Failed to reset usage. Please try again later",
			false,
		)
		outcome, _ := payload.(*service.ClaudeResetOutcome)
		switch {
		case errors.Is(redeemErr, service.ErrClaudeResetNotAvailable):
			result = "not_available"
		case errors.Is(redeemErr, service.ErrClaudeResetOutcomeUnknown):
			result = "outcome_unknown"
		case fetchErr != nil:
			result = "error"
		case statusCode < 200 || statusCode >= 300:
			result = fmt.Sprintf("upstream_status_%d", statusCode)
		case outcome != nil:
			result = outcome.Outcome
		default:
			result = "outcome_unknown"
		}
		if fetchErr != nil {
			common.ApiError(c, fetchErr)
		} else {
			writeSubscriptionUpstreamResult(c, statusCode, payload)
		}
	case constant.ChannelTypeCodex:
		provider = "codex"
		statusCode, body, fetchErr := codexChannelWhamFetch(
			c.Request.Context(), ch,
			service.ConsumeCodexWhamRateLimitResetCredit,
			common.LogText("failed to reset codex usage for subscription"),
			"Failed to reset usage. Please try again later",
		)
		switch {
		case fetchErr != nil:
			result = "error"
		case statusCode < 200 || statusCode >= 300:
			result = fmt.Sprintf("upstream_status_%d", statusCode)
		default:
			result = "consumed"
		}
		if fetchErr != nil {
			common.ApiError(c, fetchErr)
		} else {
			var outcome SubscriptionCodexResetOutcome
			_ = common.Unmarshal(body, &outcome)
			writeSubscriptionUpstreamResult(c, statusCode, outcome)
		}
	}
	// A redemption changes the windows; the next usage query asks upstream.
	service.InvalidateSubscriptionUsage(ch.Id)

	recordUserSecurityAudit(c, sub.UserId, "subscription.reset_credit_redeem", map[string]any{
		"subscription_id": sub.Id,
		"channel_id":      ch.Id,
		"provider":        provider,
		"result":          result,
		"success":         result == "reset" || result == "consumed",
	})
}
