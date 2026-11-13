package model

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func resetCostAccountingTables(t *testing.T) {
	t.Helper()
	clear := func() {
		DB.Exec("DELETE FROM logs")
		DB.Exec("DELETE FROM quota_data")
		DB.Exec("DELETE FROM users")
		DB.Exec("DELETE FROM channels")
		CacheQuotaDataLock.Lock()
		CacheQuotaData = make(map[string]*QuotaData)
		CacheQuotaDataLock.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestQuotaDataAccumulatesCostAccounting(t *testing.T) {
	resetCostAccountingTables(t)
	const hour = int64(1_700_000_000 - 1_700_000_000%3600)
	params := QuotaDataLogParams{UserID: 1, Username: "alice", ModelName: "m", UseGroup: "vip", ChannelID: 3, CreatedAt: hour + 10, Quota: 300, StandardQuota: 100, CostQuota: 60}
	LogQuotaData(params)
	params.CreatedAt = hour + 20
	LogQuotaData(params)
	SaveQuotaDataCache()

	var row QuotaData
	require.NoError(t, DB.Where("user_id = ?", 1).Take(&row).Error)
	assert.Equal(t, 600, row.Quota)
	assert.Equal(t, 200, *row.StandardQuota)
	assert.Equal(t, 120, *row.CostQuota)

	LogQuotaData(params)
	SaveQuotaDataCache()
	require.NoError(t, DB.First(&row, row.Id).Error)
	assert.Equal(t, 900, row.Quota)
	assert.Equal(t, 300, *row.StandardQuota)
	assert.Equal(t, 180, *row.CostQuota)

	// A row written before cost accounting stays NULL when new requests are
	// added to it, so the backfill can derive the whole row later.
	legacy := QuotaData{UserID: 2, Username: "bob", ModelName: "m", CreatedAt: hour, Count: 1, Quota: 50}
	require.NoError(t, DB.Omit("standard_quota", "cost_quota").Create(&legacy).Error)
	LogQuotaData(QuotaDataLogParams{UserID: 2, Username: "bob", ModelName: "m", CreatedAt: hour + 5, Quota: 40, StandardQuota: 20, CostQuota: 20})
	SaveQuotaDataCache()
	require.NoError(t, DB.First(&legacy, legacy.Id).Error)
	assert.Equal(t, 90, legacy.Quota)
	assert.Nil(t, legacy.StandardQuota)
	assert.Nil(t, legacy.CostQuota)
}

func TestCostBackfillIsResumableAndIdempotent(t *testing.T) {
	resetCostAccountingTables(t)
	user := User{Id: 7, Username: "carol", Quota: 12345, Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(&user).Error)
	const hour = int64(1_700_000_000 - 1_700_000_000%3600)
	exact := 400
	exactCost := 200
	legacyLogs := []Log{
		{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 1, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 300, Other: `{"group_ratio":1.5}`},
		{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 2, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 100, Other: `{"group_ratio":0}`},
		{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 3, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 90, Other: ``},
		{UserId: 7, Type: LogTypeRefund, CreatedAt: hour + 4, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 50, Other: `{"group_ratio":2}`},
	}
	for i := range legacyLogs {
		require.NoError(t, DB.Omit("standard_quota", "cost_quota").Create(&legacyLogs[i]).Error)
	}
	// A log written after the upgrade in the same hour keeps its exact values.
	require.NoError(t, DB.Create(&Log{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 5, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 800, StandardQuota: &exact, CostQuota: &exactCost}).Error)
	bucket := QuotaData{UserID: 7, Username: "carol", ModelName: "m", UseGroup: "vip", ChannelID: 3, CreatedAt: hour, Count: 4, Quota: 1290}
	orphan := QuotaData{UserID: 7, Username: "carol", ModelName: "gone", CreatedAt: hour - 3600, Count: 1, Quota: 77}
	require.NoError(t, DB.Omit("standard_quota", "cost_quota").Create(&bucket).Error)
	require.NoError(t, DB.Omit("standard_quota", "cost_quota").Create(&orphan).Error)

	ctx := context.Background()
	pendingLogs, pendingData, err := CountCostBackfillPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), pendingLogs, "refund logs are not cost-accounted")
	assert.Equal(t, int64(2), pendingData)

	// Interrupt after one row, then restart from the beginning.
	lastId, updated, err := BackfillLogCostBatch(ctx, 0, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, updated)
	assert.Equal(t, legacyLogs[0].Id, lastId)
	for afterId := 0; ; {
		lastId, _, err := BackfillLogCostBatch(ctx, afterId, 1)
		require.NoError(t, err)
		if lastId == 0 {
			break
		}
		afterId = lastId
	}
	for cursor := int64(0); ; {
		_, next, more, err := BackfillQuotaDataCostWindow(ctx, cursor)
		require.NoError(t, err)
		if !more {
			break
		}
		cursor = next
	}

	wantStandard := []int{200, 100, 90}
	for i, want := range wantStandard {
		var log Log
		require.NoError(t, DB.First(&log, legacyLogs[i].Id).Error)
		assert.Equal(t, legacyLogs[i].Quota, log.Quota, "the charge is never rewritten")
		require.NotNil(t, log.StandardQuota)
		assert.Equal(t, want, *log.StandardQuota)
		assert.Equal(t, want, *log.CostQuota, "historical cost uses ratio 1")
	}
	var refund Log
	require.NoError(t, DB.First(&refund, legacyLogs[3].Id).Error)
	assert.Nil(t, refund.StandardQuota)

	require.NoError(t, DB.First(&bucket, bucket.Id).Error)
	assert.Equal(t, 1290, bucket.Quota)
	// (200+100+90+400)/1290 and (200+100+90+200)/1290 of the row's quota.
	assert.Equal(t, 790, *bucket.StandardQuota)
	assert.Equal(t, 590, *bucket.CostQuota)
	require.NoError(t, DB.First(&orphan, orphan.Id).Error)
	assert.Equal(t, 77, *orphan.StandardQuota, "rows whose logs are gone fall back to ratio 1")

	var stored User
	require.NoError(t, DB.First(&stored, user.Id).Error)
	assert.Equal(t, 12345, stored.Quota, "balances are untouched")

	// A second run changes nothing.
	_, updated, err = BackfillLogCostBatch(ctx, 0, 100)
	require.NoError(t, err)
	assert.Zero(t, updated)
	_, _, more, err := BackfillQuotaDataCostWindow(ctx, 0)
	require.NoError(t, err)
	assert.False(t, more)
	pendingLogs, pendingData, err = CountCostBackfillPending(ctx)
	require.NoError(t, err)
	assert.Zero(t, pendingLogs)
	assert.Zero(t, pendingData)
}

// User-facing log and dashboard reads never carry cost accounting fields.
func TestUserReadsOmitCostAccounting(t *testing.T) {
	resetCostAccountingTables(t)
	standard, cost := 100, 60
	require.NoError(t, DB.Create(&Log{UserId: 9, Username: "dave", Type: LogTypeConsume, CreatedAt: 1_700_000_000, Quota: 150, StandardQuota: &standard, CostQuota: &cost,
		Other: `{"group_ratio":1.5,"admin_info":{"channel_cost_ratio":0.6}}`}).Error)
	require.NoError(t, DB.Create(&QuotaData{UserID: 9, Username: "dave", ModelName: "m", CreatedAt: 1_699_999_200, Count: 1, Quota: 150, StandardQuota: &standard, CostQuota: &cost}).Error)

	userLogs, _, err := GetUserLogs(9, LogFilter{}, 0, 10)
	require.NoError(t, err)
	require.Len(t, userLogs, 1)
	var streamed []*Log
	require.NoError(t, StreamUserLogs(context.Background(), 9, LogFilter{}, func(log *Log) error {
		streamed = append(streamed, log)
		return nil
	}))
	userData, err := GetQuotaDataByUserId(9, 0, 1_800_000_000)
	require.NoError(t, err)
	for _, payload := range []any{userLogs, streamed, userData} {
		data, err := common.Marshal(payload)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "standard_quota")
		assert.NotContains(t, string(data), "cost_quota")
		assert.NotContains(t, string(data), "channel_cost_ratio")
	}

	adminLogs, _, err := GetAllLogs(LogFilter{}, 0, 10)
	require.NoError(t, err)
	require.Len(t, adminLogs, 1)
	assert.Equal(t, 100, *adminLogs[0].StandardQuota)
	assert.Equal(t, 60, *adminLogs[0].CostQuota)
}

func TestCostSummaryBreakdown(t *testing.T) {
	resetCostAccountingTables(t)
	rows := []QuotaData{
		{UserID: 1, Username: "a", ModelName: "m1", UseGroup: "vip", ChannelID: 1, CreatedAt: 3600, Count: 2, Quota: 300, StandardQuota: new(200), CostQuota: new(100)},
		{UserID: 2, Username: "b", ModelName: "m2", UseGroup: "vip", ChannelID: 2, CreatedAt: 3600, Count: 1, Quota: 100, StandardQuota: new(100), CostQuota: new(80)},
		{UserID: 2, Username: "b", ModelName: "m2", UseGroup: "default", ChannelID: 2, CreatedAt: 7200, Count: 1, Quota: 50},
	}
	require.NoError(t, DB.Create(&rows[0]).Error)
	require.NoError(t, DB.Create(&rows[1]).Error)
	require.NoError(t, DB.Omit("standard_quota", "cost_quota").Create(&rows[2]).Error)

	summary, err := GetCostSummary(0, 10_000, "", CostDimensionGroup)
	require.NoError(t, err)
	assert.Equal(t, CostSummaryRow{Count: 4, Quota: 450, StandardQuota: 300, CostQuota: 180, AccountedQuota: 400, PendingRows: 1}, summary.Total)
	require.Len(t, summary.Rows, 2)
	assert.Equal(t, "vip", summary.Rows[0].UseGroup)
	assert.Equal(t, int64(400), summary.Rows[0].Quota)

	byUser, err := GetCostSummary(0, 10_000, "b", CostDimensionUser)
	require.NoError(t, err)
	require.Len(t, byUser.Rows, 1)
	assert.Equal(t, "b", byUser.Rows[0].Username)
	assert.Equal(t, int64(80), byUser.Rows[0].CostQuota)

	_, err = GetCostSummary(0, 10_000, "", "token")
	assert.Error(t, err)
}

// TestCostAccountingDatabaseMatrix runs the real startup migration twice and
// the backfill twice against databases created by the previous release
// (upgrade) and against empty ones (fresh). Point the env vars at disposable
// databases: TEST_COST_SQLITE_PATH (a legacy SQLite file), and for MySQL and
// PostgreSQL TEST_COST_<MYSQL|POSTGRES>_DSN / _LOG_DSN (legacy main and log
// databases) plus _FRESH_DSN (an empty database).
func TestCostAccountingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct {
		kind   common.DatabaseType
		prefix string
	}{
		{common.DatabaseTypeSQLite, "TEST_COST_SQLITE"},
		{common.DatabaseTypeMySQL, "TEST_COST_MYSQL"},
		{common.DatabaseTypePostgreSQL, "TEST_COST_POSTGRES"},
	} {
		open := func(t *testing.T, dsn string) *gorm.DB {
			var dialector gorm.Dialector
			switch dialect.kind {
			case common.DatabaseTypeSQLite:
				dialector = sqlite.Open(dsn)
			case common.DatabaseTypeMySQL:
				dialector = mysql.Open(dsn)
			default:
				dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			return db
		}
		t.Run(string(dialect.kind)+"/upgrade", func(t *testing.T) {
			mainDSN := os.Getenv(dialect.prefix + "_DSN")
			logDSN := os.Getenv(dialect.prefix + "_LOG_DSN")
			if dialect.kind == common.DatabaseTypeSQLite {
				mainDSN = os.Getenv("TEST_COST_SQLITE_PATH")
				logDSN = mainDSN
			}
			if mainDSN == "" || logDSN == "" {
				t.Skip(dialect.prefix + " legacy databases are not configured")
			}
			db := open(t, mainDSN)
			logDB := db
			if logDSN != mainDSN {
				logDB = open(t, logDSN)
			}
			runCostAccountingMatrix(t, dialect.kind, db, logDB, true)
		})
		t.Run(string(dialect.kind)+"/fresh", func(t *testing.T) {
			dsn := os.Getenv(dialect.prefix + "_FRESH_DSN")
			if dialect.kind == common.DatabaseTypeSQLite {
				dsn = t.TempDir() + "/fresh.db"
			}
			if dsn == "" {
				t.Skip(dialect.prefix + "_FRESH_DSN is not configured")
			}
			db := open(t, dsn)
			runCostAccountingMatrix(t, dialect.kind, db, db, false)
		})
	}
}

func runCostAccountingMatrix(t *testing.T, kind common.DatabaseType, db, logDB *gorm.DB, legacy bool) {
	previousDB, previousLogDB := DB, LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB, LOG_DB = db, logDB
	common.SetDatabaseTypes(kind, kind)
	initCol()
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		initCol()
	})
	versionQuery := "SELECT version()"
	if kind == common.DatabaseTypeSQLite {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database version: %s, separate log database: %v", version, db != logDB)

	const hour = int64(1_700_000_000 - 1_700_000_000%3600)
	var legacyLogs []Log
	var bucket, orphan QuotaData
	if legacy {
		require.False(t, logDB.Migrator().HasColumn(&Log{}, "standard_quota"), "expected a database from the previous release")
		require.False(t, db.Migrator().HasColumn(&QuotaData{}, "standard_quota"))
		require.False(t, db.Migrator().HasColumn(&Channel{}, "cost_ratio"))
		require.NoError(t, db.Omit("cost_ratio", "cost_mode", "monthly_cost_usd", "cost_effective_from", "cost_effective_to", "subscription_plan_type", "subscription_active_start", "subscription_active_until", "subscription_synced_at").Create(&Channel{Id: 3, Name: "legacy", Key: "k", Status: common.ChannelStatusEnabled}).Error)
		legacyLogs = []Log{
			{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 1, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 300, Other: `{"group_ratio":1.5}`},
			{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 2, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 100, Other: `{"group_ratio":0}`},
			{UserId: 7, Type: LogTypeConsume, CreatedAt: hour + 3, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 90, Other: ``},
			{UserId: 7, Type: LogTypeRefund, CreatedAt: hour + 4, ModelName: "m", Group: "vip", ChannelId: 3, Quota: 50, Other: `{"group_ratio":2}`},
		}
		for i := range legacyLogs {
			require.NoError(t, logDB.Omit("standard_quota", "cost_quota").Create(&legacyLogs[i]).Error)
		}
		bucket = QuotaData{UserID: 7, Username: "carol", ModelName: "m", UseGroup: "vip", ChannelID: 3, CreatedAt: hour, Count: 3, Quota: 490}
		orphan = QuotaData{UserID: 7, Username: "carol", ModelName: "gone", CreatedAt: hour - 3600, Count: 1, Quota: 77}
		require.NoError(t, db.Omit("standard_quota", "cost_quota").Create(&bucket).Error)
		require.NoError(t, db.Omit("standard_quota", "cost_quota").Create(&orphan).Error)
	}

	for range 2 {
		require.NoError(t, migrateDB())
		require.NoError(t, migrateLOGDB())
	}
	startTime := GetCostAccountingStartTime()
	require.Positive(t, startTime)
	require.NoError(t, migrateDB())
	assert.Equal(t, startTime, GetCostAccountingStartTime(), "the start time is recorded once")
	assert.True(t, logDB.Migrator().HasColumn(&Log{}, "standard_quota"))
	assert.True(t, logDB.Migrator().HasColumn(&Log{}, "cost_quota"))
	assert.True(t, db.Migrator().HasColumn(&QuotaData{}, "standard_quota"))
	assert.True(t, db.Migrator().HasColumn(&QuotaData{}, "cost_quota"))
	assert.True(t, db.Migrator().HasColumn(&Channel{}, "cost_ratio"))
	for _, column := range []string{"cost_mode", "monthly_cost_usd", "cost_effective_from", "cost_effective_to", "subscription_plan_type", "subscription_active_start", "subscription_active_until", "subscription_synced_at"} {
		assert.True(t, db.Migrator().HasColumn(&Channel{}, column), column)
	}

	fresh := Channel{Name: "fresh", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&fresh).Error)
	require.NoError(t, db.First(&fresh, fresh.Id).Error)
	require.NotNil(t, fresh.CostRatio)
	assert.Equal(t, 1.0, *fresh.CostRatio, "new channels default to cost ratio 1")
	assert.Equal(t, 1.0, channelCostRatio(fresh.Id))
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", fresh.Id).Update("cost_ratio", 0.25).Error)
	assert.Equal(t, 0.25, channelCostRatio(fresh.Id))
	assert.Nil(t, fresh.CostMode, "existing and new channels follow the type default")
	assert.Nil(t, fresh.MonthlyCostUSD)

	// A Codex subscription channel with a monthly cost is amortized on every
	// dialect; clearing the cost back to NULL reports it as not entered.
	until := hour + 86400
	codex := Channel{Name: "codex-fixed", Key: "k", Type: constant.ChannelTypeCodex, MonthlyCostUSD: new(30.0), SubscriptionActiveUntil: &until}
	require.NoError(t, db.Create(&codex).Error)
	require.NoError(t, UpdateChannelSubscriptionWindow(codex.Id, "plus", nil, &until, hour))
	// The automatic start only moves earlier: 10 days ago, then 30, then 10
	// again keeps 30 days ago.
	for _, daysAgo := range []int64{10, 30, 10} {
		start := hour - daysAgo*86400
		require.NoError(t, UpdateChannelSubscriptionWindow(codex.Id, "", &start, nil, hour))
	}
	var windowed Channel
	require.NoError(t, db.First(&windowed, codex.Id).Error)
	require.NotNil(t, windowed.SubscriptionActiveStart)
	assert.Equal(t, hour-30*86400, *windowed.SubscriptionActiveStart)
	assert.Equal(t, until, *windowed.SubscriptionActiveUntil)
	assert.Equal(t, "plus", windowed.SubscriptionPlanType, "an empty plan keeps the stored one")
	withFixed, err := GetCostSummary(hour-7200, hour+3600, "", CostDimensionChannel)
	require.NoError(t, err)
	var codexInfo *FixedCostChannel
	for i := range withFixed.FixedChannels {
		if withFixed.FixedChannels[i].ChannelID == codex.Id {
			codexInfo = &withFixed.FixedChannels[i]
		}
	}
	require.NotNil(t, codexInfo)
	assert.Positive(t, codexInfo.CostQuota)
	for _, row := range withFixed.Rows {
		if row.ChannelID == codex.Id {
			assert.Equal(t, "codex-fixed", row.ChannelName, "rows of idle fixed-cost channels are named")
			assert.Equal(t, codexInfo.CostQuota, row.FixedCostQuota)
		}
	}
	assert.Equal(t, "plus", codexInfo.PlanType)
	require.NoError(t, UpdateChannelCostSettings(&Channel{Id: codex.Id}, map[string]any{"monthly_cost_usd": nil}))
	withFixed, err = GetCostSummary(hour-7200, hour+3600, "", CostDimensionChannel)
	require.NoError(t, err)
	for _, info := range withFixed.FixedChannels {
		if info.ChannelID == codex.Id {
			assert.True(t, info.Missing)
		}
	}
	require.NoError(t, db.Delete(&Channel{}, codex.Id).Error)

	ctx := context.Background()
	if legacy {
		var stored Channel
		require.NoError(t, db.First(&stored, 3).Error)
		assert.Equal(t, 1.0, stored.GetCostRatio(), "existing channels read as cost ratio 1")
		var kept []Log
		require.NoError(t, logDB.Order("id").Find(&kept).Error)
		require.Len(t, kept, len(legacyLogs))
		for i := range kept {
			assert.Equal(t, legacyLogs[i].Quota, kept[i].Quota)
			assert.Nil(t, kept[i].StandardQuota)
		}

		// Requests flushed into a legacy row before the backfill keep it NULL.
		LogQuotaData(QuotaDataLogParams{UserID: 7, Username: "carol", ModelName: "gone", CreatedAt: hour - 3600 + 5, Quota: 23, StandardQuota: 10, CostQuota: 10})
		SaveQuotaDataCache()
		require.NoError(t, db.First(&orphan, orphan.Id).Error)
		assert.Equal(t, 100, orphan.Quota)
		assert.Nil(t, orphan.StandardQuota)

		pendingLogs, pendingData, err := CountCostBackfillPending(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(3), pendingLogs)
		assert.Equal(t, int64(2), pendingData)
		for range 2 {
			for afterId := 0; ; {
				lastId, _, err := BackfillLogCostBatch(ctx, afterId, 2)
				require.NoError(t, err)
				if lastId == 0 {
					break
				}
				afterId = lastId
			}
			for cursor := int64(0); ; {
				_, next, more, err := BackfillQuotaDataCostWindow(ctx, cursor)
				require.NoError(t, err)
				if !more {
					break
				}
				cursor = next
			}
		}
		for i, want := range []int{200, 100, 90} {
			var log Log
			require.NoError(t, logDB.First(&log, legacyLogs[i].Id).Error)
			assert.Equal(t, legacyLogs[i].Quota, log.Quota)
			require.NotNil(t, log.StandardQuota)
			assert.Equal(t, want, *log.StandardQuota)
			assert.Equal(t, want, *log.CostQuota)
		}
		var refund Log
		require.NoError(t, logDB.First(&refund, legacyLogs[3].Id).Error)
		assert.Nil(t, refund.StandardQuota)
		require.NoError(t, db.First(&bucket, bucket.Id).Error)
		assert.Equal(t, 490, bucket.Quota)
		assert.Equal(t, 390, *bucket.StandardQuota)
		assert.Equal(t, 390, *bucket.CostQuota)
		require.NoError(t, db.First(&orphan, orphan.Id).Error)
		assert.Equal(t, 100, *orphan.StandardQuota)
		pendingLogs, pendingData, err = CountCostBackfillPending(ctx)
		require.NoError(t, err)
		assert.Zero(t, pendingLogs)
		assert.Zero(t, pendingData)
	}

	// New rows accumulate list price and cost on every dialect.
	for _, offset := range []int64{10, 20} {
		LogQuotaData(QuotaDataLogParams{UserID: 8, Username: "erin", ModelName: "n", UseGroup: "default", ChannelID: fresh.Id, CreatedAt: hour + offset, Quota: 30, StandardQuota: 20, CostQuota: 5})
		SaveQuotaDataCache()
	}
	summary, err := GetCostSummary(hour-7200, hour+7200, "erin", CostDimensionChannel)
	require.NoError(t, err)
	assert.Equal(t, CostSummaryRow{Count: 2, Quota: 60, StandardQuota: 40, CostQuota: 10, AccountedQuota: 60}, summary.Total)
	require.Len(t, summary.Rows, 1)
	assert.Equal(t, "fresh", summary.Rows[0].ChannelName)
	for _, dimension := range []CostDimension{CostDimensionGroup, CostDimensionModel, CostDimensionUser} {
		_, err := GetCostSummary(hour-7200, hour+7200, "", dimension)
		require.NoError(t, err, dimension)
	}
}

func utcUnix(year int, month time.Month, day, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC).Unix()
}

func sumUSD(perDay map[int64]float64) float64 {
	total := 0.0
	for _, usd := range perDay {
		total += usd
	}
	return total
}

func TestAmortizeMonthlyCost(t *testing.T) {
	utc := time.UTC
	// Two days across a month end: 1/31 of January plus 1/28 of February.
	perDay := AmortizeMonthlyCost(310, utcUnix(2026, 1, 31, 0), utcUnix(2026, 2, 2, 0), nil, nil, utc)
	assert.Len(t, perDay, 2)
	assert.InDelta(t, 10.0, perDay[utcUnix(2026, 1, 31, 0)], 1e-9)
	assert.InDelta(t, 310.0/28, perDay[utcUnix(2026, 2, 1, 0)], 1e-9)

	// Less than a month: 10 of 30 April days.
	assert.InDelta(t, 100.0, sumUSD(AmortizeMonthlyCost(300, utcUnix(2026, 4, 1, 0), utcUnix(2026, 4, 11, 0), nil, nil, utc)), 1e-9)

	// The window truncates the range: subscription April 5..8 inside April.
	from, to := utcUnix(2026, 4, 5, 0), utcUnix(2026, 4, 8, 0)
	perDay = AmortizeMonthlyCost(300, utcUnix(2026, 4, 1, 0), utcUnix(2026, 5, 1, 0), &from, &to, utc)
	assert.Len(t, perDay, 3)
	assert.InDelta(t, 30.0, sumUSD(perDay), 1e-9)

	// A partial day counts its covered share.
	assert.InDelta(t, 5.0, sumUSD(AmortizeMonthlyCost(300, utcUnix(2026, 4, 1, 0), utcUnix(2026, 4, 1, 12), nil, nil, utc)), 1e-9)

	// A whole month costs the monthly amount; an empty window costs nothing.
	assert.InDelta(t, 300.0, sumUSD(AmortizeMonthlyCost(300, utcUnix(2026, 4, 1, 0), utcUnix(2026, 5, 1, 0), nil, nil, utc)), 1e-9)
	assert.Empty(t, AmortizeMonthlyCost(300, utcUnix(2026, 4, 1, 0), utcUnix(2026, 5, 1, 0), &to, &from, utc))
}

func TestChannelCostModeDefaultsAndWindow(t *testing.T) {
	assert.Equal(t, ChannelCostModeFixedMonthly, (&Channel{Type: constant.ChannelTypeCodex}).GetCostMode())
	assert.Equal(t, ChannelCostModeFixedMonthly, (&Channel{Type: constant.ChannelTypeClaudeOAuth}).GetCostMode())
	assert.Equal(t, ChannelCostModeRatio, (&Channel{Type: constant.ChannelTypeOpenAI}).GetCostMode())
	assert.Equal(t, ChannelCostModeRatio, (&Channel{Type: constant.ChannelTypeCodex, CostMode: new(ChannelCostModeRatio)}).GetCostMode())

	start, until, manual := int64(100), int64(200), int64(150)
	channel := &Channel{SubscriptionActiveStart: &start, SubscriptionActiveUntil: &until}
	from, fromAuto, to, toAuto := channel.FixedCostWindow()
	assert.Equal(t, start, *from)
	assert.Equal(t, until, *to)
	assert.True(t, fromAuto && toAuto)
	channel.CostEffectiveTo = &manual
	_, _, to, toAuto = channel.FixedCostWindow()
	assert.Equal(t, manual, *to, "a manual end date overrides the subscription")
	assert.False(t, toAuto)
}

// Fixed-cost channel requests record no usage cost and say so in admin_info.
func TestFixedCostChannelLogsNoUsageCost(t *testing.T) {
	resetCostAccountingTables(t)
	require.NoError(t, DB.Create(&Channel{Id: 41, Name: "codex", Key: "k", Type: constant.ChannelTypeCodex, CostRatio: new(2.0)}).Error)
	other := NewLogOther()
	accounting := newConsumeCostAccounting(41, 500, other)
	assert.Equal(t, 500, accounting.StandardQuota)
	assert.Zero(t, accounting.CostQuota)
	assert.Equal(t, ChannelCostModeFixedMonthly, other.adminInfo["cost_mode"])
	assert.NotContains(t, other.adminInfo, "channel_cost_ratio")

	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 41).Update("cost_mode", ChannelCostModeRatio).Error)
	accounting = newConsumeCostAccounting(41, 500, NewLogOther())
	assert.Equal(t, 1000, accounting.CostQuota, "ratio mode is unchanged")

	// Clearing a manual date or the monthly cost ("restore auto", "not
	// entered") writes NULL; fields absent from the request stay.
	from, to := int64(100), int64(200)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 41).Updates(map[string]any{"monthly_cost_usd": 20, "cost_effective_from": from, "cost_effective_to": to}).Error)
	require.NoError(t, UpdateChannelCostSettings(&Channel{Id: 41}, map[string]any{"cost_effective_from": nil, "monthly_cost_usd": nil}))
	var stored Channel
	require.NoError(t, DB.First(&stored, 41).Error)
	assert.Nil(t, stored.CostEffectiveFrom)
	assert.Nil(t, stored.MonthlyCostUSD)
	assert.Equal(t, to, *stored.CostEffectiveTo)
}

func TestFixedCostAllocation(t *testing.T) {
	resetCostAccountingTables(t)
	utc := time.UTC
	day1, day3 := utcUnix(2026, 4, 1, 0), utcUnix(2026, 4, 3, 0)
	end := utcUnix(2026, 4, 4, 0) - 1
	expiredUntil := utcUnix(2026, 4, 2, 12)
	require.NoError(t, DB.Create(&[]Channel{
		{Id: 51, Name: "claude-sub", Key: "k", Type: constant.ChannelTypeClaudeOAuth, MonthlyCostUSD: new(300.0)},
		{Id: 52, Name: "codex-unset", Key: "k", Type: constant.ChannelTypeCodex},
		{Id: 53, Name: "codex-expired", Key: "k", Type: constant.ChannelTypeCodex, MonthlyCostUSD: new(60.0), SubscriptionPlanType: "pro", SubscriptionActiveUntil: &expiredUntil},
		{Id: 54, Name: "api", Key: "k", Type: constant.ChannelTypeOpenAI},
	}).Error)
	// Day 1: channel 51 serves vip (list price 300) and default (100).
	// Day 2: no requests on 51. Day 3: only vip.
	require.NoError(t, DB.Create(&[]QuotaData{
		{UserID: 1, Username: "a", ModelName: "m", UseGroup: "vip", ChannelID: 51, CreatedAt: day1 + 3600, Count: 1, Quota: 600, StandardQuota: new(300), CostQuota: new(0)},
		{UserID: 2, Username: "b", ModelName: "m", UseGroup: "default", ChannelID: 51, CreatedAt: day1 + 7200, Count: 1, Quota: 100, StandardQuota: new(100), CostQuota: new(0)},
		{UserID: 1, Username: "a", ModelName: "m", UseGroup: "vip", ChannelID: 51, CreatedAt: day3, Count: 1, Quota: 200, StandardQuota: new(200), CostQuota: new(0)},
		{UserID: 1, Username: "a", ModelName: "m", UseGroup: "vip", ChannelID: 54, CreatedAt: day1, Count: 1, Quota: 50, StandardQuota: new(50), CostQuota: new(40)},
	}).Error)
	now := utcUnix(2026, 4, 10, 0)
	quotaPerDay := 10 * common.QuotaPerUnit // 300 USD / 30 days

	summarize := func(username string, dimension CostDimension) *CostSummary {
		columns := map[CostDimension]string{CostDimensionGroup: "use_group", CostDimensionChannel: "channel_id", CostDimensionUser: "user_id, username"}[dimension]
		tx := DB.Table("quota_data").Where("created_at >= ? and created_at <= ?", day1, end)
		if username != "" {
			tx = tx.Where("username = ?", username)
		}
		summary := &CostSummary{}
		require.NoError(t, tx.Select(columns+", "+costSummarySums).Group(columns).Scan(&summary.Rows).Error)
		require.NoError(t, applyFixedCosts(summary, day1, end, username, dimension, now, utc))
		return summary
	}
	byKey := func(summary *CostSummary, dimension CostDimension) map[string]int64 {
		result := map[string]int64{}
		for _, row := range summary.Rows {
			key := costRowKey(dimension, row.UseGroup, row.ChannelID, row.ModelName, row.UserID)
			if row.Unallocated {
				key = "unallocated"
			}
			result[key] = row.FixedCostQuota
		}
		return result
	}
	near := func(want float64) int64 { return int64(math.Round(want)) }

	byGroup := summarize("", CostDimensionGroup)
	groups := byKey(byGroup, CostDimensionGroup)
	// 51: day 1 split 3:1 by list price, day 2 unallocated, day 3 all vip.
	// 53: 1.5 days of 60/30 USD until the subscription expired, unallocated.
	assert.Equal(t, near(quotaPerDay*0.75)+near(quotaPerDay), groups["g:vip"])
	assert.Equal(t, near(quotaPerDay*0.25), groups["g:default"])
	assert.Equal(t, near(quotaPerDay+3*common.QuotaPerUnit), groups["unallocated"])

	byChannel := summarize("", CostDimensionChannel)
	channels := byKey(byChannel, CostDimensionChannel)
	assert.Equal(t, near(3*quotaPerDay), channels["c:51"], "fixed cost stays on its channel, idle days included")
	assert.Equal(t, near(3*common.QuotaPerUnit), channels["c:53"], "amortization stops when the subscription expires")
	assert.Zero(t, channels["c:54"])
	assert.Equal(t, byChannel.Total.FixedCostQuota, byGroup.Total.FixedCostQuota, "every breakdown adds up to the same total")
	assert.Equal(t, byChannel.Total.FixedCostQuota, summarize("", CostDimensionUser).Total.FixedCostQuota)

	infos := map[int]FixedCostChannel{}
	for _, info := range byChannel.FixedChannels {
		infos[info.ChannelID] = info
	}
	require.Len(t, infos, 3, "ratio channels are not fixed-cost channels")
	assert.True(t, infos[52].Missing, "a channel without a monthly cost is reported, not counted as 0")
	assert.Zero(t, infos[52].CostQuota)
	assert.True(t, infos[53].Expired)
	assert.True(t, infos[53].ToAuto)
	assert.Equal(t, "pro", infos[53].PlanType)
	assert.False(t, infos[51].Expired)

	// A user filter counts only that user's share; idle days belong to nobody.
	byUser := summarize("b", CostDimensionUser)
	assert.Equal(t, near(quotaPerDay*0.25), byKey(byUser, CostDimensionUser)["u:2"])
	assert.Equal(t, near(quotaPerDay*0.25), byUser.Total.FixedCostQuota)
}

func TestLargestRemainderKeepsTheTotal(t *testing.T) {
	parts := largestRemainder(map[string]float64{"a": 1, "b": 1, "c": 1}, 100)
	assert.Equal(t, map[string]int64{"a": 34, "b": 33, "c": 33}, parts)
	parts = largestRemainder(map[string]float64{"x": 0.2, "y": 0.7, "z": 0.1}, 7)
	assert.Equal(t, int64(7), parts["x"]+parts["y"]+parts["z"])
	assert.Equal(t, int64(5), parts["y"])
	assert.Empty(t, largestRemainder(map[string]float64{}, 5))
}
