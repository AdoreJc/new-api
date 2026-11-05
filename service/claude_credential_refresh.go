package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

type ClaudeCredentialRefreshOptions struct {
	ResetCaches bool
}

// claudeRefreshLocks serialises refreshes per channel so concurrent relay
// requests never send the same refresh token twice.
var claudeRefreshLocks sync.Map

func claudeRefreshLock(channelID int) *sync.Mutex {
	lock, _ := claudeRefreshLocks.LoadOrStore(channelID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func loadClaudeOAuthChannel(channelID int) (*model.Channel, *ClaudeOAuthKey, error) {
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	if ch == nil {
		return nil, nil, fmt.Errorf("channel not found")
	}
	if ch.Type != constant.ChannelTypeClaudeOAuth {
		return nil, nil, fmt.Errorf("channel type is not Claude Subscription (OAuth)")
	}
	if ch.ChannelInfo.IsMultiKey {
		return nil, nil, fmt.Errorf("multi-key channel is not supported")
	}
	key, err := ParseClaudeOAuthKey(ch.Key)
	if err != nil {
		return nil, nil, err
	}
	return ch, key, nil
}

// RefreshClaudeChannelCredential renews the stored token set with the refresh
// token and writes the new key back to the channel. A permanent OAuth error
// (IsPermanentClaudeOAuthError) means the credential must be re-authorized.
func RefreshClaudeChannelCredential(ctx context.Context, channelID int, opts ClaudeCredentialRefreshOptions) (*ClaudeOAuthKey, *model.Channel, error) {
	lock := claudeRefreshLock(channelID)
	lock.Lock()
	defer lock.Unlock()

	ch, key, err := loadClaudeOAuthChannel(channelID)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(key.RefreshToken) == "" {
		return nil, ch, fmt.Errorf("claude oauth channel: refresh_token is required to refresh credential")
	}

	client, err := GetClaudeOAuthHTTPClient(ch.GetSetting().Proxy)
	if err != nil {
		return nil, ch, err
	}
	res, err := RefreshClaudeOAuthToken(ctx, client, key.RefreshToken)
	if err != nil {
		return nil, ch, err
	}

	now := time.Now()
	refreshed := BuildClaudeOAuthKey(res, now)
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = key.RefreshToken
	}
	if refreshed.Scope == "" {
		refreshed.Scope = key.Scope
	}
	if refreshed.TokenType == "" {
		refreshed.TokenType = key.TokenType
	}
	if refreshed.OrgUUID == "" {
		refreshed.OrgUUID = key.OrgUUID
	}
	if refreshed.AccountUUID == "" {
		refreshed.AccountUUID = key.AccountUUID
	}
	if refreshed.Email == "" {
		refreshed.Email = key.Email
	}

	encoded, err := common.Marshal(refreshed)
	if err != nil {
		return nil, ch, err
	}
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("key", string(encoded)).Error; err != nil {
		return nil, ch, err
	}
	if opts.ResetCaches {
		model.InitChannelCache()
	}
	return refreshed, ch, nil
}

func claudeOAuthKeyNeedsRefresh(key *ClaudeOAuthKey, now time.Time, leeway time.Duration) bool {
	expiresAt := key.ExpiresAtTime()
	if expiresAt.IsZero() {
		return false
	}
	return expiresAt.Sub(now) <= leeway
}

// EnsureClaudeChannelAccessToken returns a key whose access token stays valid
// for at least claudeOAuthRequestLeeway, refreshing and persisting it first
// when needed. When the refresh fails but the current token is still valid,
// the current key is returned so the request can proceed.
func EnsureClaudeChannelAccessToken(ctx context.Context, channelID int, key *ClaudeOAuthKey) (*ClaudeOAuthKey, error) {
	if key == nil {
		return nil, fmt.Errorf("claude oauth channel: empty oauth key")
	}
	now := time.Now()
	if !claudeOAuthKeyNeedsRefresh(key, now, claudeOAuthRequestLeeway) {
		return key, nil
	}
	expired := !key.ExpiresAtTime().After(now)
	if channelID <= 0 || strings.TrimSpace(key.RefreshToken) == "" {
		if expired {
			return nil, fmt.Errorf("claude oauth channel: access token expired and no refresh token is available")
		}
		return key, nil
	}

	// Another request may have refreshed the channel while this one waited
	// for the lock; re-read the stored key before refreshing again.
	if _, stored, err := loadClaudeOAuthChannel(channelID); err == nil && !claudeOAuthKeyNeedsRefresh(stored, now, claudeOAuthRequestLeeway) {
		return stored, nil
	}

	refreshed, _, err := RefreshClaudeChannelCredential(ctx, channelID, ClaudeCredentialRefreshOptions{ResetCaches: true})
	if err != nil {
		if expired {
			return nil, fmt.Errorf("claude oauth channel: access token expired and refresh failed: %w", err)
		}
		logger.LogWarn(ctx, common.LogText("claude oauth: channel_id=%d inline refresh failed, using current token: %v", channelID, err))
		return key, nil
	}
	return refreshed, nil
}
