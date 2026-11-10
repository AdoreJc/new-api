package controller

import (
	"maps"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// pricingViewer is whoever looks at the model square: a signed-in user or a
// guest (userId 0). usableGroup includes the dedicated group of the user's
// active subscription binding.
type pricingViewer struct {
	userId      int
	group       string
	usableGroup map[string]string
}

func getPricingViewer(c *gin.Context) pricingViewer {
	viewer := pricingViewer{userId: c.GetInt("id")}
	if viewer.userId > 0 {
		if user, err := model.GetUserCache(viewer.userId); err == nil {
			viewer.group = user.Group
		}
	}
	viewer.usableGroup = service.GetUserUsableGroupsForUser(viewer.userId, viewer.group)
	return viewer
}

// pricingView is a model square entry as one viewer sees it.
type pricingView struct {
	model.Pricing
	// PriceNotConfigured marks a ratio-billed model without a price, ratio or
	// billing expression: relay refuses it for this viewer, so the default
	// ratio it carries is not a real price.
	PriceNotConfigured bool `json:"price_not_configured,omitempty"`
}

// pricingForViewer returns the entries a viewer may see, each listing only the
// groups that viewer can use. A model with no usable group is still listed,
// with an empty group list, when it is served in a subscription dedicated group
// (claude-ch<id>, codex-ch<id>, oauth-ch<id>): its price and status are public,
// the dedicated group names are not. Any other model without a usable group
// stays hidden. Entries are copies; the shared pricing cache is not modified.
func pricingForViewer(pricing []model.Pricing, usableGroup map[string]string, acceptUnsetRatio bool) []pricingView {
	views := make([]pricingView, 0, len(pricing))
	for _, item := range pricing {
		visibleGroups := make([]string, 0, len(item.EnableGroup))
		dedicated := false
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok || group == "all" {
				visibleGroups = append(visibleGroups, group)
				continue
			}
			if model.IsSubscriptionBoundGroup(group) {
				dedicated = true
			}
		}
		if len(visibleGroups) == 0 && !dedicated {
			continue
		}
		item.EnableGroup = visibleGroups
		views = append(views, pricingView{
			Pricing: item,
			PriceNotConfigured: !acceptUnsetRatio &&
				item.BillingUsageSchema == nil &&
				len(item.BillingPluginVariants) == 0 &&
				!helper.HasModelBillingConfig(item.ModelName),
		})
	}
	return views
}

func GetPricing(c *gin.Context) {
	viewer := getPricingViewer(c)
	groupRatio := map[string]float64{}
	maps.Copy(groupRatio, ratio_setting.GetGroupRatioCopy())
	if viewer.group != "" {
		for g := range groupRatio {
			ratio, ok := ratio_setting.GetGroupGroupRatio(viewer.group, g)
			if ok {
				groupRatio[g] = ratio
			}
		}
	}
	// check groupRatio contains usableGroup
	for group := range groupRatio {
		if _, ok := viewer.usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}

	acceptUnsetRatio := operation_setting.SelfUseModeEnabled
	if !acceptUnsetRatio && viewer.userId > 0 {
		userSettings, _ := model.GetUserSetting(viewer.userId, false)
		acceptUnsetRatio = userSettings.AcceptUnsetRatioModel
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricingForViewer(model.GetPricing(), viewer.usableGroup, acceptUnsetRatio),
		"vendors":            model.GetVendors(),
		"group_ratio":        groupRatio,
		"usable_group":       viewer.usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        service.GetUserAutoGroupForUser(viewer.userId, viewer.group),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
