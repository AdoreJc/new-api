package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// Claude and Codex OAuth refresh tokens rotate: once the token endpoint
// answers, the refresh token that was sent is dead. A renewed key that does
// not reach the database is therefore the only usable credential left, so a
// failed write is retried and the key is kept in memory until it is stored.

const (
	channelKeySaveAttempts = 4
	channelKeySaveBackoff  = 200 * time.Millisecond
	// channelKeySaveMaxWait bounds the retries. Each SQLite attempt may already
	// wait out busy_timeout, so a slow failure is not retried; the retries are
	// for a lock error returned at once (BUSY_SNAPSHOT, a deadlock victim).
	channelKeySaveMaxWait = 3 * time.Second
)

// channelCredentialLocks serialises the credential refresh of each channel so
// concurrent callers never send the same refresh token twice, and a stashed
// key is only written back by the lock holder. Each entry is a one-slot
// channel so waiting can honour the caller's context.
var channelCredentialLocks sync.Map

func acquireChannelCredentialLock(ctx context.Context, channelID int) (func(), error) {
	entry, _ := channelCredentialLocks.LoadOrStore(channelID, make(chan struct{}, 1))
	slot := entry.(chan struct{})
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// stashedChannelCredential is a renewed key whose database write failed.
type stashedChannelCredential struct {
	// rotatedFingerprint is the refresh token still stored in the database,
	// which upstream has already rotated out.
	rotatedFingerprint string
	renewedFingerprint string
	encodedKey         string
}

// stashedChannelCredentials maps a channel id to its
// *stashedChannelCredential. It lives in process memory only: a restart
// before the key is written back loses it, and the channel then needs a
// re-authorization.
var stashedChannelCredentials sync.Map

func refreshTokenFingerprint(refreshToken string) string {
	sum := sha256.Sum256([]byte(refreshToken))
	return hex.EncodeToString(sum[:])
}

func isDatabaseLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"database is locked",       // SQLite SQLITE_BUSY
		"database table is locked", // SQLite SQLITE_LOCKED
		"sqlite_busy",
		"sqlite_locked",
		"lock wait timeout", // MySQL 1205
		"deadlock",          // MySQL 1213, PostgreSQL 40P01
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// saveChannelKey writes a renewed key to the channel. A busy or locked
// database is retried with backoff until channelKeySaveAttempts,
// channelKeySaveMaxWait or ctx runs out; the first attempt always runs.
func saveChannelKey(ctx context.Context, channelID int, encodedKey string) error {
	deadline := time.Now().Add(channelKeySaveMaxWait)
	backoff := channelKeySaveBackoff
	for attempt := 1; ; attempt++ {
		err := model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("key", encodedKey).Error
		if err == nil || !isDatabaseLockError(err) || attempt == channelKeySaveAttempts || time.Now().Add(backoff).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// persistRenewedChannelKey stores the key renewed from usedRefreshToken. When
// the write keeps failing the key is stashed instead, so the channel goes on
// with the renewed refresh token rather than the one upstream rotated out.
// The caller holds the channel's credential lock.
func persistRenewedChannelKey(ctx context.Context, channelID int, usedRefreshToken string, renewedRefreshToken string, encodedKey string) {
	err := saveChannelKey(ctx, channelID, encodedKey)
	if err == nil {
		stashedChannelCredentials.Delete(channelID)
		return
	}

	rotated := refreshTokenFingerprint(usedRefreshToken)
	if value, ok := stashedChannelCredentials.Load(channelID); ok {
		previous := value.(*stashedChannelCredential)
		if previous.renewedFingerprint == rotated {
			// This refresh started from a stashed key, so the database still
			// holds the token that the earlier stash replaced.
			rotated = previous.rotatedFingerprint
		}
	}
	stashedChannelCredentials.Store(channelID, &stashedChannelCredential{
		rotatedFingerprint: rotated,
		renewedFingerprint: refreshTokenFingerprint(renewedRefreshToken),
		encodedKey:         encodedKey,
	})
	logger.LogError(ctx, common.LogText("oauth channel: channel_id=%d the renewed credential could not be written to the database and is stashed in memory for retry: %v", channelID, err))
}

// StashedOAuthChannelKey returns the stashed renewed key of the channel when
// refreshToken is the one that renewal rotated out, so a caller holding a
// stale copy of the key uses the renewed one. It never drops the stash.
func StashedOAuthChannelKey(channelID int, refreshToken string) (string, bool) {
	value, ok := stashedChannelCredentials.Load(channelID)
	if !ok {
		return "", false
	}
	stashed := value.(*stashedChannelCredential)
	if refreshTokenFingerprint(refreshToken) != stashed.rotatedFingerprint {
		return "", false
	}
	return stashed.encodedKey, true
}

// resolveStashedChannelKey is for a caller that just read the channel from
// the database while holding its credential lock. It returns the stashed key
// to use in place of a stored key that still carries the rotated-out refresh
// token. A stored key with any other refresh token (written by another node
// or a re-authorization) supersedes the stash, which is dropped.
func resolveStashedChannelKey(channelID int, storedRefreshToken string) (string, bool) {
	value, ok := stashedChannelCredentials.Load(channelID)
	if !ok {
		return "", false
	}
	stashed := value.(*stashedChannelCredential)
	if refreshTokenFingerprint(storedRefreshToken) != stashed.rotatedFingerprint {
		stashedChannelCredentials.CompareAndDelete(channelID, stashed)
		return "", false
	}
	return stashed.encodedKey, true
}

// writeBackStashedChannelKey retries writing the channel's stashed key and
// drops the stash once it is stored. The caller holds the credential lock and
// resolved the stash against the stored key; a caller about to refresh skips
// this, since persistRenewedChannelKey stores the newer key instead.
func writeBackStashedChannelKey(ctx context.Context, channelID int) {
	value, ok := stashedChannelCredentials.Load(channelID)
	if !ok {
		return
	}
	stashed := value.(*stashedChannelCredential)
	if err := saveChannelKey(ctx, channelID, stashed.encodedKey); err != nil {
		logger.LogError(ctx, common.LogText("oauth channel: channel_id=%d the stashed credential still could not be written to the database: %v", channelID, err))
		return
	}
	stashedChannelCredentials.CompareAndDelete(channelID, stashed)
	logger.LogInfo(ctx, common.LogText("oauth channel: channel_id=%d the stashed credential was written to the database", channelID))
}
