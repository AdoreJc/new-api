package controller

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := common.Marshal(claims)
	require.NoError(t, err)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func startCodexOAuth(t *testing.T, userID int) (sessionID string, state string, query url.Values) {
	t.Helper()
	envelope := postJSON(t, StartCodexOAuth, "/api/channel/codex/oauth/start", "{}", userID, nil)
	require.True(t, envelope.Success, envelope.Message)
	parsed, err := url.Parse(envelope.Data["auth_url"].(string))
	require.NoError(t, err)
	assert.Equal(t, "auth.openai.com", parsed.Host)
	assert.Equal(t, "/oauth/authorize", parsed.Path)
	return envelope.Data["session_id"].(string), parsed.Query().Get("state"), parsed.Query()
}

func TestStartCodexOAuthBuildsAuthorizationURL(t *testing.T) {
	_, state, query := startCodexOAuth(t, 1)
	assert.Equal(t, "code", query.Get("response_type"))
	assert.Equal(t, "app_EMoamEEZ73f0CkXaXp7hrann", query.Get("client_id"))
	assert.Equal(t, "http://localhost:1455/auth/callback", query.Get("redirect_uri"))
	assert.Equal(t, "openid profile email offline_access", query.Get("scope"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.Equal(t, "true", query.Get("id_token_add_organizations"))
	assert.Equal(t, "true", query.Get("codex_cli_simplified_flow"))
	assert.Len(t, state, 64)
	assert.Len(t, query.Get("code_challenge"), 43)
}

func TestParseCodexAuthorizationInput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCode  string
		wantState string
	}{
		{name: "callback url", input: "http://localhost:1455/auth/callback?code=abc&state=xyz", wantCode: "abc", wantState: "xyz"},
		{name: "query only", input: "code=abc&state=xyz", wantCode: "abc", wantState: "xyz"},
		{name: "code hash state", input: " abc#xyz ", wantCode: "abc", wantState: "xyz"},
		{name: "bare code has no state", input: "abc", wantCode: "abc", wantState: ""},
		{name: "empty", input: "   ", wantCode: "", wantState: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, state := service.ParseCodexAuthorizationInput(test.input)
			assert.Equal(t, test.wantCode, code)
			assert.Equal(t, test.wantState, state)
		})
	}
}

func TestExchangeCodexOAuthVerifiesStateBeforeExchange(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	original := service.CodexOAuthTokenURL
	service.CodexOAuthTokenURL = server.URL + "/oauth/token"
	t.Cleanup(func() { service.CodexOAuthTokenURL = original })

	sessionID, _, _ := startCodexOAuth(t, 1)
	bareCode := postJSON(t, ExchangeCodexOAuth, "/api/channel/codex/oauth/exchange", `{"session_id":"`+sessionID+`","code":"abc"}`, 1, nil)
	assert.False(t, bareCode.Success)
	assert.Contains(t, bareCode.Message, "callback URL")

	mismatch := postJSON(t, ExchangeCodexOAuth, "/api/channel/codex/oauth/exchange", `{"session_id":"`+sessionID+`","code":"http://localhost:1455/auth/callback?code=abc&state=forged"}`, 1, nil)
	assert.False(t, mismatch.Success)
	assert.Contains(t, mismatch.Message, "does not match")
	assert.Equal(t, int32(0), upstreamCalls.Load())
}

func TestExchangeCodexOAuthBuildsChannelKey(t *testing.T) {
	idToken := fakeJWT(t, map[string]any{
		"email":                       "codex@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_123"},
	})
	var form url.Values
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/oauth/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		form = r.PostForm
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-1","id_token":"` + idToken + `","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-1","scope":"openid profile email offline_access"}`))
	}))
	defer server.Close()
	original := service.CodexOAuthTokenURL
	service.CodexOAuthTokenURL = server.URL + "/oauth/token"
	t.Cleanup(func() { service.CodexOAuthTokenURL = original })

	sessionID, state, query := startCodexOAuth(t, 3)
	envelope := postJSON(t, ExchangeCodexOAuth, "/api/channel/codex/oauth/exchange", `{"session_id":"`+sessionID+`","code":"http://localhost:1455/auth/callback?code=auth-code&state=`+state+`"}`, 3, nil)
	require.True(t, envelope.Success, envelope.Message)

	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "auth-code", form.Get("code"))
	assert.Equal(t, "app_EMoamEEZ73f0CkXaXp7hrann", form.Get("client_id"))
	assert.Equal(t, "http://localhost:1455/auth/callback", form.Get("redirect_uri"))
	assert.Len(t, form.Get("code_verifier"), 128)
	assert.NotEqual(t, query.Get("code_challenge"), form.Get("code_verifier"))
	assert.Equal(t, "application/x-www-form-urlencoded", headers.Get("Content-Type"))
	assert.True(t, strings.HasPrefix(headers.Get("User-Agent"), "codex_cli_rs/"), headers.Get("User-Agent"))
	assert.Equal(t, "codex_cli_rs", headers.Get("originator"))

	key, err := codex.ParseOAuthKey(envelope.Data["key"].(string))
	require.NoError(t, err)
	assert.Equal(t, "at-1", key.AccessToken)
	assert.Equal(t, "rt-1", key.RefreshToken)
	assert.Equal(t, idToken, key.IDToken)
	assert.Equal(t, "acct_123", key.AccountID)
	assert.Equal(t, "codex@example.com", key.Email)
	assert.Equal(t, "codex", key.Type)
	assert.NotEmpty(t, key.Expired)
	assert.Equal(t, "acct_123", envelope.Data["account_id"])

	replay := postJSON(t, ExchangeCodexOAuth, "/api/channel/codex/oauth/exchange", `{"session_id":"`+sessionID+`","code":"http://localhost:1455/auth/callback?code=auth-code&state=`+state+`"}`, 3, nil)
	assert.False(t, replay.Success, "a completed session must not be reusable")
}
