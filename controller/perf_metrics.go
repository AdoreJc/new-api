package controller

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func GetPerfMetricsSummary(c *gin.Context) {
	hours := 24
	if rawHours := c.Query("hours"); rawHours != "" {
		if parsed, err := strconv.Atoi(rawHours); err == nil {
			hours = parsed
		}
	}

	recentModels := c.QueryArray("recent_model")
	if len(recentModels) > perfmetrics.MaxRecentModels {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	result, err := perfmetrics.QuerySummaryAll(hours, activeGroups, recentModels...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

func GetPerfMetrics(c *gin.Context) {
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "model is required",
		})
		return
	}

	hours := 24
	if rawHours := c.Query("hours"); rawHours != "" {
		if parsed, err := strconv.Atoi(rawHours); err == nil {
			hours = parsed
		}
	}

	// The model summary and series cover every active group, dedicated
	// subscription groups included; per-group rows only name groups the viewer
	// can use, and a group the viewer cannot use cannot be queried by name.
	viewer := getPricingViewer(c)
	group := c.Query("group")
	allowedGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	if _, visible := viewer.usableGroup[group]; group != "" && !visible {
		allowedGroups = []string{}
	}
	result, err := perfmetrics.Query(perfmetrics.QueryParams{
		Model:         modelName,
		Group:         group,
		Hours:         hours,
		AllowedGroups: allowedGroups,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	result.Groups = slices.DeleteFunc(result.Groups, func(groupResult perfmetrics.GroupResult) bool {
		_, visible := viewer.usableGroup[groupResult.Group]
		return !visible
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}
