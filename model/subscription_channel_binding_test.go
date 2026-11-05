package model

import (
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

func TestBindSubscriptionChannelMovesChannelAndUserIntoDedicatedGroup(t *testing.T) {
	f := newBindingFixture(t, "", "default,vip")
	sub := createActiveSubscription(t, f)
	dedicated := SubscriptionBoundGroupName(f.channel.Id)

	notice, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, dedicated, notice.Params["group"])

	stored := subscriptionOf(t, sub.Id)
	assert.Equal(t, f.channel.Id, stored.BoundChannelId)
	assert.Equal(t, dedicated, stored.BoundGroup)
	assert.Equal(t, dedicated, stored.UpgradeGroup)
	assert.Equal(t, "default", stored.PrevUserGroup)
	assert.Equal(t, "default,vip", stored.BoundChannelPrevGroup)
	assert.Equal(t, "", stored.BoundPrevUpgradeGroup)
	assert.Equal(t, dedicated, userGroupOf(t, f.user.Id))
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
	assert.Equal(t, "default", stored.PrevUserGroup)
	assert.Equal(t, SubscriptionBoundGroupName(codex.Id), userGroupOf(t, f.user.Id))
	assert.Equal(t, SubscriptionBoundGroupName(codex.Id), channelGroupOf(t, codex.Id))
	assert.Equal(t, "default", channelGroupOf(t, f.channel.Id), "previous channel returns to its original group")
	assert.Equal(t, []string{"default"}, abilityGroupsOf(t, f.channel.Id))
}

func TestUnbindSubscriptionChannelRestoresPlanUpgradeGroup(t *testing.T) {
	f := newBindingFixture(t, "pro", "default")
	sub := createActiveSubscription(t, f)
	require.Equal(t, "pro", userGroupOf(t, f.user.Id))
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)
	bound := subscriptionOf(t, sub.Id)
	assert.Equal(t, "pro", bound.BoundPrevUpgradeGroup)
	assert.Equal(t, "default", bound.PrevUserGroup)
	assert.InDelta(t, 2, ratio_setting.GetGroupRatio(SubscriptionBoundGroupName(f.channel.Id)), 0.0001, "ratio copied from the user's group at bind time")

	notice, err := AdminUnbindSubscriptionChannel(sub.Id)
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, "pro", notice.Params["group"])

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

func TestUnbindSubscriptionChannelWithoutPlanUpgradeGroupRevertsToPreviousGroup(t *testing.T) {
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

func TestInvalidateBoundSubscriptionRevertsUserAndReleasesChannel(t *testing.T) {
	f := newBindingFixture(t, "", "default,vip")
	sub := createActiveSubscription(t, f)
	_, err := AdminBindSubscriptionChannel(sub.Id, f.channel.Id)
	require.NoError(t, err)

	notice, err := AdminInvalidateUserSubscription(sub.Id)
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, "default", notice.Params["group"])
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
	dedicated := SubscriptionBoundGroupName(f.channel.Id)

	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("end_time", time.Now().Unix()-1).Error)
	expired, err := ExpireDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, 1, expired)
	assert.Equal(t, "default", userGroupOf(t, f.user.Id))
	assert.Equal(t, dedicated, channelGroupOf(t, f.channel.Id), "the account stays private until an administrator acts")
	assert.Equal(t, f.channel.Id, subscriptionOf(t, sub.Id).BoundChannelId)

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
	assert.Equal(t, dedicated, userGroupOf(t, f.user.Id))

	_, err = AdminUnbindSubscriptionChannel(renewal.Id)
	require.NoError(t, err)
	assert.Equal(t, "default", channelGroupOf(t, f.channel.Id))
}

func TestCreateUserSubscriptionWithChannelBindsInOneStep(t *testing.T) {
	f := newBindingFixture(t, "", "default")
	dedicated := SubscriptionBoundGroupName(f.channel.Id)

	notice, err := AdminCreateUserSubscriptionWithChannel(f.user.Id, f.plan.Id, f.channel.Id)
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, dedicated, notice.Params["group"])
	subs, err := GetAllActiveUserSubscriptions(f.user.Id)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, f.channel.Id, subs[0].Subscription.BoundChannelId)
	assert.Equal(t, dedicated, userGroupOf(t, f.user.Id))

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
	assert.Equal(t, "oauth-ch12", SubscriptionBoundGroupName(12))
	assert.True(t, IsSubscriptionBoundGroup("oauth-ch12"))
	assert.False(t, IsSubscriptionBoundGroup("oauth-ch"))
	assert.False(t, IsSubscriptionBoundGroup("oauth-chx"))
	assert.False(t, IsSubscriptionBoundGroup("default"))
}
