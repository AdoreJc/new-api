package model

import (
	"context"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"

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
	for {
		_, more, err := BackfillQuotaDataCostWindow(ctx)
		require.NoError(t, err)
		if !more {
			break
		}
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
	_, more, err := BackfillQuotaDataCostWindow(ctx)
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
	assert.Equal(t, int64(1), summary.PendingBackfillCount)

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
		require.NoError(t, db.Omit("cost_ratio").Create(&Channel{Id: 3, Name: "legacy", Key: "k", Status: common.ChannelStatusEnabled}).Error)
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

	fresh := Channel{Name: "fresh", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&fresh).Error)
	require.NoError(t, db.First(&fresh, fresh.Id).Error)
	require.NotNil(t, fresh.CostRatio)
	assert.Equal(t, 1.0, *fresh.CostRatio, "new channels default to cost ratio 1")
	assert.Equal(t, 1.0, channelCostRatio(fresh.Id))
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", fresh.Id).Update("cost_ratio", 0.25).Error)
	assert.Equal(t, 0.25, channelCostRatio(fresh.Id))

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
			for {
				_, more, err := BackfillQuotaDataCostWindow(ctx)
				require.NoError(t, err)
				if !more {
					break
				}
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
