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
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func GetCodexChannelUsage(c *gin.Context) {
	fetchCodexChannelWhamData(
		c,
		service.FetchCodexWhamUsage,
		common.LogText("failed to fetch codex usage"),
		"Failed to fetch usage. Please try again later",
	)
}

func GetCodexChannelRateLimitResetCredits(c *gin.Context) {
	fetchCodexChannelWhamData(
		c,
		service.FetchCodexWhamRateLimitResetCredits,
		common.LogText("failed to fetch codex reset credits"),
		"Failed to fetch reset credit details. Please try again later",
	)
}

func ResetCodexChannelUsage(c *gin.Context) {
	fetchCodexChannelWhamData(
		c,
		service.ConsumeCodexWhamRateLimitResetCredit,
		common.LogText("failed to reset codex usage"),
		"Failed to reset usage. Please try again later",
	)
}

type codexWhamFetchFunc func(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	accessToken string,
	accountID string,
) (statusCode int, body []byte, err error)

func fetchCodexChannelWhamData(
	c *gin.Context,
	fetch codexWhamFetchFunc,
	logPrefix string,
	userMessage string,
) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if ch == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
		return
	}
	if ch.Type != constant.ChannelTypeCodex {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel type is not Codex"})
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return
	}

	statusCode, body, err := codexChannelWhamFetch(c.Request.Context(), ch, fetch, logPrefix, userMessage)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	var payload any
	if common.Unmarshal(body, &payload) != nil {
		payload = string(body)
	}

	ok := statusCode >= 200 && statusCode < 300
	resp := gin.H{
		"success":         ok,
		"message":         "",
		"upstream_status": statusCode,
		"data":            payload,
	}
	if !ok {
		resp["message"] = fmt.Sprintf("upstream status: %d", statusCode)
	}
	c.JSON(http.StatusOK, resp)
}

// codexChannelWhamFetch runs fetch with the channel's access token and
// retries once after a token refresh when upstream answers 401/403. A non-nil
// error is ready for common.ApiError; upstream statuses are not errors.
func codexChannelWhamFetch(ctx context.Context, ch *model.Channel, fetch codexWhamFetchFunc, logPrefix string, userMessage string) (statusCode int, body []byte, err error) {
	oauthKey, err := codex.ParseOAuthKey(strings.TrimSpace(ch.Key))
	if err != nil {
		common.SysError(common.LogText("failed to parse oauth key: %s", err.Error()))
		return 0, nil, common.NewMessage("Failed to parse credentials. Please check the channel configuration")
	}
	if stashed, ok := service.StashedOAuthChannelKey(ch.Id, oauthKey.RefreshToken); ok {
		if renewed, err := codex.ParseOAuthKey(stashed); err == nil {
			oauthKey = renewed
		}
	}
	accessToken := strings.TrimSpace(oauthKey.AccessToken)
	accountID := strings.TrimSpace(oauthKey.AccountID)
	if accessToken == "" {
		return 0, nil, errors.New("codex channel: access_token is required")
	}
	if accountID == "" {
		return 0, nil, errors.New("codex channel: account_id is required")
	}

	setting := ch.GetSetting()
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return 0, nil, err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	statusCode, body, err = fetch(fetchCtx, client, ch.GetBaseURL(), accessToken, accountID)
	if err != nil {
		common.SysError(logPrefix + ": " + err.Error())
		return 0, nil, common.NewMessage(userMessage)
	}

	if (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) && strings.TrimSpace(oauthKey.RefreshToken) != "" {
		refreshCtx, refreshCancel := context.WithTimeout(ctx, 10*time.Second)
		defer refreshCancel()

		refreshedKey, _, refreshErr := service.RefreshCodexChannelCredential(refreshCtx, ch.Id, service.CodexCredentialRefreshOptions{ResetCaches: true})
		if refreshErr != nil {
			common.SysError(common.LogText("%s: token refresh after upstream %d failed: %s", logPrefix, statusCode, refreshErr.Error()))
		} else {
			retryCtx, retryCancel := context.WithTimeout(ctx, 15*time.Second)
			defer retryCancel()
			statusCode, body, err = fetch(retryCtx, client, ch.GetBaseURL(), refreshedKey.AccessToken, accountID)
			if err != nil {
				common.SysError(common.LogText("%s after refresh: %s", logPrefix, err.Error()))
				return 0, nil, common.NewMessage(userMessage)
			}
		}
	}
	return statusCode, body, nil
}
