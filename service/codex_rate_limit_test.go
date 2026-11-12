package service

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexLimitHeaders(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

// A Codex channel that answered with its usage limit cools down until the
// exhausted window resets (7d before 5h), or until the reset its error body
// names; a 429 that does not prove exhaustion gets the short fallback.
func TestMarkCodexChannelCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limitErr := types.WithOpenAIError(types.OpenAIError{Type: "usage_limit_reached", Message: "The usage limit has been reached"}, http.StatusTooManyRequests)
	tests := []struct {
		name      string
		channelID int
		multiKey  bool
		apiErr    *types.NewAPIError
		headers   http.Header
		body      string
		wantFor   time.Duration // 0: no cooldown
	}{
		{
			name: "weekly window exhausted", channelID: 9101, apiErr: limitErr,
			headers: codexLimitHeaders(
				"x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "3600",
				"x-codex-secondary-used-percent", "100", "x-codex-secondary-window-minutes", "10080", "x-codex-secondary-reset-after-seconds", "259200"),
			wantFor: 259200 * time.Second,
		},
		{
			name: "only the 5h window exhausted", channelID: 9102, apiErr: limitErr,
			headers: codexLimitHeaders(
				"x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "3600",
				"x-codex-secondary-used-percent", "40", "x-codex-secondary-window-minutes", "10080", "x-codex-secondary-reset-after-seconds", "259200"),
			wantFor: time.Hour,
		},
		{
			name: "429 below 100% falls back", channelID: 9103, apiErr: limitErr,
			headers: codexLimitHeaders("x-codex-primary-used-percent", "80", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "3600"),
			wantFor: codexUsageLimitFallbackCooldown,
		},
		{
			name: "reset named by the body", channelID: 9104, apiErr: limitErr,
			body:    `{"error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":7200}}`,
			wantFor: 2 * time.Hour,
		},
		{
			name: "bare stream error event without status 429", channelID: 9105,
			apiErr:  types.WithOpenAIError(types.OpenAIError{Type: "usage_limit_reached"}, http.StatusInternalServerError),
			wantFor: codexUsageLimitFallbackCooldown,
		},
		{name: "multi-key channel is not cooled down", channelID: 9106, multiKey: true, apiErr: limitErr},
		{name: "other error", channelID: 9107, apiErr: types.NewOpenAIError(assert.AnError, types.ErrorCodeBadResponse, http.StatusInternalServerError)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			RecordSubscriptionLimitHeaders(c, tt.headers)
			RecordCodexUsageLimitBody(c, []byte(tt.body))
			now := time.Now()
			MarkCodexChannelCooldown(c, types.ChannelError{ChannelId: tt.channelID, ChannelType: constant.ChannelTypeCodex, IsMultiKey: tt.multiKey}, tt.apiErr)
			if tt.wantFor == 0 {
				assert.False(t, model.CodexChannelCoolingDown(tt.channelID, now))
				return
			}
			assert.True(t, model.CodexChannelCoolingDown(tt.channelID, now.Add(tt.wantFor-5*time.Second)))
			assert.False(t, model.CodexChannelCoolingDown(tt.channelID, now.Add(tt.wantFor+5*time.Second)))
		})
	}
}

// Channel selection skips a cooling Codex channel while another candidate is
// available, and still returns it when it is the only one.
func TestChannelSelectionSkipsCoolingCodexChannel(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "codex-cooldown-model"
	createChannelSelectAutoGroupsChannel(t, db, 9201, "default", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 9202, "default", modelName)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	retry := 0
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: modelName, RequestPath: "/v1/responses", Retry: &retry}

	model.CoolDownCodexChannel(9201, time.Now().Add(time.Hour))
	for range 20 {
		channel, _, err := CacheGetRandomSatisfiedChannel(param)
		require.NoError(t, err)
		require.NotNil(t, channel)
		assert.Equal(t, 9202, channel.Id)
	}

	model.CoolDownCodexChannel(9202, time.Now().Add(time.Hour))
	channel, _, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, channel, "with every candidate cooling the request still reaches a channel")
}

func TestDetectSubscriptionAccountLimitCodexHeaders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	limitErr := types.WithOpenAIError(types.OpenAIError{Type: "usage_limit_reached"}, http.StatusTooManyRequests)

	limit, limited := DetectSubscriptionAccountLimit(limitErr, codexLimitHeaders(
		"x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "7200",
		"x-codex-secondary-used-percent", "100", "x-codex-secondary-window-minutes", "10080", "x-codex-secondary-reset-after-seconds", "259200"), nil, now)
	require.True(t, limited)
	assert.Equal(t, SubscriptionAccountLimit{Window: "7d", ResetAt: now.Unix() + 259200}, limit)

	limit, limited = DetectSubscriptionAccountLimit(limitErr, codexLimitHeaders(
		"x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "7200",
		"x-codex-secondary-used-percent", "12", "x-codex-secondary-window-minutes", "10080", "x-codex-secondary-reset-after-seconds", "259200"), nil, now)
	require.True(t, limited)
	assert.Equal(t, SubscriptionAccountLimit{Window: "5h", ResetAt: now.Unix() + 7200}, limit)

	// Passive samples of a Codex channel use the same window keys.
	windows := ParseCodexUsageWindows(codexLimitHeaders(
		"x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "7200"), now)
	limit, limited = DetectSubscriptionAccountLimit(limitErr, nil, windows, now)
	require.True(t, limited)
	assert.Equal(t, SubscriptionAccountLimit{Window: "5h", ResetAt: now.Unix() + 7200}, limit)
}

func fakeCodexJWT(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := common.Marshal(map[string]any{codexJWTClaimPath: map[string]any{"chatgpt_account_id": accountID}})
	require.NoError(t, err)
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// seedCodexChannel stores an enabled Codex channel with an expired token set.
func seedCodexChannel(t *testing.T, id int, refreshToken string) {
	t.Helper()
	key, err := common.Marshal(CodexOAuthKey{AccessToken: "old-access", RefreshToken: refreshToken, AccountID: "acct-old", Expired: time.Now().Add(-time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	autoBan := 1
	require.NoError(t, model.DB.Create(&model.Channel{Id: id, Type: constant.ChannelTypeCodex, Name: "codex-" + strconv.Itoa(id), Key: string(key), Status: common.ChannelStatusEnabled, AutoBan: &autoBan}).Error)
	t.Cleanup(func() { model.DB.Exec("DELETE FROM channels WHERE id = ?", id) })
}

// The refresh request names the Codex scope; an answer without a
// rotated refresh token keeps the old one and a returned id_token updates the
// account. A refresh token rejected for good disables the channel and is not
// sent again; a transient failure leaves the channel alone.
func TestRefreshCodexChannelCredential(t *testing.T) {
	var calls int
	var lastForm url.Values
	answer := func(w http.ResponseWriter) {}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.NoError(t, r.ParseForm())
		lastForm = r.PostForm
		answer(w)
	}))
	t.Cleanup(server.Close)
	original := CodexOAuthTokenURL
	CodexOAuthTokenURL = server.URL
	t.Cleanup(func() { CodexOAuthTokenURL = original })

	t.Run("renewed without rotation", func(t *testing.T) {
		seedCodexChannel(t, 9301, "refresh-1")
		idToken := fakeCodexJWT(t, "acct-new")
		answer = func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"access_token":"new-access","id_token":"` + idToken + `","expires_in":3600}`))
		}
		key, _, err := RefreshCodexChannelCredential(context.Background(), 9301, CodexCredentialRefreshOptions{})
		require.NoError(t, err)
		assert.Equal(t, "new-access", key.AccessToken)
		assert.Equal(t, "refresh-1", key.RefreshToken)
		assert.Equal(t, idToken, key.IDToken)
		assert.Equal(t, "acct-new", key.AccountID)
		assert.Equal(t, "openid profile email", lastForm.Get("scope"))
	})

	t.Run("access token already renewed by another caller", func(t *testing.T) {
		seedCodexChannel(t, 9302, "refresh-2")
		calls = 0
		key, _, err := RefreshCodexChannelCredential(context.Background(), 9302, CodexCredentialRefreshOptions{RejectedAccessToken: "a-token-from-before"})
		require.NoError(t, err)
		assert.Equal(t, "old-access", key.AccessToken)
		assert.Zero(t, calls)
	})

	t.Run("permanent failure disables the channel", func(t *testing.T) {
		seedCodexChannel(t, 9303, "refresh-3")
		answer = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"refresh_token_reused","message":"Your refresh token has already been used"}}`))
		}
		calls = 0
		_, _, err := RefreshCodexChannelCredential(context.Background(), 9303, CodexCredentialRefreshOptions{})
		require.Error(t, err)
		code, permanent := CodexPermanentRefreshErrorCode(err)
		assert.True(t, permanent)
		assert.Equal(t, "refresh_token_reused", code)
		ch, err := model.GetChannelById(9303, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusAutoDisabled, ch.Status)

		_, _, err = RefreshCodexChannelCredential(context.Background(), 9303, CodexCredentialRefreshOptions{})
		require.Error(t, err)
		assert.Equal(t, 1, calls, "a rejected refresh token is not sent again")
	})

	t.Run("transient failure keeps the channel", func(t *testing.T) {
		seedCodexChannel(t, 9304, "refresh-4")
		answer = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>bad gateway</html>`))
		}
		_, _, err := RefreshCodexChannelCredential(context.Background(), 9304, CodexCredentialRefreshOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status=502")
		_, permanent := CodexPermanentRefreshErrorCode(err)
		assert.False(t, permanent)
		ch, err := model.GetChannelById(9304, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, ch.Status)
	})
}
