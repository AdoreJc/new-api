package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func RefreshCodexOAuthToken(ctx context.Context, refreshToken string) (*CodexOAuthTokenResult, error) {
	return RefreshCodexOAuthTokenWithProxy(ctx, refreshToken, "")
}

func RefreshCodexOAuthTokenWithProxy(ctx context.Context, refreshToken string, proxyURL string) (*CodexOAuthTokenResult, error) {
	client, err := getCodexOAuthHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	return refreshCodexOAuthToken(ctx, client, codexOAuthTokenURL, codexOAuthClientID, refreshToken)
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

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err := common.DecodeJson(resp.Body, &payload); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex oauth refresh failed: status=%d", resp.StatusCode)
	}

	if strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.RefreshToken) == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("codex oauth refresh response missing fields")
	}

	return &CodexOAuthTokenResult{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
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

// CodexOAuthTokenURL is a variable so tests can point the code exchange at an
// httptest server.
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

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex oauth code exchange failed: status=%d", resp.StatusCode)
	}
	if err := common.DecodeJson(resp.Body, &payload); err != nil {
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
