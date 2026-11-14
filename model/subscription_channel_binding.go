package model

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

// Prefixes of the dedicated group a bound OAuth channel is moved into. The
// group name is the prefix for the channel type followed by the channel id, so
// the same channel always maps to the same group.
const (
	SubscriptionBoundGroupPrefixClaude = "claude-ch"
	SubscriptionBoundGroupPrefixCodex  = "codex-ch"
	// SubscriptionBoundGroupPrefixLegacy is the type-agnostic prefix used
	// before groups were named by channel type. Bindings created with it keep
	// that name (stored in UserSubscription.BoundGroup) and are not renamed;
	// it is only recognised, never generated.
	SubscriptionBoundGroupPrefixLegacy = "oauth-ch"
)

// subscriptionBoundGroupPrefixes lists every prefix a dedicated binding group
// may carry, including the legacy one.
var subscriptionBoundGroupPrefixes = [...]string{
	SubscriptionBoundGroupPrefixClaude,
	SubscriptionBoundGroupPrefixCodex,
	SubscriptionBoundGroupPrefixLegacy,
}

// SubscriptionBoundGroupName returns the dedicated group for a bound channel:
// claude-ch<id> for Claude Subscription (OAuth), codex-ch<id> for ChatGPT
// Subscription (Codex). Other channel types cannot be bound and yield "".
func SubscriptionBoundGroupName(channelType int, channelId int) string {
	switch channelType {
	case constant.ChannelTypeClaudeOAuth:
		return SubscriptionBoundGroupPrefixClaude + strconv.Itoa(channelId)
	case constant.ChannelTypeCodex:
		return SubscriptionBoundGroupPrefixCodex + strconv.Itoa(channelId)
	default:
		return ""
	}
}

// IsSubscriptionBoundGroup reports whether group is a dedicated binding group
// (claude-ch<id>, codex-ch<id> or the legacy oauth-ch<id>).
func IsSubscriptionBoundGroup(group string) bool {
	for _, prefix := range subscriptionBoundGroupPrefixes {
		rest, ok := strings.CutPrefix(group, prefix)
		if !ok || rest == "" {
			continue
		}
		if strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// isChannelSubscriptionBoundGroup reports whether group is a dedicated binding
// group of channelId under any current or legacy prefix.
func isChannelSubscriptionBoundGroup(group string, channelId int) bool {
	suffix := strconv.Itoa(channelId)
	for _, prefix := range subscriptionBoundGroupPrefixes {
		if group == prefix+suffix {
			return true
		}
	}
	return false
}

// isSubscriptionBoundGroupOf reports whether group is the dedicated group of
// sub's binding. The stored BoundGroup is authoritative, so a binding created
// under the legacy oauth-ch prefix keeps resolving to its real group; a row
// without one falls back to any dedicated name of the bound channel id.
func isSubscriptionBoundGroupOf(sub *UserSubscription, group string) bool {
	if sub == nil || sub.BoundChannelId <= 0 || group == "" {
		return false
	}
	if bound := strings.TrimSpace(sub.BoundGroup); bound != "" {
		return group == bound
	}
	return isChannelSubscriptionBoundGroup(group, sub.BoundChannelId)
}

// HasBoundGroup reports whether group is the dedicated group of this
// subscription's binding.
func (sub *UserSubscription) HasBoundGroup(group string) bool {
	return isSubscriptionBoundGroupOf(sub, group)
}

// ListActiveBoundUserSubscriptions returns the user's active subscriptions
// that are bound to a channel, newest end time first.
func ListActiveBoundUserSubscriptions(userId int) ([]UserSubscription, error) {
	var subs []UserSubscription
	err := DB.Where("user_id = ? AND bound_channel_id > 0 AND status = ? AND end_time > ?", userId, "active", GetDBTimestamp()).
		Order("end_time desc, id desc").
		Find(&subs).Error
	return subs, err
}

// subscriptionBoundGroupForChannel returns the dedicated group a new binding
// of channelId would use, or "" when the channel is missing or not bindable;
// the bind transaction then reports the precise error.
func subscriptionBoundGroupForChannel(channelId int) string {
	var channel Channel
	if err := DB.Select("id", "type").Where("id = ?", channelId).First(&channel).Error; err != nil {
		return ""
	}
	return SubscriptionBoundGroupName(channel.Type, channel.Id)
}

// IsSubscriptionBindableChannelType reports whether a channel type carries an
// OAuth subscription account whose usage windows can be queried.
func IsSubscriptionBindableChannelType(channelType int) bool {
	return channelType == constant.ChannelTypeClaudeOAuth || channelType == constant.ChannelTypeCodex
}

// SubscriptionBindableChannel is the admin-facing projection of a channel that
// can be bound to a subscription. It never carries the channel key.
type SubscriptionBindableChannel struct {
	Id                  int    `json:"id"`
	Name                string `json:"name"`
	Type                int    `json:"type"`
	Status              int    `json:"status"`
	Group               string `json:"group"`
	BoundSubscriptionId int    `json:"bound_subscription_id"`
	BoundUserId         int    `json:"bound_user_id"`
	BoundUsername       string `json:"bound_username"`
}

// SubscriptionChannelBindingChange reports what a bind/unbind changed so the
// caller can refresh caches after the transaction commits.
type SubscriptionChannelBindingChange struct {
	UserId         int
	ChannelChanged bool
	// Group is the dedicated group of the binding after a bind, "" after an
	// unbind.
	Group string
	// EnabledChannelId is the manually disabled channel a bind enabled, 0
	// when it enabled none.
	EnabledChannelId int
	// ChannelAutoDisabled is set when the bound channel is auto-disabled; a
	// bind leaves that status alone.
	ChannelAutoDisabled bool
}

func isUserSubscriptionActive(sub *UserSubscription, now int64) bool {
	return sub != nil && sub.Status == "active" && sub.EndTime > now
}

func attachBoundChannelInfo(subs []UserSubscription) {
	ids := make([]int, 0, len(subs))
	seen := make(map[int]struct{}, len(subs))
	for i := range subs {
		id := subs[i].BoundChannelId
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	var channels []Channel
	if err := DB.Select("id", "name", "type", "status").Where("id IN ?", ids).Find(&channels).Error; err != nil {
		common.SysError(common.LogText("failed to load bound channels for subscriptions: %v", err))
		return
	}
	byId := make(map[int]Channel, len(channels))
	for _, ch := range channels {
		byId[ch.Id] = ch
	}
	for i := range subs {
		ch, ok := byId[subs[i].BoundChannelId]
		if !ok {
			continue
		}
		subs[i].BoundChannelType = ch.Type
		subs[i].BoundChannelName = ch.Name
		subs[i].BoundChannelStatus = ch.Status
	}
}

func lockBindableChannelTx(tx *gorm.DB, channelId int) (*Channel, error) {
	if channelId <= 0 {
		return nil, common.NewMessage("Invalid channel ID")
	}
	var channel Channel
	if err := lockForUpdate(tx).Where("id = ?", channelId).First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, common.NewMessage("Channel not found")
		}
		return nil, err
	}
	if !IsSubscriptionBindableChannelType(channel.Type) {
		return nil, common.NewMessage("Only Claude Subscription (OAuth) and ChatGPT Subscription (Codex) channels can be bound")
	}
	if channel.ChannelInfo.IsMultiKey {
		return nil, common.NewMessage("Multi-key channels are not supported")
	}
	return &channel, nil
}

// setChannelGroupTx moves the channel into group and rebuilds its abilities.
// A non-zero status is written with the group, with the status_reason and
// status_time bookkeeping of UpdateChannelStatus, so the rebuilt abilities
// carry it; 0 keeps the current status.
func setChannelGroupTx(tx *gorm.DB, channelId int, group string, status int) error {
	var channel Channel
	if err := tx.Where("id = ?", channelId).First(&channel).Error; err != nil {
		return err
	}
	updates := map[string]any{"group": group}
	channel.Group = group
	if status != 0 && status != channel.Status {
		info := channel.GetOtherInfo()
		info["status_reason"] = "subscription binding"
		info["status_time"] = common.GetTimestamp()
		channel.SetOtherInfo(info)
		channel.Status = status
		updates["status"] = channel.Status
		updates["other_info"] = channel.OtherInfo
	}
	if err := tx.Model(&Channel{}).Where("id = ?", channelId).Updates(updates).Error; err != nil {
		return err
	}
	return channel.UpdateAbilities(tx)
}

// releaseBoundChannelTx moves the bound channel back to the group it had
// before the binding, and back to manually disabled when the binding had
// enabled it (BoundChannelPrevStatus). The channel is left alone when an
// administrator has already moved it out of the dedicated group. The
// subscription fields are not touched.
func releaseBoundChannelTx(tx *gorm.DB, sub *UserSubscription) (bool, error) {
	if sub == nil || sub.BoundChannelId <= 0 {
		return false, nil
	}
	var channel Channel
	err := lockForUpdate(tx).Where("id = ?", sub.BoundChannelId).First(&channel)
	if err.Error != nil {
		if errors.Is(err.Error, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err.Error
	}
	if !isSubscriptionBoundGroupOf(sub, channel.Group) {
		return false, nil
	}
	target := strings.TrimSpace(sub.BoundChannelPrevGroup)
	if target == "" || target == channel.Group {
		target = "default"
	}
	status := 0
	if sub.BoundChannelPrevStatus == common.ChannelStatusManuallyDisabled {
		status = common.ChannelStatusManuallyDisabled
	}
	if err := setChannelGroupTx(tx, channel.Id, target, status); err != nil {
		return false, err
	}
	return true, nil
}

// disableExpiredBoundChannelTx returns the channel of a subscription that is
// expiring to manually disabled when the binding had enabled it. The channel
// stays parked in its dedicated group and the subscription keeps pointing at
// it, as for any expired binding; only the restore mark is cleared.
func disableExpiredBoundChannelTx(tx *gorm.DB, sub *UserSubscription) (bool, error) {
	if sub == nil || sub.BoundChannelId <= 0 || sub.BoundChannelPrevStatus != common.ChannelStatusManuallyDisabled {
		return false, nil
	}
	if err := tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).
		Update("bound_channel_prev_status", 0).Error; err != nil {
		return false, err
	}
	var channel Channel
	err := lockForUpdate(tx).Where("id = ?", sub.BoundChannelId).First(&channel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !isSubscriptionBoundGroupOf(sub, channel.Group) || channel.Status == common.ChannelStatusManuallyDisabled {
		return false, nil
	}
	if err := setChannelGroupTx(tx, channel.Id, channel.Group, common.ChannelStatusManuallyDisabled); err != nil {
		return false, err
	}
	return true, nil
}

// clearSubscriptionChannelBindingTx releases the channel and clears the
// binding columns without touching the user's group (used by invalidate, where
// the downgrade path already handled it).
func clearSubscriptionChannelBindingTx(tx *gorm.DB, sub *UserSubscription) (bool, error) {
	if sub == nil || sub.BoundChannelId <= 0 {
		return false, nil
	}
	released, err := releaseBoundChannelTx(tx, sub)
	if err != nil {
		return false, err
	}
	if err := tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
		"bound_channel_id":          0,
		"bound_group":               "",
		"bound_channel_prev_group":  "",
		"bound_channel_prev_status": 0,
		"bound_prev_upgrade_group":  "",
		"updated_at":                common.GetTimestamp(),
	}).Error; err != nil {
		return false, err
	}
	return released, nil
}

// bindSubscriptionChannelTx binds channel channelId to the locked, active
// subscription sub: the channel moves into its dedicated group, which only the
// distributor's request-scoped routing uses (see ResolveSubscriptionRouteGroup).
// The user's group, UpgradeGroup and PrevUserGroup are left alone. A previous
// binding of the same subscription is released first.
func bindSubscriptionChannelTx(tx *gorm.DB, sub *UserSubscription, channelId int, now int64) (*SubscriptionChannelBindingChange, error) {
	if tx == nil || sub == nil {
		return nil, errors.New("invalid bind args")
	}
	if !isUserSubscriptionActive(sub, now) {
		return nil, common.NewMessage("Only an active subscription can be bound to a channel")
	}
	channel, err := lockBindableChannelTx(tx, channelId)
	if err != nil {
		return nil, err
	}
	change := &SubscriptionChannelBindingChange{
		UserId:              sub.UserId,
		ChannelAutoDisabled: channel.Status == common.ChannelStatusAutoDisabled,
	}
	if sub.BoundChannelId == channel.Id {
		change.Group = sub.BoundGroup
		if change.Group == "" {
			change.Group = SubscriptionBoundGroupName(channel.Type, channel.Id)
		}
		return change, nil
	}
	dedicated := SubscriptionBoundGroupName(channel.Type, channel.Id)
	if dedicated == "" {
		return nil, common.NewMessage("Only Claude Subscription (OAuth) and ChatGPT Subscription (Codex) channels can be bound")
	}

	var activeCount int64
	if err := tx.Model(&UserSubscription{}).
		Where("bound_channel_id = ? AND status = ? AND end_time > ? AND id <> ?", channel.Id, "active", now, sub.Id).
		Count(&activeCount).Error; err != nil {
		return nil, err
	}
	if activeCount > 0 {
		return nil, common.NewMessage("This channel is already bound to another active subscription")
	}

	if sub.BoundChannelId > 0 {
		if _, err := releaseBoundChannelTx(tx, sub); err != nil {
			return nil, err
		}
	}

	// Where the channel goes back to when released, and whether it goes back
	// to manually disabled. A channel still parked in its dedicated group
	// (current or legacy name) by an ended subscription keeps that older
	// origin.
	prevChannelGroup := strings.TrimSpace(channel.Group)
	prevChannelStatus := 0
	if channel.Status == common.ChannelStatusManuallyDisabled {
		prevChannelStatus = common.ChannelStatusManuallyDisabled
	}
	if isChannelSubscriptionBoundGroup(prevChannelGroup, channel.Id) {
		var lastBinding UserSubscription
		query := tx.Where("bound_channel_id = ? AND id <> ?", channel.Id, sub.Id).
			Order("id desc").Limit(1).Find(&lastBinding)
		if query.Error != nil {
			return nil, query.Error
		}
		prevChannelGroup = strings.TrimSpace(lastBinding.BoundChannelPrevGroup)
		if lastBinding.BoundChannelPrevStatus == common.ChannelStatusManuallyDisabled {
			prevChannelStatus = common.ChannelStatusManuallyDisabled
		}
	}
	if prevChannelGroup == "" || isChannelSubscriptionBoundGroup(prevChannelGroup, channel.Id) {
		prevChannelGroup = "default"
	}
	// Ended subscriptions that still point at this channel lose the reference
	// so a later unbind on them cannot release a channel bound elsewhere.
	if err := tx.Model(&UserSubscription{}).
		Where("bound_channel_id = ? AND id <> ?", channel.Id, sub.Id).
		Updates(map[string]any{
			"bound_channel_id":          0,
			"bound_group":               "",
			"bound_channel_prev_group":  "",
			"bound_channel_prev_status": 0,
			"bound_prev_upgrade_group":  "",
			"updated_at":                common.GetTimestamp(),
		}).Error; err != nil {
		return nil, err
	}

	// A manually disabled channel is enabled for the binding; an auto-disabled
	// one has rejected credentials and stays as it is.
	status := 0
	if channel.Status == common.ChannelStatusManuallyDisabled {
		status = common.ChannelStatusEnabled
		change.EnabledChannelId = channel.Id
	}
	if err := setChannelGroupTx(tx, channel.Id, dedicated, status); err != nil {
		return nil, err
	}
	change.ChannelChanged = true
	change.Group = dedicated

	sub.BoundChannelId = channel.Id
	sub.BoundGroup = dedicated
	sub.BoundChannelPrevGroup = prevChannelGroup
	sub.BoundChannelPrevStatus = prevChannelStatus
	sub.BoundPrevUpgradeGroup = ""
	if err := tx.Save(sub).Error; err != nil {
		return nil, err
	}
	return change, nil
}

// unbindSubscriptionChannelTx releases the channel and clears the binding.
// The user's group is not touched: routing was request-scoped.
func unbindSubscriptionChannelTx(tx *gorm.DB, sub *UserSubscription) (*SubscriptionChannelBindingChange, error) {
	if tx == nil || sub == nil {
		return nil, errors.New("invalid unbind args")
	}
	if sub.BoundChannelId <= 0 {
		return nil, common.NewMessage("This subscription is not bound to a channel")
	}
	change := &SubscriptionChannelBindingChange{UserId: sub.UserId}
	released, err := releaseBoundChannelTx(tx, sub)
	if err != nil {
		return nil, err
	}
	change.ChannelChanged = released

	restoreSubscriptionUpgradeGroupFromBinding(sub)
	sub.BoundChannelId = 0
	sub.BoundGroup = ""
	sub.BoundChannelPrevGroup = ""
	sub.BoundChannelPrevStatus = 0
	sub.BoundPrevUpgradeGroup = ""
	if err := tx.Save(sub).Error; err != nil {
		return nil, err
	}
	return change, nil
}

// ensureSubscriptionBoundGroupRatio registers the dedicated group in the group
// ratio table so the user can still be billed and select the group. The ratio
// is copied from the user's current group, falling back to 1. The group is
// deliberately not added to UserUsableGroups: the owner can use it because it
// is their own group, and nobody else may select it.
func ensureSubscriptionBoundGroupRatio(group string, sourceGroup string) error {
	if group == "" {
		return errors.New("empty group")
	}
	ratios := ratio_setting.GetGroupRatioCopy()
	if _, ok := ratios[group]; ok {
		return nil
	}
	ratio := 1.0
	if sourceRatio, ok := ratios[sourceGroup]; ok && sourceRatio > 0 {
		ratio = sourceRatio
	}
	ratios[group] = ratio
	encoded, err := common.Marshal(ratios)
	if err != nil {
		return err
	}
	return UpdateOption("GroupRatio", string(encoded))
}

func applySubscriptionChannelBindingChange(change *SubscriptionChannelBindingChange) {
	if change == nil {
		return
	}
	if change.EnabledChannelId > 0 {
		// As in UpdateChannelStatus, enabling ends a usage-limit cooldown.
		ClearCodexChannelCooldown(change.EnabledChannelId)
	}
	if change.ChannelChanged {
		InitChannelCache()
	}
	InvalidateSubscriptionRouteBindingCache(change.UserId)
}

func subscriptionBindingNotice(change *SubscriptionChannelBindingChange) *common.Message {
	if change == nil || change.Group == "" {
		return nil
	}
	params := map[string]any{"group": change.Group}
	switch {
	case change.ChannelAutoDisabled:
		return common.NewMessage("Requests for models on the bound channel are routed to the dedicated group {{group}}; other requests are not affected. The channel is auto-disabled and cannot serve requests: authorize the account again, then enable the channel manually", params)
	case change.EnabledChannelId > 0:
		return common.NewMessage("Requests for models on the bound channel are routed to the dedicated group {{group}}; other requests are not affected. The channel was manually disabled and has been enabled; it will be disabled again when the binding is released", params)
	default:
		return common.NewMessage("Requests for models on the bound channel are routed to the dedicated group {{group}}; other requests are not affected", params)
	}
}

// restoreSubscriptionUpgradeGroupFromBinding undoes what a bind made before
// routing became request-scoped did to the subscription: UpgradeGroup held
// the dedicated group so the downgrade paths would move the user back. The
// plan's upgrade group snapshot returns, and a subscription that never
// elevated the user loses the PrevUserGroup the bind had filled in.
func restoreSubscriptionUpgradeGroupFromBinding(sub *UserSubscription) bool {
	if sub == nil || !isSubscriptionBoundGroupOf(sub, strings.TrimSpace(sub.UpgradeGroup)) {
		return false
	}
	sub.UpgradeGroup = strings.TrimSpace(sub.BoundPrevUpgradeGroup)
	if sub.UpgradeGroup == "" {
		sub.PrevUserGroup = ""
	}
	return true
}

// MigrateSubscriptionBoundUserGroups restores users whom an earlier release
// moved into their binding's dedicated group (claude-ch<id>, codex-ch<id> or
// the legacy oauth-ch<id>): routing is request-scoped now, so a user still
// parked there could not use the default group, Auto or any other model. For
// every subscription with a bound channel it puts the plan's upgrade group
// back into UpgradeGroup and, when the user's group is the binding's group,
// moves the user to the plan's upgrade group (active subscriptions only),
// else the group held before the subscription, else default. Running it again
// changes nothing.
func MigrateSubscriptionBoundUserGroups() error {
	var subs []UserSubscription
	if err := DB.Select("id").Where("bound_channel_id > 0").Order("id asc").Find(&subs).Error; err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	now := GetDBTimestamp()
	restoredUsers := make(map[int]string)
	restoredSubscriptions := 0
	for _, row := range subs {
		err := DB.Transaction(func(tx *gorm.DB) error {
			var sub UserSubscription
			if err := lockForUpdate(tx).Where("id = ?", row.Id).First(&sub).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if sub.BoundChannelId <= 0 {
				return nil
			}
			currentGroup, err := getUserGroupByIdTx(tx, sub.UserId)
			if err != nil {
				return err
			}
			if isSubscriptionBoundGroupOf(&sub, currentGroup) {
				target := ""
				if isUserSubscriptionActive(&sub, now) {
					target = strings.TrimSpace(sub.BoundPrevUpgradeGroup)
				}
				if target == "" {
					target = strings.TrimSpace(sub.PrevUserGroup)
				}
				if target == "" || isSubscriptionBoundGroupOf(&sub, target) {
					target = "default"
				}
				if err := tx.Model(&User{}).Where("id = ?", sub.UserId).Update("group", target).Error; err != nil {
					return err
				}
				restoredUsers[sub.UserId] = target
			}
			if !restoreSubscriptionUpgradeGroupFromBinding(&sub) {
				return nil
			}
			restoredSubscriptions++
			return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
				"upgrade_group":   sub.UpgradeGroup,
				"prev_user_group": sub.PrevUserGroup,
				"updated_at":      common.GetTimestamp(),
			}).Error
		})
		if err != nil {
			return err
		}
	}
	for userId, group := range restoredUsers {
		refreshSubscriptionUserGroupCache(userId, common.LogText("subscription binding migration"))
		InvalidateSubscriptionRouteBindingCache(userId)
		common.SysLog(common.LogText("subscription binding migration: user %d moved out of the dedicated group back to %s", userId, group))
	}
	if restoredSubscriptions > 0 || len(restoredUsers) > 0 {
		common.SysLog(common.LogText("subscription binding migration: restored %d subscription upgrade groups and %d user groups", restoredSubscriptions, len(restoredUsers)))
	}
	return nil
}

// AdminBindSubscriptionChannel binds an OAuth channel to a user subscription.
func AdminBindSubscriptionChannel(userSubscriptionId int, channelId int) (*common.Message, error) {
	if userSubscriptionId <= 0 || channelId <= 0 {
		return nil, errors.New("invalid userSubscriptionId or channelId")
	}
	var sub UserSubscription
	if err := DB.Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return nil, err
	}
	// Rebinding the same channel keeps its stored (possibly legacy) group.
	dedicated := sub.BoundGroup
	if sub.BoundChannelId != channelId || dedicated == "" {
		dedicated = subscriptionBoundGroupForChannel(channelId)
	}
	if dedicated != "" {
		sourceGroup, _ := GetUserGroup(sub.UserId, true)
		if err := ensureSubscriptionBoundGroupRatio(dedicated, sourceGroup); err != nil {
			return nil, err
		}
	}
	now := GetDBTimestamp()
	var change *SubscriptionChannelBindingChange
	err := DB.Transaction(func(tx *gorm.DB) error {
		var locked UserSubscription
		if err := lockForUpdate(tx).Where("id = ?", userSubscriptionId).First(&locked).Error; err != nil {
			return err
		}
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", locked.UserId).First(&userRow).Error; err != nil {
			return err
		}
		var err error
		change, err = bindSubscriptionChannelTx(tx, &locked, channelId, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	applySubscriptionChannelBindingChange(change)
	return subscriptionBindingNotice(change), nil
}

// AdminUnbindSubscriptionChannel releases the channel bound to a subscription.
func AdminUnbindSubscriptionChannel(userSubscriptionId int) (*common.Message, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	var change *SubscriptionChannelBindingChange
	err := DB.Transaction(func(tx *gorm.DB) error {
		var locked UserSubscription
		if err := lockForUpdate(tx).Where("id = ?", userSubscriptionId).First(&locked).Error; err != nil {
			return err
		}
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", locked.UserId).First(&userRow).Error; err != nil {
			return err
		}
		var err error
		change, err = unbindSubscriptionChannelTx(tx, &locked)
		return err
	})
	if err != nil {
		return nil, err
	}
	applySubscriptionChannelBindingChange(change)
	return nil, nil
}

// AdminCreateUserSubscriptionWithChannel creates a subscription from a plan
// (no payment) and binds channelId in the same transaction.
func AdminCreateUserSubscriptionWithChannel(userId int, planId int, channelId int) (*common.Message, error) {
	if userId <= 0 || planId <= 0 || channelId <= 0 {
		return nil, errors.New("invalid userId, planId or channelId")
	}
	plan, err := GetSubscriptionPlanById(planId)
	if err != nil {
		return nil, err
	}
	if dedicated := subscriptionBoundGroupForChannel(channelId); dedicated != "" {
		sourceGroup, _ := GetUserGroup(userId, true)
		if err := ensureSubscriptionBoundGroupRatio(dedicated, sourceGroup); err != nil {
			return nil, err
		}
	}
	now := GetDBTimestamp()
	var change *SubscriptionChannelBindingChange
	err = DB.Transaction(func(tx *gorm.DB) error {
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&userRow).Error; err != nil {
			return err
		}
		sub, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, "admin")
		if err != nil {
			return err
		}
		change, err = bindSubscriptionChannelTx(tx, sub, channelId, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	applySubscriptionChannelBindingChange(change)
	return subscriptionBindingNotice(change), nil
}

// GetUserSubscriptionById loads one subscription row.
func GetUserSubscriptionById(userSubscriptionId int) (*UserSubscription, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	var sub UserSubscription
	if err := DB.Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return nil, err
	}
	return &sub, nil
}

// ListSubscriptionBindableChannels lists OAuth subscription channels with
// their current active binding, for the admin picker.
func ListSubscriptionBindableChannels() ([]SubscriptionBindableChannel, error) {
	var channels []Channel
	if err := DB.Select("id", "name", "type", "status", commonGroupCol).
		Where("type IN ?", []int{constant.ChannelTypeClaudeOAuth, constant.ChannelTypeCodex}).
		Order("id asc").Find(&channels).Error; err != nil {
		return nil, err
	}
	result := make([]SubscriptionBindableChannel, 0, len(channels))
	if len(channels) == 0 {
		return result, nil
	}
	now := GetDBTimestamp()
	var bindings []UserSubscription
	if err := DB.Select("id", "user_id", "bound_channel_id").
		Where("bound_channel_id > 0 AND status = ? AND end_time > ?", "active", now).
		Find(&bindings).Error; err != nil {
		return nil, err
	}
	bindingByChannel := make(map[int]UserSubscription, len(bindings))
	userIds := make([]int, 0, len(bindings))
	for _, b := range bindings {
		bindingByChannel[b.BoundChannelId] = b
		userIds = append(userIds, b.UserId)
	}
	usernames := make(map[int]string, len(userIds))
	if len(userIds) > 0 {
		var users []User
		if err := DB.Select("id", "username").Where("id IN ?", userIds).Find(&users).Error; err != nil {
			return nil, err
		}
		for _, u := range users {
			usernames[u.Id] = u.Username
		}
	}
	for _, ch := range channels {
		item := SubscriptionBindableChannel{
			Id:     ch.Id,
			Name:   ch.Name,
			Type:   ch.Type,
			Status: ch.Status,
			Group:  ch.Group,
		}
		if b, ok := bindingByChannel[ch.Id]; ok {
			item.BoundSubscriptionId = b.Id
			item.BoundUserId = b.UserId
			item.BoundUsername = usernames[b.UserId]
		}
		result = append(result, item)
	}
	return result, nil
}
