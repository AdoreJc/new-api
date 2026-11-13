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
	"github.com/QuantumNous/new-api/setting/operation_setting"
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
			t.Cleanup(func() { model.ClearCodexChannelCooldown(tt.channelID) })
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
// available, from the memory cache and over a session affinity binding; with
// every candidate cooling a channel is still chosen. (The database path is
// covered in model.)
func TestChannelSelectionSkipsCoolingCodexChannel(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "codex-cooldown-model"
	createChannelSelectAutoGroupsChannel(t, db, 9201, "default", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 9202, "default", modelName)
	t.Cleanup(func() {
		model.ClearCodexChannelCooldown(9201)
		model.ClearCodexChannelCooldown(9202)
	})
	gin.SetMode(gin.TestMode)

	t.Run("cache", func(t *testing.T) {
		{
			model.InitChannelCache()
			model.ClearCodexChannelCooldown(9202)
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
	})

	t.Run("affinity", func(t *testing.T) {
		model.InitChannelCache()
		model.ClearCodexChannelCooldown(9201)
		model.ClearCodexChannelCooldown(9202)
		affinity := operation_setting.GetChannelAffinitySetting()
		original := *affinity
		t.Cleanup(func() { *affinity = original })
		*affinity = operation_setting.ChannelAffinitySetting{
			Enabled: true, DefaultTTLSeconds: 60, MaxEntries: 100,
			Rules: []operation_setting.ChannelAffinityRule{{
				Name: t.Name(), ModelRegex: []string{"^" + modelName + "$"}, SessionMode: "prefer",
				KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Session"}},
			}},
		}
		newCtx := func() *gin.Context {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			ctx.Request.Header.Set("X-Session", t.Name())
			common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
			return ctx
		}
		ctx := newCtx()
		_, found := GetPreferredChannelByAffinity(ctx, modelName, "default")
		require.False(t, found)
		RecordChannelAffinity(ctx, 9201)
		t.Cleanup(func() { ClearCurrentChannelAffinityCache(ctx) })

		ctx = newCtx()
		channel, _, selectErr := SelectChannelForRequest(ctx, modelName, &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: modelName})
		require.Nil(t, selectErr)
		assert.Equal(t, 9201, channel.Id, "the binding wins while the channel is not cooling")

		model.CoolDownCodexChannel(9201, time.Now().Add(time.Hour))
		ctx = newCtx()
		channel, _, selectErr = SelectChannelForRequest(ctx, modelName, &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: modelName})
		require.Nil(t, selectErr)
		assert.Equal(t, 9202, channel.Id)
	})
}

// A cooldown ends early when the channel is enabled, its key is replaced, or
// it answers successfully below its limit.
func TestCodexChannelCooldownEndsEarly(t *testing.T) {
	const channelID = 9501
	require.NoError(t, model.DB.AutoMigrate(&model.Ability{}))
	seedCodexChannel(t, channelID, "refresh-cooldown")
	t.Cleanup(func() { model.ClearCodexChannelCooldown(channelID) })
	cool := func() {
		model.CoolDownCodexChannel(channelID, time.Now().Add(time.Hour))
		require.True(t, model.CodexChannelCoolingDown(channelID, time.Now()))
	}

	cool()
	assert.False(t, model.UpdateChannelStatus(channelID, "", common.ChannelStatusEnabled, "manual operation"), "already enabled")
	assert.False(t, model.CodexChannelCoolingDown(channelID, time.Now()), "enabling ends the cooldown")

	cool()
	ch, err := model.GetChannelById(channelID, true)
	require.NoError(t, err)
	ch.Key = `{"access_token":"new","refresh_token":"new","account_id":"acct-new"}`
	require.NoError(t, ch.Update())
	assert.False(t, model.CodexChannelCoolingDown(channelID, time.Now()), "a new key ends the cooldown")

	cool()
	EndCodexChannelCooldownAfterSuccess(channelID, codexLimitHeaders("x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "3600"))
	assert.True(t, model.CodexChannelCoolingDown(channelID, time.Now()), "a success that still shows an exhausted window keeps it")
	EndCodexChannelCooldownAfterSuccess(channelID, codexLimitHeaders("x-codex-primary-used-percent", "20", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-after-seconds", "3600"))
	assert.False(t, model.CodexChannelCoolingDown(channelID, time.Now()))
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
	seedCodexChannelWithAutoBan(t, id, refreshToken, 1)
}

func seedCodexChannelWithAutoBan(t *testing.T, id int, refreshToken string, autoBan int) {
	t.Helper()
	key, err := common.Marshal(CodexOAuthKey{AccessToken: "old-access", RefreshToken: refreshToken, AccountID: "acct-old", Expired: time.Now().Add(-time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.Channel{Id: id, Type: constant.ChannelTypeCodex, Name: "codex-" + strconv.Itoa(id), Key: string(key), Status: common.ChannelStatusEnabled, AutoBan: &autoBan}).Error)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM channels WHERE id = ?", id)
		codexRefreshFailures.Delete(id)
	})
}

// An answer without a
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
		assert.Equal(t, "refresh_token", lastForm.Get("grant_type"))
		assert.Empty(t, lastForm.Get("scope"))
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

	t.Run("automatic disabling turned off only blocks the token", func(t *testing.T) {
		seedCodexChannelWithAutoBan(t, 9305, "refresh-5", 0)
		answer = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
		}
		calls = 0
		_, _, err := RefreshCodexChannelCredential(context.Background(), 9305, CodexCredentialRefreshOptions{})
		require.Error(t, err)
		ch, err := model.GetChannelById(9305, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, ch.Status)
		_, _, err = RefreshCodexChannelCredential(context.Background(), 9305, CodexCredentialRefreshOptions{})
		require.Error(t, err)
		assert.Equal(t, 1, calls)
	})

	// Client and scope errors are configuration problems, and only the
	// structured code of a 400/401 counts, not words in the description.
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "invalid_client", status: http.StatusUnauthorized, body: `{"error":"invalid_client"}`},
		{name: "invalid_scope", status: http.StatusBadRequest, body: `{"error":"invalid_scope"}`},
		{name: "code word only in the description", status: http.StatusBadRequest, body: `{"error":"server_error","error_description":"retry; not an invalid_grant"}`},
		{name: "token code on a 5xx", status: http.StatusBadGateway, body: `{"error":"invalid_grant"}`},
		{name: "non-JSON error page", status: http.StatusBadGateway, body: `<html>bad gateway</html>`},
	} {
		t.Run("not permanent: "+tc.name, func(t *testing.T) {
			seedCodexChannel(t, 9310, "refresh-10")
			answer = func(w http.ResponseWriter) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}
			calls = 0
			_, _, err := RefreshCodexChannelCredential(context.Background(), 9310, CodexCredentialRefreshOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "status="+strconv.Itoa(tc.status))
			_, permanent := CodexPermanentRefreshErrorCode(err)
			assert.False(t, permanent)
			ch, err := model.GetChannelById(9310, true)
			require.NoError(t, err)
			assert.Equal(t, common.ChannelStatusEnabled, ch.Status)
			_, _, err = RefreshCodexChannelCredential(context.Background(), 9310, CodexCredentialRefreshOptions{})
			require.Error(t, err)
			assert.Equal(t, 2, calls, "the token is tried again")
		})
	}

	t.Run("background task skips a rejected token", func(t *testing.T) {
		seedCodexChannel(t, 9306, "refresh-6")
		codexRefreshFailures.Store(9306, codexRefreshFailure{fingerprint: refreshTokenFingerprint("refresh-6"), code: "invalid_grant"})
		calls = 0
		runCodexCredentialAutoRefreshOnce()
		assert.Zero(t, calls)
	})
}

// fakeCodexSubscriptionJWT builds an unsigned test id_token whose ChatGPT
// auth claim carries the given subscription fields.
func fakeCodexSubscriptionJWT(t *testing.T, auth map[string]any) string {
	t.Helper()
	payload, err := common.Marshal(map[string]any{codexJWTClaimPath: auth})
	require.NoError(t, err)
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestExtractCodexSubscriptionFromJWT(t *testing.T) {
	sub, ok := ExtractCodexSubscriptionFromJWT(fakeCodexSubscriptionJWT(t, map[string]any{
		"chatgpt_plan_type":                 "pro",
		"chatgpt_subscription_active_start": "2026-10-05T08:00:00+00:00",
		"chatgpt_subscription_active_until": float64(1794000000),
	}))
	require.True(t, ok)
	assert.Equal(t, "pro", sub.PlanType)
	assert.Equal(t, time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC).Unix(), *sub.ActiveStart)
	assert.Equal(t, int64(1794000000), *sub.ActiveUntil)

	sub, ok = ExtractCodexSubscriptionFromJWT(fakeCodexSubscriptionJWT(t, map[string]any{"chatgpt_plan_type": "plus"}))
	require.True(t, ok)
	assert.Nil(t, sub.ActiveStart, "missing dates stay unknown")
	assert.Nil(t, sub.ActiveUntil)

	_, ok = ExtractCodexSubscriptionFromJWT(fakeCodexSubscriptionJWT(t, map[string]any{"chatgpt_account_id": "acct", "chatgpt_subscription_active_until": "soon"}))
	assert.False(t, ok, "no usable subscription field")
	_, ok = ExtractCodexSubscriptionFromJWT("not-a-jwt")
	assert.False(t, ok)
}

// The subscription window follows the account: saving reads it, a refresh
// after renewal moves the end date, and an id_token without dates keeps the
// stored window. Manual cost dates are separate columns and never touched.
func TestCodexSubscriptionWindowFollowsRefresh(t *testing.T) {
	until1 := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	until2 := until1.AddDate(0, 1, 0)
	idToken := fakeCodexSubscriptionJWT(t, map[string]any{
		"chatgpt_plan_type":                 "plus",
		"chatgpt_subscription_active_start": "2026-10-05T00:00:00Z",
		"chatgpt_subscription_active_until": until1.Format(time.RFC3339),
	})
	key, err := common.Marshal(CodexOAuthKey{AccessToken: "a", RefreshToken: "refresh-sub", IDToken: idToken, Expired: time.Now().Add(-time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	manualFrom := int64(1700000000)
	require.NoError(t, model.DB.AutoMigrate(&model.QuotaData{}))
	require.NoError(t, model.DB.Create(&model.Channel{Id: 9320, Type: constant.ChannelTypeCodex, Name: "codex-sub", Key: string(key), Status: common.ChannelStatusEnabled, CostEffectiveFrom: &manualFrom}).Error)
	t.Cleanup(func() { model.DB.Delete(&model.Channel{}, 9320) })
	// October's amortized fixed cost, read from the automatic start date.
	octoberCost := func() int64 {
		t.Helper()
		summary, err := model.GetCostSummary(time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local).Unix(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local).Unix()-1, "", model.CostDimensionChannel)
		require.NoError(t, err)
		for _, info := range summary.FixedChannels {
			if info.ChannelID == 9320 {
				return info.CostQuota
			}
		}
		return 0
	}

	SyncCodexChannelSubscription(9320)
	var ch model.Channel
	require.NoError(t, model.DB.First(&ch, 9320).Error)
	assert.Equal(t, "plus", ch.SubscriptionPlanType)
	assert.Equal(t, until1.Unix(), *ch.SubscriptionActiveUntil)
	require.NotNil(t, ch.SubscriptionSyncedAt)
	from, fromAuto, to, toAuto := ch.FixedCostWindow()
	assert.Equal(t, manualFrom, *from, "a manual start date wins over the subscription")
	assert.False(t, fromAuto)
	assert.Equal(t, until1.Unix(), *to)
	assert.True(t, toAuto)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 9320).Updates(map[string]any{"cost_effective_from": nil, "monthly_cost_usd": 310}).Error)
	before := octoberCost()
	require.Positive(t, before, "October is inside the subscription that started 10-05")

	// The renewed token reports the start of the new billing period.
	renewed := fakeCodexSubscriptionJWT(t, map[string]any{"chatgpt_plan_type": "plus", "chatgpt_subscription_active_start": until1.Format(time.RFC3339), "chatgpt_subscription_active_until": until2.Format(time.RFC3339)})
	respond := `{"access_token":"new-access","id_token":"` + renewed + `","expires_in":3600}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(respond))
	}))
	t.Cleanup(server.Close)
	original := CodexOAuthTokenURL
	CodexOAuthTokenURL = server.URL
	t.Cleanup(func() { CodexOAuthTokenURL = original })

	_, _, err = RefreshCodexChannelCredential(context.Background(), 9320, CodexCredentialRefreshOptions{})
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&ch, 9320).Error)
	assert.Equal(t, until2.Unix(), *ch.SubscriptionActiveUntil, "renewal extends the window")
	assert.Equal(t, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).Unix(), *ch.SubscriptionActiveStart, "the automatic start never moves later")
	assert.Equal(t, before, octoberCost(), "a refresh does not change past months")

	respond = `{"access_token":"newer-access","id_token":"` + fakeCodexSubscriptionJWT(t, map[string]any{"chatgpt_account_id": "acct"}) + `","expires_in":3600}`
	_, _, err = RefreshCodexChannelCredential(context.Background(), 9320, CodexCredentialRefreshOptions{})
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&ch, 9320).Error)
	assert.Equal(t, until2.Unix(), *ch.SubscriptionActiveUntil, "a token without dates keeps the stored window")
}
