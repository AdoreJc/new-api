package controller

import (
	"net/http"
	"sync"
	"time"

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

// GetCostBackfillStatus reports the latest backfill run and how many
// dashboard rows still lack estimates. It deliberately does not count logs:
// that needs a full scan of the log table, which only the backfill run does.
func GetCostBackfillStatus(c *gin.Context) {
	task, err := model.GetLatestSystemTask(model.SystemTaskTypeCostBackfill)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pendingData, err := pendingQuotaDataCount(c, task)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	data := gin.H{
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

// The pending count scans quota_data, so it is cached briefly: dashboard
// loads and the progress poll during a run then cost one scan a minute. The
// cache is dropped when the latest backfill task changes state.
var costPendingCache struct {
	sync.Mutex
	value     int64
	expiresAt time.Time
	taskKey   string
}

func pendingQuotaDataCount(c *gin.Context, task *model.SystemTask) (int64, error) {
	taskKey := ""
	if task != nil {
		taskKey = task.TaskID + ":" + string(task.Status)
	}
	costPendingCache.Lock()
	defer costPendingCache.Unlock()
	if time.Now().Before(costPendingCache.expiresAt) && costPendingCache.taskKey == taskKey {
		return costPendingCache.value, nil
	}
	var pending int64
	if err := model.DB.WithContext(c.Request.Context()).Model(&model.QuotaData{}).Where("standard_quota IS NULL").Count(&pending).Error; err != nil {
		return 0, err
	}
	costPendingCache.value = pending
	costPendingCache.expiresAt = time.Now().Add(time.Minute)
	costPendingCache.taskKey = taskKey
	return pending, nil
}
