package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type bindingFixture struct {
	user    User
	plan    SubscriptionPlan
	channel Channel
}

func newBindingFixture(t *testing.T, planUpgradeGroup string, channelGroup string) bindingFixture {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM options") })
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMapRWMutex.Unlock()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"pro":2}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios)) })

	f := bindingFixture{
		user: User{Username: "binding-user", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "bu1"},
		plan: SubscriptionPlan{Title: "Dedicated", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 0, UpgradeGroup: planUpgradeGroup, Enabled: true},
		channel: Channel{
			Type:   constant.ChannelTypeClaudeOAuth,
			Key:    `{"access_token":"at"}`,
			Name:   "claude-account-1",
			Status: common.ChannelStatusEnabled,
			Models: "claude-sonnet-4-5",
			Group:  channelGroup,
		},
	}
	require.NoError(t, DB.Create(&f.user).Error)
	require.NoError(t, DB.Create(&f.plan).Error)
	require.NoError(t, f.channel.Insert())
	return f
}

func userGroupOf(t *testing.T, userId int) string {
	t.Helper()
	var user User
	require.NoError(t, DB.First(&user, userId).Error)
	return user.Group
}

func channelGroupOf(t *testing.T, channelId int) string {
	t.Helper()
	var channel Channel
	require.NoError(t, DB.First(&channel, channelId).Error)
	return channel.Group
}

func abilityGroupsOf(t *testing.T, channelId int) []string {
	t.Helper()
	var groups []string
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", channelId).Order(commonGroupCol).Pluck(commonGroupCol, &groups).Error)
	return groups
}

func subscriptionOf(t *testing.T, id int) UserSubscription {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.First(&sub, id).Error)
	return sub
}

func createActiveSubscription(t *testing.T, f bindingFixture) *UserSubscription {
	t.Helper()
	var sub *UserSubscription
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		sub, err = CreateUserSubscriptionFromPlanTx(tx, f.user.Id, &f.plan, "admin")
		return err
	}))
	require.NoError(t, RefreshUserGroupCache(f.user.Id))
	return sub
}

func TestBindSubscriptionChannelMovesChannelIntoDedicatedGroupAndKeepsUserGroup(t *testing.T) {
	f := newBindingFixture(t, "", "default,vip")
	sub := createActiveSubscription(t, f)
	dedicated := "claude-ch" + strconv.Itoa(f.channel.Id)

	notice, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, dedicated, notice.Params["group"])
	assert.Contains(t, notice.Error(), "routed to the dedicated group")

	stored := subscriptionOf(t, sub.Id)
	assert.Equal(t, f.channel.Id, stored.BoundChannelId)
	assert.Equal(t, dedicated, stored.BoundGroup)
	assert.Equal(t, "", stored.UpgradeGroup, "binding no longer elevates the user")
	assert.Equal(t, "", stored.PrevUserGroup)
	assert.Equal(t, "default,vip", stored.BoundChannelPrevGroup)
	assert.Equal(t, "", stored.BoundPrevUpgradeGroup)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id), "the user stays in their own group")
	assert.Equal(t, dedicated, channelGroupOf(t, f.channel.Id))
	assert.Equal(t, []string{dedicated}, abilityGroupsOf(t, f.channel.Id))
	assert.True(t, ratio_setting.ContainsGroupRatio(dedicated))
	assert.InDelta(t, 1, ratio_setting.GetGroupRatio(dedicated), 0.0001)

	channels, err := ListSubscriptionBindableChannels()
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, sub.Id, channels[0].BoundSubscriptionId)
	assert.Equal(t, "binding-user", channels[0].BoundUsername)

	summaries, err := GetAllUserSubscriptions(f.user.Id)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, constant.ChannelTypeClaudeOAuth, summaries[0].Subscription.BoundChannelType)
	assert.Equal(t, "claude-account-1", summaries[0].Subscription.BoundChannelName)
}

func TestBindSubscriptionChannelRejectsChannelBoundElsewhereAndInactiveSubscriptions(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	first := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(first.Id, f.channel.Id)
	require.NoError(t, err)

	other := User{Username: "binding-user-2", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "bu2"}
	require.NoError(t, DB.Create(&other).Error)
	second := UserSubscription{UserId: other.Id, PlanId: f.plan.Id, Status: "active", StartTime: time.Now().Unix(), EndTime: time.Now().Unix() + 3600}
	require.NoError(t, DB.Create(&second).Error)
	_, err = AdminBindSubscriptionChannel(second.Id, f.channel.Id)
	require.EqualError(t, err, "This channel is already bound to another active subscription")
	assert.Equal(t, "default", userGroupOf(t, other.Id))

	expired := UserSubscription{UserId: other.Id, PlanId: f.plan.Id, Status: "active", StartTime: time.Now().Unix() - 7200, EndTime: time.Now().Unix() - 3600}
	require.NoError(t, DB.Create(&expired).Error)
	apiKey := Channel{Type: constant.ChannelTypeOpenAI, Key: "sk-x", Name: "openai", Status: common.ChannelStatusEnabled, Models: "gpt-4o", Group: "default"}
	require.NoError(t, apiKey.Insert())
	_, err = AdminBindSubscriptionChannel(expired.Id, apiKey.Id)
	require.EqualError(t, err, "Only an active subscription can be bound to a channel")
	_, err = AdminBindSubscriptionChannel(second.Id, apiKey.Id)
	require.EqualError(t, err, "Only Claude Subscription (OAuth) and ChatGPT Subscription (Codex) channels can be bound")
}

func TestRebindSubscriptionChannelReleasesPreviousChannel(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	sub := createActiveSubscription(t, f)
	codex := Channel{Type: constant.ChannelTypeCodex, Key: `{"access_token":"at","account_id":"acc"}`, Name: "codex-account", Status: common.ChannelStatusEnabled, Models: "gpt-5-codex", Group: "vip"}
	require.NoError(t, codex.Insert())
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)

	_, err = AdminBindSubscriptionChannel(sub.Id, codex.Id)
	require.NoError(t, err)

	stored := subscriptionOf(t, sub.Id)
	assert.Equal(t, codex.Id, stored.BoundChannelId)
	assert.Equal(t, "vip", stored.BoundChannelPrevGroup)
	codexGroup := "codex-ch" + strconv.Itoa(codex.Id)
	assert.Equal(t, codexGroup, stored.BoundGroup)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	routed, ok := GetUserSubscriptionRouteBinding(f.user.Id, time.Now().Unix())
	require.True(t, ok)
	assert.Equal(t, codex.Id, routed.ChannelId, "routing follows the new channel at once")
	assert.Equal(t, codexGroup, routed.Group)
	assert.Equal(t, codexGroup, channelGroupOf(t, codex.Id))
	assert.Equal(t, "default", channelGroupOf(t, f.channel.Id), "previous channel returns to its original group")
	assert.Equal(t, []string{"default"}, abilityGroupsOf(t, f.channel.Id))
}

func TestUnbindSubscriptionChannelKeepsPlanUpgradeGroup(t *testing.T) {
	f := newBindingFixture(t, "pro", "default")
	sub := createActiveSubscription(t, f)
	require.Equal(t, "pro", userGroupOf(t, f.user.Id))
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	bound := subscriptionOf(t, sub.Id)
	assert.Equal(t, "pro", bound.UpgradeGroup, "the plan's elevation is untouched by the binding")
	assert.Equal(t, "default", bound.PrevUserGroup)
	assert.Equal(t, "pro", userGroupOf(t, f.user.Id))
	assert.InDelta(t, 2, ratio_setting.GetGroupRatio(SubscriptionBoundGroupName(f.channel.Type, f.channel.Id)), 0.0001, "ratio copied from the user's group at bind time")

	notice, err := AdminUnbindSubscriptionChannel(sub.Id)
	require.NoError(t, err)
	assert.Nil(t, notice, "nothing about the user changes")

	stored := subscriptionOf(t, sub.Id)
	assert.Equal(t, 0, stored.BoundChannelId)
	assert.Equal(t, "", stored.BoundGroup)
	assert.Equal(t, "pro", stored.UpgradeGroup)
	assert.Equal(t, "default", stored.PrevUserGroup)
	assert.Equal(t, "pro", userGroupOf(t, f.user.Id))
	assert.Equal(t, "default", channelGroupOf(t, f.channel.Id))

	_, err = AdminUnbindSubscriptionChannel(sub.Id)
	require.EqualError(t, err, "This subscription is not bound to a channel")

	// Expiry after the unbind follows the plan's own upgrade/downgrade snapshot.
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("end_time", time.Now().Unix()-1).Error)
	_, err = ExpireDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
}

func TestUnbindSubscriptionChannelWithoutPlanUpgradeGroupLeavesUserAlone(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	sub := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)

	_, err = AdminUnbindSubscriptionChannel(sub.Id)
	require.NoError(t, err)
	stored := subscriptionOf(t, sub.Id)
	assert.Equal(t, "", stored.UpgradeGroup)
	assert.Equal(t, "", stored.PrevUserGroup)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
}

func TestInvalidateBoundSubscriptionReleasesChannelAndKeepsUserGroup(t *testing.T) {
	f := newBindingFixture(t, "", "default,vip")
	sub := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)

	notice, err := AdminInvalidateUserSubscription(sub.Id)
	require.NoError(t, err)
	assert.Nil(t, notice, "the user was never moved, so nothing is reverted")
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	assert.Equal(t, "default,vip", channelGroupOf(t, f.channel.Id))
	stored := subscriptionOf(t, sub.Id)
	assert.Equal(t, "cancelled", stored.Status)
	assert.Equal(t, 0, stored.BoundChannelId)
}

func TestDeleteBoundSubscriptionReleasesChannel(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	sub := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)

	_, err = AdminDeleteUserSubscription(sub.Id)
	require.NoError(t, err)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	assert.Equal(t, "default", channelGroupOf(t, f.channel.Id))
}

func TestExpiredBoundSubscriptionRevertsUserButKeepsChannelParkedUntilRebound(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	sub := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	dedicated := SubscriptionBoundGroupName(f.channel.Type, f.channel.Id)

	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("end_time", time.Now().Unix()-1).Error)
	expired, err := ExpireDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, 1, expired)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	assert.Equal(t, dedicated, channelGroupOf(t, f.channel.Id), "the account stays private until an administrator acts")
	assert.Equal(t, f.channel.Id, subscriptionOf(t, sub.Id).BoundChannelId)
	_, ok := GetUserSubscriptionRouteBinding(f.user.Id, time.Now().Unix())
	assert.False(t, ok, "an expired binding routes nothing")

	channels, err := ListSubscriptionBindableChannels()
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, 0, channels[0].BoundSubscriptionId, "an ended subscription does not count as a binding")

	// Rebinding the parked channel to a renewal keeps the original return
	// group and detaches the ended subscription from the channel.
	renewal := createActiveSubscription(t, f)
	_, err = AdminBindSubscriptionChannel(renewal.Id, f.channel.Id)
	require.NoError(t, err)
	assert.Equal(t, "default", subscriptionOf(t, renewal.Id).BoundChannelPrevGroup)
	assert.Equal(t, 0, subscriptionOf(t, sub.Id).BoundChannelId)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	routed, ok := GetUserSubscriptionRouteBinding(f.user.Id, time.Now().Unix())
	require.True(t, ok)
	assert.Equal(t, renewal.Id, routed.SubscriptionId)

	_, err = AdminUnbindSubscriptionChannel(renewal.Id)
	require.NoError(t, err)
	assert.Equal(t, "default", channelGroupOf(t, f.channel.Id))
}

func TestCreateUserSubscriptionWithChannelBindsInOneStep(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	dedicated := SubscriptionBoundGroupName(f.channel.Type, f.channel.Id)

	notice, err := AdminCreateUserSubscriptionWithChannel(f.user.Id, f.plan.Id, f.channel.Id)
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, dedicated, notice.Params["group"])
	subs, err := GetAllActiveUserSubscriptions(f.user.Id)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, f.channel.Id, subs[0].Subscription.BoundChannelId)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))

	// A failing bind rolls the subscription back as well.
	otherUser := User{Username: "binding-user-3", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "bu3"}
	require.NoError(t, DB.Create(&otherUser).Error)
	_, err = AdminCreateUserSubscriptionWithChannel(otherUser.Id, f.plan.Id, f.channel.Id)
	require.Error(t, err)
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", otherUser.Id).Count(&count).Error)
	assert.EqualValues(t, 0, count)
}

func TestSubscriptionBoundGroupNames(t *testing.T) {
	assert.Equal(t, "claude-ch12", SubscriptionBoundGroupName(constant.ChannelTypeClaudeOAuth, 12))
	assert.Equal(t, "codex-ch12", SubscriptionBoundGroupName(constant.ChannelTypeCodex, 12))
	assert.Equal(t, "", SubscriptionBoundGroupName(constant.ChannelTypeOpenAI, 12), "unbindable types get no group")
	assert.Equal(t, "", SubscriptionBoundGroupName(constant.ChannelTypeAnthropic, 12), "unbindable types get no group")

	cases := map[string]bool{
		"claude-ch12":  true,
		"codex-ch12":   true,
		"oauth-ch12":   true, // legacy bindings keep their original name
		"claude-ch":    false,
		"codex-ch":     false,
		"oauth-ch":     false,
		"claude-chx":   false,
		"codex-ch12a":  false,
		"oauth-ch-1":   false,
		"claude-ch 12": false,
		"xclaude-ch12": false,
		"default":      false,
		"":             false,
	}
	for group, want := range cases {
		assert.Equal(t, want, IsSubscriptionBoundGroup(group), group)
	}
}

// bindLegacySubscriptionChannel reproduces a binding created before groups
// were named by channel type: everything points at oauth-ch<id>.
func bindLegacySubscriptionChannel(t *testing.T, f bindingFixture, sub *UserSubscription) string {
	t.Helper()
	legacy := "oauth-ch" + strconv.Itoa(f.channel.Id)
	require.NoError(t, ensureSubscriptionBoundGroupRatio(legacy, "default"))
	prevChannelGroup := f.channel.Group
	require.NoError(t, setChannelGroupTx(DB, f.channel.Id, legacy))
	require.NoError(t, DB.Model(&User{}).Where("id = ?", f.user.Id).Update("group", legacy).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
		"upgrade_group":            legacy,
		"prev_user_group":          "default",
		"bound_channel_id":         f.channel.Id,
		"bound_group":              legacy,
		"bound_channel_prev_group": prevChannelGroup,
		"bound_prev_upgrade_group": "",
	}).Error)
	require.NoError(t, RefreshUserGroupCache(f.user.Id))
	return legacy
}

func TestLegacyOAuthChBindingKeepsWorkingUnderItsStoredGroup(t *testing.T) {
	t.Run("routing recognises the stored legacy group", func(t *testing.T) {
		f := newBindingFixture(t, "", "default,vip")
		sub := createActiveSubscription(t, f)
		legacy := bindLegacySubscriptionChannel(t, f, sub)
		require.NoError(t, MigrateSubscriptionBoundUserGroups())

		routed, ok := GetUserSubscriptionRouteBinding(f.user.Id, time.Now().Unix())
		require.True(t, ok)
		assert.Equal(t, legacy, routed.Group, "the stored group is used, not re-derived")
		resolved, ok := ResolveSubscriptionRoute(f.user.Id, "claude-sonnet-4-5", time.Now().Unix())
		require.True(t, ok)
		assert.Equal(t, legacy, resolved.Group)
	})

	t.Run("unbind of an unmigrated row restores the plan snapshot and the channel", func(t *testing.T) {
		f := newBindingFixture(t, "", "default,vip")
		sub := createActiveSubscription(t, f)
		legacy := bindLegacySubscriptionChannel(t, f, sub)
		require.Equal(t, []string{legacy}, abilityGroupsOf(t, f.channel.Id))

		notice, err := AdminUnbindSubscriptionChannel(sub.Id)
		require.NoError(t, err)
		assert.Nil(t, notice)
		assert.Equal(t, legacy, userGroupOf(t, f.user.Id), "unbind never moves the user; the startup migration does")
		assert.Equal(t, "default,vip", channelGroupOf(t, f.channel.Id))
		assert.Equal(t, []string{"default", "vip"}, abilityGroupsOf(t, f.channel.Id))
		stored := subscriptionOf(t, sub.Id)
		assert.Equal(t, 0, stored.BoundChannelId)
		assert.Equal(t, "", stored.BoundGroup)
		assert.Equal(t, "", stored.UpgradeGroup)
	})

	t.Run("invalidate releases the channel", func(t *testing.T) {
		f := newBindingFixture(t, "", "default")
		sub := createActiveSubscription(t, f)
		bindLegacySubscriptionChannel(t, f, sub)

		_, err := AdminInvalidateUserSubscription(sub.Id)
		require.NoError(t, err)
		assert.Equal(t, "default", userGroupOf(t, f.user.Id))
		assert.Equal(t, "default", channelGroupOf(t, f.channel.Id))
	})

	t.Run("a parked channel rebinds under the typed name and keeps its origin", func(t *testing.T) {
		f := newBindingFixture(t, "", "default,vip")
		sub := createActiveSubscription(t, f)
		legacy := bindLegacySubscriptionChannel(t, f, sub)
		require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("end_time", time.Now().Unix()-1).Error)
		_, err := ExpireDueSubscriptions(10)
		require.NoError(t, err)
		require.Equal(t, "default", userGroupOf(t, f.user.Id), "the unmigrated upgrade snapshot still reverts the user on expiry")
		require.Equal(t, legacy, channelGroupOf(t, f.channel.Id))

		renewal := createActiveSubscription(t, f)
		_, err = AdminBindSubscriptionChannel(renewal.Id, f.channel.Id)
		require.NoError(t, err)
		typed := "claude-ch" + strconv.Itoa(f.channel.Id)
		stored := subscriptionOf(t, renewal.Id)
		assert.Equal(t, typed, stored.BoundGroup)
		assert.Equal(t, "default,vip", stored.BoundChannelPrevGroup, "the legacy parking group is not mistaken for the origin")
		assert.Equal(t, "default", userGroupOf(t, f.user.Id))
		assert.Equal(t, typed, channelGroupOf(t, f.channel.Id))
	})
}

func TestSubscriptionRouteBindingCacheFollowsBindingLifecycle(t *testing.T) {
	f := newBindingFixture(t, "", "default,vip")
	sub := createActiveSubscription(t, f)
	now := time.Now().Unix()
	InvalidateSubscriptionRouteBindingCache(f.user.Id)

	_, ok := GetUserSubscriptionRouteBinding(f.user.Id, now)
	assert.False(t, ok, "no binding yet")
	_, ok = ResolveSubscriptionRoute(f.user.Id, "claude-sonnet-4-5", now)
	assert.False(t, ok)

	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	dedicated := SubscriptionBoundGroupName(f.channel.Type, f.channel.Id)
	resolved, ok := ResolveSubscriptionRoute(f.user.Id, "claude-sonnet-4-5", now)
	require.True(t, ok, "the bind invalidated the cached miss")
	assert.Equal(t, dedicated, resolved.Group)
	assert.Equal(t, sub.Id, resolved.SubscriptionId)
	_, ok = ResolveSubscriptionRoute(f.user.Id, "gpt-4o", now)
	assert.False(t, ok, "models the bound channel does not serve keep their group")
	_, ok = ResolveSubscriptionRoute(f.user.Id, "", now)
	assert.False(t, ok)

	// The cache answers until it is invalidated: a row edited behind its back
	// keeps routing, an unbind through the model stops it immediately.
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("status", "cancelled").Error)
	_, ok = GetUserSubscriptionRouteBinding(f.user.Id, now)
	assert.True(t, ok, "served from the cache")
	InvalidateSubscriptionRouteBindingCache(f.user.Id)
	_, ok = GetUserSubscriptionRouteBinding(f.user.Id, now)
	assert.False(t, ok)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("status", "active").Error)
	InvalidateSubscriptionRouteBindingCache(f.user.Id)
	routed, ok := GetUserSubscriptionRouteBinding(f.user.Id, now)
	require.True(t, ok)
	_, ok = GetUserSubscriptionRouteBinding(f.user.Id, routed.EndTime)
	assert.False(t, ok, "the stored end time ends routing without a cache refresh")

	_, err = AdminUnbindSubscriptionChannel(sub.Id)
	require.NoError(t, err)
	_, ok = GetUserSubscriptionRouteBinding(f.user.Id, now)
	assert.False(t, ok)

	_, err = AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	_, ok = GetUserSubscriptionRouteBinding(f.user.Id, now)
	require.True(t, ok)
	_, err = AdminInvalidateUserSubscription(sub.Id)
	require.NoError(t, err)
	_, ok = GetUserSubscriptionRouteBinding(f.user.Id, now)
	assert.False(t, ok, "invalidate drops the cached binding")
}

func TestSubscriptionBoundChannelServesModelCachesAbilityLookups(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	sub := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	dedicated := SubscriptionBoundGroupName(f.channel.Type, f.channel.Id)
	clearSubscriptionRouteModelCache()

	assert.True(t, subscriptionBoundChannelServesModel(dedicated, "claude-sonnet-4-5", f.channel.Id))
	assert.False(t, subscriptionBoundChannelServesModel(dedicated, "gpt-4o", f.channel.Id))

	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", f.channel.Id).Update("enabled", false).Error)
	assert.True(t, subscriptionBoundChannelServesModel(dedicated, "claude-sonnet-4-5", f.channel.Id), "answered from the cache")
	InitChannelCache()
	assert.False(t, subscriptionBoundChannelServesModel(dedicated, "claude-sonnet-4-5", f.channel.Id), "the channel cache rebuild drops the entry")
}

func TestMigrateSubscriptionBoundUserGroupsRestoresUsersIdempotently(t *testing.T) {
	t.Run("plan without upgrade group returns to the previous group", func(t *testing.T) {
		f := newBindingFixture(t, "", "default,vip")
		sub := createActiveSubscription(t, f)
		legacy := bindLegacySubscriptionChannel(t, f, sub)
		require.Equal(t, legacy, userGroupOf(t, f.user.Id))

		require.NoError(t, MigrateSubscriptionBoundUserGroups())
		assert.Equal(t, "default", userGroupOf(t, f.user.Id))
		stored := subscriptionOf(t, sub.Id)
		assert.Equal(t, "", stored.UpgradeGroup)
		assert.Equal(t, "", stored.PrevUserGroup)
		assert.Equal(t, f.channel.Id, stored.BoundChannelId, "the binding itself survives")
		assert.Equal(t, legacy, stored.BoundGroup)
		assert.Equal(t, legacy, channelGroupOf(t, f.channel.Id))

		require.NoError(t, DB.Model(&User{}).Where("id = ?", f.user.Id).Update("group", "vip").Error)
		require.NoError(t, MigrateSubscriptionBoundUserGroups())
		assert.Equal(t, "vip", userGroupOf(t, f.user.Id), "a second run leaves a restored user alone")
		assert.Equal(t, stored.UpdatedAt, subscriptionOf(t, sub.Id).UpdatedAt)
	})

	t.Run("empty previous group falls back to default", func(t *testing.T) {
		f := newBindingFixture(t, "", "default")
		sub := createActiveSubscription(t, f)
		legacy := bindLegacySubscriptionChannel(t, f, sub)
		require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("prev_user_group", "").Error)

		require.NoError(t, MigrateSubscriptionBoundUserGroups())
		assert.Equal(t, "default", userGroupOf(t, f.user.Id))
		assert.NotEqual(t, legacy, userGroupOf(t, f.user.Id))
	})

	t.Run("active plan with upgrade group returns the user to that group", func(t *testing.T) {
		f := newBindingFixture(t, "pro", "default")
		sub := createActiveSubscription(t, f)
		typed := "claude-ch" + strconv.Itoa(f.channel.Id)
		require.NoError(t, ensureSubscriptionBoundGroupRatio(typed, "pro"))
		require.NoError(t, setChannelGroupTx(DB, f.channel.Id, typed))
		require.NoError(t, DB.Model(&User{}).Where("id = ?", f.user.Id).Update("group", typed).Error)
		require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
			"upgrade_group":            typed,
			"prev_user_group":          "default",
			"bound_channel_id":         f.channel.Id,
			"bound_group":              typed,
			"bound_channel_prev_group": "default",
			"bound_prev_upgrade_group": "pro",
		}).Error)

		require.NoError(t, MigrateSubscriptionBoundUserGroups())
		assert.Equal(t, "pro", userGroupOf(t, f.user.Id))
		stored := subscriptionOf(t, sub.Id)
		assert.Equal(t, "pro", stored.UpgradeGroup)
		assert.Equal(t, "default", stored.PrevUserGroup)

		// Expiry afterwards follows the plan snapshot as if the binding had
		// never touched the user.
		require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("end_time", time.Now().Unix()-1).Error)
		_, err := ExpireDueSubscriptions(10)
		require.NoError(t, err)
		assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	})

	t.Run("expired binding still parked in the group goes to the previous group", func(t *testing.T) {
		f := newBindingFixture(t, "pro", "default")
		sub := createActiveSubscription(t, f)
		typed := "claude-ch" + strconv.Itoa(f.channel.Id)
		require.NoError(t, DB.Model(&User{}).Where("id = ?", f.user.Id).Update("group", typed).Error)
		require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
			"status":                   "expired",
			"end_time":                 time.Now().Unix() - 10,
			"upgrade_group":            typed,
			"prev_user_group":          "default",
			"bound_channel_id":         f.channel.Id,
			"bound_group":              typed,
			"bound_prev_upgrade_group": "pro",
		}).Error)

		require.NoError(t, MigrateSubscriptionBoundUserGroups())
		assert.Equal(t, "default", userGroupOf(t, f.user.Id), "an ended plan no longer elevates the user")
		assert.Equal(t, "pro", subscriptionOf(t, sub.Id).UpgradeGroup)
	})

	t.Run("a user already elsewhere is not moved", func(t *testing.T) {
		f := newBindingFixture(t, "", "default")
		sub := createActiveSubscription(t, f)
		_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
		require.NoError(t, err)
		require.NoError(t, DB.Model(&User{}).Where("id = ?", f.user.Id).Update("group", "vip").Error)

		require.NoError(t, MigrateSubscriptionBoundUserGroups())
		assert.Equal(t, "vip", userGroupOf(t, f.user.Id))
	})
}
