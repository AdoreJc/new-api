package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
)

const (
	claudeOAuthUsagePath   = "/api/oauth/usage"
	claudeOAuthProfilePath = "/api/oauth/profile"
	claudeResetProgram     = "cedar_ember"
	claudeOAuthMaxBody     = 1 << 20
)

// ErrClaudeResetNotAvailable is returned by RedeemClaudeResetCredit when the
// fresh upstream status shows no redeemable grant.
var ErrClaudeResetNotAvailable = errors.New("no reset credit is redeemable right now")

var claudeResetGrantIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// ClaudeResetCredit is one grant projected for the console. It carries no
// upstream grant or organization identifiers; the server selects the grant
// again at redemption time.
type ClaudeResetCredit struct {
	Label            string             `json:"label"`
	ResetsLeft       int                `json:"resets_left"`
	StartsAt         *time.Time         `json:"starts_at,omitempty"`
	ExpiresAt        *time.Time         `json:"expires_at,omitempty"`
	Clears           []string           `json:"clears"`
	PercentUsed      map[string]float64 `json:"percent_used"`
	Blocking         []string           `json:"blocking"`
	UseRequiresLimit bool               `json:"use_requires_limit"`
	Redeemable       bool               `json:"redeemable"`
}

type ClaudeResetCredits struct {
	Eligible       bool                `json:"eligible"`
	AtLimit        bool                `json:"at_limit"`
	AvailableCount int                 `json:"available_count"`
	Credits        []ClaudeResetCredit `json:"credits"`
	CooldownUntil  *time.Time          `json:"cooldown_until,omitempty"`
	WeeklyResetsAt *time.Time          `json:"weekly_resets_at,omitempty"`
	FetchedAt      time.Time           `json:"fetched_at"`
}

// ClaudeResetOutcome is the sanitized redemption result.
type ClaudeResetOutcome struct {
	Outcome       string              `json:"outcome"`
	Reason        string              `json:"reason,omitempty"`
	Cleared       []string            `json:"cleared,omitempty"`
	CooldownUntil *time.Time          `json:"cooldown_until,omitempty"`
	Credits       *ClaudeResetCredits `json:"credits,omitempty"`
}

type claudeResetGrant struct {
	ID               string             `json:"id"`
	Label            string             `json:"label"`
	ResetsLeft       int                `json:"resets_left"`
	StartsAt         *time.Time         `json:"starts_at"`
	EndsAt           *time.Time         `json:"ends_at"`
	Clears           []string           `json:"clears"`
	Paused           bool               `json:"paused"`
	UsableNow        bool               `json:"usable_now"`
	UseRequiresLimit *bool              `json:"use_requires_limit"`
	PercentUsed      map[string]float64 `json:"percent_used"`
	Blocking         []string           `json:"blocking"`
}

type claudeResetBlock struct {
	Eligible       bool               `json:"eligible"`
	AtLimit        bool               `json:"at_limit"`
	Grants         []claudeResetGrant `json:"grants"`
	NextGrantID    string             `json:"next_grant_id"`
	CooldownUntil  *time.Time         `json:"cooldown_until"`
	WeeklyResetsAt *time.Time         `json:"weekly_resets_at"`
}

var (
	claudeResetKnownOutcomes = map[string]bool{
		"reset": true, "already_used": true, "not_limited": true, "cooldown": true, "ineligible": true, "unavailable": true,
	}
	claudeResetKnownWindows = map[string]bool{
		"five_hour": true, "seven_day": true, "seven_day_sonnet": true, "seven_day_overage_included": true,
	}
	claudeResetReasonPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

func setClaudeOAuthRequestHeaders(req *http.Request, accessToken string, cliClient bool) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", ClaudeOAuthBeta)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if cliClient {
		req.Header.Set("x-app", "cli")
		req.Header.Set("User-Agent", ClaudeCLIUserAgent())
		return
	}
	req.Header.Set("User-Agent", "claude-code/"+ClaudeCodeClientVersion())
}

func claudeOAuthEndpoint(baseURL string, path string) (string, error) {
	bu := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if bu == "" {
		return "", fmt.Errorf("empty baseURL")
	}
	return bu + path, nil
}

func doClaudeOAuthRequest(ctx context.Context, client *http.Client, req *http.Request) (int, []byte, error) {
	if client == nil {
		return 0, nil, fmt.Errorf("nil http client")
	}
	resp, err := noRedirectClient(client).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, claudeOAuthMaxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// FetchClaudeOAuthUsage returns the raw usage document: five_hour, seven_day,
// seven_day_sonnet and seven_day_overage_included windows with utilization
// percentages and resets_at timestamps.
func FetchClaudeOAuthUsage(ctx context.Context, client *http.Client, baseURL string, accessToken string) (int, []byte, error) {
	at := strings.TrimSpace(accessToken)
	if at == "" {
		return 0, nil, fmt.Errorf("empty accessToken")
	}
	endpoint, err := claudeOAuthEndpoint(baseURL, claudeOAuthUsagePath)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	setClaudeOAuthRequestHeaders(req, at, false)
	return doClaudeOAuthRequest(ctx, client, req)
}

// fetchClaudeResetBlock returns the raw cedar_ember block (nil when the
// account has none). It carries grant IDs and stays inside this package.
func fetchClaudeResetBlock(ctx context.Context, client *http.Client, baseURL string, accessToken string) (int, *claudeResetBlock, error) {
	endpoint, err := claudeOAuthEndpoint(baseURL, claudeOAuthUsagePath+"?cedar_ember=1&skip_spend=1")
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	setClaudeOAuthRequestHeaders(req, accessToken, true)
	status, body, err := doClaudeOAuthRequest(ctx, client, req)
	if err != nil {
		return status, nil, err
	}
	if status != http.StatusOK {
		return status, nil, nil
	}
	var envelope map[string]json.RawMessage
	if err := common.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return status, nil, errors.New("invalid reset status response")
	}
	if _, ok := envelope["error"]; ok {
		return status, nil, errors.New("reset status response carries an error")
	}
	raw, present := envelope["cedar_ember"]
	if !present || string(raw) == "null" {
		return status, nil, nil
	}
	var block claudeResetBlock
	if err := common.Unmarshal(raw, &block); err != nil {
		return status, nil, errors.New("invalid reset grants")
	}
	return status, &block, nil
}

// FetchClaudeResetCredits returns the projected reset-credit status. A non-2xx
// upstream status is returned with a nil result so the caller can retry after
// a token refresh.
func FetchClaudeResetCredits(ctx context.Context, client *http.Client, baseURL string, accessToken string) (int, *ClaudeResetCredits, error) {
	at := strings.TrimSpace(accessToken)
	if at == "" {
		return 0, nil, fmt.Errorf("empty accessToken")
	}
	status, block, err := fetchClaudeResetBlock(ctx, client, baseURL, at)
	if err != nil || status != http.StatusOK {
		return status, nil, err
	}
	return status, projectClaudeResetCredits(block, time.Now()), nil
}

func projectClaudeResetCredits(b *claudeResetBlock, now time.Time) *ClaudeResetCredits {
	r := &ClaudeResetCredits{Credits: []ClaudeResetCredit{}, FetchedAt: now.UTC()}
	if b == nil {
		return r
	}
	r.Eligible = b.Eligible
	r.AtLimit = b.AtLimit
	if b.CooldownUntil != nil && now.Before(*b.CooldownUntil) {
		r.CooldownUntil = b.CooldownUntil
	}
	r.WeeklyResetsAt = b.WeeklyResetsAt
	for _, g := range b.Grants {
		if !claudeResetGrantHeld(g, now) {
			continue
		}
		requires := g.UseRequiresLimit == nil || *g.UseRequiresLimit
		redeemable := claudeResetGrantRedeemable(b, g, now)
		used := map[string]float64{}
		for k, v := range g.PercentUsed {
			if v >= 0 && v <= 100 {
				used[k] = v
			}
		}
		clears := g.Clears
		if clears == nil {
			clears = []string{}
		}
		blocking := g.Blocking
		if blocking == nil {
			blocking = []string{}
		}
		r.Credits = append(r.Credits, ClaudeResetCredit{
			Label:            g.Label,
			ResetsLeft:       g.ResetsLeft,
			StartsAt:         g.StartsAt,
			ExpiresAt:        g.EndsAt,
			Clears:           clears,
			PercentUsed:      used,
			Blocking:         blocking,
			UseRequiresLimit: requires,
			Redeemable:       redeemable,
		})
		if redeemable {
			r.AvailableCount += g.ResetsLeft
		}
	}
	return r
}

// claudeResetGrantHeld reports whether a grant is a live, well-formed credit.
func claudeResetGrantHeld(g claudeResetGrant, now time.Time) bool {
	return claudeResetGrantIDPattern.MatchString(g.ID) && len(g.Clears) > 0 && g.ResetsLeft > 0 && !g.Paused &&
		(g.StartsAt == nil || !now.Before(*g.StartsAt)) && (g.EndsAt == nil || now.Before(*g.EndsAt))
}

// claudeResetGrantRedeemable is the single gate shared by the projection and
// the redemption: only the upstream next grant, usable now, unblocked, outside
// cooldown, and with its at-limit requirement satisfied.
func claudeResetGrantRedeemable(b *claudeResetBlock, g claudeResetGrant, now time.Time) bool {
	if b == nil || !claudeResetGrantHeld(g, now) {
		return false
	}
	requires := g.UseRequiresLimit == nil || *g.UseRequiresLimit
	return b.Eligible && g.UsableNow && g.ID == b.NextGrantID && (!requires || b.AtLimit) && len(g.Blocking) == 0 &&
		(b.CooldownUntil == nil || !now.Before(*b.CooldownUntil))
}

func fetchClaudeOAuthOrganizationUUID(ctx context.Context, client *http.Client, baseURL string, accessToken string) (int, string, error) {
	endpoint, err := claudeOAuthEndpoint(baseURL, claudeOAuthProfilePath)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", err
	}
	setClaudeOAuthRequestHeaders(req, accessToken, true)
	status, body, err := doClaudeOAuthRequest(ctx, client, req)
	if err != nil {
		return status, "", err
	}
	if status != http.StatusOK {
		return status, "", nil
	}
	var payload struct {
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		return status, "", errors.New("invalid oauth profile response")
	}
	parsed, err := uuid.Parse(strings.TrimSpace(payload.Organization.UUID))
	if err != nil || parsed == uuid.Nil {
		return status, "", errors.New("oauth profile has no organization uuid")
	}
	return status, parsed.String(), nil
}

// RedeemClaudeResetCredit consumes the upstream next grant. The grant is
// selected from a fresh status query, the organization comes from the OAuth
// profile, and the request is sent exactly once with a random request_id.
func RedeemClaudeResetCredit(ctx context.Context, client *http.Client, baseURL string, accessToken string) (int, *ClaudeResetOutcome, error) {
	at := strings.TrimSpace(accessToken)
	if at == "" {
		return 0, nil, fmt.Errorf("empty accessToken")
	}
	status, block, err := fetchClaudeResetBlock(ctx, client, baseURL, at)
	if err != nil || status != http.StatusOK {
		return status, nil, err
	}
	now := time.Now()
	grantID := ""
	if block != nil {
		for _, g := range block.Grants {
			if g.ID == block.NextGrantID && claudeResetGrantRedeemable(block, g, now) {
				grantID = g.ID
				break
			}
		}
	}
	if grantID == "" {
		return status, nil, ErrClaudeResetNotAvailable
	}

	status, orgUUID, err := fetchClaudeOAuthOrganizationUUID(ctx, client, baseURL, at)
	if err != nil {
		return status, nil, err
	}
	if status != http.StatusOK {
		return status, nil, fmt.Errorf("oauth profile upstream status: %d", status)
	}

	requestIDBytes, err := randomBytes(32)
	if err != nil {
		return 0, nil, err
	}
	body, err := common.Marshal(map[string]string{
		"program":    claudeResetProgram,
		"grant_id":   grantID,
		"request_id": hex.EncodeToString(requestIDBytes),
	})
	if err != nil {
		return 0, nil, err
	}
	endpoint, err := claudeOAuthEndpoint(baseURL, "/api/organizations/"+orgUUID+"/reset_rate_limits")
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	setClaudeOAuthRequestHeaders(req, at, true)
	req.Header.Set("Content-Type", "application/json")
	status, raw, err := doClaudeOAuthRequest(ctx, client, req)
	if err != nil {
		return status, nil, err
	}
	if status != http.StatusOK {
		return status, nil, fmt.Errorf("reset upstream status: %d", status)
	}
	var result struct {
		Result        string     `json:"result"`
		Reason        string     `json:"reason"`
		Cleared       []string   `json:"cleared"`
		CooldownUntil *time.Time `json:"cooldown_until"`
	}
	if err := common.Unmarshal(raw, &result); err != nil {
		return status, nil, errors.New("invalid reset response")
	}
	outcome := &ClaudeResetOutcome{Outcome: "unknown", CooldownUntil: result.CooldownUntil}
	if claudeResetKnownOutcomes[result.Result] {
		outcome.Outcome = result.Result
	}
	if claudeResetReasonPattern.MatchString(result.Reason) {
		outcome.Reason = result.Reason
	}
	for _, w := range result.Cleared {
		if claudeResetKnownWindows[w] {
			outcome.Cleared = append(outcome.Cleared, w)
		}
	}
	if freshStatus, fresh, freshErr := fetchClaudeResetBlock(ctx, client, baseURL, at); freshErr == nil && freshStatus == http.StatusOK {
		outcome.Credits = projectClaudeResetCredits(fresh, time.Now())
	}
	return status, outcome, nil
}
