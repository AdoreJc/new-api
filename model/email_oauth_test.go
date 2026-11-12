package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedEmailOAuthOption(t *testing.T, key string) string {
	t.Helper()
	var option Option
	require.NoError(t, DB.Where(&Option{Key: key}).First(&option).Error)
	return option.Value
}

// A re-authorization that arrives while a rotation is between its database
// write and its option map update must not leave the map with the rotated
// token of the old grant next to the new grant's mailbox.
func TestEmailOAuthRotationAndReauthorizationDoNotInterleave(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Option{}))
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		emailOAuthAfterDatabaseWrite = nil
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
		DB.Where("1 = 1").Delete(&Option{})
	})
	require.NoError(t, SaveEmailOAuthAuthorization(common.EmailOAuthGrant{RefreshToken: "grant-1", Email: "old@example.com"}))

	reauthorized := make(chan error, 1)
	emailOAuthAfterDatabaseWrite = func() {
		emailOAuthAfterDatabaseWrite = nil
		go func() {
			reauthorized <- SaveEmailOAuthAuthorization(common.EmailOAuthGrant{RefreshToken: "grant-2", Email: "new@example.com"})
		}()
		// Give an unserialized re-authorization time to finish inside the
		// rotation's window. Serialized, it waits for the rotation instead.
		select {
		case err := <-reauthorized:
			reauthorized <- err
		case <-time.After(200 * time.Millisecond):
		}
	}
	require.NoError(t, ReplaceEmailOAuthRefreshToken("grant-1", "grant-1-rotated"))
	require.NoError(t, <-reauthorized)

	assert.Equal(t, "grant-2", storedEmailOAuthOption(t, common.EmailOAuthRefreshTokenOption))
	assert.Equal(t, "new@example.com", storedEmailOAuthOption(t, common.EmailOAuthEmailOption))
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, "grant-2", common.OptionMap[common.EmailOAuthRefreshTokenOption])
	assert.Equal(t, "new@example.com", common.OptionMap[common.EmailOAuthEmailOption])
}
