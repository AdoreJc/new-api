package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

// Automatic reset-credit redemption for Codex and Claude Subscription (OAuth)
// channels. It runs the same upstream redemption an administrator can start
// by hand from the usage dialog, when a condition tree on the channel matches.
//
// The setting lives in other_info.auto_reset_credit (ChannelAutoResetConfig).
// The channel edit form never sends other_info and the generic channel update
// keeps this key, so only the dedicated endpoint changes it. The engine never
// writes the setting back except to remove fired or expired alarm times, which
// it does with a compare-and-swap on the freshly read row.
//
// The runtime state, the redemption lease and the redemption history live in
// the channel_auto_reset_states table (model.ChannelAutoResetState). Manual
// and subscriber redemptions take the same lease and record themselves there,
// so every redemption of a channel counts against the minimum interval.

const (
	ChannelAutoResetConfigKey = "auto_reset_credit"

	ChannelAutoResetCondFiveHour = "five_hour_used_pct"
	ChannelAutoResetCondWeekly   = "weekly_used_pct"
	ChannelAutoResetCondAtTime   = "at_time"
	ChannelAutoResetCondExpiring = "credit_expires_within"

	// ChannelAutoResetAlarmGrace is how long after its time an alarm can still
	// fire. The scheduler ticks every minute, so an alarm normally fires within
	// a minute; the rest of the grace rides out a node restart or a usage API
	// outage. An alarm missed for longer is dropped as expired.
	ChannelAutoResetAlarmGrace = 30 * time.Minute

	channelAutoResetMaxDepth  = 3
	channelAutoResetMaxNodes  = 20
	channelAutoResetMaxAlarms = 20

	channelAutoResetDefaultMinIntervalMinutes = 60
	channelAutoResetDefaultMaxPerDay          = 2
	channelAutoResetDefaultMaxPerWeek         = 5
	channelAutoResetDefaultNearResetMinutes   = 15

	// channelAutoResetUsageMaxAge bounds how often evaluation asks for usage:
	// within it the last snapshot is reused, so the per-minute tick does not
	// query the upstream usage API every minute.
	channelAutoResetUsageMaxAge = 5 * time.Minute
	// channelAutoResetCreditsMaxAge is the same bound for the reset-credit
	// list read by credit_expires_within.
	channelAutoResetCreditsMaxAge = 15 * time.Minute
	// channelAutoResetPause stops evaluation of a channel whose rule matched
	// but which holds no redeemable credit, or whose credentials upstream
	// rejected; it is not a failure and does not grow.
	channelAutoResetPause       = 30 * time.Minute
	channelAutoResetBackoffBase = 5 * time.Minute
	channelAutoResetBackoffMax  = 6 * time.Hour
	// ChannelRedemptionLeaseTTL is the redemption lease. It is renewed right
	// before the redemption request, which is bounded by
	// channelAutoResetRedeemTimeout, so it outlasts the request. A holder that
	// crashes leaves its in-flight record behind, which the guards count.
	ChannelRedemptionLeaseTTL     = 5 * time.Minute
	channelAutoResetRedeemTimeout = 150 * time.Second
	// channelAutoResetReadTimeout bounds the usage and credit reads made while
	// holding the lease.
	channelAutoResetReadTimeout  = 90 * time.Second
	channelAutoResetTickInterval = time.Minute
	channelAutoResetFiredKeep    = 48 * time.Hour
	channelAutoResetHistoryKeep  = 7 * 24 * time.Hour
)

// Decision reasons. Each is also an English key in the frontend locales.
const (
	AutoResetReasonDisabled        = "disabled"
	AutoResetReasonUnsupported     = "unsupported_channel"
	AutoResetReasonChannelDisabled = "channel_disabled"
	AutoResetReasonNoRule          = "no_rule"
	AutoResetReasonUsageMissing    = "usage_unavailable"
	AutoResetReasonNotMatched      = "not_matched"
	AutoResetReasonBackoff         = "backoff"
	AutoResetReasonMinInterval     = "min_interval"
	AutoResetReasonDailyCap        = "daily_cap"
	AutoResetReasonWeeklyCap       = "weekly_cap"
	AutoResetReasonResetsSoon      = "window_resets_soon"
	AutoResetReasonNoCredit        = "no_credit"
	AutoResetReasonBusy            = "busy"
	AutoResetReasonWouldRedeem     = "would_redeem"
	AutoResetReasonRedeemed        = "redeemed"
	AutoResetReasonFailed          = "failed"
	AutoResetReasonInFlight        = "in_flight"
	AutoResetReasonInvalidSetting  = "invalid_setting"
	AutoResetReasonCredential      = "credential_problem"
)

var channelAutoResetWeeklyWindows = map[int][]string{
	constant.ChannelTypeCodex:       {"seven_day"},
	constant.ChannelTypeClaudeOAuth: {"seven_day", "seven_day_sonnet", "seven_day_overage_included"},
}

// ChannelAutoResetRule is a node of the condition tree: a group when Op is
// set ("and" | "or"), a condition otherwise.
type ChannelAutoResetRule struct {
	Op       string                 `json:"op,omitempty"`
	Children []ChannelAutoResetRule `json:"children,omitempty"`

	Type string `json:"type,omitempty"`
	// five_hour_used_pct / weekly_used_pct: matches at or above, 0-100
	Threshold float64 `json:"threshold,omitempty"`
	// weekly_used_pct: usage window key (seven_day, seven_day_sonnet,
	// seven_day_overage_included)
	Window string `json:"window,omitempty"`
	// credit_expires_within: matches at or below this many hours
	Hours float64 `json:"hours,omitempty"`
	// at_time: alarm times, RFC 3339 UTC
	Times []string `json:"times,omitempty"`
}

type ChannelAutoResetConfig struct {
	Enabled bool                  `json:"enabled"`
	Rule    *ChannelAutoResetRule `json:"rule,omitempty"`
	// Shortest time between two automatic redemptions
	MinIntervalMinutes int `json:"min_interval_minutes"`
	// Redemption caps over the last 24 hours and the last 7 days
	MaxPerDay  int `json:"max_per_day"`
	MaxPerWeek int `json:"max_per_week"`
	// No redemption when every window the matched conditions look at resets
	// on its own within this many minutes; 0 turns the check off.
	SkipIfResetWithinMinutes int `json:"skip_if_reset_within_minutes"`
	// A matched alarm / expiring-credit condition overrides that check.
	AlarmIgnoresResetSoon    bool `json:"alarm_ignores_reset_soon"`
	ExpiringIgnoresResetSoon bool `json:"expiring_ignores_reset_soon"`
	// Administrator who last saved the setting; automatic redemptions are
	// audited under this account.
	UpdatedBy     int   `json:"updated_by,omitempty"`
	UpdatedByRole int   `json:"updated_by_role,omitempty"`
	UpdatedAt     int64 `json:"updated_at,omitempty"`
}

// ChannelAutoResetHit is one matched condition with the value that matched.
type ChannelAutoResetHit struct {
	Type      string  `json:"type"`
	Window    string  `json:"window,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
	Hours     float64 `json:"hours,omitempty"`
	Value     float64 `json:"value,omitempty"`
	Alarm     string  `json:"alarm,omitempty"`
}

func (h ChannelAutoResetHit) String() string {
	switch h.Type {
	case ChannelAutoResetCondFiveHour:
		return fmt.Sprintf("five_hour_used_pct %.1f>=%.1f", h.Value, h.Threshold)
	case ChannelAutoResetCondWeekly:
		return fmt.Sprintf("%s_used_pct %.1f>=%.1f", h.Window, h.Value, h.Threshold)
	case ChannelAutoResetCondExpiring:
		return fmt.Sprintf("credit_expires_within %.1fh<=%.1fh", h.Value, h.Hours)
	case ChannelAutoResetCondAtTime:
		return "at_time " + h.Alarm
	}
	return h.Type
}

// ChannelAutoResetState is the console view of model.ChannelAutoResetState.
type ChannelAutoResetState struct {
	// Automatic redemptions that may have used a credit, last 7 days
	RedeemedAt []int64 `json:"redeemed_at,omitempty"`
	// Last redemption from any source (auto, manual, subscription) that may
	// have used a credit; the minimum interval counts from it.
	LastRedeemAt     int64  `json:"last_redeem_at,omitempty"`
	LastRedeemSource string `json:"last_redeem_source,omitempty"`
	InFlightSince    int64  `json:"in_flight_since,omitempty"`
	InFlightSource   string `json:"in_flight_source,omitempty"`
	// The in-flight record is older than any request could run: the request
	// was interrupted and its outcome is unknown. It stays counted.
	InFlightInterrupted bool                  `json:"in_flight_interrupted,omitempty"`
	LastAttemptAt       int64                 `json:"last_attempt_at,omitempty"`
	LastResult          string                `json:"last_result,omitempty"`
	LastSuccess         bool                  `json:"last_success,omitempty"`
	LastHits            []ChannelAutoResetHit `json:"last_hits,omitempty"`
	FailCount           int                   `json:"fail_count,omitempty"`
	BackoffUntil        int64                 `json:"backoff_until,omitempty"`
	FiredAlarms         []string              `json:"fired_alarms,omitempty"`
	LastSkipReason      string                `json:"last_skip_reason,omitempty"`
	LastSkipAt          int64                 `json:"last_skip_at,omitempty"`
}

type ChannelAutoResetWindow struct {
	// 0-100
	Utilization float64 `json:"utilization"`
	// Zero when unknown
	ResetsAt time.Time `json:"resets_at"`
}

type ChannelAutoResetUsage struct {
	Windows   map[string]ChannelAutoResetWindow `json:"windows"`
	FetchedAt time.Time                         `json:"fetched_at"`
	// FromUpstream is true when the usage API was asked (false: passive
	// samples only)
	FromUpstream bool `json:"from_upstream"`
}

type ChannelAutoResetCredits struct {
	// Credits redeemable now
	Available int `json:"available"`
	// Earliest expiry among the held credits, nil when none expire
	EarliestExpiry *time.Time `json:"earliest_expiry,omitempty"`
	FetchedAt      time.Time  `json:"fetched_at"`
}

// ChannelAutoResetRedeemResult is the outcome of one redemption request.
type ChannelAutoResetRedeemResult struct {
	// Short machine-readable result, e.g. reset, consumed, cooldown,
	// not_available, outcome_unknown, upstream_status_429
	Result  string
	Success bool
	// MaybeConsumed is true when upstream may have used a credit without a
	// definite answer; it counts against the caps like a success.
	MaybeConsumed bool
	// CredentialProblem is true when the request was never sent or upstream
	// rejected the credentials; no credit was used and it is not retried with
	// backoff, only paused.
	CredentialProblem bool
	// RetryAfter, when set, is the earliest time upstream accepts another
	// redemption (Claude cooldown).
	RetryAfter time.Time

	// The raw answer, for the manual endpoints that relay it.
	Err     error
	Status  int
	Body    []byte
	Payload any
}

// ChannelAutoResetProvider is the upstream side, implemented by the
// controller with the same calls the usage dialog uses; tests replace it.
type ChannelAutoResetProvider interface {
	// LoadUsage may ask the usage API only when allowUpstream is true;
	// otherwise it answers from the passive samples.
	LoadUsage(ctx context.Context, ch *model.Channel, allowUpstream bool) (*ChannelAutoResetUsage, error)
	LoadCredits(ctx context.Context, ch *model.Channel) (*ChannelAutoResetCredits, error)
	Redeem(ctx context.Context, ch *model.Channel) ChannelAutoResetRedeemResult
}

// ChannelAutoResetDecision is what one evaluation found and did.
type ChannelAutoResetDecision struct {
	ChannelId   int                               `json:"channel_id"`
	EvaluatedAt int64                             `json:"evaluated_at"`
	DryRun      bool                              `json:"dry_run"`
	Matched     bool                              `json:"matched"`
	Hits        []ChannelAutoResetHit             `json:"hits"`
	Reason      string                            `json:"reason"`
	Result      string                            `json:"result,omitempty"`
	Usage       map[string]ChannelAutoResetWindow `json:"usage,omitempty"`
	Credits     *ChannelAutoResetCredits          `json:"credits,omitempty"`
	// Earliest time the guards allow the next redemption (0: now)
	NextAllowedAt int64 `json:"next_allowed_at,omitempty"`
}

// ---- Setting ----

func DefaultChannelAutoResetConfig() ChannelAutoResetConfig {
	return ChannelAutoResetConfig{
		MinIntervalMinutes:       channelAutoResetDefaultMinIntervalMinutes,
		MaxPerDay:                channelAutoResetDefaultMaxPerDay,
		MaxPerWeek:               channelAutoResetDefaultMaxPerWeek,
		SkipIfResetWithinMinutes: channelAutoResetDefaultNearResetMinutes,
		AlarmIgnoresResetSoon:    true,
		ExpiringIgnoresResetSoon: true,
	}
}

func decodeChannelOtherInfoEntry(ch *model.Channel, key string, target any) bool {
	if ch == nil {
		return false
	}
	raw, ok := ch.GetOtherInfo()[key]
	if !ok || raw == nil {
		return false
	}
	encoded, err := common.Marshal(raw)
	return err == nil && common.Unmarshal(encoded, target) == nil
}

// LoadChannelAutoResetConfig returns the stored setting, or the defaults
// (switched off) when the channel has none or it is malformed.
func LoadChannelAutoResetConfig(ch *model.Channel) ChannelAutoResetConfig {
	cfg := DefaultChannelAutoResetConfig()
	if !decodeChannelOtherInfoEntry(ch, ChannelAutoResetConfigKey, &cfg) {
		return DefaultChannelAutoResetConfig()
	}
	return cfg
}

func IsChannelAutoResetSupported(channelType int) bool {
	_, ok := channelAutoResetWeeklyWindows[channelType]
	return ok
}

// NormalizeChannelAutoResetConfig validates a setting for channelType and
// returns it with alarm times in UTC and alarms already past their grace
// dropped. Errors are web console messages.
func NormalizeChannelAutoResetConfig(cfg ChannelAutoResetConfig, channelType int, now time.Time) (ChannelAutoResetConfig, error) {
	weekly, ok := channelAutoResetWeeklyWindows[channelType]
	if !ok {
		return cfg, common.NewMessage("This channel type does not support reset credits")
	}
	if cfg.MinIntervalMinutes < 10 || cfg.MinIntervalMinutes > 7*24*60 {
		return cfg, common.NewMessage("The minimum interval must be between 10 minutes and 7 days")
	}
	if cfg.MaxPerDay < 1 || cfg.MaxPerDay > 20 || cfg.MaxPerWeek < 1 || cfg.MaxPerWeek > 100 {
		return cfg, common.NewMessage("The daily cap must be 1-20 and the weekly cap 1-100")
	}
	if cfg.SkipIfResetWithinMinutes < 0 || cfg.SkipIfResetWithinMinutes > 24*60 {
		return cfg, common.NewMessage("The natural reset margin must be between 0 and 1440 minutes")
	}
	if cfg.Rule == nil {
		if cfg.Enabled {
			return cfg, common.NewMessage("Add at least one condition before turning on automatic reset")
		}
		return cfg, nil
	}
	nodes, alarms, conditions := 0, 0, 0
	var walk func(node *ChannelAutoResetRule, depth int) error
	walk = func(node *ChannelAutoResetRule, depth int) error {
		nodes++
		if depth > channelAutoResetMaxDepth || nodes > channelAutoResetMaxNodes {
			return common.NewMessage("The condition tree may nest at most {{depth}} levels and hold at most {{nodes}} nodes", map[string]any{"depth": channelAutoResetMaxDepth, "nodes": channelAutoResetMaxNodes})
		}
		if node.Op != "" {
			if node.Op != "and" && node.Op != "or" {
				return common.NewMessage("A condition group must be AND or OR")
			}
			node.Type, node.Threshold, node.Window, node.Hours, node.Times = "", 0, "", 0, nil
			for i := range node.Children {
				if err := walk(&node.Children[i], depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		node.Children = nil
		conditions++
		switch node.Type {
		case ChannelAutoResetCondFiveHour, ChannelAutoResetCondWeekly:
			if math.IsNaN(node.Threshold) || node.Threshold <= 0 || node.Threshold > 100 {
				return common.NewMessage("A usage threshold must be greater than 0 and at most 100")
			}
			node.Hours, node.Times = 0, nil
			if node.Type == ChannelAutoResetCondFiveHour {
				node.Window = ""
				return nil
			}
			if node.Window == "" {
				node.Window = "seven_day"
			}
			if !slices.Contains(weekly, node.Window) {
				return common.NewMessage("Unknown weekly usage window: {{window}}", map[string]any{"window": node.Window})
			}
		case ChannelAutoResetCondExpiring:
			if math.IsNaN(node.Hours) || node.Hours <= 0 || node.Hours > 24*30 {
				return common.NewMessage("The credit expiry window must be greater than 0 and at most 720 hours")
			}
			node.Threshold, node.Window, node.Times = 0, "", nil
		case ChannelAutoResetCondAtTime:
			node.Threshold, node.Window, node.Hours = 0, "", 0
			kept := make([]string, 0, len(node.Times))
			for _, raw := range node.Times {
				at, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
				if err != nil {
					return common.NewMessage("Invalid alarm time: {{time}}", map[string]any{"time": raw})
				}
				if now.Sub(at) > ChannelAutoResetAlarmGrace {
					continue
				}
				formatted := at.UTC().Format(time.RFC3339)
				if !slices.Contains(kept, formatted) {
					kept = append(kept, formatted)
				}
			}
			slices.Sort(kept)
			node.Times = kept
			alarms += len(kept)
			if alarms > channelAutoResetMaxAlarms {
				return common.NewMessage("At most {{count}} alarm times are allowed", map[string]any{"count": channelAutoResetMaxAlarms})
			}
		default:
			return common.NewMessage("Unknown condition type: {{type}}", map[string]any{"type": node.Type})
		}
		return nil
	}
	rule := cloneChannelAutoResetRule(*cfg.Rule)
	if err := walk(&rule, 1); err != nil {
		return cfg, err
	}
	cfg.Rule = &rule
	if cfg.Enabled && conditions == 0 {
		return cfg, common.NewMessage("Add at least one condition before turning on automatic reset")
	}
	return cfg, nil
}

func cloneChannelAutoResetRule(node ChannelAutoResetRule) ChannelAutoResetRule {
	node.Times = slices.Clone(node.Times)
	if node.Children != nil {
		children := make([]ChannelAutoResetRule, len(node.Children))
		for i, child := range node.Children {
			children[i] = cloneChannelAutoResetRule(child)
		}
		node.Children = children
	}
	return node
}

// ---- Rule evaluation ----

type channelAutoResetFacts struct {
	now     time.Time
	usage   *ChannelAutoResetUsage
	credits *ChannelAutoResetCredits
	fired   []string
}

// window returns the utilization of a usage window; a window past its reset
// time counts as 0 (it has reset since the snapshot).
func (f channelAutoResetFacts) window(key string) (ChannelAutoResetWindow, bool) {
	if f.usage == nil {
		return ChannelAutoResetWindow{}, false
	}
	w, ok := f.usage.Windows[key]
	if !ok {
		return w, false
	}
	if !w.ResetsAt.IsZero() && !f.now.Before(w.ResetsAt) {
		w.Utilization = 0
	}
	return w, true
}

// dueAlarms returns the alarm times of node that are due at now and have not
// fired yet.
func (f channelAutoResetFacts) dueAlarms(node *ChannelAutoResetRule) []string {
	var due []string
	for _, raw := range node.Times {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil || f.now.Before(at) || f.now.Sub(at) > ChannelAutoResetAlarmGrace {
			continue
		}
		if slices.Contains(f.fired, at.UTC().Format(time.RFC3339)) {
			continue
		}
		due = append(due, raw)
	}
	return due
}

// evaluateChannelAutoResetRule reports whether node matches and the matched
// conditions. Every child is evaluated so all matching conditions are listed;
// a condition without data does not match; an empty group does not match.
func evaluateChannelAutoResetRule(node *ChannelAutoResetRule, f channelAutoResetFacts) (bool, []ChannelAutoResetHit) {
	if node == nil {
		return false, nil
	}
	if node.Op != "" {
		if len(node.Children) == 0 {
			return false, nil
		}
		var hits []ChannelAutoResetHit
		matchedAll, matchedAny := true, false
		for i := range node.Children {
			ok, childHits := evaluateChannelAutoResetRule(&node.Children[i], f)
			if ok {
				matchedAny = true
				hits = append(hits, childHits...)
			} else {
				matchedAll = false
			}
		}
		if node.Op == "and" {
			if !matchedAll {
				return false, nil
			}
			return true, hits
		}
		return matchedAny, hits
	}
	switch node.Type {
	case ChannelAutoResetCondFiveHour, ChannelAutoResetCondWeekly:
		key := "five_hour"
		if node.Type == ChannelAutoResetCondWeekly {
			key = node.Window
			if key == "" {
				key = "seven_day"
			}
		}
		w, ok := f.window(key)
		if !ok || w.Utilization < node.Threshold {
			return false, nil
		}
		hit := ChannelAutoResetHit{Type: node.Type, Threshold: node.Threshold, Value: w.Utilization}
		if node.Type == ChannelAutoResetCondWeekly {
			hit.Window = key
		}
		return true, []ChannelAutoResetHit{hit}
	case ChannelAutoResetCondExpiring:
		if f.credits == nil || f.credits.EarliestExpiry == nil {
			return false, nil
		}
		left := f.credits.EarliestExpiry.Sub(f.now).Hours()
		if left < 0 || left > node.Hours {
			return false, nil
		}
		return true, []ChannelAutoResetHit{{Type: node.Type, Hours: node.Hours, Value: math.Round(left*10) / 10}}
	case ChannelAutoResetCondAtTime:
		due := f.dueAlarms(node)
		if len(due) == 0 {
			return false, nil
		}
		hits := make([]ChannelAutoResetHit, 0, len(due))
		for _, at := range due {
			hits = append(hits, ChannelAutoResetHit{Type: node.Type, Alarm: at})
		}
		return true, hits
	}
	return false, nil
}

func channelAutoResetRuleHas(node *ChannelAutoResetRule, types ...string) bool {
	if node == nil {
		return false
	}
	if node.Op == "" {
		return slices.Contains(types, node.Type)
	}
	for i := range node.Children {
		if channelAutoResetRuleHas(&node.Children[i], types...) {
			return true
		}
	}
	return false
}

// collectChannelAutoResetAlarms lists every alarm of the tree.
func collectChannelAutoResetAlarms(node *ChannelAutoResetRule, into []string) []string {
	if node == nil {
		return into
	}
	if node.Op == "" {
		if node.Type == ChannelAutoResetCondAtTime {
			into = append(into, node.Times...)
		}
		return into
	}
	for i := range node.Children {
		into = collectChannelAutoResetAlarms(&node.Children[i], into)
	}
	return into
}

// pruneChannelAutoResetAlarms removes fired alarms and alarms past their
// grace from the tree and reports whether anything was removed.
func pruneChannelAutoResetAlarms(node *ChannelAutoResetRule, fired []string, now time.Time) bool {
	if node == nil {
		return false
	}
	changed := false
	if node.Op == "" {
		if node.Type != ChannelAutoResetCondAtTime {
			return false
		}
		kept := node.Times[:0:0]
		for _, raw := range node.Times {
			at, err := time.Parse(time.RFC3339, raw)
			if err != nil || now.Sub(at) > ChannelAutoResetAlarmGrace || slices.Contains(fired, at.UTC().Format(time.RFC3339)) {
				changed = true
				continue
			}
			kept = append(kept, raw)
		}
		node.Times = kept
		return changed
	}
	for i := range node.Children {
		if pruneChannelAutoResetAlarms(&node.Children[i], fired, now) {
			changed = true
		}
	}
	return changed
}

// ---- State ----

func decodeChannelAutoResetJSON[T any](raw string) T {
	var value T
	if raw != "" {
		_ = common.UnmarshalJsonStr(raw, &value)
	}
	return value
}

func encodeChannelAutoResetJSON(value any) string {
	encoded, err := common.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func channelAutoResetStateView(row *model.ChannelAutoResetState) ChannelAutoResetState {
	return ChannelAutoResetState{
		RedeemedAt:       decodeChannelAutoResetJSON[[]int64](row.AutoHistory),
		LastRedeemAt:     row.LastRedeemAt,
		LastRedeemSource: row.LastRedeemSource,
		InFlightSince:    row.InFlightSince,
		InFlightSource:   row.InFlightSource,
		LastAttemptAt:    row.LastAttemptAt,
		LastResult:       row.LastResult,
		LastSuccess:      row.LastSuccess,
		LastHits:         decodeChannelAutoResetJSON[[]ChannelAutoResetHit](row.LastHits),
		FailCount:        row.FailCount,
		BackoffUntil:     row.BackoffUntil,
		FiredAlarms:      decodeChannelAutoResetJSON[[]string](row.FiredAlarms),
		LastSkipReason:   row.LastSkipReason,
		LastSkipAt:       row.LastSkipAt,
	}
}

// LoadChannelAutoResetState reads the shared state of a channel.
func LoadChannelAutoResetState(channelId int) (ChannelAutoResetState, error) {
	row, err := model.GetChannelAutoResetState(channelId)
	if err != nil {
		return ChannelAutoResetState{}, err
	}
	state := channelAutoResetStateView(row)
	state.InFlightInterrupted = state.InFlightSince > 0 && time.Since(time.Unix(state.InFlightSince, 0)) > ChannelRedemptionLeaseTTL
	return state, nil
}

// ---- Guards ----

func countChannelAutoResetSince(times []int64, since int64) int {
	n := 0
	for _, t := range times {
		if t > since {
			n++
		}
	}
	return n
}

// ChannelAutoResetNextAllowed returns the earliest time an unfinished
// redemption, the backoff, the minimum interval and the caps allow another
// automatic redemption, and the guard that sets it (empty when nothing blocks
// at now). The minimum interval counts from the last redemption of any source.
func ChannelAutoResetNextAllowed(cfg ChannelAutoResetConfig, state ChannelAutoResetState, now time.Time) (time.Time, string) {
	next, reason := now, ""
	push := func(at time.Time, why string) {
		if at.After(next) {
			next, reason = at, why
		}
	}
	if state.BackoffUntil > 0 {
		push(time.Unix(state.BackoffUntil, 0), AutoResetReasonBackoff)
	}
	if state.LastRedeemAt > 0 {
		push(time.Unix(state.LastRedeemAt, 0).Add(time.Duration(cfg.MinIntervalMinutes)*time.Minute), AutoResetReasonMinInterval)
	}
	sorted := slices.Clone(state.RedeemedAt)
	slices.Sort(sorted)
	if limit := cfg.MaxPerDay; limit > 0 {
		since := now.Add(-24 * time.Hour).Unix()
		if n := countChannelAutoResetSince(sorted, since); n >= limit {
			// The window frees up when the oldest redemption counted leaves it.
			push(time.Unix(sorted[len(sorted)-n], 0).Add(24*time.Hour), AutoResetReasonDailyCap)
		}
	}
	if limit := cfg.MaxPerWeek; limit > 0 {
		since := now.Add(-channelAutoResetHistoryKeep).Unix()
		if n := countChannelAutoResetSince(sorted, since); n >= limit {
			push(time.Unix(sorted[len(sorted)-n], 0).Add(channelAutoResetHistoryKeep), AutoResetReasonWeeklyCap)
		}
	}
	// A redemption whose outcome is not recorded yet blocks while the lease
	// that wrote it may still be held. After that the minimum interval, which
	// counts from the same time, keeps blocking; the next lease holder
	// resolves the marker.
	if state.InFlightSince > 0 {
		push(time.Unix(state.InFlightSince, 0).Add(ChannelRedemptionLeaseTTL), AutoResetReasonInFlight)
	}
	return next, reason
}

// channelAutoResetResetsSoon reports whether the redemption would be wasted
// because the windows it targets reset on their own within the margin. The
// targeted windows are those of the matched usage conditions; when only
// alarm / expiring conditions matched, every window in use (utilization above
// 0 with a known reset time). A matched alarm or expiring-credit condition
// overrides the check when the setting says so.
func channelAutoResetResetsSoon(cfg ChannelAutoResetConfig, hits []ChannelAutoResetHit, f channelAutoResetFacts) bool {
	if cfg.SkipIfResetWithinMinutes <= 0 {
		return false
	}
	var windows []string
	for _, hit := range hits {
		switch hit.Type {
		case ChannelAutoResetCondAtTime:
			if cfg.AlarmIgnoresResetSoon {
				return false
			}
		case ChannelAutoResetCondExpiring:
			if cfg.ExpiringIgnoresResetSoon {
				return false
			}
		case ChannelAutoResetCondFiveHour:
			windows = append(windows, "five_hour")
		case ChannelAutoResetCondWeekly:
			windows = append(windows, hit.Window)
		}
	}
	if len(windows) == 0 && f.usage != nil {
		for key := range f.usage.Windows {
			if w, ok := f.window(key); ok && w.Utilization > 0 && !w.ResetsAt.IsZero() {
				windows = append(windows, key)
			}
		}
	}
	if len(windows) == 0 {
		return false
	}
	margin := f.now.Add(time.Duration(cfg.SkipIfResetWithinMinutes) * time.Minute)
	for _, key := range windows {
		w, ok := f.window(key)
		if !ok || w.ResetsAt.IsZero() || w.ResetsAt.After(margin) {
			return false
		}
	}
	return true
}

// channelAutoResetNeedsUsage reports whether evaluating cfg needs usage data:
// for a usage condition, or for the natural-reset check of a condition type
// that does not override it. A rule of alarms and expiring credits with both
// overrides on works while the usage API is down.
func channelAutoResetNeedsUsage(cfg ChannelAutoResetConfig) bool {
	if channelAutoResetRuleHas(cfg.Rule, ChannelAutoResetCondFiveHour, ChannelAutoResetCondWeekly) {
		return true
	}
	if cfg.SkipIfResetWithinMinutes <= 0 {
		return false
	}
	return (!cfg.AlarmIgnoresResetSoon && channelAutoResetRuleHas(cfg.Rule, ChannelAutoResetCondAtTime)) ||
		(!cfg.ExpiringIgnoresResetSoon && channelAutoResetRuleHas(cfg.Rule, ChannelAutoResetCondExpiring))
}

func channelAutoResetBackoff(failures int) time.Duration {
	d := channelAutoResetBackoffBase
	for range max(0, failures-1) {
		d *= 2
		if d >= channelAutoResetBackoffMax {
			return channelAutoResetBackoffMax
		}
	}
	return d
}

func channelAutoResetRecentAlarms(fired []string, now time.Time) []string {
	kept := make([]string, 0, len(fired))
	for _, raw := range fired {
		if at, err := time.Parse(time.RFC3339, raw); err == nil && now.Sub(at) <= channelAutoResetFiredKeep {
			kept = append(kept, raw)
		}
	}
	return kept
}

func collectChannelAutoResetDue(node *ChannelAutoResetRule, f channelAutoResetFacts) []string {
	var due []string
	for _, raw := range collectChannelAutoResetAlarms(node, nil) {
		probe := ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{raw}}
		for _, at := range f.dueAlarms(&probe) {
			formatted := at
			if parsed, err := time.Parse(time.RFC3339, at); err == nil {
				formatted = parsed.UTC().Format(time.RFC3339)
			}
			if !slices.Contains(due, formatted) {
				due = append(due, formatted)
			}
		}
	}
	return due
}

// ---- Shared redemption lease ----

// ErrChannelRedemptionBusy reports that another redemption of the channel
// holds the lease.
var ErrChannelRedemptionBusy = errors.New("another reset credit redemption of this channel is in progress")

// ChannelRedemption is one claimed redemption of a channel's reset credit.
// Begin records it as in flight (and as the last redemption) before the
// request is sent, so a node that takes the lease over after a crash or a
// slow request sees it; Finish records the outcome and releases the lease.
type ChannelRedemption struct {
	channelId  int
	owner      string
	source     string
	at         int64
	prevAt     int64
	prevSource string
	history    []int64
}

// BeginChannelRedemption claims the redemption lease of channelId for a
// manual ("manual") or subscriber ("subscription") redemption.
func BeginChannelRedemption(channelId int, source string) (*ChannelRedemption, error) {
	now := time.Now()
	owner := source + "_" + common.GetRandomString(12)
	acquired, err := model.AcquireChannelAutoResetLease(channelId, owner, now, ChannelRedemptionLeaseTTL)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrChannelRedemptionBusy
	}
	row, err := model.GetChannelAutoResetState(channelId)
	if err == nil {
		redemption := &ChannelRedemption{channelId: channelId, owner: owner, source: source, at: now.Unix(), prevAt: row.LastRedeemAt, prevSource: row.LastRedeemSource}
		err = redemption.markInFlight(now, nil, nil)
		if err == nil {
			return redemption, nil
		}
	}
	_ = model.ReleaseChannelAutoResetLease(channelId, owner, nil)
	return nil, err
}

// markInFlight records the redemption before it is sent. history (automatic
// redemptions) and fired (alarms this redemption answers) are written in the
// same conditional update, so a node that takes over after a crash neither
// misses the redemption nor fires the same alarm again.
func (r *ChannelRedemption) markInFlight(now time.Time, history []int64, fired []string) error {
	updates := map[string]any{
		"in_flight_since":    r.at,
		"in_flight_source":   r.source,
		"last_redeem_at":     r.at,
		"last_redeem_source": r.source,
		"lease_until":        now.Add(ChannelRedemptionLeaseTTL).Unix(),
	}
	if history != nil {
		r.history = history
		updates["auto_history"] = encodeChannelAutoResetJSON(history)
	}
	if fired != nil {
		updates["fired_alarms"] = encodeChannelAutoResetJSON(fired)
	}
	return model.UpdateChannelAutoResetStateAsOwner(r.channelId, r.owner, now, updates)
}

// Finish records the outcome and releases the lease. consumed means a credit
// was or may have been used; otherwise the redemption is taken back out of
// the history so it does not count against the guards. updates are written
// with it (result columns of an automatic redemption). If another redemption
// took the lease over meanwhile, its record is left as it is; a consumed
// credit is still recorded as the last redemption unless a later one is.
func (r *ChannelRedemption) Finish(consumed bool, updates map[string]any) {
	if updates == nil {
		updates = map[string]any{}
	}
	updates["in_flight_since"] = 0
	updates["in_flight_source"] = ""
	if !consumed {
		updates["last_redeem_at"] = r.prevAt
		updates["last_redeem_source"] = r.prevSource
		if r.history != nil {
			updates["auto_history"] = encodeChannelAutoResetJSON(slices.DeleteFunc(slices.Clone(r.history), func(t int64) bool { return t == r.at }))
		}
	}
	owned, err := model.FinishChannelAutoResetRedemption(r.channelId, r.owner, updates)
	if err != nil {
		common.SysError(common.LogText("failed to store reset credit redemption outcome: channel_id=%d, error=%v", r.channelId, err))
	}
	if !owned && consumed {
		if err := model.RaiseChannelLastRedeem(r.channelId, r.at, r.source); err != nil {
			common.SysError(common.LogText("failed to store reset credit redemption: channel_id=%d, error=%v", r.channelId, err))
		}
	}
	if consumed {
		InvalidateSubscriptionUsage(r.channelId)
		if engine := DefaultChannelAutoResetEngine(); engine != nil {
			engine.forgetSnapshots(r.channelId)
		}
	}
}

// ---- Engine ----

type channelAutoResetSnapshot[T any] struct {
	value *T
	at    time.Time
}

// ChannelAutoResetEngine evaluates and runs automatic redemptions. One
// engine runs per process; the database lease keeps engines on different
// nodes, and manual redemptions, from redeeming for the same channel at once.
type ChannelAutoResetEngine struct {
	provider ChannelAutoResetProvider
	nodeId   string

	mu         sync.Mutex
	upstreamAt map[int]time.Time
	usage      map[int]channelAutoResetSnapshot[ChannelAutoResetUsage]
	credits    map[int]channelAutoResetSnapshot[ChannelAutoResetCredits]
	last       map[int]ChannelAutoResetDecision
	running    map[int]bool
	enabled    map[int]bool
}

func NewChannelAutoResetEngine(provider ChannelAutoResetProvider) *ChannelAutoResetEngine {
	return &ChannelAutoResetEngine{
		provider:   provider,
		nodeId:     common.GetRandomString(12),
		upstreamAt: map[int]time.Time{},
		usage:      map[int]channelAutoResetSnapshot[ChannelAutoResetUsage]{},
		credits:    map[int]channelAutoResetSnapshot[ChannelAutoResetCredits]{},
		last:       map[int]ChannelAutoResetDecision{},
		running:    map[int]bool{},
		enabled:    map[int]bool{},
	}
}

// LastDecision returns the last evaluation this node made for channelId.
func (e *ChannelAutoResetEngine) LastDecision(channelId int) (ChannelAutoResetDecision, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.last[channelId]
	return d, ok
}

func (e *ChannelAutoResetEngine) loadUsage(ctx context.Context, ch *model.Channel, now time.Time, fresh bool) (*ChannelAutoResetUsage, error) {
	e.mu.Lock()
	snap, ok := e.usage[ch.Id]
	lastUpstream, asked := e.upstreamAt[ch.Id]
	e.mu.Unlock()
	if ok && !fresh && now.Sub(snap.at) < channelAutoResetUsageMaxAge {
		return snap.value, nil
	}
	// A failed upstream attempt counts too, so a failing usage API is not
	// asked on every tick. A fresh read (after the lease) may always ask; the
	// usage cache of the existing usage endpoints still applies below it.
	allowUpstream := fresh || !asked || now.Sub(lastUpstream) >= channelAutoResetUsageMaxAge
	usage, err := e.provider.LoadUsage(ctx, ch, allowUpstream)
	e.mu.Lock()
	defer e.mu.Unlock()
	if allowUpstream && (err != nil || usage.FromUpstream) {
		e.upstreamAt[ch.Id] = now
	}
	if err != nil {
		return nil, err
	}
	e.usage[ch.Id] = channelAutoResetSnapshot[ChannelAutoResetUsage]{value: usage, at: now}
	return usage, nil
}

func (e *ChannelAutoResetEngine) loadCredits(ctx context.Context, ch *model.Channel, now time.Time, fresh bool) (*ChannelAutoResetCredits, error) {
	e.mu.Lock()
	snap, ok := e.credits[ch.Id]
	e.mu.Unlock()
	if ok && !fresh && now.Sub(snap.at) < channelAutoResetCreditsMaxAge {
		return snap.value, nil
	}
	credits, err := e.provider.LoadCredits(ctx, ch)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.credits[ch.Id] = channelAutoResetSnapshot[ChannelAutoResetCredits]{value: credits, at: now}
	e.mu.Unlock()
	return credits, nil
}

// ForgetUsage drops the usage snapshot of channelId, so the next evaluation
// reads usage again (for example after a new passive sample was stored).
func (e *ChannelAutoResetEngine) ForgetUsage(channelId int) {
	e.mu.Lock()
	delete(e.usage, channelId)
	e.mu.Unlock()
}

func (e *ChannelAutoResetEngine) forgetSnapshots(channelId int) {
	e.mu.Lock()
	delete(e.usage, channelId)
	delete(e.credits, channelId)
	e.mu.Unlock()
}

// Evaluate runs one evaluation of ch at now. With dryRun nothing is redeemed
// or written, and draft (when not nil) replaces the stored setting so an
// unsaved draft can be tried. Times written for a redemption follow the wall
// clock from now on, so a slow evaluation does not date them in the past.
func (e *ChannelAutoResetEngine) Evaluate(ctx context.Context, ch *model.Channel, now time.Time, dryRun bool, draft *ChannelAutoResetConfig) ChannelAutoResetDecision {
	started := time.Now()
	clock := func() time.Time { return now.Add(time.Since(started)) }
	decision := ChannelAutoResetDecision{ChannelId: ch.Id, EvaluatedAt: now.Unix(), DryRun: dryRun, Hits: []ChannelAutoResetHit{}}
	cfg := LoadChannelAutoResetConfig(ch)
	if draft != nil {
		cfg = *draft
	}
	if !cfg.Enabled && !dryRun {
		decision.Reason = AutoResetReasonDisabled
		return decision
	}
	if !IsChannelAutoResetSupported(ch.Type) || ch.ChannelInfo.IsMultiKey {
		decision.Reason = AutoResetReasonUnsupported
		return decision
	}
	// The stored setting may have been written by an older version or by
	// hand; it is evaluated only after the same validation as a save.
	normalized, err := NormalizeChannelAutoResetConfig(cfg, ch.Type, now)
	if err != nil {
		decision.Reason = AutoResetReasonInvalidSetting
		decision.Result = err.Error()
		if !dryRun {
			if state, loadErr := LoadChannelAutoResetState(ch.Id); loadErr == nil {
				e.recordSkip(ch.Id, &state, decision)
			}
		}
		return decision
	}
	cfg = normalized
	if !dryRun {
		e.mu.Lock()
		if e.running[ch.Id] {
			e.mu.Unlock()
			decision.Reason = AutoResetReasonBusy
			return decision
		}
		e.running[ch.Id] = true
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			delete(e.running, ch.Id)
			e.last[ch.Id] = decision
			e.mu.Unlock()
		}()
	}
	state, err := LoadChannelAutoResetState(ch.Id)
	if err != nil {
		decision.Reason = AutoResetReasonBusy
		decision.Result = err.Error()
		return decision
	}
	state.FiredAlarms = channelAutoResetRecentAlarms(state.FiredAlarms, now)
	if !dryRun {
		if stored := LoadChannelAutoResetConfig(ch); stored.Rule != nil {
			probe := cloneChannelAutoResetRule(*stored.Rule)
			if pruneChannelAutoResetAlarms(&probe, state.FiredAlarms, now) {
				pruneStoredChannelAutoResetAlarms(ch.Id, state.FiredAlarms, now)
			}
		}
	}
	if ch.Status != common.ChannelStatusEnabled {
		decision.Reason = AutoResetReasonChannelDisabled
		e.recordSkip(ch.Id, &state, decision)
		return decision
	}
	if cfg.Rule == nil {
		decision.Reason = AutoResetReasonNoRule
		return decision
	}

	facts := channelAutoResetFacts{now: now, fired: state.FiredAlarms}
	if channelAutoResetNeedsUsage(cfg) {
		usage, err := e.loadUsage(ctx, ch, now, false)
		if err != nil {
			decision.Reason = AutoResetReasonUsageMissing
			decision.Result = err.Error()
			e.recordSkip(ch.Id, &state, decision)
			return decision
		}
		facts.usage = usage
		decision.Usage = usage.Windows
	} else if dryRun {
		// Shown in the trial result only; the rule does not depend on it.
		if usage, err := e.loadUsage(ctx, ch, now, false); err == nil {
			decision.Usage = usage.Windows
		}
	}
	if dryRun || channelAutoResetRuleHas(cfg.Rule, ChannelAutoResetCondExpiring) {
		// A failed credit query only leaves credit_expires_within unmatched.
		if credits, err := e.loadCredits(ctx, ch, now, false); err == nil {
			facts.credits = credits
			decision.Credits = credits
		}
	}

	matched, hits := evaluateChannelAutoResetRule(cfg.Rule, facts)
	decision.Matched = matched
	if hits != nil {
		decision.Hits = hits
	}
	// Alarms due at this evaluation fire once, whatever the outcome below,
	// unless the evaluation could not finish (lease busy, usage unavailable
	// after the lease); they are recorded only afterwards so the re-check
	// under the lease still sees them in the stored rule.
	due := collectChannelAutoResetDue(cfg.Rule, facts)
	if !dryRun && len(due) > 0 {
		defer func() {
			if decision.Reason != AutoResetReasonBusy && decision.Reason != AutoResetReasonUsageMissing {
				e.recordFiredAlarms(ch.Id, append(slices.Clone(state.FiredAlarms), due...), now)
			}
		}()
	}

	nextAllowed, guard := ChannelAutoResetNextAllowed(cfg, state, now)
	if guard != "" {
		decision.NextAllowedAt = nextAllowed.Unix()
	}
	switch {
	case !matched:
		decision.Reason = AutoResetReasonNotMatched
	case guard != "":
		decision.Reason = guard
	case channelAutoResetResetsSoon(cfg, hits, facts):
		decision.Reason = AutoResetReasonResetsSoon
	case dryRun:
		decision.Reason = AutoResetReasonWouldRedeem
		if decision.Credits != nil && decision.Credits.Available <= 0 {
			decision.Reason = AutoResetReasonNoCredit
		}
	}
	if decision.Reason != "" {
		if !dryRun {
			e.recordSkip(ch.Id, &state, decision)
		}
		return decision
	}
	e.redeem(ctx, ch, facts, due, clock, &decision)
	return decision
}

// recordFiredAlarms keeps the fired alarms in the shared state (which
// evaluation filters on) and removes them from the stored setting.
func (e *ChannelAutoResetEngine) recordFiredAlarms(channelId int, fired []string, now time.Time) {
	if err := model.UpdateChannelAutoResetStateFields(channelId, map[string]any{"fired_alarms": encodeChannelAutoResetJSON(fired)}); err != nil {
		common.SysError(common.LogText("failed to store fired alarms: channel_id=%d, error=%v", channelId, err))
	}
	pruneStoredChannelAutoResetAlarms(channelId, fired, now)
}

// pruneStoredChannelAutoResetAlarms removes fired and expired alarm times
// from the freshly read setting with a compare-and-swap; nothing else of the
// setting is written, so a concurrent save is never overwritten.
func pruneStoredChannelAutoResetAlarms(channelId int, fired []string, now time.Time) {
	err := model.MutateChannelOtherInfo(channelId, func(info map[string]any) bool {
		raw, ok := info[ChannelAutoResetConfigKey]
		if !ok {
			return false
		}
		var cfg ChannelAutoResetConfig
		encoded, err := common.Marshal(raw)
		if err != nil || common.Unmarshal(encoded, &cfg) != nil || !pruneChannelAutoResetAlarms(cfg.Rule, fired, now) {
			return false
		}
		info[ChannelAutoResetConfigKey] = cfg
		return true
	})
	if err != nil {
		common.SysError(common.LogText("failed to remove fired alarms: channel_id=%d, error=%v", channelId, err))
	}
}

// redeem claims the lease, re-reads the switch, the rule, the state and the
// usage, evaluates the rule again on that data, records the redemption as in
// flight and only then sends it.
func (e *ChannelAutoResetEngine) redeem(ctx context.Context, ch *model.Channel, facts channelAutoResetFacts, due []string, clock func() time.Time, decision *ChannelAutoResetDecision) {
	owner := "auto_" + e.nodeId + "_" + common.GetRandomString(8)
	acquired, err := model.AcquireChannelAutoResetLease(ch.Id, owner, clock(), ChannelRedemptionLeaseTTL)
	if err != nil || !acquired {
		decision.Reason = AutoResetReasonBusy
		if err != nil {
			decision.Result = err.Error()
		}
		return
	}
	released := false
	release := func(updates map[string]any) {
		if released {
			return
		}
		released = true
		if err := model.ReleaseChannelAutoResetLease(ch.Id, owner, updates); err != nil {
			common.SysError(common.LogText("failed to release auto reset lease: channel_id=%d, error=%v", ch.Id, err))
		}
	}
	defer release(nil)

	fresh, err := model.GetChannelById(ch.Id, true)
	if err != nil {
		decision.Reason = AutoResetReasonBusy
		decision.Result = err.Error()
		return
	}
	ch = fresh
	cfg, err := NormalizeChannelAutoResetConfig(LoadChannelAutoResetConfig(ch), ch.Type, facts.now)
	if err != nil || !cfg.Enabled || ch.Status != common.ChannelStatusEnabled || cfg.Rule == nil {
		decision.Reason = AutoResetReasonDisabled
		return
	}
	row, err := model.GetChannelAutoResetState(ch.Id)
	if err != nil {
		decision.Reason = AutoResetReasonBusy
		decision.Result = err.Error()
		return
	}
	state := channelAutoResetStateView(row)
	if state.InFlightSince > 0 {
		// The previous holder never recorded its outcome (crash, or its
		// lease ran out mid-request). The redemption stays counted as the
		// last redemption; only the marker is resolved.
		resolved := map[string]any{"in_flight_since": 0, "in_flight_source": "", "last_result": "interrupted", "last_success": false}
		if err := model.UpdateChannelAutoResetStateAsOwner(ch.Id, owner, clock(), resolved); err != nil {
			decision.Reason = AutoResetReasonBusy
			return
		}
		state.InFlightSince = 0
		common.SysLog(common.LogText("auto reset found an unfinished redemption: channel_id=%d, since=%d", ch.Id, row.InFlightSince))
	}
	if next, guard := ChannelAutoResetNextAllowed(cfg, state, clock()); guard != "" {
		decision.Reason = guard
		decision.NextAllowedAt = next.Unix()
		e.recordSkip(ch.Id, &state, *decision)
		return
	}

	// Usage may be minutes old and a manual reset may have happened since:
	// read it again and evaluate the stored rule on it.
	recheck := channelAutoResetFacts{now: clock(), fired: facts.fired, credits: facts.credits}
	if channelAutoResetNeedsUsage(cfg) {
		readCtx, cancel := context.WithTimeout(ctx, channelAutoResetReadTimeout)
		usage, err := e.loadUsage(readCtx, ch, clock(), true)
		cancel()
		if err != nil {
			decision.Reason = AutoResetReasonUsageMissing
			decision.Result = err.Error()
			e.recordSkip(ch.Id, &state, *decision)
			return
		}
		recheck.usage = usage
		decision.Usage = usage.Windows
	}
	readCtx, cancel := context.WithTimeout(ctx, channelAutoResetReadTimeout)
	credits, err := e.loadCredits(readCtx, ch, clock(), true)
	cancel()
	if err != nil {
		decision.Reason = AutoResetReasonCredential
		decision.Result = err.Error()
		release(map[string]any{"backoff_until": clock().Add(channelAutoResetPause).Unix(), "last_skip_reason": decision.Reason, "last_skip_at": decision.EvaluatedAt})
		common.SysLog(common.LogText("auto reset credit skipped: channel_id=%d, reason=%s", ch.Id, decision.Reason))
		return
	}
	decision.Credits = credits
	recheck.credits = credits
	matched, hits := evaluateChannelAutoResetRule(cfg.Rule, recheck)
	decision.Matched = matched
	decision.Hits = hits
	if hits == nil {
		decision.Hits = []ChannelAutoResetHit{}
	}
	switch {
	case !matched:
		decision.Reason = AutoResetReasonNotMatched
	case channelAutoResetResetsSoon(cfg, hits, recheck):
		decision.Reason = AutoResetReasonResetsSoon
	case credits.Available <= 0:
		decision.Reason = AutoResetReasonNoCredit
		release(map[string]any{"backoff_until": clock().Add(channelAutoResetPause).Unix(), "last_skip_reason": decision.Reason, "last_skip_at": decision.EvaluatedAt})
		return
	}
	if decision.Reason != "" {
		e.recordSkip(ch.Id, &state, *decision)
		return
	}

	// The upstream reads above may have taken minutes: the switch is read
	// once more right before anything is sent.
	if latest, err := model.GetChannelById(ch.Id, true); err != nil || !LoadChannelAutoResetConfig(latest).Enabled || latest.Status != common.ChannelStatusEnabled {
		decision.Reason = AutoResetReasonDisabled
		return
	}

	// Record the redemption, and the alarms it answers, before sending it: a
	// node that takes the lease over after a crash or a request that outlives
	// the lease counts it and does not fire those alarms again.
	at := clock()
	var fired []string
	if len(due) > 0 {
		fired = append(slices.Clone(facts.fired), due...)
	}
	history := slices.DeleteFunc(slices.Clone(state.RedeemedAt), func(t int64) bool { return t <= at.Add(-channelAutoResetHistoryKeep).Unix() })
	redemption := &ChannelRedemption{channelId: ch.Id, owner: owner, source: "auto", at: at.Unix(), prevAt: state.LastRedeemAt, prevSource: state.LastRedeemSource}
	if err := redemption.markInFlight(at, append(history, at.Unix()), fired); err != nil {
		decision.Reason = AutoResetReasonBusy
		decision.Result = err.Error()
		return
	}
	released = true // Finish releases the lease

	redeemCtx, cancel := context.WithTimeout(ctx, channelAutoResetRedeemTimeout)
	result := e.provider.Redeem(redeemCtx, ch)
	cancel()
	done := clock()
	decision.Result = result.Result
	consumed := result.Success || result.MaybeConsumed
	outcome := map[string]any{
		"last_attempt_at":  at.Unix(),
		"last_result":      result.Result,
		"last_success":     result.Success,
		"last_hits":        encodeChannelAutoResetJSON(decision.Hits),
		"last_skip_reason": "",
	}
	switch {
	case result.Success:
		decision.Reason = AutoResetReasonRedeemed
		outcome["fail_count"] = 0
		outcome["backoff_until"] = 0
	case result.CredentialProblem:
		decision.Reason = AutoResetReasonCredential
		outcome["backoff_until"] = done.Add(channelAutoResetPause).Unix()
	default:
		decision.Reason = AutoResetReasonFailed
		failures := state.FailCount + 1
		backoff := done.Add(channelAutoResetBackoff(failures))
		if result.RetryAfter.After(backoff) {
			backoff = result.RetryAfter
		}
		outcome["fail_count"] = failures
		outcome["backoff_until"] = backoff.Unix()
		decision.NextAllowedAt = backoff.Unix()
	}
	// The outcome is stored before anything else is asked of upstream.
	redemption.Finish(consumed, outcome)

	if decision.Reason == AutoResetReasonCredential {
		common.SysLog(common.LogText("auto reset credit skipped: channel_id=%d, reason=%s, result=%s", ch.Id, decision.Reason, result.Result))
		return
	}
	var after *ChannelAutoResetUsage
	if result.Success {
		logger.LogInfo(ctx, common.LogText("auto reset credit redeemed: channel_id=%d, result=%s", ch.Id, result.Result))
		afterCtx, cancel := context.WithTimeout(ctx, channelAutoResetReadTimeout)
		after, _ = e.loadUsage(afterCtx, ch, clock(), true)
		cancel()
	} else {
		common.SysLog(common.LogText("auto reset credit failed: channel_id=%d, result=%s", ch.Id, result.Result))
	}
	recordChannelAutoResetAudit(ch, cfg, decision, result, recheck.usage, after, credits)
}

// recordSkip keeps the skip reason in the shared state when it changed, so
// the console shows it from any node without a write on every tick.
func (e *ChannelAutoResetEngine) recordSkip(channelId int, state *ChannelAutoResetState, decision ChannelAutoResetDecision) {
	if decision.Reason == "" || decision.Reason == state.LastSkipReason {
		return
	}
	state.LastSkipReason = decision.Reason
	if err := model.UpdateChannelAutoResetStateFields(channelId, map[string]any{"last_skip_reason": decision.Reason, "last_skip_at": decision.EvaluatedAt}); err != nil {
		common.SysError(common.LogText("failed to store auto reset skip: channel_id=%d, error=%v", channelId, err))
	}
	if decision.Reason != AutoResetReasonNotMatched {
		common.SysLog(common.LogText("auto reset credit skipped: channel_id=%d, reason=%s", channelId, decision.Reason))
	}
}

func channelAutoResetUsagePercent(usage *ChannelAutoResetUsage) map[string]float64 {
	if usage == nil {
		return nil
	}
	out := make(map[string]float64, len(usage.Windows))
	for key, w := range usage.Windows {
		out[key] = math.Round(w.Utilization*10) / 10
	}
	return out
}

// recordChannelAutoResetAudit writes one audit event per redemption attempt.
// It is attributed to the administrator who last saved the setting.
func recordChannelAutoResetAudit(ch *model.Channel, cfg ChannelAutoResetConfig, decision *ChannelAutoResetDecision, result ChannelAutoResetRedeemResult, before *ChannelAutoResetUsage, after *ChannelAutoResetUsage, credits *ChannelAutoResetCredits) {
	provider := "codex"
	if ch.Type == constant.ChannelTypeClaudeOAuth {
		provider = "claude"
	}
	matched := make([]string, 0, len(decision.Hits))
	for _, hit := range decision.Hits {
		matched = append(matched, hit.String())
	}
	params := map[string]any{
		"source":       "auto",
		"channel_id":   ch.Id,
		"provider":     provider,
		"result":       result.Result,
		"success":      result.Success,
		"matched":      matched,
		"usage_before": channelAutoResetUsagePercent(before),
		"usage_after":  channelAutoResetUsagePercent(after),
	}
	if credits != nil {
		params["credits_available_before"] = credits.Available
	}
	content := fmt.Sprintf("Automatically redeemed a %s reset credit for channel %d: %s", provider, ch.Id, result.Result)
	status := 200
	if !result.Success {
		status = 500
	}
	model.RecordOperationAuditLog(cfg.UpdatedBy, cfg.UpdatedByRole, content, "", "channel.reset_credit_auto_redeem", params,
		&model.AuditAdminInfo{AdminID: cfg.UpdatedBy, AdminRole: cfg.UpdatedByRole, AuthMethod: "auto"},
		&model.AuditRequestInfo{Method: "AUTO", Route: "auto_reset_credit", Path: "auto_reset_credit", Status: status, Success: result.Success})
}

// ---- Scheduler ----

var (
	channelAutoResetEngineMu sync.RWMutex
	channelAutoResetEngine   *ChannelAutoResetEngine
	channelAutoResetOnce     sync.Once
)

// DefaultChannelAutoResetEngine returns the process engine, nil until
// StartChannelAutoResetTask ran.
func DefaultChannelAutoResetEngine() *ChannelAutoResetEngine {
	channelAutoResetEngineMu.RLock()
	defer channelAutoResetEngineMu.RUnlock()
	return channelAutoResetEngine
}

// StartChannelAutoResetTask installs the engine with provider and, on the
// master node, starts the per-minute evaluation of the channels whose switch
// is on.
func StartChannelAutoResetTask(provider ChannelAutoResetProvider) {
	channelAutoResetOnce.Do(func() {
		engine := NewChannelAutoResetEngine(provider)
		channelAutoResetEngineMu.Lock()
		channelAutoResetEngine = engine
		channelAutoResetEngineMu.Unlock()
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			ticker := time.NewTicker(channelAutoResetTickInterval)
			defer ticker.Stop()
			for range ticker.C {
				engine.RunOnce(context.Background())
			}
		})
	})
}

// RunOnce evaluates every channel whose switch is on. The list comes from an
// indexed query on the small state table; each channel is evaluated at its
// own wall-clock time.
func (e *ChannelAutoResetEngine) RunOnce(ctx context.Context) {
	ids, err := model.ListChannelAutoResetEnabledIds()
	if err != nil {
		common.SysError(common.LogText("failed to list auto reset channels: %v", err))
		return
	}
	enabled := make(map[int]bool, len(ids))
	for _, id := range ids {
		enabled[id] = true
	}
	e.mu.Lock()
	e.enabled = enabled
	e.mu.Unlock()
	for _, id := range ids {
		ch, err := model.GetChannelById(id, true)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The channel was deleted; its state goes with it.
			_ = model.DeleteChannelAutoResetState(id)
			continue
		}
		if err != nil {
			// A transient read error must not drop the in-flight record,
			// the history or the switch mirror.
			common.SysError(common.LogText("failed to read auto reset channel: channel_id=%d, error=%v", id, err))
			continue
		}
		e.Evaluate(ctx, ch, time.Now(), false, nil)
	}
}

// NotifyChannelAutoResetUsageSample lets a freshly stored passive usage sample
// trigger an evaluation without waiting for the next tick. For channels with
// the switch off it is a map lookup.
func NotifyChannelAutoResetUsageSample(channelId int) {
	engine := DefaultChannelAutoResetEngine()
	if engine == nil {
		return
	}
	engine.mu.Lock()
	on := engine.enabled[channelId] && !engine.running[channelId]
	engine.mu.Unlock()
	if !on {
		return
	}
	engine.ForgetUsage(channelId)
	gopool.Go(func() {
		ch, err := model.GetChannelById(channelId, true)
		if err != nil {
			return
		}
		engine.Evaluate(context.Background(), ch, time.Now(), false, nil)
	})
}

// SaveChannelAutoResetConfig stores a normalized setting (compare-and-swap on
// other_info) and mirrors its switch into the shared state.
func SaveChannelAutoResetConfig(channelId int, cfg ChannelAutoResetConfig) error {
	err := model.MutateChannelOtherInfo(channelId, func(info map[string]any) bool {
		info[ChannelAutoResetConfigKey] = cfg
		return true
	})
	if err != nil {
		return err
	}
	if err := model.UpdateChannelAutoResetStateFields(channelId, map[string]any{"auto_enabled": cfg.Enabled}); err != nil {
		return err
	}
	if engine := DefaultChannelAutoResetEngine(); engine != nil {
		engine.mu.Lock()
		if cfg.Enabled {
			engine.enabled[channelId] = true
		} else {
			delete(engine.enabled, channelId)
		}
		engine.mu.Unlock()
	}
	return nil
}

// ErrChannelAutoResetUnavailable reports that the engine is not installed.
var ErrChannelAutoResetUnavailable = errors.New("automatic reset is not available on this node")

// ChannelAutoResetWeeklyWindows lists the weekly usage windows a
// weekly_used_pct condition may name for channelType.
func ChannelAutoResetWeeklyWindows(channelType int) []string {
	return slices.Clone(channelAutoResetWeeklyWindows[channelType])
}

// ChannelAutoResetNextAlarm returns the next alarm (RFC 3339) that has not
// fired and is not past its grace, or "".
func ChannelAutoResetNextAlarm(cfg ChannelAutoResetConfig, state ChannelAutoResetState, now time.Time) string {
	next := ""
	var nextAt time.Time
	for _, raw := range collectChannelAutoResetAlarms(cfg.Rule, nil) {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil || now.Sub(at) > ChannelAutoResetAlarmGrace || slices.Contains(state.FiredAlarms, at.UTC().Format(time.RFC3339)) {
			continue
		}
		if next == "" || at.Before(nextAt) {
			next, nextAt = raw, at
		}
	}
	return next
}
