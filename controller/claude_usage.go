package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	claudeUsageRequestTimeout = 25 * time.Second
	// claudeResetRequestTimeout covers the status query, the profile lookup,
	// the single reset POST and the status re-fetch.
	claudeResetRequestTimeout = 60 * time.Second
)

func GetClaudeChannelUsage(c *gin.Context) {
	fetchClaudeChannelOAuthData(
		c,
		func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
			status, body, err := service.FetchClaudeOAuthUsage(ctx, client, ch.GetBaseURL(), accessToken)
			if err != nil {
				return status, nil, err
			}
			var payload any
			if common.Unmarshal(body, &payload) != nil {
				payload = string(body)
			}
			// Windows the usage API left out are filled from the rate-limit
			// headers sampled on relayed requests; API values take precedence.
			if doc, ok := payload.(map[string]any); ok && status >= 200 && status < 300 {
				service.BackfillClaudeUsageWindows(doc, service.LoadClaudePassiveUsage(ch), time.Now())
			}
			return status, payload, nil
		},
		claudeUsageRequestTimeout,
		common.LogText("failed to fetch claude usage"),
		"Failed to fetch usage. Please try again later",
	)
}

func GetClaudeChannelResetCredits(c *gin.Context) {
	fetchClaudeChannelOAuthData(
		c,
		func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (int, any, error) {
			status, credits, err := service.FetchClaudeResetCredits(ctx, client, ch.GetBaseURL(), accessToken)
			if err != nil || credits == nil {
				return status, nil, err
			}
			return status, credits, nil
		},
		claudeUsageRequestTimeout,
		common.LogText("failed to fetch claude reset credits"),
		"Failed to fetch reset credit details. Please try again later",
	)
}

func ResetClaudeChannelUsage(c *gin.Context) {
	ch, ok := resolveClaudeOAuthChannel(c)
	if !ok {
		return
	}
	// The manual reset shares the redemption lease with the automatic reset
	// and subscriber resets, and is recorded before it is sent.
	redemption, err := service.BeginChannelRedemption(ch.Id, "manual")
	if errors.Is(err, service.ErrChannelRedemptionBusy) {
		common.ApiErrorT(c, "Another reset for this account is still in progress. Refresh the reset credits after it finishes")
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	result := redeemClaudeChannelCredit(c.Request.Context(), ch, common.LogText("failed to reset claude usage"))
	redemption.Finish(result.Success || result.MaybeConsumed, nil)
	if result.Err != nil {
		common.ApiError(c, result.Err)
		return
	}
	writeClaudeChannelOAuthResult(c, result.Status, result.Payload)
}

type claudeOAuthFetchFunc func(ctx context.Context, client *http.Client, ch *model.Channel, accessToken string) (statusCode int, payload any, err error)

// fetchClaudeChannelOAuthData resolves the channel token, calls fetch, and
// retries once after a forced refresh when upstream answers 401. A 403 (scope
// or plan) is reported as is.
func fetchClaudeChannelOAuthData(c *gin.Context, fetch claudeOAuthFetchFunc, timeout time.Duration, logPrefix string, userMessage string) {
	ch, ok := resolveClaudeOAuthChannel(c)
	if !ok {
		return
	}
	statusCode, payload, err := claudeChannelOAuthFetch(c.Request.Context(), ch, fetch, timeout, logPrefix, userMessage, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	writeClaudeChannelOAuthResult(c, statusCode, payload)
}

// resolveClaudeOAuthChannel loads the :id Claude OAuth channel. On failure
// the error response has been written.
func resolveClaudeOAuthChannel(c *gin.Context) (*model.Channel, bool) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return nil, false
	}
	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return nil, false
	}
	if ch == nil {
		common.ApiErrorT(c, "Channel not found")
		return nil, false
	}
	if ch.Type != constant.ChannelTypeClaudeOAuth {
		common.ApiErrorT(c, "This channel is not a Claude Subscription (OAuth) channel")
		return nil, false
	}
	if ch.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels are not supported")
		return nil, false
	}
	return ch, true
}

func writeClaudeChannelOAuthResult(c *gin.Context, statusCode int, payload any) {
	if statusCode < 200 || statusCode >= 300 {
		common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Upstream returned status {{status}}", map[string]any{"status": statusCode}), gin.H{
			"upstream_status": statusCode,
			"data":            payload,
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

// claudeChannelOAuthFetch runs fetch with the channel's access token and
// retries once after a forced refresh when upstream answers 401. With
// refreshCooldown, which the subscriber usage endpoints set because clients
// poll them, a forced refresh happens at most once per
// service.SubscriptionUsageRefreshCooldown and channel; inside the cooldown
// the retry only picks up a key another caller already renewed. A non-nil
// error is ready for common.ApiError; upstream statuses are not errors.
func claudeChannelOAuthFetch(ctx context.Context, ch *model.Channel, fetch claudeOAuthFetchFunc, timeout time.Duration, logPrefix string, userMessage string, refreshCooldown bool) (statusCode int, payload any, err error) {
	key, err := service.ParseClaudeOAuthKey(ch.Key)
	if err != nil {
		common.SysError(common.LogText("failed to parse oauth key: %s", err.Error()))
		return 0, nil, common.NewMessage("Failed to parse credentials. Please check the channel configuration")
	}
	if !key.HasScope("user:profile") {
		return 0, nil, common.NewMessage("This credential lacks the user:profile scope required for usage queries. Re-authorize the channel")
	}

	setting := ch.GetSetting()
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return 0, nil, err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	statusCode, payload, err = fetch(fetchCtx, client, ch, key.AccessToken)
	if err == nil && statusCode == http.StatusUnauthorized && key.RefreshToken != "" {
		force := !refreshCooldown || service.ClaimSubscriptionUsageRefresh(ch.Id, time.Now())
		refreshCtx, refreshCancel := context.WithTimeout(ctx, 20*time.Second)
		refreshedKey, _, refreshErr := service.RefreshClaudeChannelCredential(refreshCtx, ch.Id, service.ClaudeCredentialRefreshOptions{ResetCaches: true, Force: force})
		refreshCancel()
		if refreshErr != nil {
			common.SysError(common.LogText("%s: token refresh after upstream %d failed: %s", logPrefix, statusCode, refreshErr.Error()))
		} else if force || refreshedKey.AccessToken != key.AccessToken {
			retryCtx, retryCancel := context.WithTimeout(ctx, timeout)
			defer retryCancel()
			statusCode, payload, err = fetch(retryCtx, client, ch, refreshedKey.AccessToken)
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, service.ErrClaudeResetNotAvailable):
			return statusCode, nil, common.NewMessage("No reset credit is redeemable right now")
		case errors.Is(err, service.ErrClaudeResetOutcomeUnknown):
			common.SysError(logPrefix + ": " + err.Error())
			return statusCode, nil, common.NewMessage("The reset request was interrupted and its result is unknown. Query the reset credits again before redeeming another credit")
		default:
			common.SysError(logPrefix + ": " + err.Error())
			return statusCode, nil, common.NewMessage(userMessage)
		}
	}
	return statusCode, payload, nil
}
