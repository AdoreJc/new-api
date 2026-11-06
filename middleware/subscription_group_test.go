package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Users of setupSubscriptionGroupTokenTest, all in the default group.
const (
	subscriptionGroupBoundUser     = 20 // active binding to channel 1 (claude-ch1)
	subscriptionGroupPlainUser     = 21 // no binding
	subscriptionGroupExpiredUser   = 22 // expired binding to channel 1
	subscriptionGroupCancelledUser = 23 // cancelled binding to channel 1
	subscriptionGroupLegacyUser    = 24 // active binding to channel 5 under the legacy oauth-ch5 name
)

// setupSubscriptionGroupTokenTest backs TokenAuth and Distribute with a SQLite
// database: channel 1 is a bound OAuth account in claude-ch1, channel 5 one in
// the legacy oauth-ch5, channel 2 the shared default pool. Every user owns one
// token per entry of tokenGroups.
func setupSubscriptionGroupTokenTest(t *testing.T, tokenGroups ...string) {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	previousMaster, previousMemory, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.SQLitePath
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	previousRatios := ratio_setting.GroupRatio2JSONString()
	users := []int{subscriptionGroupBoundUser, subscriptionGroupPlainUser, subscriptionGroupExpiredUser, subscriptionGroupCancelledUser, subscriptionGroupLegacyUser}

	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, true, false
	common.SQLitePath = filepath.Join(t.TempDir(), "subscription-group.db")
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","auto":"Auto"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2,"claude-ch1":1,"oauth-ch5":1}`))
	t.Cleanup(func() {
		for _, userId := range users {
			model.InvalidateSubscriptionRouteBindingCache(userId)
		}
		model.DB = previousDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.RedisEnabled = previousRedis
		common.IsMasterNode, common.MemoryCacheEnabled, common.SQLitePath = previousMaster, previousMemory, previousSQLite
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
		require.NoError(t, sqlDB.Close())
	})

	channels := []struct {
		id          int
		channelType int
		group       string
		models      string
	}{
		{1, constant.ChannelTypeClaudeOAuth, "claude-ch1", "claude-x"},
		{2, constant.ChannelTypeOpenAI, "default", "gpt-x"},
		{5, constant.ChannelTypeClaudeOAuth, "oauth-ch5", "claude-z"},
	}
	for _, ch := range channels {
		priority := int64(0)
		weight := uint(100)
		require.NoError(t, db.Create(&model.Channel{
			Id: ch.id, Type: ch.channelType, Key: fmt.Sprintf("key-%d", ch.id), Status: common.ChannelStatusEnabled,
			Name: fmt.Sprintf("channel-%d", ch.id), Weight: &weight, Models: ch.models, Group: ch.group, Priority: &priority,
		}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: ch.group, Model: ch.models, ChannelId: ch.id, Enabled: true, Priority: &priority, Weight: weight}).Error)
	}
	now := time.Now().Unix()
	subscriptions := []model.UserSubscription{
		{UserId: subscriptionGroupBoundUser, PlanId: 1, Status: "active", StartTime: now - 60, EndTime: now + 3600, BoundChannelId: 1, BoundGroup: "claude-ch1"},
		{UserId: subscriptionGroupExpiredUser, PlanId: 1, Status: "active", StartTime: now - 7200, EndTime: now - 60, BoundChannelId: 1, BoundGroup: "claude-ch1"},
		{UserId: subscriptionGroupCancelledUser, PlanId: 1, Status: "cancelled", StartTime: now - 60, EndTime: now + 3600, BoundChannelId: 1, BoundGroup: "claude-ch1"},
		{UserId: subscriptionGroupLegacyUser, PlanId: 1, Status: "active", StartTime: now - 60, EndTime: now + 3600, BoundChannelId: 5, BoundGroup: "oauth-ch5"},
	}
	for i := range subscriptions {
		require.NoError(t, db.Create(&subscriptions[i]).Error)
	}
	for _, userId := range users {
		require.NoError(t, db.Create(&model.User{
			Id: userId, Username: fmt.Sprintf("subscription-group-%d", userId), Status: common.UserStatusEnabled,
			Group: "default", Quota: 1000, AuthVersion: 1, AffCode: fmt.Sprintf("subscription-group-%d", userId),
		}).Error)
		for _, group := range tokenGroups {
			require.NoError(t, db.Create(&model.Token{
				UserId: userId, Name: group, Key: subscriptionGroupTokenKey(userId, group), Status: common.TokenStatusEnabled,
				ExpiredTime: -1, UnlimitedQuota: true, Group: group,
			}).Error)
		}
		model.InvalidateSubscriptionRouteBindingCache(userId)
	}
	model.InitChannelCache()
}

func subscriptionGroupTokenKey(userId int, group string) string {
	// TokenAuth splits the key at "-", so the key itself carries none.
	return fmt.Sprintf("u%dg%s", userId, strings.ReplaceAll(group, "-", ""))
}

// serveSubscriptionGroupTokenRequest sends a chat request with the user's token
// for tokenGroup through TokenAuth and Distribute, and returns the response
// with the group and channel the request ended up on.
func serveSubscriptionGroupTokenRequest(t *testing.T, userId int, tokenGroup string, modelName string) (*httptest.ResponseRecorder, string, int) {
	t.Helper()
	result := serveSubscriptionGroupAutoRequest(t, userId, tokenGroup, modelName)
	return result.recorder, result.group, result.channel
}

type subscriptionGroupAutoResult struct {
	recorder       *httptest.ResponseRecorder
	group          string
	autoGroup      string
	autoGroupIndex int
	channel        int
}

// serveSubscriptionGroupAutoRequest is serveSubscriptionGroupTokenRequest that
// also reports where in its Auto list an Auto token's request was served.
func serveSubscriptionGroupAutoRequest(t *testing.T, userId int, tokenGroup string, modelName string) subscriptionGroupAutoResult {
	t.Helper()
	var result subscriptionGroupAutoResult
	router := gin.New()
	router.POST("/v1/chat/completions", RequestId(), TokenAuth(), Distribute(), func(c *gin.Context) {
		result.group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
		result.autoGroup = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
		result.autoGroupIndex = common.GetContextKeyInt(c, constant.ContextKeyAutoGroupIndex)
		result.channel = common.GetContextKeyInt(c, constant.ContextKeyChannelId)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q}`, modelName)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Language", "en")
	request.Header.Set("Authorization", "Bearer sk-"+subscriptionGroupTokenKey(userId, tokenGroup))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	result.recorder = recorder
	return result
}

func TestGetUserUsableGroupsForUserAddsOnlyTheOwnersActiveBindingGroup(t *testing.T) {
	setupSubscriptionGroupTokenTest(t)

	shared := map[string]string{"default": "Default", "vip": "VIP", "auto": "Auto"}
	withGroup := func(group string) map[string]string {
		groups := map[string]string{group: service.SubscriptionBoundGroupDescription}
		for name, desc := range shared {
			groups[name] = desc
		}
		return groups
	}
	cases := []struct {
		name   string
		userId int
		want   map[string]string
	}{
		{"active binding", subscriptionGroupBoundUser, withGroup("claude-ch1")},
		{"legacy oauth-ch binding", subscriptionGroupLegacyUser, withGroup("oauth-ch5")},
		{"no binding", subscriptionGroupPlainUser, shared},
		{"expired binding", subscriptionGroupExpiredUser, shared},
		{"cancelled binding", subscriptionGroupCancelledUser, shared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, service.GetUserUsableGroupsForUser(tc.userId, "default"))
		})
	}
}

func TestSubscriptionGroupLeadsTheOwnersAutoGroups(t *testing.T) {
	setupSubscriptionGroupTokenTest(t)

	cases := []struct {
		name   string
		userId int
		want   []string
	}{
		{"active binding", subscriptionGroupBoundUser, []string{"claude-ch1", "default", "vip"}},
		{"legacy oauth-ch binding", subscriptionGroupLegacyUser, []string{"oauth-ch5", "default", "vip"}},
		{"no binding", subscriptionGroupPlainUser, []string{"default", "vip"}},
		{"expired binding", subscriptionGroupExpiredUser, []string{"default", "vip"}},
		{"cancelled binding", subscriptionGroupCancelledUser, []string{"default", "vip"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, service.GetUserAutoGroupForUser(tc.userId, "default"))

			inherit := newTokenAutoGroupsContext()
			inherit.Set("id", tc.userId)
			assert.Equal(t, tc.want, service.GetRequestAutoGroups(inherit, "default"), "global Auto list")
		})
	}

	snapshot := newTokenAutoGroupsContext()
	snapshot.Set("id", subscriptionGroupBoundUser)
	common.SetContextKey(snapshot, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	assert.Equal(t, []string{"claude-ch1", "vip", "default"}, service.GetRequestAutoGroups(snapshot, "default"), "token snapshot")
}

func TestAutoTokenTriesTheSubscriptionGroupFirstAndSkipsItForOtherModels(t *testing.T) {
	setupSubscriptionGroupTokenTest(t, "auto")

	bound := serveSubscriptionGroupAutoRequest(t, subscriptionGroupBoundUser, "auto", "claude-x")
	require.Equal(t, http.StatusNoContent, bound.recorder.Code, bound.recorder.Body.String())
	assert.Equal(t, "claude-ch1", bound.group, "a bound model is served from the dedicated group")
	assert.Equal(t, 1, bound.channel)

	other := serveSubscriptionGroupAutoRequest(t, subscriptionGroupBoundUser, "auto", "gpt-x")
	require.Equal(t, http.StatusNoContent, other.recorder.Code, other.recorder.Body.String())
	assert.Equal(t, "auto", other.group)
	assert.Equal(t, "default", other.autoGroup, "the dedicated group has no channel for the model, so Auto moves on")
	assert.Equal(t, 1, other.autoGroupIndex, "Auto tried the dedicated group at index 0 first")
	assert.Equal(t, 2, other.channel)

	// Once the binding ends the dedicated group leaves the Auto list.
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", subscriptionGroupBoundUser).
		Updates(map[string]any{"bound_channel_id": 0, "bound_group": ""}).Error)
	model.InvalidateSubscriptionRouteBindingCache(subscriptionGroupBoundUser)
	assert.Equal(t, []string{"default", "vip"}, service.GetUserAutoGroupForUser(subscriptionGroupBoundUser, "default"))
	unbound := serveSubscriptionGroupAutoRequest(t, subscriptionGroupBoundUser, "auto", "gpt-x")
	require.Equal(t, http.StatusNoContent, unbound.recorder.Code, unbound.recorder.Body.String())
	assert.Equal(t, "default", unbound.autoGroup)
	assert.Equal(t, 0, unbound.autoGroupIndex)
	assert.Equal(t, 2, unbound.channel)
}

func TestTokenAuthAcceptsTokensInTheOwnersSubscriptionGroup(t *testing.T) {
	setupSubscriptionGroupTokenTest(t, "claude-ch1", "oauth-ch5", "svip")

	cases := []struct {
		name        string
		userId      int
		tokenGroup  string
		modelName   string
		wantStatus  int
		wantGroup   string
		wantChannel int
		wantMessage string
	}{
		{"own dedicated group, bound model", subscriptionGroupBoundUser, "claude-ch1", "claude-x", http.StatusNoContent, "claude-ch1", 1, ""},
		{"legacy oauth-ch group, bound model", subscriptionGroupLegacyUser, "oauth-ch5", "claude-z", http.StatusNoContent, "oauth-ch5", 5, ""},
		{"own dedicated group, model not on the bound channel", subscriptionGroupBoundUser, "claude-ch1", "gpt-x", http.StatusServiceUnavailable, "", 0, "only serves models supported by the bound subscription account"},
		{"expired binding", subscriptionGroupExpiredUser, "claude-ch1", "claude-x", http.StatusForbidden, "", 0, "is no longer available"},
		{"cancelled binding", subscriptionGroupCancelledUser, "claude-ch1", "claude-x", http.StatusForbidden, "", 0, "is no longer available"},
		{"another user's dedicated group", subscriptionGroupPlainUser, "claude-ch1", "claude-x", http.StatusForbidden, "", 0, "is no longer available"},
		{"ordinary group outside the usable groups", subscriptionGroupBoundUser, "svip", "claude-x", http.StatusForbidden, "", 0, "No permission to access group svip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder, gotGroup, gotChannel := serveSubscriptionGroupTokenRequest(t, tc.userId, tc.tokenGroup, tc.modelName)
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			assert.Equal(t, tc.wantGroup, gotGroup)
			assert.Equal(t, tc.wantChannel, gotChannel)
			if tc.wantMessage != "" {
				assert.Contains(t, recorder.Body.String(), tc.wantMessage)
				assert.Contains(t, recorder.Body.String(), tc.tokenGroup)
			}
		})
	}
}

func TestTokenAuthRejectsTheSubscriptionGroupOnceTheBindingIsRemoved(t *testing.T) {
	setupSubscriptionGroupTokenTest(t, "claude-ch1")

	recorder, group, channel := serveSubscriptionGroupTokenRequest(t, subscriptionGroupBoundUser, "claude-ch1", "claude-x")
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.Equal(t, "claude-ch1", group)
	assert.Equal(t, 1, channel)

	// Unbinding clears the binding columns and then drops the cached binding.
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", subscriptionGroupBoundUser).
		Updates(map[string]any{"bound_channel_id": 0, "bound_group": ""}).Error)
	model.InvalidateSubscriptionRouteBindingCache(subscriptionGroupBoundUser)

	recorder, _, _ = serveSubscriptionGroupTokenRequest(t, subscriptionGroupBoundUser, "claude-ch1", "claude-x")
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "The subscription dedicated group claude-ch1 of this token is no longer available")
	assert.NotContains(t, service.GetUserUsableGroupsForUser(subscriptionGroupBoundUser, "default"), "claude-ch1")
}
