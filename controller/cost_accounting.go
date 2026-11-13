package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetCostSummary returns admin-only list price, revenue and channel cost
// totals from quota_data, split by group, channel, model or user.
func GetCostSummary(c *gin.Context) {
	startTimestamp, endTimestamp, ok := parseFlowQuotaTimeRange(c)
	if !ok {
		return
	}
	dimension := model.CostDimension(c.DefaultQuery("dimension", string(model.CostDimensionGroup)))
	summary, err := model.GetCostSummary(startTimestamp, endTimestamp, c.Query("username"), dimension)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    summary,
	})
}

// GetCostBackfillStatus reports the latest backfill run and what is left.
func GetCostBackfillStatus(c *gin.Context) {
	pendingLogs, pendingData, err := model.CountCostBackfillPending(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	task, err := model.GetLatestSystemTask(model.SystemTaskTypeCostBackfill)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	data := gin.H{
		"pending_logs":       pendingLogs,
		"pending_quota_data": pendingData,
		"task":               nil,
	}
	if task != nil {
		data["task"] = task.ToResponse()
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

// StartCostBackfill queues the historical cost estimate backfill.
func StartCostBackfill(c *gin.Context) {
	task, err := service.StartCostBackfillTask()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    task.ToResponse(),
	})
}
