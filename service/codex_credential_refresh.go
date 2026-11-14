package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type CodexCredentialRefreshOptions struct {
	ResetCaches bool
	// Threshold, when positive, skips the token request while the stored key
	// expires later than Threshold from now; the background task uses it so
	// a caller queued behind another refresh reuses the renewed key. Zero
	// always refreshes.
	Threshold time.Duration
	// RejectedAccessToken, when set, is the access token an upstream just
	// refused; a stored key with another access token was renewed meanwhile
	// and is returned without a new token request.
	RejectedAccessToken string
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
	if opts.RejectedAccessToken != "" && oauthKey.AccessToken != "" && oauthKey.AccessToken != opts.RejectedAccessToken {
		if fromStash {
			writeBackStashedChannelKey(ctx, ch.Id)
		}
		return oauthKey, ch, nil
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
	if code, failed := codexRefreshPermanentlyFailed(ch.Id, usedRefreshToken); failed {
		return nil, nil, fmt.Errorf("codex channel: the refresh token was rejected (%s); authorize the account again", code)
	}
	res, err := RefreshCodexOAuthTokenWithProxy(refreshCtx, usedRefreshToken, ch.GetSetting().Proxy)
	if err != nil {
		if code, permanent := CodexPermanentRefreshErrorCode(err); permanent {
			handleCodexPermanentRefreshFailure(ch, usedRefreshToken, code, err)
		}
		return nil, nil, err
	}

	oauthKey.AccessToken = res.AccessToken
	oauthKey.RefreshToken = res.RefreshToken
	if res.IDToken != "" {
		oauthKey.IDToken = res.IDToken
		if accountID, ok := ExtractCodexAccountIDFromJWT(res.IDToken); ok {
			oauthKey.AccountID = accountID
		}
	}
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
	model.BumpSyncTopic(model.SyncTopicChannels)
	ch.Key = string(encoded)
	syncCodexSubscriptionFromIDToken(ch.Id, oauthKey.IDToken)

	if opts.ResetCaches {
		model.InitChannelCache()
	}

	return oauthKey, ch, nil
}

// codexRefreshFailures remembers, per channel, the refresh token the token
// endpoint rejected for good (see CodexPermanentRefreshErrorCode), so neither
// the background task nor a relay retries it. A new authorization brings a
// new refresh token and clears the block.
var codexRefreshFailures sync.Map // channel id -> codexRefreshFailure

type codexRefreshFailure struct {
	fingerprint string
	code        string
}

func codexRefreshPermanentlyFailed(channelID int, refreshToken string) (string, bool) {
	value, ok := codexRefreshFailures.Load(channelID)
	if !ok {
		return "", false
	}
	failure := value.(codexRefreshFailure)
	if failure.fingerprint != refreshTokenFingerprint(refreshToken) {
		codexRefreshFailures.CompareAndDelete(channelID, value)
		return "", false
	}
	return failure.code, true
}

// handleCodexPermanentRefreshFailure stops further refreshes with the
// rejected token and disables the channel with the reason, unless automatic
// disabling is turned off for it.
func handleCodexPermanentRefreshFailure(ch *model.Channel, refreshToken string, code string, err error) {
	codexRefreshFailures.Store(ch.Id, codexRefreshFailure{fingerprint: refreshTokenFingerprint(refreshToken), code: code})
	reason := fmt.Sprintf("Codex OAuth refresh failed permanently (%s); authorize the account again: %v", code, err)
	if ch.Status != common.ChannelStatusEnabled {
		common.SysLog(common.LogText("codex channel #%d: %s", ch.Id, reason))
		return
	}
	DisableChannel(*types.NewChannelError(ch.Id, ch.Type, ch.Name, ch.ChannelInfo.IsMultiKey, "", ch.GetAutoBan()), reason)
}
