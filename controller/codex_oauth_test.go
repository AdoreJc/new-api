package controller

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
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

// postForBody runs handler and returns the whole response document and its
// text, for failures whose fields sit beside "data".
func postForBody(t *testing.T, handler gin.HandlerFunc, path string, body string, params gin.Params) (map[string]any, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", 1)
	handler(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var document map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &document))
	return document, recorder.Body.String()
}

func TestExchangeCodexOAuthReportsTheOAuthErrorCodeWithoutTheAuthorizationCode(t *testing.T) {
	const authCode = "auth-code-secret-7f3a"
	var answer func(w http.ResponseWriter, r *http.Request)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { answer(w, r) }))
	defer server.Close()
	original := service.CodexOAuthTokenURL
	service.CodexOAuthTokenURL = server.URL + "/oauth/token"
	t.Cleanup(func() { service.CodexOAuthTokenURL = original })

	exchange := func(t *testing.T) (map[string]any, string) {
		sessionID, state, _ := startCodexOAuth(t, 1)
		return postForBody(t, ExchangeCodexOAuth, "/api/channel/codex/oauth/exchange",
			`{"session_id":"`+sessionID+`","code":"http://localhost:1455/auth/callback?code=`+authCode+`&state=`+state+`"}`, nil)
	}

	t.Run("an OAuth error answer names its code", func(t *testing.T) {
		// The description repeats the code and the verifier, as a careless
		// upstream might; neither may come back.
		var verifier string
		answer = func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, r.ParseForm())
			verifier = r.PostForm.Get("code_verifier")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"code ` + r.PostForm.Get("code") + ` verifier ` + verifier + ` is not valid"}`))
		}
		document, text := exchange(t)
		assert.Equal(t, false, document["success"])
		assert.Equal(t, "invalid_grant", document["oauth_error_code"])
		assert.Equal(t, "Authorization code exchange failed: OpenAI returned the error {{code}}. Generate a new authorization link and try again", document["message_key"])
		assert.Contains(t, document["message"], "invalid_grant")
		require.NotEmpty(t, verifier)
		assert.NotContains(t, text, authCode)
		assert.NotContains(t, text, verifier)

		_, err := service.ExchangeCodexAuthorizationCodeWithProxy(t.Context(), authCode, "verifier-secret-91c2", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status=400 code=invalid_grant")
		assert.NotContains(t, err.Error(), authCode)
		assert.NotContains(t, err.Error(), "verifier-secret-91c2")
	})

	t.Run("a nested error object names its code", func(t *testing.T) {
		answer = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"token_expired","message":"The authorization code has expired"}}`))
		}
		document, _ := exchange(t)
		assert.Equal(t, "token_expired", document["oauth_error_code"])
	})

	t.Run("an answer without an OAuth error keeps the generic message", func(t *testing.T) {
		answer = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>bad gateway ` + authCode + `</html>`))
		}
		document, text := exchange(t)
		assert.Equal(t, "Authorization code exchange failed. Generate a new authorization link and try again", document["message_key"])
		assert.NotContains(t, document, "oauth_error_code")
		assert.NotContains(t, text, authCode)
	})
}

func TestRefreshCodexChannelCredentialReportsTheOAuthErrorCodeWithoutTheToken(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var status int
	var body string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if status == 0 {
			// Drop the connection: a network error, not an OAuth answer.
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	original := service.CodexOAuthTokenURL
	service.CodexOAuthTokenURL = server.URL + "/oauth/token"
	t.Cleanup(func() { service.CodexOAuthTokenURL = original })

	// Automatic disabling is off so the refresh only reports.
	autoBan := 0
	seed := func(t *testing.T, refreshToken string) gin.Params {
		channel := &model.Channel{
			Type:    constant.ChannelTypeCodex,
			Key:     `{"access_token":"old-access-token","refresh_token":"` + refreshToken + `","account_id":"acct-test"}`,
			Name:    "codex-refresh-test",
			Status:  common.ChannelStatusEnabled,
			AutoBan: &autoBan,
		}
		require.NoError(t, model.DB.Create(channel).Error)
		return gin.Params{{Key: "id", Value: strconv.Itoa(channel.Id)}}
	}
	refresh := func(t *testing.T, params gin.Params) (map[string]any, string) {
		return postForBody(t, RefreshCodexChannelCredential, "/api/channel/"+params[0].Value+"/codex/refresh", "{}", params)
	}

	t.Run("a rejected refresh token asks for a new authorization, also on the next attempt", func(t *testing.T) {
		const refreshToken = "refresh-secret-4b1d"
		params := seed(t, refreshToken)
		status, body = http.StatusUnauthorized, `{"error":{"code":"refresh_token_invalidated","message":"Your refresh token has been invalidated"}}`
		calls.Store(0)

		for attempt := range 2 {
			document, text := refresh(t, params)
			assert.Equal(t, false, document["success"], "attempt %d", attempt)
			assert.Equal(t, "refresh_token_invalidated", document["oauth_error_code"], "attempt %d", attempt)
			assert.Equal(t, true, document["reauthorization_required"], "attempt %d", attempt)
			assert.Equal(t, "OpenAI rejected the refresh token ({{code}}). Authorize the account again", document["message_key"], "attempt %d", attempt)
			assert.Equal(t, map[string]any{"code": "refresh_token_invalidated"}, document["message_params"], "attempt %d", attempt)
			assert.NotContains(t, text, refreshToken)
			assert.NotContains(t, text, "old-access-token")
			assert.NotContains(t, text, "has been invalidated", "the upstream description stays in the server log")
		}
		assert.Equal(t, int32(1), calls.Load(), "the rejected token is not sent twice")
	})

	t.Run("another OAuth error names its code without asking for a new authorization", func(t *testing.T) {
		const refreshToken = "refresh-secret-8e02"
		params := seed(t, refreshToken)
		status, body = http.StatusBadRequest, `{"error":"invalid_client","error_description":"client `+refreshToken+` unknown"}`

		document, text := refresh(t, params)
		assert.Equal(t, "invalid_client", document["oauth_error_code"])
		assert.Equal(t, false, document["reauthorization_required"])
		assert.Equal(t, "Failed to refresh credentials: OpenAI returned the error {{code}}", document["message_key"])
		assert.NotContains(t, text, refreshToken)
	})

	t.Run("a failure without an OAuth error keeps the generic message", func(t *testing.T) {
		params := seed(t, "refresh-secret-c7aa")
		status = 0

		document, _ := refresh(t, params)
		assert.Equal(t, false, document["success"])
		assert.Equal(t, "Failed to refresh credentials. Please try again later", document["message_key"])
		assert.NotContains(t, document, "oauth_error_code")
	})
}
