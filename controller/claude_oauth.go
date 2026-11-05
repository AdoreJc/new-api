package controller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// oauthExchangeRequest is shared by the Claude and Codex browser flows. Code
// is whatever the admin pasted from the callback page: "code#state" for
// Claude, the full localhost callback URL (or "code#state") for Codex.
type oauthExchangeRequest struct {
	SessionID string `json:"session_id"`
	Code      string `json:"code"`
	Proxy     string `json:"proxy"`
}

func StartClaudeOAuth(c *gin.Context) {
	flow, err := service.CreateClaudeOAuthAuthorizationFlow(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"session_id":   flow.SessionID,
			"auth_url":     flow.AuthURL,
			"expires_in":   flow.ExpiresIn,
			"redirect_uri": service.ClaudeOAuthRedirectURI,
		},
	})
}

// ExchangeClaudeOAuth turns the pasted "code#state" into a channel key. The
// key is returned to the console and saved with the channel form; nothing is
// persisted here.
func ExchangeClaudeOAuth(c *gin.Context) {
	var req oauthExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	code, state := service.SplitClaudeAuthorizationInput(req.Code)
	if code == "" {
		common.ApiErrorT(c, "Authorization code is required")
		return
	}
	if state == "" {
		common.ApiErrorT(c, "Paste the complete code#state value shown after authorization")
		return
	}
	proxy := strings.TrimSpace(req.Proxy)
	if err := service.ValidateProxyURL(proxy); err != nil {
		common.ApiError(c, fmt.Errorf("invalid proxy: %w", err))
		return
	}

	session := service.TakeClaudeOAuthAuthorizationSession(strings.TrimSpace(req.SessionID), c.GetInt("id"))
	if session == nil {
		common.ApiErrorT(c, "The authorization session was not found or has expired. Generate a new authorization link")
		return
	}
	if !service.OAuthStateMatches(session.State, state) {
		common.ApiErrorT(c, "The pasted state does not match this authorization session. Generate a new authorization link")
		return
	}

	client, err := service.GetClaudeOAuthHTTPClient(proxy)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	res, err := service.ExchangeClaudeAuthorizationCode(ctx, client, code, session.Verifier, state)
	if err != nil {
		common.SysError(common.LogText("claude oauth code exchange failed: %s", err.Error()))
		common.ApiErrorT(c, "Authorization code exchange failed. Generate a new authorization link and try again")
		return
	}

	key := service.BuildClaudeOAuthKey(res, time.Now())
	encoded, err := common.Marshal(key)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"key":          string(encoded),
			"email":        key.Email,
			"org_uuid":     key.OrgUUID,
			"account_uuid": key.AccountUUID,
			"scope":        key.Scope,
			"expires_at":   key.ExpiresAt,
		},
	})
}

func RefreshClaudeChannelCredential(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	key, ch, err := service.RefreshClaudeChannelCredential(ctx, channelId, service.ClaudeCredentialRefreshOptions{ResetCaches: true, Force: true})
	if err != nil {
		common.SysError(common.LogText("failed to refresh claude oauth channel credential: %s", err.Error()))
		if service.IsPermanentClaudeOAuthError(err) {
			common.ApiErrorT(c, "The refresh token was rejected. Re-authorize the channel to obtain a new credential")
			return
		}
		common.ApiErrorT(c, "Failed to refresh credentials. Please try again later")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "refreshed",
		"data": gin.H{
			"expires_at":   key.ExpiresAt,
			"last_refresh": key.LastRefresh,
			"email":        key.Email,
			"org_uuid":     key.OrgUUID,
			"channel_id":   ch.Id,
			"channel_type": ch.Type,
			"channel_name": ch.Name,
		},
	})
}
