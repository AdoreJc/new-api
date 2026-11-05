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
	// Force refreshes even when the stored token is not near expiry; the
	// manual endpoint and the usage 401 retry use it. Without it the refresh
	// is skipped when another caller already renewed the key.
	Force bool
	// Leeway is the "near expiry" window used when Force is false; zero means
	// claudeOAuthRequestLeeway.
	Leeway time.Duration
}

// claudeRefreshLocks serialises refreshes per channel so concurrent relay
// requests never send the same refresh token twice. Each entry is a one-slot
// channel so waiting can honour the caller's context.
var claudeRefreshLocks sync.Map

func acquireClaudeRefreshLock(ctx context.Context, channelID int) (func(), error) {
	entry, _ := claudeRefreshLocks.LoadOrStore(channelID, make(chan struct{}, 1))
	slot := entry.(chan struct{})
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
// token and writes the new key back to the channel. The stored key is re-read
// under the per-channel lock, so a caller that queued behind another refresh
// gets the renewed key without a second token request unless opts.Force is
// set. A permanent OAuth error (IsPermanentClaudeOAuthError) means the
// credential must be re-authorized.
func RefreshClaudeChannelCredential(ctx context.Context, channelID int, opts ClaudeCredentialRefreshOptions) (*ClaudeOAuthKey, *model.Channel, error) {
	release, err := acquireClaudeRefreshLock(ctx, channelID)
	if err != nil {
		return nil, nil, fmt.Errorf("claude oauth channel: waiting for the credential lock: %w", err)
	}
	defer release()

	ch, key, err := loadClaudeOAuthChannel(channelID)
	if err != nil {
		return nil, nil, err
	}
	if !opts.Force {
		leeway := opts.Leeway
		if leeway <= 0 {
			leeway = claudeOAuthRequestLeeway
		}
		if !claudeOAuthKeyNeedsRefresh(key, time.Now(), leeway) {
			return key, ch, nil
		}
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
		if IsClientLevelClaudeOAuthError(err) {
			logger.LogError(ctx, common.LogText("claude oauth: channel_id=%d token endpoint rejected the OAuth client, not this credential; check the client id and upstream status, the channel stays enabled: %v", ch.Id, err))
		}
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

// claudeOAuthKeyNeedsRefresh reports whether the access token expires within
// leeway. A key without a parseable expires_at (pasted by hand) counts as
// needing a refresh so the background task records a real expiry on its next
// tick; the relay path never refreshes such a key inline (see
// EnsureClaudeChannelAccessToken).
func claudeOAuthKeyNeedsRefresh(key *ClaudeOAuthKey, now time.Time, leeway time.Duration) bool {
	expiresAt := key.ExpiresAtTime()
	if expiresAt.IsZero() {
		return true
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
	expiresAt := key.ExpiresAtTime()
	// An unknown expiry is not refreshed on the request path: a failing token
	// endpoint would otherwise cost every request a token call before it
	// falls back to the still valid access token. The background task
	// renews such a key once and stores a real expiry.
	if expiresAt.IsZero() || !claudeOAuthKeyNeedsRefresh(key, now, claudeOAuthRequestLeeway) {
		return key, nil
	}
	expired := !expiresAt.After(now)
	if channelID <= 0 || strings.TrimSpace(key.RefreshToken) == "" {
		if expired {
			return nil, fmt.Errorf("claude oauth channel: access token expired and no refresh token is available")
		}
		return key, nil
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
