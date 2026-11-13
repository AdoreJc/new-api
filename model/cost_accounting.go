package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Cost accounting keeps three amounts per consume log, all in quota units:
//   - quota          what the user was charged (group ratio applied);
//   - standard_quota the same charge with the group ratio treated as 1;
//   - cost_quota     standard_quota x the serving channel's cost ratio.
//
// Rows written before cost accounting existed have NULL standard/cost values
// until the backfill estimates them from quota / group_ratio.

// CostAccountingStartTimeOptionKey stores the first time this instance ran with
// cost accounting. Dashboard amounts before it are backfill estimates.
const CostAccountingStartTimeOptionKey = "CostAccountingStartTime"

type consumeCostAccounting struct {
	StandardQuota int
	CostQuota     int
}

// newConsumeCostAccounting snapshots the channel cost ratio into the log's
// admin_info so later ratio changes never rewrite history.
func newConsumeCostAccounting(channelId int, standardQuota int, other *LogOther) consumeCostAccounting {
	standardQuota = max(standardQuota, 0)
	costRatio := channelCostRatio(channelId)
	other.SetAdmin("channel_cost_ratio", costRatio)
	return consumeCostAccounting{
		StandardQuota: standardQuota,
		CostQuota:     common.QuotaRound(float64(standardQuota) * costRatio),
	}
}

func channelCostRatio(channelId int) float64 {
	if channelId <= 0 {
		return 1
	}
	if common.MemoryCacheEnabled {
		if channel, err := CacheGetChannel(channelId); err == nil {
			return channel.GetCostRatio()
		}
	}
	var channel Channel
	if err := DB.Model(&Channel{}).Select("id, cost_ratio").Where("id = ?", channelId).Take(&channel).Error; err != nil {
		return 1
	}
	return channel.GetCostRatio()
}

// EnsureCostAccountingStartTime records when cost accounting started. It keeps
// the first recorded value on later startups.
func EnsureCostAccountingStartTime() error {
	option := Option{Key: CostAccountingStartTimeOptionKey}
	return DB.Where(Option{Key: CostAccountingStartTimeOptionKey}).
		Attrs(Option{Value: strconv.FormatInt(common.GetTimestamp(), 10)}).
		FirstOrCreate(&option).Error
}

func GetCostAccountingStartTime() int64 {
	var option Option
	if err := DB.Where(Option{Key: CostAccountingStartTimeOptionKey}).Take(&option).Error; err != nil {
		return 0
	}
	value, _ := strconv.ParseInt(option.Value, 10, 64)
	return value
}

// EstimateStandardQuota estimates the list price of a historical consume log
// from its charged quota and the group ratio snapshot stored in other.
func EstimateStandardQuota(quota int, other string) int {
	if quota <= 0 {
		return 0
	}
	groupRatio := 1.0
	if other != "" {
		var values map[string]any
		if err := common.UnmarshalJsonStr(other, &values); err == nil {
			if ratio, ok := values["group_ratio"].(float64); ok && ratio > 0 {
				groupRatio = ratio
			}
		}
	}
	return common.QuotaRound(float64(quota) / groupRatio)
}

// CostDimension is a dashboard breakdown of quota_data.
type CostDimension string

const (
	CostDimensionGroup   CostDimension = "group"
	CostDimensionChannel CostDimension = "channel"
	CostDimensionModel   CostDimension = "model"
	CostDimensionUser    CostDimension = "user"
)

type CostSummaryRow struct {
	UseGroup      string `json:"use_group,omitempty"`
	ChannelID     int    `json:"channel_id,omitempty"`
	ChannelName   string `json:"channel_name,omitempty"`
	ModelName     string `json:"model_name,omitempty"`
	UserID        int    `json:"user_id,omitempty"`
	Username      string `json:"username,omitempty"`
	Count         int64  `json:"count"`
	Quota         int64  `json:"quota"`
	StandardQuota int64  `json:"standard_quota"`
	CostQuota     int64  `json:"cost_quota"`
	// AccountedQuota is the part of Quota whose rows carry list price and
	// cost; profit and margin must be computed against it.
	AccountedQuota int64 `json:"accounted_quota"`
	// PendingRows counts quota_data rows not yet backfilled; their amounts
	// are missing from StandardQuota, CostQuota and AccountedQuota.
	PendingRows int64 `json:"pending_rows"`
}

type CostSummary struct {
	Total               CostSummaryRow    `json:"total"`
	Rows                []*CostSummaryRow `json:"rows"`
	AccountingStartTime int64             `json:"accounting_start_time"`
}

const costSummarySums = "COALESCE(sum(count), 0) as count, COALESCE(sum(quota), 0) as quota, " +
	"COALESCE(sum(standard_quota), 0) as standard_quota, COALESCE(sum(cost_quota), 0) as cost_quota, " +
	"COALESCE(sum(CASE WHEN standard_quota IS NULL THEN 0 ELSE quota END), 0) as accounted_quota, " +
	"COALESCE(sum(CASE WHEN standard_quota IS NULL THEN 1 ELSE 0 END), 0) as pending_rows"

// GetCostSummary aggregates list price, revenue and cost from quota_data for
// administrators, split by one dimension.
func GetCostSummary(startTime int64, endTime int64, username string, dimension CostDimension) (*CostSummary, error) {
	var columns string
	switch dimension {
	case CostDimensionGroup:
		columns = "use_group"
	case CostDimensionChannel:
		columns = "channel_id"
	case CostDimensionModel:
		columns = "model_name"
	case CostDimensionUser:
		columns = "user_id, username"
	default:
		return nil, errors.New("invalid cost dimension")
	}
	base := func() *gorm.DB {
		tx := DB.Table("quota_data").Where("created_at >= ? and created_at <= ?", startTime, endTime)
		if username != "" {
			tx = tx.Where("username = ?", username)
		}
		return tx
	}

	summary := &CostSummary{Rows: []*CostSummaryRow{}}
	if err := base().Select(costSummarySums).Scan(&summary.Total).Error; err != nil {
		return nil, err
	}
	if err := base().Select(columns + ", " + costSummarySums).Group(columns).Order("quota desc").Scan(&summary.Rows).Error; err != nil {
		return nil, err
	}
	if dimension == CostDimensionChannel {
		if err := fillCostChannelNames(summary.Rows); err != nil {
			return nil, err
		}
	}
	summary.AccountingStartTime = GetCostAccountingStartTime()
	return summary, nil
}

func fillCostChannelNames(rows []*CostSummaryRow) error {
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.ChannelID != 0 {
			ids = append(ids, row.ChannelID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var channels []struct {
		Id   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := DB.Table("channels").Select("id, name").Where("id IN ?", ids).Find(&channels).Error; err != nil {
		return err
	}
	names := make(map[int]string, len(channels))
	for _, channel := range channels {
		names[channel.Id] = channel.Name
	}
	for _, row := range rows {
		row.ChannelName = names[row.ChannelID]
	}
	return nil
}

// CountCostBackfillPending returns the consume logs and quota_data rows that
// still lack cost accounting values. ClickHouse logs are not backfilled.
func CountCostBackfillPending(ctx context.Context) (logs int64, quotaData int64, err error) {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		if err = LOG_DB.WithContext(ctx).Model(&Log{}).Where("type = ? AND standard_quota IS NULL", LogTypeConsume).Count(&logs).Error; err != nil {
			return 0, 0, err
		}
	}
	if err = DB.WithContext(ctx).Model(&QuotaData{}).Where("standard_quota IS NULL").Count(&quotaData).Error; err != nil {
		return 0, 0, err
	}
	return logs, quotaData, nil
}

// BackfillLogCostBatch estimates standard and cost quota for up to limit
// consume logs with id > afterId that have none. It returns the last id it
// examined (0 when nothing is left) and how many rows it updated. Rows are
// only written while still NULL, so reruns and concurrent writers are safe;
// quota and balances are never touched.
func BackfillLogCostBatch(ctx context.Context, afterId int, limit int) (lastId int, updated int, err error) {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return 0, 0, nil
	}
	var rows []struct {
		Id    int
		Quota int
		Other string
	}
	err = LOG_DB.WithContext(ctx).Model(&Log{}).Select("id, quota, other").
		Where("id > ? AND type = ? AND standard_quota IS NULL", afterId, LogTypeConsume).
		Order("id asc").Limit(limit).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return 0, 0, err
	}
	err = LOG_DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			standard := EstimateStandardQuota(row.Quota, row.Other)
			result := tx.Model(&Log{}).Where("id = ? AND standard_quota IS NULL", row.Id).
				Updates(map[string]any{"standard_quota": standard, "cost_quota": standard})
			if result.Error != nil {
				return result.Error
			}
			updated += int(result.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return rows[len(rows)-1].Id, updated, nil
}

// costBackfillWindow is the quota_data time span processed per batch.
const costBackfillWindow int64 = 6 * 3600

type costBucketKey struct {
	UserID    int
	ModelName string
	Hour      int64
	Group     string
	TokenID   int
	ChannelID int
}

type costBucketSum struct {
	Quota    int64
	Standard int64
	Cost     int64
}

// BackfillQuotaDataCostWindow fills the earliest window of quota_data rows
// without cost values. Each row gets its bucket's standard/quota and
// cost/quota ratios from the matching consume logs (estimated where a log
// has none), or 1 when the logs are gone. The update multiplies the row's
// current quota in SQL, so increments flushed concurrently stay consistent.
// It returns the rows updated and false once nothing is left.
func BackfillQuotaDataCostWindow(ctx context.Context) (updated int, more bool, err error) {
	var start struct{ Min *int64 }
	if err = DB.WithContext(ctx).Model(&QuotaData{}).Select("MIN(created_at) as min").
		Where("standard_quota IS NULL").Scan(&start).Error; err != nil {
		return 0, false, err
	}
	if start.Min == nil {
		return 0, false, nil
	}
	windowStart := *start.Min
	windowEnd := windowStart + costBackfillWindow

	var dataRows []*QuotaData
	if err = DB.WithContext(ctx).Select("id, user_id, model_name, created_at, use_group, token_id, channel_id").
		Where("standard_quota IS NULL AND created_at >= ? AND created_at < ?", windowStart, windowEnd).
		Find(&dataRows).Error; err != nil {
		return 0, false, err
	}

	var logs []struct {
		UserId        int
		ModelName     string
		CreatedAt     int64
		Group         string
		TokenId       int
		ChannelId     int
		Quota         int
		StandardQuota *int
		CostQuota     *int
		Other         string
	}
	if err = LOG_DB.WithContext(ctx).Model(&Log{}).
		Select([]string{"user_id", "model_name", "created_at", "group", "token_id", "channel_id", "quota", "standard_quota", "cost_quota", "other"}).
		Where("type = ? AND created_at >= ? AND created_at < ?", LogTypeConsume, windowStart, windowEnd).
		Find(&logs).Error; err != nil {
		return 0, false, err
	}
	buckets := make(map[costBucketKey]*costBucketSum)
	for _, log := range logs {
		key := costBucketKey{log.UserId, log.ModelName, log.CreatedAt - log.CreatedAt%3600, log.Group, log.TokenId, log.ChannelId}
		sum := buckets[key]
		if sum == nil {
			sum = &costBucketSum{}
			buckets[key] = sum
		}
		standard := 0
		cost := 0
		if log.StandardQuota != nil {
			standard = *log.StandardQuota
			cost = standard
			if log.CostQuota != nil {
				cost = *log.CostQuota
			}
		} else {
			standard = EstimateStandardQuota(log.Quota, log.Other)
			cost = standard
		}
		sum.Quota += int64(log.Quota)
		sum.Standard += int64(standard)
		sum.Cost += int64(cost)
	}

	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range dataRows {
			standardRatio, costRatio := 1.0, 1.0
			sum := buckets[costBucketKey{row.UserID, row.ModelName, row.CreatedAt, row.UseGroup, row.TokenID, row.ChannelID}]
			if sum != nil && sum.Quota > 0 {
				standardRatio = float64(sum.Standard) / float64(sum.Quota)
				costRatio = float64(sum.Cost) / float64(sum.Quota)
			}
			result := tx.Model(&QuotaData{}).Where("id = ? AND standard_quota IS NULL", row.Id).Updates(map[string]any{
				"standard_quota": gorm.Expr(fmt.Sprintf("FLOOR(quota * %s + 0.5)", strconv.FormatFloat(standardRatio, 'f', -1, 64))),
				"cost_quota":     gorm.Expr(fmt.Sprintf("FLOOR(quota * %s + 0.5)", strconv.FormatFloat(costRatio, 'f', -1, 64))),
			})
			if result.Error != nil {
				return result.Error
			}
			updated += int(result.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return updated, true, nil
}
