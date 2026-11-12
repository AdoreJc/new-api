package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Without the memory cache, selection reads the database and still skips a
// cooling Codex channel while another candidate exists.
func TestDatabaseChannelSelectionSkipsCoolingCodexChannel(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousType := DB, common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	initCol()
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		initCol()
		ClearCodexChannelCooldown(9601)
		ClearCodexChannelCooldown(9602)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	for _, id := range []int{9601, 9602} {
		require.NoError(t, db.Create(&Channel{Id: id, Type: constant.ChannelTypeCodex, Name: "codex", Key: "{}", Status: common.ChannelStatusEnabled}).Error)
		require.NoError(t, db.Create(&Ability{Group: "default", Model: "codex-db-model", ChannelId: id, Enabled: true, Weight: 100}).Error)
	}

	CoolDownCodexChannel(9601, time.Now().Add(time.Hour))
	for range 20 {
		channel, err := GetChannel("default", "codex-db-model", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, channel)
		assert.Equal(t, 9602, channel.Id)
	}

	CoolDownCodexChannel(9602, time.Now().Add(time.Hour))
	channel, err := GetChannel("default", "codex-db-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, channel, "with every candidate cooling a channel is still chosen")
}
