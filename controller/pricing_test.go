package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pricingGuest      = 0
	pricingPlainUser  = 1201 // default group, no subscription binding
	pricingBoundUser  = 1202 // default group, bound to channel 42 (claude-ch42)
	pricingClaudeOpus = "zz-claude-opus"
	pricingClaudeFree = "zz-claude-unpriced"
	pricingShared     = "zz-shared"
	pricingHiddenOnly = "zz-hidden-only"
)

// setupPricingVisibilityTest mirrors production: Claude OAuth channels 39, 41
// and 42 serve Claude models only in their dedicated groups (the default-group
// ability is disabled), a shared channel serves default/vip/hidden, and only
// default and vip are usable by ordinary users.
func setupPricingVisibilityTest(t *testing.T) {
	t.Helper()
	withSelfUseModeDisabled(t)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.UserSubscription{}, &model.PerfMetric{}))

	originalUsable := setting.UserUsableGroups2JSONString()
	originalAuto := setting.AutoGroups2JsonString()
	originalGroupRatio := ratio_setting.GroupRatio2JSONString()
	originalModelRatio := ratio_setting.ModelRatio2JSONString()
	originalMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":0.5,"hidden":2,"oauth-ch39":1,"oauth-ch41":1,"claude-ch42":1}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"zz-claude-opus":2.5,"zz-shared":1,"zz-hidden-only":1}`))
	model.InvalidatePricingCache()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCache
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsable))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAuto))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatio))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalModelRatio))
		model.InvalidateSubscriptionRouteBindingCache(pricingPlainUser)
		model.InvalidateSubscriptionRouteBindingCache(pricingBoundUser)
		model.InvalidatePricingCache()
	})

	for _, id := range []int{pricingPlainUser, pricingBoundUser} {
		require.NoError(t, db.Create(&model.User{
			Id: id, Username: "pricing-user-" + stringInt(id), Password: "password",
			Group: "default", Status: common.UserStatusEnabled, AffCode: "pricing-" + stringInt(id),
		}).Error)
	}
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 39, Type: constant.ChannelTypeClaudeOAuth, Name: "claude-39", Key: "fake", Status: common.ChannelStatusEnabled, Group: "oauth-ch39"},
		{Id: 41, Type: constant.ChannelTypeClaudeOAuth, Name: "claude-41", Key: "fake", Status: common.ChannelStatusEnabled, Group: "oauth-ch41"},
		{Id: 42, Type: constant.ChannelTypeClaudeOAuth, Name: "claude-42", Key: "fake", Status: common.ChannelStatusEnabled, Group: "claude-ch42"},
		{Id: 50, Type: constant.ChannelTypeOpenAI, Name: "shared", Key: "fake", Status: common.ChannelStatusEnabled, Group: "default,vip,hidden"},
	}).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "oauth-ch39", Model: pricingClaudeOpus, ChannelId: 39, Enabled: true},
		{Group: "oauth-ch41", Model: pricingClaudeOpus, ChannelId: 41, Enabled: true},
		{Group: "claude-ch42", Model: pricingClaudeOpus, ChannelId: 42, Enabled: true},
		{Group: "default", Model: pricingClaudeOpus, ChannelId: 42, Enabled: false},
		{Group: "claude-ch42", Model: pricingClaudeFree, ChannelId: 42, Enabled: true},
		{Group: "default", Model: pricingShared, ChannelId: 50, Enabled: true},
		{Group: "vip", Model: pricingShared, ChannelId: 50, Enabled: true},
		{Group: "hidden", Model: pricingShared, ChannelId: 50, Enabled: true},
		{Group: "hidden", Model: pricingHiddenOnly, ChannelId: 50, Enabled: true},
	}).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSubscription{
		UserId: pricingBoundUser, PlanId: 1, Status: "active",
		StartTime: now - 60, EndTime: now + 3600,
		BoundChannelId: 42, BoundGroup: "claude-ch42",
	}).Error)
}

type pricingTestResponse struct {
	Success     bool               `json:"success"`
	Data        []pricingView      `json:"data"`
	GroupRatio  map[string]float64 `json:"group_ratio"`
	UsableGroup map[string]string  `json:"usable_group"`
	AutoGroups  []string           `json:"auto_groups"`
}

func requestPricing(t *testing.T, userID int) pricingTestResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	if userID > 0 {
		ctx.Set("id", userID)
	}
	GetPricing(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response pricingTestResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotContains(t, recorder.Body.String(), `"enable_groups":null`)
	return response
}

func TestGetPricingShowsDedicatedGroupModelsWithViewerGroupsOnly(t *testing.T) {
	setupPricingVisibilityTest(t)

	tests := []struct {
		name           string
		userID         int
		claudeGroups   []string
		sharedGroups   []string
		groupRatio     map[string]float64
		usableGroup    map[string]string
		autoGroups     []string
		unpricedForAll bool
	}{
		{
			name:         "guest",
			userID:       pricingGuest,
			claudeGroups: []string{},
			sharedGroups: []string{"default", "vip"},
			groupRatio:   map[string]float64{"default": 1, "vip": 0.5},
			usableGroup:  map[string]string{"default": "Default", "vip": "VIP"},
			autoGroups:   []string{"default", "vip"},
		},
		{
			name:         "user without binding",
			userID:       pricingPlainUser,
			claudeGroups: []string{},
			sharedGroups: []string{"default", "vip"},
			groupRatio:   map[string]float64{"default": 1, "vip": 0.5},
			usableGroup:  map[string]string{"default": "Default", "vip": "VIP"},
			autoGroups:   []string{"default", "vip"},
		},
		{
			name:         "user bound to claude-ch42",
			userID:       pricingBoundUser,
			claudeGroups: []string{"claude-ch42"},
			sharedGroups: []string{"default", "vip"},
			groupRatio:   map[string]float64{"default": 1, "vip": 0.5, "claude-ch42": 1},
			usableGroup: map[string]string{
				"default": "Default", "vip": "VIP",
				"claude-ch42": "Subscription account (bound channel models only)",
			},
			autoGroups: []string{"claude-ch42", "default", "vip"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requestPricing(t, test.userID)
			byName := map[string]pricingView{}
			for _, item := range response.Data {
				byName[item.ModelName] = item
			}

			require.Contains(t, byName, pricingClaudeOpus)
			require.Contains(t, byName, pricingClaudeFree)
			require.Contains(t, byName, pricingShared)
			assert.NotContains(t, byName, pricingHiddenOnly)

			claude := byName[pricingClaudeOpus]
			assert.ElementsMatch(t, test.claudeGroups, claude.EnableGroup)
			assert.Equal(t, 0, claude.QuotaType)
			assert.Equal(t, 2.5, claude.ModelRatio)
			assert.False(t, claude.PriceNotConfigured)
			assert.ElementsMatch(t, test.claudeGroups, byName[pricingClaudeFree].EnableGroup)
			assert.True(t, byName[pricingClaudeFree].PriceNotConfigured)
			assert.ElementsMatch(t, test.sharedGroups, byName[pricingShared].EnableGroup)

			assert.Equal(t, test.groupRatio, response.GroupRatio)
			assert.Equal(t, test.usableGroup, response.UsableGroup)
			assert.Equal(t, test.autoGroups, response.AutoGroups)
		})
	}

	// The cached pricing still lists every group: filtering is per viewer.
	cached := pricingByModelName(model.GetPricing())
	assert.ElementsMatch(t, []string{"oauth-ch39", "oauth-ch41", "claude-ch42"}, cached[pricingClaudeOpus].EnableGroup)
	assert.ElementsMatch(t, []string{"default", "vip", "hidden"}, cached[pricingShared].EnableGroup)
}

func TestGetPerfMetricsHidesGroupsTheViewerCannotUse(t *testing.T) {
	setupPricingVisibilityTest(t)
	now := time.Now().Unix()
	hour := now - now%3600
	for _, row := range []model.PerfMetric{
		{ModelName: pricingClaudeOpus, Group: "oauth-ch39", BucketTs: hour, RequestCount: 3, SuccessCount: 1, TotalLatencyMs: 3000},
		{ModelName: pricingClaudeOpus, Group: "claude-ch42", BucketTs: hour, RequestCount: 1, SuccessCount: 1, TotalLatencyMs: 1000},
	} {
		require.NoError(t, model.UpsertPerfMetric(&row))
	}

	request := func(userID int, group string) (string, map[string]any) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		target := "/api/perf-metrics?model=" + pricingClaudeOpus
		if group != "" {
			target += "&group=" + group
		}
		ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
		if userID > 0 {
			ctx.Set("id", userID)
		}
		GetPerfMetrics(ctx)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Success bool           `json:"success"`
			Data    map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		return recorder.Body.String(), response.Data
	}

	body, data := request(pricingGuest, "")
	assert.NotContains(t, body, "oauth-ch39")
	assert.NotContains(t, body, "claude-ch42")
	assert.Empty(t, data["groups"])
	require.NotNil(t, data["summary"])
	assert.Equal(t, 50.0, data["summary"].(map[string]any)["success_rate"])

	body, data = request(pricingBoundUser, "")
	assert.NotContains(t, body, "oauth-ch39")
	groups := data["groups"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, "claude-ch42", groups[0].(map[string]any)["group"])
	assert.Equal(t, 50.0, data["summary"].(map[string]any)["success_rate"])

	// Naming a group the viewer cannot use returns no data for it.
	body, data = request(pricingPlainUser, "oauth-ch39")
	assert.NotContains(t, body, `"group":"oauth-ch39"`)
	assert.Nil(t, data["summary"])
}
