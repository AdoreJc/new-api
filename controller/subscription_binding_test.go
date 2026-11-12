package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSubscriptionUsageDB(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.UserSubscription{}, &model.User{}, &model.AuditLog{}, &model.Token{}))
	originalLogDB, originalRedisEnabled := model.LOG_DB, common.RedisEnabled
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	t.Cleanup(func() {
		// Channel ids restart in every in-memory database; drop the usage the
		// shared per-channel cache kept for them.
		var channelIds []int
		_ = database.Model(&model.Channel{}).Pluck("id", &channelIds).Error
		for _, id := range channelIds {
			service.InvalidateSubscriptionUsage(id)
		}
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.RedisEnabled = originalRedisEnabled
	})
}

func newSubscriptionUsageSub(t *testing.T, userID int, channelID int, endOffset time.Duration) *model.UserSubscription {
	t.Helper()
	now := time.Now().Unix()
	sub := &model.UserSubscription{
		UserId:         userID,
		PlanId:         1,
		Status:         "active",
		StartTime:      now - 60,
		EndTime:        now + int64(endOffset/time.Second),
		BoundChannelId: channelID,
	}
	if channelID > 0 {
		sub.BoundGroup = model.SubscriptionBoundGroupName(constant.ChannelTypeClaudeOAuth, channelID)
	}
	require.NoError(t, model.DB.Create(sub).Error)
	return sub
}

func getSubscriptionUsage(t *testing.T, userID int, subID int) apiEnvelope {
	t.Helper()
	return subscriptionSelfRequest(t, GetSubscriptionSelfUsage, http.MethodGet, "/api/subscription/self/usage/"+itoa(subID), userID, subID)
}

func subscriptionSelfRequest(t *testing.T, handler gin.HandlerFunc, method string, path string, userID int, subID int) apiEnvelope {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, nil)
	c.Params = channelParams(subID)
	c.Set("id", userID)
	c.Set("role", common.RoleCommonUser)
	handler(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope apiEnvelope
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope
}

func itoa(v int) string {
	return channelParams(v)[0].Value
}

func TestGetSubscriptionSelfUsageRejectsForeignInactiveAndUnboundSubscriptions(t *testing.T) {
	setupSubscriptionUsageDB(t)
	channel := newClaudeOAuthTestChannel(t, "http://127.0.0.1:9", service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})

	foreign := newSubscriptionUsageSub(t, 2, channel.Id, time.Hour)
	envelope := getSubscriptionUsage(t, 1, foreign.Id)
	assert.False(t, envelope.Success)
	assert.Equal(t, "Subscription not found", envelope.Message)

	envelope = getSubscriptionUsage(t, 1, 9999)
	assert.False(t, envelope.Success)
	assert.Equal(t, "Subscription not found", envelope.Message)

	expired := newSubscriptionUsageSub(t, 1, channel.Id, -time.Hour)
	envelope = getSubscriptionUsage(t, 1, expired.Id)
	assert.False(t, envelope.Success)
	assert.Equal(t, "This subscription is no longer active", envelope.Message)

	unbound := newSubscriptionUsageSub(t, 1, 0, time.Hour)
	envelope = getSubscriptionUsage(t, 1, unbound.Id)
	assert.False(t, envelope.Success)
	assert.Equal(t, "This subscription is not bound to a channel", envelope.Message)

	dangling := newSubscriptionUsageSub(t, 1, channel.Id+100, time.Hour)
	envelope = getSubscriptionUsage(t, 1, dangling.Id)
	assert.False(t, envelope.Success)
	assert.Equal(t, "The bound channel is no longer available", envelope.Message)
}

func TestGetSubscriptionSelfUsageProjectsClaudeWindowsWithoutAccountDetails(t *testing.T) {
	setupSubscriptionUsageDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/oauth/usage", r.URL.Path)
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":33.5,"resets_at":"2026-11-05T10:00:00Z"},"seven_day":{"utilization":120,"resets_at":"2026-11-09T00:00:00Z"},"seven_day_sonnet":null,"extra_usage":{"is_enabled":true,"monthly_limit":500,"used_credits":12.5},"organization":{"uuid":"org-secret"}}`))
	}))
	t.Cleanup(upstream.Close)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	sub := newSubscriptionUsageSub(t, 1, channel.Id, time.Hour)

	envelope := getSubscriptionUsage(t, 1, sub.Id)
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, http.StatusOK, envelope.UpstreamStatus)
	assert.Equal(t, "claude", envelope.Data["provider"])
	assert.EqualValues(t, constant.ChannelTypeClaudeOAuth, envelope.Data["channel_type"])
	assert.Equal(t, true, envelope.Data["limit_reached"])
	windows, ok := envelope.Data["windows"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, windows, 2)
	fiveHour := windows["five_hour"].(map[string]any)
	assert.EqualValues(t, 33.5, fiveHour["utilization"])
	assert.Equal(t, "2026-11-05T10:00:00Z", fiveHour["resets_at"])
	sevenDay := windows["seven_day"].(map[string]any)
	assert.EqualValues(t, 100, sevenDay["utilization"], "utilization is clamped")
	for _, forbidden := range []string{"extra_usage", "organization", "key", "access_token"} {
		_, present := envelope.Data[forbidden]
		assert.False(t, present, forbidden)
	}
}

func TestGetSubscriptionSelfUsageBackfillsFableWindowFromSampledHeaders(t *testing.T) {
	setupSubscriptionUsageDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":33.5,"resets_at":"2026-11-05T10:00:00Z"},"seven_day":{"utilization":20}}`))
	}))
	t.Cleanup(upstream.Close)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	now := time.Now()
	info := channel.GetOtherInfo()
	info[service.ClaudePassiveUsageKey] = map[string]service.ClaudePassiveUsageWindow{
		"seven_day_overage_included": {Utilization: 37.5, ResetsAt: now.Add(time.Hour).Unix(), SampledAt: now.Unix()},
	}
	channel.SetOtherInfo(info)
	require.NoError(t, model.DB.Model(channel).Update("other_info", channel.OtherInfo).Error)
	sub := newSubscriptionUsageSub(t, 1, channel.Id, time.Hour)

	envelope := getSubscriptionUsage(t, 1, sub.Id)
	require.True(t, envelope.Success, envelope.Message)
	windows, ok := envelope.Data["windows"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, windows, 3)
	fable := windows["seven_day_overage_included"].(map[string]any)
	assert.EqualValues(t, 37.5, fable["utilization"])
	assert.Equal(t, "passive", fable["source"])
	assert.Equal(t, now.UTC().Format(time.RFC3339), fable["sampled_at"])
	_, hasSource := windows["seven_day"].(map[string]any)["source"]
	assert.False(t, hasSource, "API windows carry no source")
}

func TestGetSubscriptionSelfUsageProjectsCodexWindows(t *testing.T) {
	setupSubscriptionUsageDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/wham/usage", r.URL.Path)
		require.Equal(t, "Bearer codex-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus","user_id":"user-secret","email":"someone@example.com","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1762340400},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_after_seconds":86400}},"rate_limit_reset_credits":{"available_count":2}}`))
	}))
	t.Cleanup(upstream.Close)
	channel := &model.Channel{
		Type:    constant.ChannelTypeCodex,
		BaseURL: &upstream.URL,
		Key:     `{"access_token":"codex-token","account_id":"acc-1"}`,
		Name:    "codex-test",
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	sub := newSubscriptionUsageSub(t, 7, channel.Id, time.Hour)

	envelope := getSubscriptionUsage(t, 7, sub.Id)
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, "codex", envelope.Data["provider"])
	assert.Equal(t, "plus", envelope.Data["plan_type"])
	assert.Equal(t, false, envelope.Data["limit_reached"])
	windows, ok := envelope.Data["windows"].(map[string]any)
	require.True(t, ok)
	primary := windows["primary"].(map[string]any)
	assert.EqualValues(t, 12, primary["utilization"])
	assert.Equal(t, "2025-11-05T11:00:00Z", primary["resets_at"])
	assert.EqualValues(t, 18000, primary["window_seconds"])
	secondary := windows["secondary"].(map[string]any)
	assert.EqualValues(t, 40, secondary["utilization"])
	assert.NotEmpty(t, secondary["resets_at"], "derived from reset_after_seconds")
	for _, forbidden := range []string{"email", "user_id", "rate_limit_reset_credits"} {
		_, present := envelope.Data[forbidden]
		assert.False(t, present, forbidden)
	}
}

func TestGetSubscriptionSelfUsageReportsUpstreamFailureWithoutPayload(t *testing.T) {
	setupSubscriptionUsageDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"plan details that must not leak"}}`))
	}))
	t.Cleanup(upstream.Close)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	sub := newSubscriptionUsageSub(t, 1, channel.Id, time.Hour)

	envelope := getSubscriptionUsage(t, 1, sub.Id)
	assert.False(t, envelope.Success)
	assert.Equal(t, http.StatusForbidden, envelope.UpstreamStatus)
	assert.Nil(t, envelope.Data)
}

// ---- Self reset credits (administrator switch) ----

func newResetCreditUser(t *testing.T, username string, allowed bool) *model.User {
	t.Helper()
	user := &model.User{Username: username, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: username + "-aff"}
	user.SetSetting(dto.UserSetting{AllowSubscriptionResetCredit: allowed})
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func getSelfResetCredits(t *testing.T, userID int, subID int) apiEnvelope {
	t.Helper()
	return subscriptionSelfRequest(t, GetSubscriptionSelfResetCredits, http.MethodGet, "/api/subscription/self/usage/"+itoa(subID)+"/reset-credits", userID, subID)
}

func postSelfReset(t *testing.T, userID int, subID int) apiEnvelope {
	t.Helper()
	return subscriptionSelfRequest(t, ResetSubscriptionSelfUsage, http.MethodPost, "/api/subscription/self/usage/"+itoa(subID)+"/reset", userID, subID)
}

func resetCreditAuditLogs(t *testing.T) []model.AuditLog {
	t.Helper()
	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "subscription.reset_credit_redeem").Order("id").Find(&logs).Error)
	return logs
}

func resetCreditAuditParams(t *testing.T, log model.AuditLog) map[string]any {
	t.Helper()
	require.NotNil(t, log.Other.Op)
	encoded, err := common.Marshal(log.Other.Op.Params)
	require.NoError(t, err)
	var params map[string]any
	require.NoError(t, common.Unmarshal(encoded, &params))
	return params
}

// claudeResetUpstream fakes the Claude OAuth reset endpoints with one
// redeemable grant and counts every request it receives.
func claudeResetUpstream(t *testing.T, hits *atomic.Int32, resets *atomic.Int32) *httptest.Server {
	t.Helper()
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/oauth/usage" && r.URL.Query().Get("cedar_ember") == "1":
			_, _ = w.Write([]byte(`{"cedar_ember":{"eligible":true,"at_limit":true,"next_grant_id":"grant_next","grants":[{"id":"grant_next","label":"Weekly reset","resets_left":1,"ends_at":"` + future + `","clears":["five_hour"],"usable_now":true,"use_requires_limit":true,"percent_used":{},"blocking":[]}]}}`))
		case r.URL.Path == "/api/oauth/usage":
			_, _ = w.Write([]byte(`{"five_hour":{"utilization":100,"resets_at":"` + future + `"}}`))
		case r.URL.Path == "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"organization":{"uuid":"2b3f6e9a-1b2c-4d5e-8f90-123456789abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
			resets.Add(1)
			_, _ = w.Write([]byte(`{"result":"reset","cleared":["five_hour"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSubscriptionSelfResetCreditsRequireTheAdministratorSwitch(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var hits, resets atomic.Int32
	upstream := claudeResetUpstream(t, &hits, &resets)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "fake-token", Scope: "user:profile"})
	user := newResetCreditUser(t, "switch-off", false)
	sub := newSubscriptionUsageSub(t, user.Id, channel.Id, time.Hour)

	for _, envelope := range []apiEnvelope{getSelfResetCredits(t, user.Id, sub.Id), postSelfReset(t, user.Id, sub.Id)} {
		assert.False(t, envelope.Success)
		assert.Equal(t, "Reset credits are not enabled for your account", envelope.Message)
		assert.Nil(t, envelope.Data)
	}
	assert.Zero(t, hits.Load(), "no upstream request while the switch is off")
	assert.Zero(t, resets.Load())
	assert.Empty(t, resetCreditAuditLogs(t))

	usage := getSubscriptionUsage(t, user.Id, sub.Id)
	require.True(t, usage.Success, usage.Message)
	assert.Equal(t, false, usage.Data["can_reset"])
}

func TestSubscriptionSelfResetCreditsRejectForeignInactiveUnboundAndMultiKeySubscriptions(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var hits, resets atomic.Int32
	upstream := claudeResetUpstream(t, &hits, &resets)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "fake-token", Scope: "user:profile"})
	multiKey := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "fake-token", Scope: "user:profile"})
	multiKey.ChannelInfo.IsMultiKey = true
	require.NoError(t, model.DB.Model(multiKey).Update("channel_info", multiKey.ChannelInfo).Error)
	user := newResetCreditUser(t, "switch-on", true)
	other := newResetCreditUser(t, "other-owner", true)

	for _, tc := range []struct {
		name    string
		subId   int
		message string
	}{
		{"foreign", newSubscriptionUsageSub(t, other.Id, channel.Id, time.Hour).Id, "Subscription not found"},
		{"missing", 9999, "Subscription not found"},
		{"expired", newSubscriptionUsageSub(t, user.Id, channel.Id, -time.Hour).Id, "This subscription is no longer active"},
		{"unbound", newSubscriptionUsageSub(t, user.Id, 0, time.Hour).Id, "This subscription is not bound to a channel"},
		{"multi-key", newSubscriptionUsageSub(t, user.Id, multiKey.Id, time.Hour).Id, "Multi-key channels are not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, envelope := range []apiEnvelope{getSelfResetCredits(t, user.Id, tc.subId), postSelfReset(t, user.Id, tc.subId)} {
				assert.False(t, envelope.Success)
				assert.Equal(t, tc.message, envelope.Message)
				assert.Nil(t, envelope.Data)
			}
		})
	}
	assert.Zero(t, hits.Load())
	assert.Zero(t, resets.Load())
	assert.Empty(t, resetCreditAuditLogs(t))
}

func TestSubscriptionSelfResetCreditsClaudeViewRedeemAndAuditLog(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var hits, resets atomic.Int32
	upstream := claudeResetUpstream(t, &hits, &resets)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "fake-token", Scope: "user:profile"})
	user := newResetCreditUser(t, "claude-subscriber", true)
	sub := newSubscriptionUsageSub(t, user.Id, channel.Id, time.Hour)

	usage := getSubscriptionUsage(t, user.Id, sub.Id)
	require.True(t, usage.Success, usage.Message)
	assert.Equal(t, true, usage.Data["can_reset"])

	credits := getSelfResetCredits(t, user.Id, sub.Id)
	require.True(t, credits.Success, credits.Message)
	assert.EqualValues(t, 1, credits.Data["available_count"])
	assert.Zero(t, resets.Load(), "viewing credits never redeems")

	envelope := postSelfReset(t, user.Id, sub.Id)
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, "reset", envelope.Data["outcome"])
	assert.Equal(t, int32(1), resets.Load())

	logs := resetCreditAuditLogs(t)
	require.Len(t, logs, 1)
	assert.Equal(t, user.Id, logs[0].UserId)
	assert.True(t, logs[0].Success)
	params := resetCreditAuditParams(t, logs[0])
	assert.EqualValues(t, sub.Id, params["subscription_id"])
	assert.EqualValues(t, channel.Id, params["channel_id"])
	assert.Equal(t, "claude", params["provider"])
	assert.Equal(t, "reset", params["result"])
}

func TestSubscriptionSelfResetCreditsCodexTrimsAccountDetails(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var consumes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			_, _ = w.Write([]byte(`{"available_count":1,"total_earned_count":3,"credits":[{"id":"credit-secret","reset_type":"weekly","status":"available","expires_at":"2026-12-01T00:00:00Z","profile_user_id":"user-secret","profile_image_url":"https://example.com/me.png","title":"Weekly reset"}]}`))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumes.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"windows_reset":2,"code":"ok","account_email":"someone@example.com"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	channel := &model.Channel{
		Type:    constant.ChannelTypeCodex,
		BaseURL: &upstream.URL,
		Key:     `{"access_token":"fake-codex-token","account_id":"acc-1"}`,
		Name:    "codex-test",
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	user := newResetCreditUser(t, "codex-subscriber", true)
	sub := newSubscriptionUsageSub(t, user.Id, channel.Id, time.Hour)

	credits := getSelfResetCredits(t, user.Id, sub.Id)
	require.True(t, credits.Success, credits.Message)
	assert.EqualValues(t, 1, credits.Data["available_count"])
	list, ok := credits.Data["credits"].([]any)
	require.True(t, ok)
	require.Len(t, list, 1)
	credit := list[0].(map[string]any)
	assert.Equal(t, "available", credit["status"])
	for _, forbidden := range []string{"id", "profile_user_id", "profile_image_url"} {
		_, present := credit[forbidden]
		assert.False(t, present, forbidden)
	}

	envelope := postSelfReset(t, user.Id, sub.Id)
	require.True(t, envelope.Success, envelope.Message)
	assert.EqualValues(t, 2, envelope.Data["windows_reset"])
	_, leaked := envelope.Data["account_email"]
	assert.False(t, leaked)
	assert.Equal(t, int32(1), consumes.Load())

	logs := resetCreditAuditLogs(t)
	require.Len(t, logs, 1)
	params := resetCreditAuditParams(t, logs[0])
	assert.Equal(t, "codex", params["provider"])
	assert.Equal(t, "consumed", params["result"])
}

func TestResetSubscriptionSelfUsageAllowsOneRedemptionPerAccount(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var hits, resets atomic.Int32
	upstream := claudeResetUpstream(t, &hits, &resets)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "fake-token", Scope: "user:profile"})
	user := newResetCreditUser(t, "double-click", true)
	sub := newSubscriptionUsageSub(t, user.Id, channel.Id, time.Hour)

	release, acquired, err := service.AcquireSubscriptionResetLock(channel.Id)
	require.NoError(t, err)
	require.True(t, acquired)
	envelope := postSelfReset(t, user.Id, sub.Id)
	assert.False(t, envelope.Success)
	assert.Equal(t, "Another reset for this account is still in progress. Refresh the reset credits after it finishes", envelope.Message)
	assert.Zero(t, hits.Load(), "the second redemption never reaches upstream")
	release()

	envelope = postSelfReset(t, user.Id, sub.Id)
	require.True(t, envelope.Success, envelope.Message)
	assert.Equal(t, int32(1), resets.Load())
}

// Only the administrator user editor changes the switch; the user's own
// settings endpoints keep the stored value whatever the request carries.
func TestSubscriptionResetCreditSwitchIsAdministratorOnly(t *testing.T) {
	_, identity, target := setupAdminUserTest(t)
	stored := func() bool {
		setting, err := model.GetUserSetting(target.Id, true)
		require.NoError(t, err)
		return setting.AllowSubscriptionResetCredit
	}
	selfRequest := func(handler gin.HandlerFunc, path string, body string) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", target.Id)
		c.Set("role", common.RoleCommonUser)
		handler(c)
		require.Contains(t, recorder.Body.String(), `"success":true`)
	}
	adminUpdate := func(extra string) {
		body := fmt.Sprintf(`{"id":%d,"username":"managed-user","group":"default"%s}`, target.Id, extra)
		response := adminUserRequest(http.MethodPut, "/api/user/", body, "", identity, common.RoleAdminUser, nil, UpdateUser)
		require.Contains(t, response.Body.String(), `"success":true`)
	}

	selfRequest(UpdateUserSetting, "/api/user/setting", `{"notify_type":"email","allow_subscription_reset_credit":true}`)
	selfRequest(UpdateSelf, "/api/user/self", `{"sidebar_modules":"{}","allow_subscription_reset_credit":true}`)
	selfRequest(UpdateSelf, "/api/user/self", `{"language":"en","setting":"{\"allow_subscription_reset_credit\":true}"}`)
	assert.False(t, stored(), "a user cannot enable the switch")

	adminUpdate(`,"allow_subscription_reset_credit":true`)
	assert.True(t, stored())
	response := adminUserRequest(http.MethodGet, "/api/user/", "", "", identity, common.RoleAdminUser, channelParams(target.Id), GetUser)
	var fetched apiEnvelope
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &fetched))
	assert.Equal(t, true, fetched.Data["allow_subscription_reset_credit"])

	selfRequest(UpdateUserSetting, "/api/user/setting", `{"notify_type":"email","allow_subscription_reset_credit":false}`)
	assert.True(t, stored(), "a user cannot disable the switch either")

	adminUpdate("")
	assert.True(t, stored(), "an update without the field keeps the switch")

	adminUpdate(`,"allow_subscription_reset_credit":false`)
	assert.False(t, stored())
}

// ---- API key: GET /api/usage/subscription ----

func newSubscriptionUsageTokenUser(t *testing.T, username string) *model.User {
	t.Helper()
	user := &model.User{Username: username, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: username + "-aff"}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func newSubscriptionUsageToken(t *testing.T, userID int, key string, group string, status int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.Token{UserId: userID, Key: key, Name: key, Status: status, Group: group, ExpiredTime: -1}).Error)
}

func newCodexSubscriptionTestChannel(t *testing.T, baseURL string) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Type:    constant.ChannelTypeCodex,
		BaseURL: &baseURL,
		Key:     `{"access_token":"codex-token","account_id":"acc-1"}`,
		Name:    "codex-test",
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

// tokenSubscriptionUsageRouter serves the endpoint behind its real
// middleware chain (read-only token auth and the per-token limit).
func tokenSubscriptionUsageRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// Initialize production column quoting for the token key lookup.
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	router := gin.New()
	router.GET("/api/usage/subscription", middleware.TokenAuthReadOnly(), middleware.SubscriptionUsageRateLimit(), GetTokenSubscriptionUsage)
	return router
}

func getTokenSubscriptionUsage(t *testing.T, router *gin.Engine, key string, query string) (int, apiEnvelope) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/usage/subscription"+query, nil)
	request.Header.Set("Authorization", "Bearer sk-"+key)
	request.Header.Set("Accept-Language", "zh-CN")
	router.ServeHTTP(recorder, request)
	var envelope apiEnvelope
	if recorder.Body.Len() > 0 {
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	}
	return recorder.Code, envelope
}

func tokenUsageItems(t *testing.T, envelope apiEnvelope) []map[string]any {
	t.Helper()
	raw, ok := envelope.Data["items"].([]any)
	require.True(t, ok, "items is an array")
	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		items = append(items, item.(map[string]any))
	}
	return items
}

func TestTokenSubscriptionUsageServesBoundWindowsAsItemsFromTheSharedCache(t *testing.T) {
	setupSubscriptionUsageDB(t)
	require.NoError(t, i18n.Init())
	var claudeHits, codexHits atomic.Int32
	claudeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":23,"resets_at":"2026-11-12T19:00:00Z"},"seven_day":{"utilization":41.5,"resets_at":"2026-11-15T00:00:00Z"},"organization":{"uuid":"org-secret"}}`))
	}))
	t.Cleanup(claudeUpstream.Close)
	codexUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		codexHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus","email":"someone@example.com","rate_limit":{"limit_reached":false,"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_at":1762340400},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_at":1762900000}}}`))
	}))
	t.Cleanup(codexUpstream.Close)
	claude := newClaudeOAuthTestChannel(t, claudeUpstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	now := time.Now()
	info := claude.GetOtherInfo()
	info[service.ClaudePassiveUsageKey] = map[string]service.ClaudePassiveUsageWindow{
		"seven_day_overage_included": {Utilization: 5, ResetsAt: now.Add(time.Hour).Unix(), SampledAt: now.Unix()},
	}
	claude.SetOtherInfo(info)
	require.NoError(t, model.DB.Model(claude).Update("other_info", claude.OtherInfo).Error)
	codex := newCodexSubscriptionTestChannel(t, codexUpstream.URL)
	user := newSubscriptionUsageTokenUser(t, "bound-user")
	claudeSub := newSubscriptionUsageSub(t, user.Id, claude.Id, time.Hour)
	newSubscriptionUsageSub(t, user.Id, codex.Id, 2*time.Hour)
	newSubscriptionUsageToken(t, user.Id, "boundkey", "default", common.TokenStatusEnabled)
	router := tokenSubscriptionUsageRouter(t)

	code, envelope := getTokenSubscriptionUsage(t, router, "boundkey", "")
	require.Equal(t, http.StatusOK, code)
	require.True(t, envelope.Success, envelope.Message)
	items := tokenUsageItems(t, envelope)
	require.Len(t, items, 5)
	var names []string
	for _, item := range items {
		names = append(names, item["planName"].(string))
		assert.EqualValues(t, 100, item["total"])
		assert.Equal(t, "%", item["unit"])
		assert.InDelta(t, 100, item["used"].(float64)+item["remaining"].(float64), 1e-9)
	}
	assert.ElementsMatch(t, []string{"Claude 5小时", "Claude 7天", "Claude 7天 Fable", "Codex 5小时", "Codex 每周"}, names)
	for _, item := range items {
		switch item["planName"] {
		case "Claude 5小时":
			assert.EqualValues(t, 23, item["used"])
			assert.Equal(t, "2026-11-12T19:00:00Z", item["resetsAt"])
			assert.Nil(t, item["extra"])
		case "Claude 7天 Fable":
			assert.EqualValues(t, 95, item["remaining"])
			assert.Contains(t, item["extra"], "被动采样")
		case "Codex 每周":
			assert.EqualValues(t, 40, item["used"])
			assert.Equal(t, time.Unix(1762900000, 0).UTC().Format(time.RFC3339), item["resetsAt"])
		}
	}
	subscriptions, ok := envelope.Data["subscriptions"].([]any)
	require.True(t, ok)
	require.Len(t, subscriptions, 2)
	for _, raw := range subscriptions {
		entry := raw.(map[string]any)
		for _, forbidden := range []string{"can_reset", "channel_id", "email", "organization"} {
			_, present := entry[forbidden]
			assert.False(t, present, forbidden)
		}
	}

	code, envelope = getTokenSubscriptionUsage(t, router, "boundkey", "?provider=codex")
	require.Equal(t, http.StatusOK, code)
	require.True(t, envelope.Success, envelope.Message)
	items = tokenUsageItems(t, envelope)
	require.Len(t, items, 2)
	for _, item := range items {
		assert.Contains(t, item["planName"], "Codex")
	}

	// The session endpoint shares the per-channel cache.
	session := getSubscriptionUsage(t, user.Id, claudeSub.Id)
	require.True(t, session.Success, session.Message)
	assert.Equal(t, int32(1), claudeHits.Load(), "repeated queries within the TTL are served from the cache")
	assert.Equal(t, int32(1), codexHits.Load())
}

func TestTokenSubscriptionUsageScopesToTheCallerAndTheTokenGroup(t *testing.T) {
	setupSubscriptionUsageDB(t)
	require.NoError(t, i18n.Init())
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":10}}`))
	}))
	t.Cleanup(upstream.Close)
	first := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	second := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	user := newSubscriptionUsageTokenUser(t, "two-bindings")
	other := newSubscriptionUsageTokenUser(t, "someone-else")
	unbound := newSubscriptionUsageTokenUser(t, "no-binding")
	newSubscriptionUsageSub(t, user.Id, first.Id, time.Hour)
	secondSub := newSubscriptionUsageSub(t, user.Id, second.Id, 2*time.Hour)
	foreign := newSubscriptionUsageSub(t, other.Id, first.Id, time.Hour)
	newSubscriptionUsageSub(t, unbound.Id, 0, time.Hour)
	newSubscriptionUsageToken(t, user.Id, "defaultkey", "", common.TokenStatusEnabled)
	newSubscriptionUsageToken(t, user.Id, "dedicatedkey", secondSub.BoundGroup, common.TokenStatusEnabled)
	newSubscriptionUsageToken(t, user.Id, "disabledkey", "", common.TokenStatusDisabled)
	newSubscriptionUsageToken(t, unbound.Id, "unboundkey", "", common.TokenStatusEnabled)
	router := tokenSubscriptionUsageRouter(t)

	_, envelope := getTokenSubscriptionUsage(t, router, "defaultkey", "")
	require.True(t, envelope.Success, envelope.Message)
	items := tokenUsageItems(t, envelope)
	require.Len(t, items, 2, "every bound subscription, told apart by id")
	assert.Equal(t, "Claude 5小时 #"+itoa(secondSub.Id), items[0]["planName"], "newest end time first")

	_, envelope = getTokenSubscriptionUsage(t, router, "dedicatedkey", "")
	require.True(t, envelope.Success, envelope.Message)
	items = tokenUsageItems(t, envelope)
	require.Len(t, items, 1, "a dedicated-group token sees its own binding")
	assert.Equal(t, "Claude 5小时", items[0]["planName"])
	subscriptions := envelope.Data["subscriptions"].([]any)
	assert.EqualValues(t, secondSub.Id, subscriptions[0].(map[string]any)["subscription_id"])

	hitsBefore := hits.Load()
	_, envelope = getTokenSubscriptionUsage(t, router, "defaultkey", "?subscription_id="+itoa(foreign.Id))
	assert.False(t, envelope.Success)
	assert.Equal(t, "Subscription not found", envelope.Message)
	assert.Nil(t, envelope.Data)

	_, envelope = getTokenSubscriptionUsage(t, router, "defaultkey", "?provider=codex")
	assert.True(t, envelope.Success)
	assert.Equal(t, "no bound subscription", envelope.Message)
	assert.Empty(t, tokenUsageItems(t, envelope))

	_, envelope = getTokenSubscriptionUsage(t, router, "unboundkey", "")
	assert.True(t, envelope.Success)
	assert.Equal(t, "no bound subscription", envelope.Message)
	assert.Empty(t, tokenUsageItems(t, envelope))

	code, envelope := getTokenSubscriptionUsage(t, router, "disabledkey", "")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.False(t, envelope.Success)
	assert.Equal(t, hitsBefore, hits.Load(), "rejected queries never reach upstream")
}

func TestTokenSubscriptionUsageReportsUpstreamFailureAndRetriesIt(t *testing.T) {
	setupSubscriptionUsageDB(t)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"account details that must not leak"}}`))
	}))
	t.Cleanup(upstream.Close)
	channel := newClaudeOAuthTestChannel(t, upstream.URL, service.ClaudeOAuthKey{AccessToken: "token", Scope: "user:profile"})
	user := newSubscriptionUsageTokenUser(t, "upstream-down")
	newSubscriptionUsageSub(t, user.Id, channel.Id, time.Hour)
	newSubscriptionUsageToken(t, user.Id, "downkey", "", common.TokenStatusEnabled)
	router := tokenSubscriptionUsageRouter(t)

	for range 2 {
		code, envelope := getTokenSubscriptionUsage(t, router, "downkey", "")
		assert.Equal(t, http.StatusOK, code)
		assert.False(t, envelope.Success)
		assert.Equal(t, "Upstream returned status 500", envelope.Message)
		assert.Empty(t, tokenUsageItems(t, envelope))
		assert.NotContains(t, fmt.Sprint(envelope.Data), "must not leak")
	}
	assert.Equal(t, int32(2), hits.Load(), "failures are not cached")
}
