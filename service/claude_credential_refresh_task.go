package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	claudeCredentialRefreshTickInterval = 5 * time.Minute
	claudeCredentialRefreshBatchSize    = 200
	claudeCredentialRefreshTimeout      = 20 * time.Second
	// ClaudeOAuthRefreshRejectedReason is the auto-disable reason; it is also a
	// key in the web console locale files.
	ClaudeOAuthRefreshRejectedReason = "Claude OAuth refresh token was rejected; re-authorize the channel"
)

var (
	claudeCredentialRefreshOnce    sync.Once
	claudeCredentialRefreshRunning atomic.Bool
	// claudeRejectedRefreshTokens remembers, per channel, the fingerprint of a
	// refresh token upstream rejected permanently, so the task does not resend
	// it every tick. A re-authorized channel stores a different token and is
	// retried.
	claudeRejectedRefreshTokens sync.Map
)

func refreshTokenFingerprint(refreshToken string) string {
	sum := sha256.Sum256([]byte(refreshToken))
	return hex.EncodeToString(sum[:])
}

// StartClaudeCredentialAutoRefreshTask renews Claude Subscription (OAuth)
// channel tokens on the master node before they expire, mirroring the Codex
// task. A permanently rejected refresh token auto-disables the channel so the
// admin re-authorizes it instead of every request failing upstream.
func StartClaudeCredentialAutoRefreshTask() {
	claudeCredentialRefreshOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}

		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("claude credential auto-refresh task started: tick=%s leeway=%s", claudeCredentialRefreshTickInterval, claudeOAuthRefreshLeeway))

			ticker := time.NewTicker(claudeCredentialRefreshTickInterval)
			defer ticker.Stop()

			RunClaudeCredentialAutoRefreshOnce()
			for range ticker.C {
				RunClaudeCredentialAutoRefreshOnce()
			}
		})
	})
}

// RunClaudeCredentialAutoRefreshOnce performs one scan of the enabled and
// auto-disabled Claude Subscription channels. It returns the number of
// channels scanned and refreshed.
func RunClaudeCredentialAutoRefreshOnce() (scanned int, refreshed int) {
	if !claudeCredentialRefreshRunning.CompareAndSwap(false, true) {
		return 0, 0
	}
	defer claudeCredentialRefreshRunning.Store(false)

	ctx := context.Background()
	now := time.Now()

	offset := 0
	for {
		var channels []*model.Channel
		err := model.DB.
			Select("id", "name", "key", "status", "channel_info", "auto_ban").
			Where("type = ? AND (status = ? OR status = ?)",
				constant.ChannelTypeClaudeOAuth,
				common.ChannelStatusEnabled,
				common.ChannelStatusAutoDisabled,
			).
			Order("id asc").
			Limit(claudeCredentialRefreshBatchSize).
			Offset(offset).
			Find(&channels).Error
		if err != nil {
			logger.LogError(ctx, common.LogText("claude credential auto-refresh: query channels failed: %v", err))
			return scanned, refreshed
		}
		if len(channels) == 0 {
			break
		}
		offset += claudeCredentialRefreshBatchSize

		for _, ch := range channels {
			if ch == nil || ch.ChannelInfo.IsMultiKey {
				continue
			}
			scanned++

			key, err := ParseClaudeOAuthKey(ch.Key)
			if err != nil || strings.TrimSpace(key.RefreshToken) == "" {
				continue
			}
			if !claudeOAuthKeyNeedsRefresh(key, now, claudeOAuthRefreshLeeway) {
				continue
			}
			fingerprint := refreshTokenFingerprint(key.RefreshToken)
			if rejected, ok := claudeRejectedRefreshTokens.Load(ch.Id); ok && rejected == fingerprint {
				continue
			}

			refreshCtx, cancel := context.WithTimeout(ctx, claudeCredentialRefreshTimeout)
			newKey, _, err := RefreshClaudeChannelCredential(refreshCtx, ch.Id, ClaudeCredentialRefreshOptions{Leeway: claudeOAuthRefreshLeeway})
			cancel()
			if err != nil {
				logger.LogWarn(ctx, common.LogText("claude credential auto-refresh: channel_id=%d name=%s refresh failed: %v", ch.Id, ch.Name, err))
				if IsPermanentClaudeOAuthError(err) {
					disableClaudeChannelWithRejectedRefreshToken(ch, fingerprint)
				}
				continue
			}

			refreshed++
			logger.LogInfo(ctx, common.LogText("claude credential auto-refresh: channel_id=%d name=%s refreshed, expires_at=%s", ch.Id, ch.Name, newKey.ExpiresAt))
		}
	}

	if refreshed > 0 {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogWarn(ctx, common.LogText("claude credential auto-refresh: InitChannelCache panic: %v", r))
				}
			}()
			model.InitChannelCache()
		}()
	}

	if common.DebugEnabled {
		logger.LogDebug(ctx, "claude credential auto-refresh: scanned=%d refreshed=%d", scanned, refreshed)
	}
	return scanned, refreshed
}

// disableClaudeChannelWithRejectedRefreshToken disables the channel only when
// the stored refresh token is still the one upstream rejected. Another node or
// an admin may have rotated or re-authorized the credential meanwhile; that
// channel stays enabled.
func disableClaudeChannelWithRejectedRefreshToken(ch *model.Channel, rejectedFingerprint string) {
	_, stored, err := loadClaudeOAuthChannel(ch.Id)
	if err != nil || refreshTokenFingerprint(stored.RefreshToken) != rejectedFingerprint {
		return
	}
	claudeRejectedRefreshTokens.Store(ch.Id, rejectedFingerprint)
	if ch.Status != common.ChannelStatusEnabled {
		return
	}
	DisableChannel(types.ChannelError{
		ChannelId:   ch.Id,
		ChannelName: ch.Name,
		AutoBan:     ch.GetAutoBan(),
	}, ClaudeOAuthRefreshRejectedReason)
}
