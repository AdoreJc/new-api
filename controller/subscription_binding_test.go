package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.UserSubscription{}))
	model.DB = database
	t.Cleanup(func() { model.DB = originalDB })
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
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/self/usage/"+itoa(subID), nil)
	c.Params = channelParams(subID)
	c.Set("id", userID)
	GetSubscriptionSelfUsage(c)
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
