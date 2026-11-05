package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/claude_oauth"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type apiEnvelope struct {
	Success        bool           `json:"success"`
	Message        string         `json:"message"`
	UpstreamStatus int            `json:"upstream_status"`
	Data           map[string]any `json:"data"`
}

func setupClaudeOAuthChannelDB(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}))
	model.DB = database
	t.Cleanup(func() { model.DB = originalDB })
}

func newClaudeOAuthTestChannel(t *testing.T, baseURL string, key service.ClaudeOAuthKey) *model.Channel {
	t.Helper()
	encoded, err := common.Marshal(key)
	require.NoError(t, err)
	channel := &model.Channel{
		Type:    constant.ChannelTypeClaudeOAuth,
		BaseURL: &baseURL,
		Key:     string(encoded),
		Name:    "claude-oauth-test",
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

func storedClaudeOAuthKey(t *testing.T, channelID int) *service.ClaudeOAuthKey {
	t.Helper()
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, "id = ?", channelID).Error)
	key, err := service.ParseClaudeOAuthKey(channel.Key)
	require.NoError(t, err)
	return key
}

func postJSON(t *testing.T, handler gin.HandlerFunc, path string, body string, userID int, params gin.Params) apiEnvelope {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", userID)
	handler(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope apiEnvelope
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope
}

func getJSON(t *testing.T, handler gin.HandlerFunc, path string, params gin.Params) apiEnvelope {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	c.Params = params
	c.Set("id", 1)
	handler(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope apiEnvelope
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope
}

func channelParams(id int) gin.Params {
	return gin.Params{{Key: "id", Value: strconv.Itoa(id)}}
}

func useClaudeOAuthTokenServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	original := service.ClaudeOAuthTokenURL
	service.ClaudeOAuthTokenURL = server.URL + "/v1/oauth/token"
	t.Cleanup(func() { service.ClaudeOAuthTokenURL = original })
}

func TestStartClaudeOAuthBuildsAuthorizationURL(t *testing.T) {
	envelope := postJSON(t, StartClaudeOAuth, "/api/channel/claude/oauth/start", "{}", 1, nil)
	require.True(t, envelope.Success, envelope.Message)
	assert.NotEmpty(t, envelope.Data["session_id"])

	authURL, ok := envelope.Data["auth_url"].(string)
	require.True(t, ok)
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	assert.Equal(t, "claude.com", parsed.Host)
	assert.Equal(t, "/cai/oauth/authorize", parsed.Path)
	assert.True(t, strings.HasPrefix(parsed.RawQuery, "code=true&client_id="+service.ClaudeOAuthClientID+"&response_type=code&redirect_uri="), parsed.RawQuery)
	assert.Contains(t, parsed.RawQuery, "&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference+")
	query := parsed.Query()
	assert.Equal(t, service.ClaudeOAuthRedirectURI, query.Get("redirect_uri"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.Len(t, query.Get("state"), 43)
	assert.Len(t, query.Get("code_challenge"), 43)
}

func TestExchangeClaudeOAuthRejectsBadSessionsBeforeContactingUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	start := func(userID int) (sessionID string, state string) {
		envelope := postJSON(t, StartClaudeOAuth, "/api/channel/claude/oauth/start", "{}", userID, nil)
		require.True(t, envelope.Success)
		parsed, err := url.Parse(envelope.Data["auth_url"].(string))
		require.NoError(t, err)
		return envelope.Data["session_id"].(string), parsed.Query().Get("state")
	}

	t.Run("missing state", func(t *testing.T) {
		sessionID, _ := start(1)
		envelope := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"code-only"}`, 1, nil)
		assert.False(t, envelope.Success)
		assert.Contains(t, envelope.Message, "code#state")
	})

	t.Run("state mismatch consumes the session", func(t *testing.T) {
		sessionID, state := start(1)
		envelope := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"abc#wrong-state"}`, 1, nil)
		assert.False(t, envelope.Success)
		assert.Contains(t, envelope.Message, "does not match")

		replay := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"abc#`+state+`"}`, 1, nil)
		assert.False(t, replay.Success)
		assert.Contains(t, replay.Message, "not found or has expired")
	})

	t.Run("another admin cannot complete the session", func(t *testing.T) {
		sessionID, state := start(1)
		envelope := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"abc#`+state+`"}`, 2, nil)
		assert.False(t, envelope.Success)
		assert.Contains(t, envelope.Message, "not found or has expired")
	})

	t.Run("expired session", func(t *testing.T) {
		sessionID, state := start(1)
		originalTTL := service.OAuthAuthorizationSessionTTL
		service.OAuthAuthorizationSessionTTL = -time.Second
		t.Cleanup(func() { service.OAuthAuthorizationSessionTTL = originalTTL })
		envelope := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"abc#`+state+`"}`, 1, nil)
		assert.False(t, envelope.Success)
		assert.Contains(t, envelope.Message, "not found or has expired")
	})

	t.Run("unknown session", func(t *testing.T) {
		envelope := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"nope","code":"abc#state"}`, 1, nil)
		assert.False(t, envelope.Success)
	})

	assert.Equal(t, int32(0), upstreamCalls.Load(), "no token request may be sent for a rejected session")
}

func TestExchangeClaudeOAuthReturnsChannelKey(t *testing.T) {
	var captured struct {
		body    map[string]string
		headers http.Header
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/oauth/token", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		assert.NoError(t, common.Unmarshal(raw, &captured.body))
		captured.headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-1","token_type":"Bearer","expires_in":28800,"refresh_token":"rt-1","scope":"user:profile user:inference","organization":{"uuid":"org-1"},"account":{"uuid":"acc-1","email_address":"dev@example.com"}}`))
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	startEnvelope := postJSON(t, StartClaudeOAuth, "/api/channel/claude/oauth/start", "{}", 7, nil)
	require.True(t, startEnvelope.Success)
	sessionID := startEnvelope.Data["session_id"].(string)
	parsed, err := url.Parse(startEnvelope.Data["auth_url"].(string))
	require.NoError(t, err)
	state := parsed.Query().Get("state")
	challenge := parsed.Query().Get("code_challenge")

	envelope := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"auth-code#`+state+`"}`, 7, nil)
	require.True(t, envelope.Success, envelope.Message)

	assert.Equal(t, "authorization_code", captured.body["grant_type"])
	assert.Equal(t, "auth-code", captured.body["code"])
	assert.Equal(t, state, captured.body["state"])
	assert.Equal(t, service.ClaudeOAuthClientID, captured.body["client_id"])
	assert.Equal(t, service.ClaudeOAuthRedirectURI, captured.body["redirect_uri"])
	assert.NotEmpty(t, captured.body["code_verifier"])
	assert.NotEqual(t, challenge, captured.body["code_verifier"], "verifier must not be the challenge")
	assert.Equal(t, "application/json", captured.headers.Get("Content-Type"))
	assert.Equal(t, "application/json, text/plain, */*", captured.headers.Get("Accept"))
	assert.Equal(t, "axios/1.13.6", captured.headers.Get("User-Agent"))

	key, err := service.ParseClaudeOAuthKey(envelope.Data["key"].(string))
	require.NoError(t, err)
	assert.Equal(t, "at-1", key.AccessToken)
	assert.Equal(t, "rt-1", key.RefreshToken)
	assert.Equal(t, "org-1", key.OrgUUID)
	assert.Equal(t, "acc-1", key.AccountUUID)
	assert.Equal(t, "dev@example.com", key.Email)
	assert.Equal(t, "user:profile user:inference", key.Scope)
	assert.WithinDuration(t, time.Now().Add(8*time.Hour), key.ExpiresAtTime(), time.Minute)
	assert.Equal(t, "dev@example.com", envelope.Data["email"])

	replay := postJSON(t, ExchangeClaudeOAuth, "/api/channel/claude/oauth/exchange", `{"session_id":"`+sessionID+`","code":"auth-code#`+state+`"}`, 7, nil)
	assert.False(t, replay.Success, "a completed session must not be reusable")
}

func TestRefreshClaudeChannelCredentialWritesKeyBack(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	var refreshBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		assert.NoError(t, common.Unmarshal(raw, &refreshBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-2","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`))
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour).Format(time.RFC3339),
		OrgUUID:      "org-1",
		Email:        "dev@example.com",
	})

	envelope := postJSON(t, RefreshClaudeChannelCredential, "/api/channel/1/claude/refresh", "", 1, channelParams(channel.Id))
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, "refresh_token", refreshBody["grant_type"])
	assert.Equal(t, "rt-1", refreshBody["refresh_token"])
	assert.Equal(t, service.ClaudeOAuthClientID, refreshBody["client_id"])

	stored := storedClaudeOAuthKey(t, channel.Id)
	assert.Equal(t, "at-2", stored.AccessToken)
	assert.Equal(t, "rt-1", stored.RefreshToken, "a refresh response without refresh_token keeps the old one")
	assert.Equal(t, "org-1", stored.OrgUUID)
	assert.Equal(t, "dev@example.com", stored.Email)
	assert.WithinDuration(t, time.Now().Add(time.Hour), stored.ExpiresAtTime(), time.Minute)
	assert.NotEmpty(t, stored.LastRefresh)
}

func TestRefreshClaudeChannelCredentialReportsRejectedRefreshToken(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at-1", RefreshToken: "rt-1"})
	envelope := postJSON(t, RefreshClaudeChannelCredential, "/api/channel/1/claude/refresh", "", 1, channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Contains(t, envelope.Message, "Re-authorize")
	assert.Equal(t, "at-1", storedClaudeOAuthKey(t, channel.Id).AccessToken)
}

func TestEnsureClaudeChannelAccessTokenRefreshesOnlyNearExpiry(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-fresh","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-2"}`))
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	valid := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{
		AccessToken: "at-valid", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	validKey, err := service.ParseClaudeOAuthKey(valid.Key)
	require.NoError(t, err)
	key, err := service.EnsureClaudeChannelAccessToken(context.Background(), valid.Id, validKey)
	require.NoError(t, err)
	assert.Equal(t, "at-valid", key.AccessToken)
	assert.Equal(t, int32(0), refreshCalls.Load())

	expiring := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{
		AccessToken: "at-old", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	expiringKey, err := service.ParseClaudeOAuthKey(expiring.Key)
	require.NoError(t, err)
	key, err = service.EnsureClaudeChannelAccessToken(context.Background(), expiring.Id, expiringKey)
	require.NoError(t, err)
	assert.Equal(t, "at-fresh", key.AccessToken)
	assert.Equal(t, int32(1), refreshCalls.Load())
	assert.Equal(t, "rt-2", storedClaudeOAuthKey(t, expiring.Id).RefreshToken)

	// The stored key is fresh now, so the stale in-memory copy does not
	// trigger a second refresh.
	key, err = service.EnsureClaudeChannelAccessToken(context.Background(), expiring.Id, expiringKey)
	require.NoError(t, err)
	assert.Equal(t, "at-fresh", key.AccessToken)
	assert.Equal(t, int32(1), refreshCalls.Load())
}

func TestGetClaudeChannelUsagePassesThroughAndRetriesAfterRefresh(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	var usageTokens []string
	var usageHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-new"}`))
		case "/api/oauth/usage":
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			usageTokens = append(usageTokens, token)
			usageHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			if token != "at-new" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"type":"authentication_error"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"five_hour":{"utilization":42.5,"resets_at":"2026-11-05T03:00:00Z"},"seven_day":{"utilization":10,"resets_at":"2026-11-10T00:00:00Z"},"seven_day_sonnet":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{
		AccessToken: "at-stale", RefreshToken: "rt-1", Scope: "user:profile user:inference",
	})
	envelope := getJSON(t, GetClaudeChannelUsage, "/api/channel/1/claude/usage", channelParams(channel.Id))
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, http.StatusOK, envelope.UpstreamStatus)
	assert.Equal(t, []string{"at-stale", "at-new"}, usageTokens)
	assert.Equal(t, "oauth-2025-04-20", usageHeaders.Get("anthropic-beta"))
	assert.True(t, strings.HasPrefix(usageHeaders.Get("User-Agent"), "claude-code/"), usageHeaders.Get("User-Agent"))

	fiveHour, ok := envelope.Data["five_hour"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 42.5, fiveHour["utilization"])
	assert.Contains(t, envelope.Data, "seven_day_sonnet")
	assert.Equal(t, "at-new", storedClaudeOAuthKey(t, channel.Id).AccessToken)
}

func TestGetClaudeChannelUsageRequiresProfileScope(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	channel := newClaudeOAuthTestChannel(t, "http://127.0.0.1:1", service.ClaudeOAuthKey{AccessToken: "at", Scope: "user:inference"})
	envelope := getJSON(t, GetClaudeChannelUsage, "/api/channel/1/claude/usage", channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Contains(t, envelope.Message, "user:profile")
}

func TestGetClaudeChannelResetCreditsProjectsRedeemableGrants(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	grants := `[
		{"id":"grant_next","label":"Weekly reset","resets_left":2,"starts_at":"` + past + `","ends_at":"` + future + `","clears":["five_hour","seven_day"],"paused":false,"usable_now":true,"use_requires_limit":true,"percent_used":{"five_hour":100,"seven_day":180},"blocking":[]},
		{"id":"grant_other","label":"Later grant","resets_left":1,"starts_at":"` + past + `","ends_at":"` + future + `","clears":["five_hour"],"paused":false,"usable_now":true,"use_requires_limit":false,"percent_used":{},"blocking":[]},
		{"id":"grant_paused","label":"Paused","resets_left":1,"clears":["five_hour"],"paused":true,"usable_now":true,"percent_used":{},"blocking":[]},
		{"id":"grant_blocked","label":"Blocked","resets_left":1,"clears":["five_hour"],"paused":false,"usable_now":true,"percent_used":{},"blocking":["cooldown"]}
	]`
	var requestedQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/oauth/usage", r.URL.Path)
		requestedQuery = r.URL.Query()
		assert.Equal(t, "cli", r.Header.Get("x-app"))
		assert.True(t, strings.HasPrefix(r.Header.Get("User-Agent"), "claude-cli/"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":100,"resets_at":"` + future + `"},"cedar_ember":{"eligible":true,"at_limit":true,"next_grant_id":"grant_next","cooldown_until":null,"weekly_resets_at":"` + future + `","grants":` + grants + `}}`))
	}))
	defer server.Close()

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at", Scope: "user:profile"})
	envelope := getJSON(t, GetClaudeChannelResetCredits, "/api/channel/1/claude/usage/reset-credits", channelParams(channel.Id))
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, "1", requestedQuery.Get("cedar_ember"))
	assert.Equal(t, "1", requestedQuery.Get("skip_spend"))

	assert.Equal(t, true, envelope.Data["eligible"])
	assert.Equal(t, float64(2), envelope.Data["available_count"])
	credits, ok := envelope.Data["credits"].([]any)
	require.True(t, ok)
	require.Len(t, credits, 3, "the paused grant is not a held credit")
	byLabel := map[string]map[string]any{}
	for _, item := range credits {
		credit := item.(map[string]any)
		byLabel[credit["label"].(string)] = credit
		assert.NotContains(t, credit, "id", "grant ids never reach the console")
	}
	assert.Equal(t, true, byLabel["Weekly reset"]["redeemable"])
	assert.Equal(t, false, byLabel["Later grant"]["redeemable"], "only the upstream next grant is redeemable")
	assert.Equal(t, false, byLabel["Blocked"]["redeemable"])
	percentUsed := byLabel["Weekly reset"]["percent_used"].(map[string]any)
	assert.Equal(t, float64(100), percentUsed["five_hour"])
	assert.NotContains(t, percentUsed, "seven_day", "out-of-range percentages are dropped")
}

func TestResetClaudeChannelUsageRedeemsNextGrantOnce(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	var resetCalls atomic.Int32
	var resetBody map[string]string
	var resetPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/oauth/usage":
			_, _ = w.Write([]byte(`{"cedar_ember":{"eligible":true,"at_limit":false,"next_grant_id":"grant_next","grants":[{"id":"grant_next","label":"Weekly reset","resets_left":1,"ends_at":"` + future + `","clears":["five_hour"],"usable_now":true,"use_requires_limit":false,"percent_used":{},"blocking":[]}]}}`))
		case r.URL.Path == "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"organization":{"uuid":"2b3f6e9a-1b2c-4d5e-8f90-123456789abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
			resetCalls.Add(1)
			resetPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			assert.NoError(t, common.Unmarshal(raw, &resetBody))
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "cli", r.Header.Get("x-app"))
			assert.Equal(t, "oauth-2025-04-20", r.Header.Get("anthropic-beta"))
			_, _ = w.Write([]byte(`{"result":"reset","reason":"","cleared":["five_hour","bogus_window"],"cooldown_until":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at", Scope: "user:profile"})
	envelope := postJSON(t, ResetClaudeChannelUsage, "/api/channel/1/claude/usage/reset", "", 1, channelParams(channel.Id))
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, int32(1), resetCalls.Load())
	assert.Equal(t, "/api/organizations/2b3f6e9a-1b2c-4d5e-8f90-123456789abc/reset_rate_limits", resetPath)
	assert.Equal(t, "cedar_ember", resetBody["program"])
	assert.Equal(t, "grant_next", resetBody["grant_id"])
	assert.Len(t, resetBody["request_id"], 64)
	assert.Equal(t, "reset", envelope.Data["outcome"])
	assert.Equal(t, []any{"five_hour"}, envelope.Data["cleared"])
}

func TestResetClaudeChannelUsageRefusesWhenNothingRedeemable(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	var resetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/reset_rate_limits") {
			resetCalls.Add(1)
		}
		_, _ = w.Write([]byte(`{"cedar_ember":{"eligible":true,"at_limit":false,"next_grant_id":"grant_next","grants":[{"id":"grant_next","label":"Weekly reset","resets_left":1,"clears":["five_hour"],"usable_now":true,"use_requires_limit":true,"percent_used":{},"blocking":[]}]}}`))
	}))
	defer server.Close()

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at", Scope: "user:profile"})
	envelope := postJSON(t, ResetClaudeChannelUsage, "/api/channel/1/claude/usage/reset", "", 1, channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Contains(t, envelope.Message, "No reset credit")
	assert.Equal(t, int32(0), resetCalls.Load(), "a grant that requires at_limit is not redeemed below the limit")
}

func TestClaudeOAuthAdaptorSendsBearerTokenAndClaudeCodeIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(nil))
	c.Request.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14,oauth-2025-04-20")
	c.Request.Header.Set("User-Agent", "my-client/1.0")

	encoded, err := common.Marshal(service.ClaudeOAuthKey{AccessToken: "at-1", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: string(encoded)}}
	headers := http.Header{}
	headers.Set("x-api-key", "should-not-leak")
	adaptor := &claude_oauth.Adaptor{}
	require.NoError(t, adaptor.SetupRequestHeader(c, &headers, info))

	assert.Equal(t, "Bearer at-1", headers.Get("Authorization"))
	assert.Empty(t, headers.Get("x-api-key"))
	assert.Equal(t, "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14", headers.Get("anthropic-beta"))
	assert.Equal(t, "cli", headers.Get("x-app"))
	assert.True(t, strings.HasPrefix(headers.Get("User-Agent"), "claude-cli/"), headers.Get("User-Agent"))
	assert.Equal(t, "2023-06-01", headers.Get("anthropic-version"))

	assert.Equal(t, "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14", claude_oauth.MergeClaudeOAuthBetas(""))
	assert.Equal(t, "claude-code-20250219,oauth-2025-04-20,context-1m-2025-08-07", claude_oauth.MergeClaudeOAuthBetas("context-1m-2025-08-07, claude-code-20250219"))

	tests := []struct {
		name   string
		system any
		want   []string
	}{
		{name: "missing", system: nil, want: []string{service.ClaudeCodeSystemPrompt}},
		{name: "string prompt is kept after the identity", system: "Be terse.", want: []string{service.ClaudeCodeSystemPrompt, "Be terse."}},
		{name: "existing identity is untouched", system: service.ClaudeCodeSystemPrompt + " Extra.", want: []string{service.ClaudeCodeSystemPrompt + " Extra."}},
		{name: "block list gets the identity first", system: []any{map[string]any{"type": "text", "text": "Rules."}}, want: []string{service.ClaudeCodeSystemPrompt, "Rules."}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &dto.ClaudeRequest{System: test.system}
			claude_oauth.EnsureClaudeCodeSystemPrompt(request)
			var got []string
			if request.IsStringSystem() {
				got = []string{request.GetStringSystem()}
			} else {
				for _, block := range request.ParseSystem() {
					got = append(got, block.GetText())
				}
			}
			assert.Equal(t, test.want, got)
		})
	}
}

func TestEnsureClaudeChannelAccessTokenConcurrentCallersRefreshOnce(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-fresh","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-2"}`))
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{
		AccessToken: "at-old", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	staleKey, err := service.ParseClaudeOAuthKey(channel.Key)
	require.NoError(t, err)

	const callers = 20
	results := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			key, err := service.EnsureClaudeChannelAccessToken(ctx, channel.Id, staleKey)
			if err != nil {
				errs <- err
				return
			}
			results <- key.AccessToken
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for token := range results {
		assert.Equal(t, "at-fresh", token)
	}
	assert.Equal(t, int32(1), refreshCalls.Load(), "queued callers must reuse the renewed key")
	assert.Equal(t, "rt-2", storedClaudeOAuthKey(t, channel.Id).RefreshToken)
}

func TestEnsureClaudeChannelAccessTokenLockWaitHonoursContext(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-fresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{
		AccessToken: "at-old", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	key, err := service.ParseClaudeOAuthKey(channel.Key)
	require.NoError(t, err)

	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = service.EnsureClaudeChannelAccessToken(context.Background(), channel.Id, key)
	}()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := service.EnsureClaudeChannelAccessToken(ctx, channel.Id, key)
	require.NoError(t, err, "a still-valid token is used when the lock wait times out")
	assert.Equal(t, "at-old", got.AccessToken)
	blocked <- struct{}{}
	<-first
}

func newClaudeOAuthTaskFixture(t *testing.T, tokenHandler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	setupClaudeOAuthChannelDB(t)
	require.NoError(t, i18n.Init())
	// DisableChannel notifies the root user; the limiter must use memory here.
	originalRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedis })
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	require.NoError(t, model.DB.Create(&model.User{Username: "root", Password: "unused-password", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		tokenHandler(w, r)
	}))
	t.Cleanup(server.Close)
	useClaudeOAuthTokenServer(t, server)
	return server, &tokenCalls
}

// newClaudeOAuthTaskChannel creates an auto-ban channel. Refresh tokens are
// unique per case because the task remembers rejected tokens by fingerprint.
func newClaudeOAuthTaskChannel(t *testing.T, baseURL string, refreshToken string, expiresIn time.Duration) *model.Channel {
	t.Helper()
	channel := newClaudeOAuthTestChannel(t, baseURL, service.ClaudeOAuthKey{
		AccessToken: "at-1", RefreshToken: refreshToken, ExpiresAt: time.Now().Add(expiresIn).Format(time.RFC3339),
	})
	require.NoError(t, model.DB.Model(channel).Update("auto_ban", 1).Error)
	return channel
}

func channelStatus(t *testing.T, channelID int) int {
	t.Helper()
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, "id = ?", channelID).Error)
	return channel.Status
}

func TestClaudeCredentialAutoRefreshTaskDisablesOnlyRejectedRefreshTokens(t *testing.T) {
	t.Run("permanent rejection disables once and is not resent", func(t *testing.T) {
		server, tokenCalls := newClaudeOAuthTaskFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		})
		channel := newClaudeOAuthTaskChannel(t, server.URL, "rt-dead", 10*time.Minute)

		scanned, refreshed := service.RunClaudeCredentialAutoRefreshOnce()
		assert.Equal(t, 1, scanned)
		assert.Equal(t, 0, refreshed)
		assert.Equal(t, common.ChannelStatusAutoDisabled, channelStatus(t, channel.Id))
		assert.Equal(t, int32(1), tokenCalls.Load())

		service.RunClaudeCredentialAutoRefreshOnce()
		assert.Equal(t, int32(1), tokenCalls.Load(), "a rejected refresh token is not resent")
	})

	t.Run("transient failure keeps the channel enabled", func(t *testing.T) {
		server, tokenCalls := newClaudeOAuthTaskFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html>proxy auth required</html>`))
		})
		channel := newClaudeOAuthTaskChannel(t, server.URL, "rt-transient", 10*time.Minute)

		service.RunClaudeCredentialAutoRefreshOnce()
		assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, channel.Id))
		assert.Equal(t, int32(1), tokenCalls.Load())
		assert.Equal(t, "rt-transient", storedClaudeOAuthKey(t, channel.Id).RefreshToken)
	})

	t.Run("rejection after another node rotated the token keeps the channel enabled", func(t *testing.T) {
		var channelID int
		server, _ := newClaudeOAuthTaskFixture(t, func(w http.ResponseWriter, r *http.Request) {
			// Another node renewed the credential while this request was in flight.
			rotated, err := common.Marshal(service.ClaudeOAuthKey{
				AccessToken: "at-rotated", RefreshToken: "rt-rotated", ExpiresAt: time.Now().Add(8 * time.Hour).Format(time.RFC3339),
			})
			assert.NoError(t, err)
			assert.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("key", string(rotated)).Error)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"refresh_token_reused"}`))
		})
		channel := newClaudeOAuthTaskChannel(t, server.URL, "rt-stale", 10*time.Minute)
		channelID = channel.Id

		service.RunClaudeCredentialAutoRefreshOnce()
		assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, channel.Id))
		assert.Equal(t, "rt-rotated", storedClaudeOAuthKey(t, channel.Id).RefreshToken)
	})

	t.Run("keys that are not near expiry are skipped", func(t *testing.T) {
		server, tokenCalls := newClaudeOAuthTaskFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":3600}`))
		})
		fresh := newClaudeOAuthTaskChannel(t, server.URL, "rt-fresh", 2*time.Hour)
		expiring := newClaudeOAuthTaskChannel(t, server.URL, "rt-expiring", 10*time.Minute)

		scanned, refreshed := service.RunClaudeCredentialAutoRefreshOnce()
		assert.Equal(t, 2, scanned)
		assert.Equal(t, 1, refreshed)
		assert.Equal(t, int32(1), tokenCalls.Load())
		assert.Equal(t, "at-1", storedClaudeOAuthKey(t, fresh.Id).AccessToken)
		assert.Equal(t, "at-new", storedClaudeOAuthKey(t, expiring.Id).AccessToken)
	})
}

func TestResetClaudeChannelUsageDoesNotRetryRejectedResetRequest(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	var resetCalls, tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/oauth/token":
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":3600}`))
		case r.URL.Path == "/api/oauth/usage":
			_, _ = w.Write([]byte(`{"cedar_ember":{"eligible":true,"at_limit":true,"next_grant_id":"grant_next","grants":[{"id":"grant_next","label":"Weekly reset","resets_left":1,"ends_at":"` + future + `","clears":["five_hour"],"usable_now":true,"use_requires_limit":true,"percent_used":{},"blocking":[]}]}}`))
		case r.URL.Path == "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"organization":{"uuid":"2b3f6e9a-1b2c-4d5e-8f90-123456789abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
			resetCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at", RefreshToken: "rt-1", Scope: "user:profile"})
	envelope := postJSON(t, ResetClaudeChannelUsage, "/api/channel/1/claude/usage/reset", "", 1, channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Equal(t, int32(1), resetCalls.Load(), "the reset POST is sent exactly once")
	assert.Equal(t, int32(0), tokenCalls.Load(), "a rejected reset POST does not rotate the token")
}

func TestResetClaudeChannelUsageReportsInterruptedRequestAsUnknown(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	var resetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/oauth/usage":
			_, _ = w.Write([]byte(`{"cedar_ember":{"eligible":true,"at_limit":true,"next_grant_id":"grant_next","grants":[{"id":"grant_next","label":"Weekly reset","resets_left":1,"ends_at":"` + future + `","clears":["five_hour"],"usable_now":true,"use_requires_limit":true,"percent_used":{},"blocking":[]}]}}`))
		case r.URL.Path == "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"organization":{"uuid":"2b3f6e9a-1b2c-4d5e-8f90-123456789abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
			resetCalls.Add(1)
			// Drop the connection after the request was received.
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijacker.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at", Scope: "user:profile"})
	envelope := postJSON(t, ResetClaudeChannelUsage, "/api/channel/1/claude/usage/reset", "", 1, channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Contains(t, envelope.Message, "result is unknown")
	assert.Equal(t, int32(1), resetCalls.Load())
}

func TestGetClaudeChannelUsageDoesNotRefreshOrRetryOnForbidden(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	var usageCalls, tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth/token":
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":3600}`))
		case "/api/oauth/usage":
			usageCalls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"type":"permission_error","message":"scope"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	useClaudeOAuthTokenServer(t, server)

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at-1", RefreshToken: "rt-1", Scope: "user:profile"})
	envelope := getJSON(t, GetClaudeChannelUsage, "/api/channel/1/claude/usage", channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Equal(t, http.StatusForbidden, envelope.UpstreamStatus)
	assert.Equal(t, int32(1), usageCalls.Load(), "a 403 is not retried")
	assert.Equal(t, int32(0), tokenCalls.Load(), "a 403 does not rotate the token")
	assert.Equal(t, "at-1", storedClaudeOAuthKey(t, channel.Id).AccessToken)
}

func TestClaudeOAuthKeyWithoutExpiryIsRefreshedByTaskNotInline(t *testing.T) {
	server, tokenCalls := newClaudeOAuthTaskFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":28800,"refresh_token":"rt-noexp-2"}`))
	})
	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at-pasted", RefreshToken: "rt-noexp"})
	pasted, err := service.ParseClaudeOAuthKey(channel.Key)
	require.NoError(t, err)

	// The relay path uses the pasted token as is.
	key, err := service.EnsureClaudeChannelAccessToken(context.Background(), channel.Id, pasted)
	require.NoError(t, err)
	assert.Equal(t, "at-pasted", key.AccessToken)
	assert.Equal(t, int32(0), tokenCalls.Load(), "an unknown expiry is not refreshed inline")

	// The background task renews it once and stores a real expiry.
	scanned, refreshed := service.RunClaudeCredentialAutoRefreshOnce()
	assert.Equal(t, 1, scanned)
	assert.Equal(t, 1, refreshed)
	assert.Equal(t, int32(1), tokenCalls.Load())
	stored := storedClaudeOAuthKey(t, channel.Id)
	assert.Equal(t, "at-new", stored.AccessToken)
	assert.Equal(t, "rt-noexp-2", stored.RefreshToken)
	assert.WithinDuration(t, time.Now().Add(8*time.Hour), stored.ExpiresAtTime(), time.Minute)

	_, refreshed = service.RunClaudeCredentialAutoRefreshOnce()
	assert.Equal(t, 0, refreshed, "a key with a known, distant expiry is skipped on the next tick")
	assert.Equal(t, int32(1), tokenCalls.Load())
}

func TestClaudeCredentialAutoRefreshTaskKeepsChannelOnClientLevelError(t *testing.T) {
	server, tokenCalls := newClaudeOAuthTaskFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"client not found"}`))
	})
	channel := newClaudeOAuthTaskChannel(t, server.URL, "rt-client-level", 10*time.Minute)

	service.RunClaudeCredentialAutoRefreshOnce()
	assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, channel.Id))
	assert.Equal(t, "rt-client-level", storedClaudeOAuthKey(t, channel.Id).RefreshToken)

	service.RunClaudeCredentialAutoRefreshOnce()
	assert.Equal(t, int32(2), tokenCalls.Load(), "a client-level error is retried on the next tick")

	_, _, err := service.RefreshClaudeChannelCredential(context.Background(), channel.Id, service.ClaudeCredentialRefreshOptions{Force: true})
	require.Error(t, err)
	assert.False(t, service.IsPermanentClaudeOAuthError(err))
	assert.True(t, service.IsClientLevelClaudeOAuthError(err))
}

func TestResetClaudeChannelUsageReportsUpstreamServerErrorAsUnknown(t *testing.T) {
	setupClaudeOAuthChannelDB(t)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	var resetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/oauth/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"cedar_ember":{"eligible":true,"at_limit":true,"next_grant_id":"grant_next","grants":[{"id":"grant_next","label":"Weekly reset","resets_left":1,"ends_at":"` + future + `","clears":["five_hour"],"usable_now":true,"use_requires_limit":true,"percent_used":{},"blocking":[]}]}}`))
		case r.URL.Path == "/api/oauth/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"organization":{"uuid":"2b3f6e9a-1b2c-4d5e-8f90-123456789abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
			resetCalls.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	channel := newClaudeOAuthTestChannel(t, server.URL, service.ClaudeOAuthKey{AccessToken: "at", Scope: "user:profile"})
	envelope := postJSON(t, ResetClaudeChannelUsage, "/api/channel/1/claude/usage/reset", "", 1, channelParams(channel.Id))
	assert.False(t, envelope.Success)
	assert.Contains(t, envelope.Message, "result is unknown")
	assert.Equal(t, int32(1), resetCalls.Load(), "a 5xx reset answer is not retried")
}
