package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func GetUserUsableGroups(userGroup string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" {
		specialSettings, b := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get(userGroup)
		if b {
			// 处理特殊可用分组
			for specialGroup, desc := range specialSettings {
				if after, ok := strings.CutPrefix(specialGroup, "-:"); ok {
					// 移除分组
					groupToRemove := after
					delete(groupsCopy, groupToRemove)
				} else if after, ok := strings.CutPrefix(specialGroup, "+:"); ok {
					// 添加分组
					groupToAdd := after
					groupsCopy[groupToAdd] = desc
				} else {
					// 直接添加分组
					groupsCopy[specialGroup] = desc
				}
			}
		}
		// 如果userGroup不在UserUsableGroups中，返回UserUsableGroups + userGroup。
		// 描述是英文源文本，由 web 控制台翻译；管理员配置的描述原样显示。
		if _, ok := groupsCopy[userGroup]; !ok {
			groupsCopy[userGroup] = "User group"
		}
	}
	return groupsCopy
}

// SubscriptionBoundGroupDescription describes a user's subscription dedicated
// group in their usable groups. English source text, translated by the web
// console.
const SubscriptionBoundGroupDescription = "Subscription account (bound channel models only)"

// GetUserSubscriptionGroup returns the dedicated group of the user's active
// subscription channel binding (claude-ch<id>, codex-ch<id> or the legacy
// oauth-ch<id>), read through the binding cache.
func GetUserSubscriptionGroup(userId int) (string, bool) {
	binding, ok := model.GetUserSubscriptionRouteBinding(userId, common.GetTimestamp())
	if !ok {
		return "", false
	}
	return binding.Group, true
}

// GetUserUsableGroupsForUser is GetUserUsableGroups plus the dedicated group of
// the user's active subscription binding. That group is never part of the
// shared UserUsableGroups setting, so only its owner sees or selects it, and
// only while the binding is active.
func GetUserUsableGroupsForUser(userId int, userGroup string) map[string]string {
	groups := GetUserUsableGroups(userGroup)
	if group, ok := GetUserSubscriptionGroup(userId); ok {
		groups[group] = SubscriptionBoundGroupDescription
	}
	return groups
}

func GroupInUserUsableGroups(userGroup, groupName string) bool {
	_, ok := GetUserUsableGroups(userGroup)[groupName]
	return ok
}

func IsUserSelectableGroup(userGroup, groupName string) bool {
	if groupName == "" || groupName == "auto" {
		return false
	}
	return GroupInUserUsableGroups(userGroup, groupName) && ratio_setting.ContainsGroupRatio(groupName)
}

// GetUserAutoGroup 根据用户分组获取自动分组设置
func GetUserAutoGroup(userGroup string) []string {
	autoGroups := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range setting.GetAutoGroups() {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		autoGroups = append(autoGroups, group)
	}
	return autoGroups
}

// GetUserAutoGroupForUser is GetUserAutoGroup with the dedicated group of the
// user's active subscription binding placed first. Without an active binding
// it is identical to GetUserAutoGroup.
func GetUserAutoGroupForUser(userId int, userGroup string) []string {
	return prependSubscriptionAutoGroup(userId, GetUserAutoGroup(userGroup))
}

// prependSubscriptionAutoGroup puts the user's active subscription dedicated
// group in front of an Auto list. The group is implicit and fixed: it is never
// stored in a token's Auto snapshot nor counted against the per-token limit,
// and it leaves every Auto list once the binding ends. A model the bound
// channel does not serve finds no channel there, and Auto moves on to the next
// group.
func prependSubscriptionAutoGroup(userId int, groups []string) []string {
	group, ok := GetUserSubscriptionGroup(userId)
	if !ok {
		return groups
	}
	ordered := make([]string, 0, len(groups)+1)
	ordered = append(ordered, group)
	for _, g := range groups {
		if g != group {
			ordered = append(ordered, g)
		}
	}
	return ordered
}

// FilterUserTokenAutoGroups applies current permissions before the current
// per-token limit. It intentionally does not fall back to the global Auto list.
func FilterUserTokenAutoGroups(userGroup string, groups []string) []string {
	maxCount := setting.GetMaxTokenAutoGroups()
	filtered := make([]string, 0, min(len(groups), maxCount))
	seen := make(map[string]struct{})
	for _, group := range groups {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		filtered = append(filtered, group)
		if len(filtered) == maxCount {
			break
		}
	}
	return filtered
}

// GetRequestAutoGroups resolves the ordered Auto groups for the current token.
// The absence of the context value means that the token inherits the complete
// global Auto list; a present (even empty) value is an explicit token snapshot.
// Either way the requesting user's active subscription dedicated group comes
// first; a malformed snapshot still fails closed.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	userId := c.GetInt("id")
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return GetUserAutoGroupForUser(userId, userGroup)
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	return prependSubscriptionAutoGroup(userId, FilterUserTokenAutoGroups(userGroup, groups))
}

// GetGroupsEnabledModels 按 groups 顺序获取各分组启用的模型并去重
func GetGroupsEnabledModels(groups []string) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}

// GetUserGroupRatio 获取用户使用某个分组的倍率
// userGroup 用户分组
// group 需要获取倍率的分组
func GetUserGroupRatio(userGroup, group string) float64 {
	ratio, ok := ratio_setting.GetGroupGroupRatio(userGroup, group)
	if ok {
		return ratio
	}
	return ratio_setting.GetGroupRatio(group)
}
