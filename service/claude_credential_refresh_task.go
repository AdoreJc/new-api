package service

import (
	"context"
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
)

var (
	claudeCredentialRefreshOnce    sync.Once
	claudeCredentialRefreshRunning atomic.Bool
)

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

			runClaudeCredentialAutoRefreshOnce()
			for range ticker.C {
				runClaudeCredentialAutoRefreshOnce()
			}
		})
	})
}

func runClaudeCredentialAutoRefreshOnce() {
	if !claudeCredentialRefreshRunning.CompareAndSwap(false, true) {
		return
	}
	defer claudeCredentialRefreshRunning.Store(false)

	ctx := context.Background()
	now := time.Now()

	var refreshed int
	var scanned int

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
			return
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

			refreshCtx, cancel := context.WithTimeout(ctx, claudeCredentialRefreshTimeout)
			newKey, _, err := RefreshClaudeChannelCredential(refreshCtx, ch.Id, ClaudeCredentialRefreshOptions{ResetCaches: false})
			cancel()
			if err != nil {
				logger.LogWarn(ctx, common.LogText("claude credential auto-refresh: channel_id=%d name=%s refresh failed: %v", ch.Id, ch.Name, err))
				if IsPermanentClaudeOAuthError(err) && ch.Status == common.ChannelStatusEnabled {
					DisableChannel(types.ChannelError{
						ChannelId:   ch.Id,
						ChannelName: ch.Name,
						AutoBan:     ch.GetAutoBan(),
					}, "Claude OAuth refresh token was rejected; re-authorize the channel")
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
}
