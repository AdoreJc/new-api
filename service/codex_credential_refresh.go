package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

type CodexCredentialRefreshOptions struct {
	ResetCaches bool
	// Threshold, when positive, skips the token request while the stored key
	// expires later than Threshold from now; the background task uses it so
	// a caller queued behind another refresh reuses the renewed key. Zero
	// always refreshes.
	Threshold time.Duration
}

type CodexOAuthKey struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`

	AccountID   string `json:"account_id,omitempty"`
	LastRefresh string `json:"last_refresh,omitempty"`
	Email       string `json:"email,omitempty"`
	Type        string `json:"type,omitempty"`
	Expired     string `json:"expired,omitempty"`
}

func parseCodexOAuthKey(raw string) (*CodexOAuthKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("codex channel: empty oauth key")
	}
	var key CodexOAuthKey
	if err := common.Unmarshal([]byte(raw), &key); err != nil {
		return nil, errors.New("codex channel: invalid oauth key json")
	}
	return &key, nil
}

// RefreshCodexChannelCredential renews the channel's token set and writes it
// back, stashing the renewed key in memory when the write keeps failing (see
// persistRenewedChannelKey). Refreshes of one channel are serialised, and a
// stashed key replaces a stored key that carries the rotated-out refresh
// token.
func RefreshCodexChannelCredential(ctx context.Context, channelID int, opts CodexCredentialRefreshOptions) (*CodexOAuthKey, *model.Channel, error) {
	release, err := acquireChannelCredentialLock(ctx, channelID)
	if err != nil {
		return nil, nil, fmt.Errorf("codex channel: waiting for the credential lock: %w", err)
	}
	defer release()

	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	if ch == nil {
		return nil, nil, fmt.Errorf("channel not found")
	}
	if ch.Type != constant.ChannelTypeCodex {
		return nil, nil, fmt.Errorf("channel type is not Codex")
	}

	oauthKey, err := parseCodexOAuthKey(strings.TrimSpace(ch.Key))
	if err != nil {
		return nil, nil, err
	}
	fromStash := false
	if stashed, ok := resolveStashedChannelKey(ch.Id, oauthKey.RefreshToken); ok {
		if renewed, err := parseCodexOAuthKey(stashed); err == nil {
			ch.Key = stashed
			oauthKey = renewed
			fromStash = true
		}
	}
	if opts.Threshold > 0 {
		expiredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(oauthKey.Expired))
		if err == nil && time.Until(expiredAt) > opts.Threshold {
			if fromStash {
				writeBackStashedChannelKey(ctx, ch.Id)
			}
			return oauthKey, ch, nil
		}
	}
	if strings.TrimSpace(oauthKey.RefreshToken) == "" {
		return nil, nil, fmt.Errorf("codex channel: refresh_token is required to refresh credential")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	usedRefreshToken := oauthKey.RefreshToken
	res, err := RefreshCodexOAuthTokenWithProxy(refreshCtx, usedRefreshToken, ch.GetSetting().Proxy)
	if err != nil {
		return nil, nil, err
	}

	oauthKey.AccessToken = res.AccessToken
	oauthKey.RefreshToken = res.RefreshToken
	oauthKey.LastRefresh = time.Now().Format(time.RFC3339)
	oauthKey.Expired = res.ExpiresAt.Format(time.RFC3339)
	if strings.TrimSpace(oauthKey.Type) == "" {
		oauthKey.Type = "codex"
	}

	if strings.TrimSpace(oauthKey.AccountID) == "" {
		if accountID, ok := ExtractCodexAccountIDFromJWT(oauthKey.AccessToken); ok {
			oauthKey.AccountID = accountID
		}
	}
	if strings.TrimSpace(oauthKey.Email) == "" {
		if email, ok := ExtractEmailFromJWT(oauthKey.AccessToken); ok {
			oauthKey.Email = email
		}
	}

	encoded, err := common.Marshal(oauthKey)
	if err != nil {
		return nil, nil, err
	}

	persistRenewedChannelKey(ctx, ch.Id, usedRefreshToken, oauthKey.RefreshToken, string(encoded))
	ch.Key = string(encoded)

	if opts.ResetCaches {
		model.InitChannelCache()
	}

	return oauthKey, ch, nil
}
