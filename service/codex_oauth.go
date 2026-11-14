package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthTokenURL = "https://auth.openai.com/oauth/token"
	codexJWTClaimPath  = "https://api.openai.com/auth"
	defaultHTTPTimeout = 20 * time.Second
)

type CodexOAuthTokenResult struct {
	AccessToken string
	// RefreshToken is the rotated refresh token, or the one sent when the
	// response did not rotate it.
	RefreshToken string
	// IDToken is empty when the response carried none.
	IDToken   string
	ExpiresAt time.Time
}

// codexOAuthErrorBodyLimit bounds how much of a token endpoint answer is read.
const codexOAuthErrorBodyLimit = 64 << 10

// CodexOAuthRefreshError is a non-2xx answer of the token endpoint. Code is
// the OAuth error code (error, or error.code), e.g. invalid_grant.
type CodexOAuthRefreshError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *CodexOAuthRefreshError) Error() string {
	msg := fmt.Sprintf("codex oauth refresh failed: status=%d", e.StatusCode)
	if e.Code != "" {
		msg += " code=" + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// codexPermanentRefreshErrorCodes are OAuth error codes saying the refresh
// token or the session behind it is gone, so no retry can succeed and the
// account must be authorized again (the token-related subset of sub2api
// isNonRetryableRefreshError, 2026-11-05). Client or scope errors such as
// invalid_client are configuration problems and are not in the list.
var codexPermanentRefreshErrorCodes = []string{
	"invalid_grant",
	"invalid_refresh_token",
	"refresh_token_reused",
	"refresh_token_invalidated",
	"refresh_token_expired",
	"token_expired",
	"app_session_terminated",
}

// CodexPermanentRefreshErrorCode returns the error code when err is a 400 or
// 401 answer of the token endpoint whose structured error code says the
// refresh token is no longer valid.
func CodexPermanentRefreshErrorCode(err error) (string, bool) {
	var refreshErr *CodexOAuthRefreshError
	if !errors.As(err, &refreshErr) {
		return "", false
	}
	if refreshErr.StatusCode != http.StatusBadRequest && refreshErr.StatusCode != http.StatusUnauthorized {
		return "", false
	}
	code := strings.ToLower(strings.TrimSpace(refreshErr.Code))
	if slices.Contains(codexPermanentRefreshErrorCodes, code) {
		return code, true
	}
	return "", false
}

// CodexOAuthExchangeError is a non-2xx answer of the token endpoint to an
// authorization code exchange, with the same fields as a refresh error.
type CodexOAuthExchangeError CodexOAuthRefreshError

func (e *CodexOAuthExchangeError) Error() string {
	msg := fmt.Sprintf("codex oauth code exchange failed: status=%d", e.StatusCode)
	if e.Code != "" {
		msg += " code=" + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// CodexRefreshTokenRejectedError says the stored refresh token was already
// rejected for good with Code, so it is not sent again.
type CodexRefreshTokenRejectedError struct {
	Code string
}

func (e *CodexRefreshTokenRejectedError) Error() string {
	return fmt.Sprintf("codex channel: the refresh token was rejected (%s); authorize the account again", e.Code)
}

// CodexOAuthErrorCode returns the OAuth error code the token endpoint gave
// for a failed code exchange or refresh, and whether it means the account
// must be authorized again. The code is safe to show: anything that is not a
// short identifier is dropped. Timeouts and network errors yield ok == false.
func CodexOAuthErrorCode(err error) (code string, reauthorize bool, ok bool) {
	var rejected *CodexRefreshTokenRejectedError
	var refreshErr *CodexOAuthRefreshError
	var exchangeErr *CodexOAuthExchangeError
	switch {
	case errors.As(err, &rejected):
		code, reauthorize = rejected.Code, true
	case errors.As(err, &refreshErr):
		code = refreshErr.Code
		_, reauthorize = CodexPermanentRefreshErrorCode(err)
	case errors.As(err, &exchangeErr):
		code = exchangeErr.Code
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return "", false, false
	}
	if strings.Trim(code, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
		return "", false, false
	}
	return code, reauthorize, true
}

func RefreshCodexOAuthToken(ctx context.Context, refreshToken string) (*CodexOAuthTokenResult, error) {
	return RefreshCodexOAuthTokenWithProxy(ctx, refreshToken, "")
}

func RefreshCodexOAuthTokenWithProxy(ctx context.Context, refreshToken string, proxyURL string) (*CodexOAuthTokenResult, error) {
	client, err := getCodexOAuthHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	return refreshCodexOAuthToken(ctx, client, CodexOAuthTokenURL, codexOAuthClientID, refreshToken)
}

func refreshCodexOAuthToken(
	ctx context.Context,
	client *http.Client,
	tokenURL string,
	clientID string,
	refreshToken string,
) (*CodexOAuthTokenResult, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, errors.New("empty refresh_token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, codexOAuthErrorBodyLimit))
	if err != nil {
		return nil, err
	}
	// The status decides first: an error page that is not JSON must still
	// report the status, and a JSON error must report its code.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseCodexOAuthRefreshError(resp.StatusCode, body)
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("codex oauth refresh response missing fields")
	}
	// A refresh that does not rotate the refresh token keeps the old one.
	newRefreshToken := strings.TrimSpace(payload.RefreshToken)
	if newRefreshToken == "" {
		newRefreshToken = rt
	}

	return &CodexOAuthTokenResult{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: newRefreshToken,
		IDToken:      strings.TrimSpace(payload.IDToken),
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// parseCodexOAuthRefreshError reads the OAuth error of a failed refresh:
// {"error":"invalid_grant","error_description":"..."} or
// {"error":{"code":"refresh_token_reused","message":"..."}}. Only the code and
// description are kept, never the raw body.
func parseCodexOAuthRefreshError(statusCode int, body []byte) *CodexOAuthRefreshError {
	refreshErr := &CodexOAuthRefreshError{StatusCode: statusCode}
	var payload struct {
		Error            any    `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if common.Unmarshal(body, &payload) != nil {
		return refreshErr
	}
	refreshErr.Description = payload.ErrorDescription
	switch value := payload.Error.(type) {
	case string:
		refreshErr.Code = value
	case map[string]any:
		if code, ok := value["code"].(string); ok {
			refreshErr.Code = code
		} else if code, ok := value["type"].(string); ok {
			refreshErr.Code = code
		}
		if message, ok := value["message"].(string); ok && refreshErr.Description == "" {
			refreshErr.Description = message
		}
	}
	const descriptionLimit = 300
	if len(refreshErr.Description) > descriptionLimit {
		refreshErr.Description = refreshErr.Description[:descriptionLimit]
	}
	return refreshErr
}

func getCodexOAuthHTTPClient(proxyURL string) (*http.Client, error) {
	baseClient, err := GetHttpClientWithProxy(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	if baseClient == nil {
		return &http.Client{Timeout: defaultHTTPTimeout}, nil
	}
	clientCopy := *baseClient
	clientCopy.Timeout = defaultHTTPTimeout
	return &clientCopy, nil
}

func ExtractCodexAccountIDFromJWT(token string) (string, bool) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return "", false
	}
	raw, ok := claims[codexJWTClaimPath]
	if !ok {
		return "", false
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := obj["chatgpt_account_id"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func ExtractEmailFromJWT(token string) (string, bool) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return "", false
	}
	v, ok := claims["email"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func decodeJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := common.Unmarshal(payloadRaw, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

// Codex browser authorization flow. The admin opens the authorization URL,
// signs in, and pastes the localhost callback URL back into the console; the
// gateway holds the PKCE verifier and state and exchanges the code.
const (
	codexOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	codexOAuthRedirectURI  = "http://localhost:1455/auth/callback"
	codexOAuthScope        = "openid profile email offline_access"
	codexOAuthUserAgent    = "codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	codexOAuthOriginator   = "codex_cli_rs"
)

// CodexOAuthTokenURL is a variable so tests can point the code exchange and
// the token refresh at an httptest server.
var CodexOAuthTokenURL = codexOAuthTokenURL

type CodexOAuthAuthorizationFlow struct {
	SessionID string
	AuthURL   string
	ExpiresIn int
}

type CodexOAuthExchangeResult struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
}

// CreateCodexOAuthAuthorizationFlow generates state and PKCE material, stores
// them server side for the admin user, and returns the authorization URL.
func CreateCodexOAuthAuthorizationFlow(userID int) (*CodexOAuthAuthorizationFlow, error) {
	stateBytes, err := randomBytes(32)
	if err != nil {
		return nil, err
	}
	verifierBytes, err := randomBytes(64)
	if err != nil {
		return nil, err
	}
	state := hex.EncodeToString(stateBytes)
	verifier := hex.EncodeToString(verifierBytes)

	sessionID, err := storeOAuthAuthorizationSession(&OAuthAuthorizationSession{
		Provider:  oauthProviderCodex,
		State:     state,
		Verifier:  verifier,
		UserID:    userID,
		CreatedAt: time.Now(),
	})
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", codexOAuthClientID)
	query.Set("redirect_uri", codexOAuthRedirectURI)
	query.Set("scope", codexOAuthScope)
	query.Set("state", state)
	query.Set("code_challenge", pkceChallengeS256(verifier))
	query.Set("code_challenge_method", "S256")
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	return &CodexOAuthAuthorizationFlow{
		SessionID: sessionID,
		AuthURL:   codexOAuthAuthorizeURL + "?" + query.Encode(),
		ExpiresIn: int(OAuthAuthorizationSessionTTL.Seconds()),
	}, nil
}

// TakeCodexOAuthAuthorizationSession consumes the pending Codex session
// started by userID; it returns nil when none is usable.
func TakeCodexOAuthAuthorizationSession(sessionID string, userID int) *OAuthAuthorizationSession {
	return takeOAuthAuthorizationSession(sessionID, oauthProviderCodex, userID)
}

// ParseCodexAuthorizationInput accepts the full callback URL
// (http://localhost:1455/auth/callback?code=...&state=...), a bare query
// string, or "code#state". A bare code without state is returned with an
// empty state so the caller can reject it.
func ParseCodexAuthorizationInput(input string) (code string, state string) {
	v := strings.TrimSpace(input)
	if v == "" {
		return "", ""
	}
	if strings.Contains(v, "code=") {
		if u, err := url.Parse(v); err == nil && u.Query().Get("code") != "" {
			return strings.TrimSpace(u.Query().Get("code")), strings.TrimSpace(u.Query().Get("state"))
		}
		if q, err := url.ParseQuery(strings.TrimPrefix(v, "?")); err == nil && q.Get("code") != "" {
			return strings.TrimSpace(q.Get("code")), strings.TrimSpace(q.Get("state"))
		}
	}
	code, state, _ = strings.Cut(v, "#")
	return strings.TrimSpace(code), strings.TrimSpace(state)
}

// ExchangeCodexAuthorizationCodeWithProxy trades the authorization code for a
// token set through the proxy, if any.
func ExchangeCodexAuthorizationCodeWithProxy(ctx context.Context, code string, verifier string, proxyURL string) (*CodexOAuthExchangeResult, error) {
	client, err := getCodexOAuthHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", codexOAuthClientID)
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", codexOAuthRedirectURI)
	form.Set("code_verifier", strings.TrimSpace(verifier))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexOAuthUserAgent)
	req.Header.Set("originator", codexOAuthOriginator)

	resp, err := noRedirectClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, codexOAuthErrorBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exchangeErr := (*CodexOAuthExchangeError)(parseCodexOAuthRefreshError(resp.StatusCode, body))
		// The description is upstream text; drop it if it repeats what was
		// sent, so neither the code nor the verifier reaches a log.
		for _, secret := range []string{form.Get("code"), form.Get("code_verifier")} {
			if secret != "" && strings.Contains(exchangeErr.Description, secret) {
				exchangeErr.Description = ""
			}
		}
		return nil, exchangeErr
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.RefreshToken) == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("codex oauth token response missing fields")
	}
	return &CodexOAuthExchangeResult{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		IDToken:      strings.TrimSpace(payload.IDToken),
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}
