package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func StartCodexOAuth(c *gin.Context) {
	flow, err := service.CreateCodexOAuthAuthorizationFlow(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"session_id": flow.SessionID,
			"auth_url":   flow.AuthURL,
			"expires_in": flow.ExpiresIn,
		},
	})
}

// ExchangeCodexOAuth turns the pasted callback URL into a Codex channel key
// in the same JSON format the relay already consumes. Nothing is persisted
// here; the console saves the key with the channel form.
func ExchangeCodexOAuth(c *gin.Context) {
	var req oauthExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	code, state := service.ParseCodexAuthorizationInput(req.Code)
	if code == "" {
		common.ApiErrorT(c, "Authorization code is required")
		return
	}
	if state == "" {
		common.ApiErrorT(c, "Paste the complete callback URL so the state can be verified")
		return
	}
	proxy := strings.TrimSpace(req.Proxy)
	if err := service.ValidateProxyURL(proxy); err != nil {
		common.ApiError(c, fmt.Errorf("invalid proxy: %w", err))
		return
	}

	session := service.TakeCodexOAuthAuthorizationSession(strings.TrimSpace(req.SessionID), c.GetInt("id"))
	if session == nil {
		common.ApiErrorT(c, "The authorization session was not found or has expired. Generate a new authorization link")
		return
	}
	if !service.OAuthStateMatches(session.State, state) {
		common.ApiErrorT(c, "The pasted state does not match this authorization session. Generate a new authorization link")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	res, err := service.ExchangeCodexAuthorizationCodeWithProxy(ctx, code, session.Verifier, proxy)
	if err != nil {
		common.SysError(common.LogText("codex oauth code exchange failed: %s", err.Error()))
		common.ApiErrorT(c, "Authorization code exchange failed. Generate a new authorization link and try again")
		return
	}

	accountID, ok := service.ExtractCodexAccountIDFromJWT(res.IDToken)
	if !ok {
		accountID, ok = service.ExtractCodexAccountIDFromJWT(res.AccessToken)
	}
	if !ok {
		common.ApiErrorT(c, "The token response does not carry a ChatGPT account id")
		return
	}
	email, ok := service.ExtractEmailFromJWT(res.IDToken)
	if !ok {
		email, _ = service.ExtractEmailFromJWT(res.AccessToken)
	}

	now := time.Now()
	key := codex.OAuthKey{
		IDToken:      res.IDToken,
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		AccountID:    accountID,
		LastRefresh:  now.Format(time.RFC3339),
		Email:        email,
		Type:         "codex",
		Expired:      res.ExpiresAt.Format(time.RFC3339),
	}
	encoded, err := common.Marshal(key)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"key":        string(encoded),
			"account_id": accountID,
			"email":      email,
			"expires_at": key.Expired,
		},
	})
}
