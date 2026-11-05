package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Claude subscription OAuth (the Claude Code CLI client) constants. The
// authorization code flow runs in the admin's browser; the gateway only holds
// the PKCE verifier and state, exchanges the pasted code, and stores the
// resulting token set as the channel key.
const (
	ClaudeOAuthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	ClaudeOAuthAuthorizeURL = "https://claude.com/cai/oauth/authorize"
	ClaudeOAuthRedirectURI  = "https://platform.claude.com/oauth/code/callback"
	// ClaudeOAuthScope is the browser flow scope. user:profile is required by
	// the usage and reset-credit endpoints.
	ClaudeOAuthScope = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

	// ClaudeOAuthBeta must accompany every request authenticated with a
	// subscription OAuth token.
	ClaudeOAuthBeta = "oauth-2025-04-20"
	// ClaudeCodeSystemPrompt is the identity prefix Anthropic expects from a
	// Claude Code OAuth client; the relay adaptor prepends it to the system
	// prompt when it is missing.
	ClaudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

	claudeOAuthExchangeUserAgent = "axios/1.13.6"
	oauthSessionMaxCount         = 1000
	// claudeOAuthRefreshLeeway is how long before expiry the background task
	// renews a token.
	claudeOAuthRefreshLeeway = 30 * time.Minute
	// claudeOAuthRequestLeeway is how long before expiry a relay request
	// renews the token inline, before sending the request upstream.
	claudeOAuthRequestLeeway = 3 * time.Minute

	oauthProviderClaude = "claude"
	oauthProviderCodex  = "codex"
)

// ClaudeOAuthTokenURL is a variable so tests can point the exchange and
// refresh requests at an httptest server.
var ClaudeOAuthTokenURL = "https://platform.claude.com/v1/oauth/token"

// OAuthAuthorizationSessionTTL bounds how long a generated authorization link
// can be completed. The session is single use.
var OAuthAuthorizationSessionTTL = 30 * time.Minute

// ClaudeOAuthKey is the JSON stored in channel.key for a Claude Subscription
// (OAuth) channel. It never leaves the server except through the admin
// channel key reveal endpoint.
type ClaudeOAuthKey struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Scope        string `json:"scope,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	OrgUUID      string `json:"org_uuid,omitempty"`
	AccountUUID  string `json:"account_uuid,omitempty"`
	Email        string `json:"email,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
}

func ParseClaudeOAuthKey(raw string) (*ClaudeOAuthKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("claude oauth channel: empty oauth key")
	}
	if !strings.HasPrefix(raw, "{") {
		return nil, errors.New("claude oauth channel: key must be a JSON object")
	}
	var key ClaudeOAuthKey
	if err := common.Unmarshal([]byte(raw), &key); err != nil {
		return nil, errors.New("claude oauth channel: invalid oauth key json")
	}
	if strings.TrimSpace(key.AccessToken) == "" {
		return nil, errors.New("claude oauth channel: access_token is required")
	}
	return &key, nil
}

// ExpiresAtTime returns the parsed expiry, or the zero time when unknown.
func (k *ClaudeOAuthKey) ExpiresAtTime() time.Time {
	if k == nil {
		return time.Time{}
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(k.ExpiresAt))
	if err != nil {
		return time.Time{}
	}
	return expiresAt
}

// HasScope reports whether the stored scope list contains scope. An empty
// stored scope is treated as unknown and passes, so keys pasted by hand
// without a scope field are not rejected.
func (k *ClaudeOAuthKey) HasScope(scope string) bool {
	if k == nil {
		return false
	}
	stored := strings.TrimSpace(k.Scope)
	if stored == "" {
		return true
	}
	for item := range strings.FieldsSeq(stored) {
		if item == scope {
			return true
		}
	}
	return false
}

// ClaudeCodeClientVersion is the Claude Code CLI version announced in
// User-Agent headers. CLAUDE_CODE_CLIENT_VERSION overrides the built-in value.
func ClaudeCodeClientVersion() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CODE_CLIENT_VERSION")); v != "" {
		return v
	}
	return "2.1.258"
}

func ClaudeCLIUserAgent() string {
	return "claude-cli/" + ClaudeCodeClientVersion() + " (external, cli)"
}

// OAuthAuthorizationSession is the server-side half of a browser
// authorization: PKCE verifier and state never reach the browser, and the
// session is bound to the admin who started it.
type OAuthAuthorizationSession struct {
	Provider  string
	State     string
	Verifier  string
	UserID    int
	CreatedAt time.Time
}

var oauthSessions = struct {
	sync.Mutex
	items map[string]*OAuthAuthorizationSession
}{items: make(map[string]*OAuthAuthorizationSession)}

func storeOAuthAuthorizationSession(session *OAuthAuthorizationSession) (string, error) {
	idBytes, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	sessionID := hex.EncodeToString(idBytes)

	oauthSessions.Lock()
	defer oauthSessions.Unlock()
	now := time.Now()
	for id, item := range oauthSessions.items {
		if now.Sub(item.CreatedAt) > OAuthAuthorizationSessionTTL {
			delete(oauthSessions.items, id)
		}
	}
	if len(oauthSessions.items) >= oauthSessionMaxCount {
		return "", errors.New("too many pending oauth authorizations")
	}
	oauthSessions.items[sessionID] = session
	return sessionID, nil
}

// takeOAuthAuthorizationSession removes and returns the pending session. It
// returns nil when the id is unknown, expired, for another provider, or was
// started by another user, so every outcome consumes the single-use session.
func takeOAuthAuthorizationSession(sessionID string, provider string, userID int) *OAuthAuthorizationSession {
	oauthSessions.Lock()
	defer oauthSessions.Unlock()
	session, ok := oauthSessions.items[sessionID]
	if !ok {
		return nil
	}
	delete(oauthSessions.items, sessionID)
	if session.Provider != provider || session.UserID != userID {
		return nil
	}
	if time.Since(session.CreatedAt) > OAuthAuthorizationSessionTTL {
		return nil
	}
	return session
}

// OAuthStateMatches compares the state echoed by the provider with the stored
// one in constant time.
func OAuthStateMatches(expected string, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func base64URLNoPad(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64URLNoPad(sum[:])
}

type ClaudeOAuthAuthorizationFlow struct {
	SessionID string
	AuthURL   string
	ExpiresIn int
}

// CreateClaudeOAuthAuthorizationFlow generates state and PKCE material, stores
// them server side for the admin user, and returns the authorization URL.
func CreateClaudeOAuthAuthorizationFlow(userID int) (*ClaudeOAuthAuthorizationFlow, error) {
	stateBytes, err := randomBytes(32)
	if err != nil {
		return nil, err
	}
	verifierBytes, err := randomBytes(32)
	if err != nil {
		return nil, err
	}
	state := base64URLNoPad(stateBytes)
	verifier := base64URLNoPad(verifierBytes)

	sessionID, err := storeOAuthAuthorizationSession(&OAuthAuthorizationSession{
		Provider:  oauthProviderClaude,
		State:     state,
		Verifier:  verifier,
		UserID:    userID,
		CreatedAt: time.Now(),
	})
	if err != nil {
		return nil, err
	}
	return &ClaudeOAuthAuthorizationFlow{
		SessionID: sessionID,
		AuthURL:   BuildClaudeOAuthAuthorizeURL(state, pkceChallengeS256(verifier), ClaudeOAuthScope),
		ExpiresIn: int(OAuthAuthorizationSessionTTL.Seconds()),
	}, nil
}

// BuildClaudeOAuthAuthorizeURL keeps the parameter order the Claude Code CLI
// uses; scope spaces are encoded as '+'.
func BuildClaudeOAuthAuthorizeURL(state string, codeChallenge string, scope string) string {
	encodedScope := strings.ReplaceAll(url.QueryEscape(scope), "%20", "+")
	return fmt.Sprintf("%s?code=true&client_id=%s&response_type=code&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
		ClaudeOAuthAuthorizeURL,
		ClaudeOAuthClientID,
		url.QueryEscape(ClaudeOAuthRedirectURI),
		encodedScope,
		codeChallenge,
		state,
	)
}

// SplitClaudeAuthorizationInput splits the "code#state" string shown on the
// Claude callback page.
func SplitClaudeAuthorizationInput(input string) (code string, state string) {
	code, state, _ = strings.Cut(strings.TrimSpace(input), "#")
	return strings.TrimSpace(code), strings.TrimSpace(state)
}

type ClaudeOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Organization *struct {
		UUID string `json:"uuid"`
	} `json:"organization"`
	Account *struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
}

// ClaudeOAuthTokenError is a failed token endpoint call. Permanent reports an
// OAuth error that a retry with the same refresh token cannot fix.
type ClaudeOAuthTokenError struct {
	StatusCode int
	Code       string
	Permanent  bool
}

func (e *ClaudeOAuthTokenError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("claude oauth token request failed: status=%d error=%s", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("claude oauth token request failed: status=%d", e.StatusCode)
}

var claudeOAuthPermanentErrors = map[string]bool{
	"invalid_grant":         true,
	"invalid_refresh_token": true,
	"token_expired":         true,
	"refresh_token_reused":  true,
	"invalid_client":        true,
	"unauthorized_client":   true,
}

// IsPermanentClaudeOAuthError reports whether err is a token error the caller
// should not retry with the same credential.
func IsPermanentClaudeOAuthError(err error) bool {
	var tokenErr *ClaudeOAuthTokenError
	return errors.As(err, &tokenErr) && tokenErr.Permanent
}

// ExchangeClaudeAuthorizationCode trades the pasted code for a token set. The
// state is sent along as the Claude Code CLI does.
func ExchangeClaudeAuthorizationCode(ctx context.Context, client *http.Client, code string, verifier string, state string) (*ClaudeOAuthTokenResponse, error) {
	return postClaudeOAuthToken(ctx, client, map[string]string{
		"code":          code,
		"grant_type":    "authorization_code",
		"client_id":     ClaudeOAuthClientID,
		"redirect_uri":  ClaudeOAuthRedirectURI,
		"code_verifier": verifier,
		"state":         state,
	})
}

func RefreshClaudeOAuthToken(ctx context.Context, client *http.Client, refreshToken string) (*ClaudeOAuthTokenResponse, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, errors.New("empty refresh_token")
	}
	return postClaudeOAuthToken(ctx, client, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": rt,
		"client_id":     ClaudeOAuthClientID,
	})
}

func postClaudeOAuthToken(ctx context.Context, client *http.Client, form map[string]string) (*ClaudeOAuthTokenResponse, error) {
	if client == nil {
		return nil, errors.New("nil http client")
	}
	body, err := common.Marshal(form)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ClaudeOAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", claudeOAuthExchangeUserAgent)

	resp, err := noRedirectClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseClaudeOAuthTokenError(resp.StatusCode, raw)
	}
	var payload ClaudeOAuthTokenResponse
	if err := common.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("claude oauth token response is not valid json")
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("claude oauth token response missing fields")
	}
	return &payload, nil
}

// parseClaudeOAuthTokenError reads only the error code from the body so no
// token material can end up in logs or messages.
func parseClaudeOAuthTokenError(statusCode int, raw []byte) error {
	code := ""
	var envelope struct {
		Error any `json:"error"`
	}
	if common.Unmarshal(raw, &envelope) == nil {
		switch v := envelope.Error.(type) {
		case string:
			code = v
		case map[string]any:
			if s, ok := v["type"].(string); ok {
				code = s
			} else if s, ok := v["code"].(string); ok {
				code = s
			}
		}
	}
	code = strings.TrimSpace(code)
	// Only an explicit OAuth error code is permanent; a 400 or 401 without a
	// parseable body (a proxy or WAF page) is treated as transient.
	return &ClaudeOAuthTokenError{StatusCode: statusCode, Code: code, Permanent: claudeOAuthPermanentErrors[code]}
}

// noRedirectClient returns a copy of client that never follows redirects, so a
// bearer token is never replayed to another host.
func noRedirectClient(client *http.Client) *http.Client {
	isolated := *client
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if isolated.Timeout == 0 {
		isolated.Timeout = defaultHTTPTimeout
	}
	return &isolated
}

// BuildClaudeOAuthKey converts a token response into the stored key.
func BuildClaudeOAuthKey(res *ClaudeOAuthTokenResponse, now time.Time) *ClaudeOAuthKey {
	key := &ClaudeOAuthKey{
		AccessToken:  strings.TrimSpace(res.AccessToken),
		RefreshToken: strings.TrimSpace(res.RefreshToken),
		ExpiresAt:    now.Add(time.Duration(res.ExpiresIn) * time.Second).Format(time.RFC3339),
		Scope:        strings.TrimSpace(res.Scope),
		TokenType:    strings.TrimSpace(res.TokenType),
		LastRefresh:  now.Format(time.RFC3339),
	}
	if res.Organization != nil {
		key.OrgUUID = strings.TrimSpace(res.Organization.UUID)
	}
	if res.Account != nil {
		key.AccountUUID = strings.TrimSpace(res.Account.UUID)
		key.Email = strings.TrimSpace(res.Account.EmailAddress)
	}
	return key
}

// GetClaudeOAuthHTTPClient returns a proxy-aware client with the OAuth timeout.
func GetClaudeOAuthHTTPClient(proxyURL string) (*http.Client, error) {
	return getCodexOAuthHTTPClient(proxyURL)
}

// TakeClaudeOAuthAuthorizationSession consumes the pending Claude session
// started by userID; it returns nil when none is usable.
func TakeClaudeOAuthAuthorizationSession(sessionID string, userID int) *OAuthAuthorizationSession {
	return takeOAuthAuthorizationSession(sessionID, oauthProviderClaude, userID)
}
