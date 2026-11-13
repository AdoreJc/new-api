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
)

// Automatic reset-credit redemption for Codex and Claude Subscription (OAuth)
// channels. It runs the same upstream redemption an administrator can start
// by hand from the usage dialog, when a condition tree on the channel matches.
//
// The setting and its runtime state live in the channel's other_info, which
// the channel edit form never sends back, so alarms the scheduler removes are
// not restored by a stale form:
//
//	other_info.auto_reset_credit        ChannelAutoResetConfig
//	other_info.auto_reset_credit_state  ChannelAutoResetState

const (
	ChannelAutoResetConfigKey = "auto_reset_credit"
	ChannelAutoResetStateKey  = "auto_reset_credit_state"

	ChannelAutoResetCondFiveHour = "five_hour_used_pct"
	ChannelAutoResetCondWeekly   = "weekly_used_pct"
	ChannelAutoResetCondAtTime   = "at_time"
	ChannelAutoResetCondExpiring = "credit_expires_within"

	// ChannelAutoResetAlarmGrace is how long after its time an alarm can still
	// fire. The scheduler ticks every minute; an alarm missed for longer (node
	// down, channel disabled, usage unavailable) is dropped as expired.
	ChannelAutoResetAlarmGrace = 10 * time.Minute

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
	// channelAutoResetNoCreditRecheck pauses a channel whose rule matched but
	// which holds no redeemable credit.
	channelAutoResetNoCreditRecheck = 30 * time.Minute
	channelAutoResetBackoffBase     = 5 * time.Minute
	channelAutoResetBackoffMax      = 6 * time.Hour
	// channelAutoResetLeaseTTL outlasts one redemption including the token
	// refresh, the single reset request and the usage re-read.
	channelAutoResetLeaseTTL     = 3 * time.Minute
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

// ChannelAutoResetState is the persisted runtime state of one channel.
type ChannelAutoResetState struct {
	// Unix times of redemptions that may have consumed a credit (success or
	// unknown outcome) within the last 7 days
	RedeemedAt    []int64               `json:"redeemed_at,omitempty"`
	LastAttemptAt int64                 `json:"last_attempt_at,omitempty"`
	LastResult    string                `json:"last_result,omitempty"`
	LastSuccess   bool                  `json:"last_success,omitempty"`
	LastHits      []ChannelAutoResetHit `json:"last_hits,omitempty"`
	FailCount     int                   `json:"fail_count,omitempty"`
	BackoffUntil  int64                 `json:"backoff_until,omitempty"`
	// Alarms that already fired, kept for 48 hours so a stale save cannot
	// make them fire again
	FiredAlarms []string `json:"fired_alarms,omitempty"`
	// Last skip reason, written only when it changes
	LastSkipReason string `json:"last_skip_reason,omitempty"`
	LastSkipAt     int64  `json:"last_skip_at,omitempty"`
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
	// RetryAfter, when set, is the earliest time upstream accepts another
	// redemption (Claude cooldown).
	RetryAfter time.Time
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

func LoadChannelAutoResetState(ch *model.Channel) ChannelAutoResetState {
	var state ChannelAutoResetState
	if !decodeChannelOtherInfoEntry(ch, ChannelAutoResetStateKey, &state) {
		return ChannelAutoResetState{}
	}
	return state
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

// ChannelAutoResetNextAllowed returns the earliest time the backoff, the
// minimum interval and the caps allow another redemption, and the guard that
// sets it (empty when nothing blocks at now).
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
	if last := slices.Max(append([]int64{0}, state.RedeemedAt...)); last > 0 {
		push(time.Unix(last, 0).Add(time.Duration(cfg.MinIntervalMinutes)*time.Minute), AutoResetReasonMinInterval)
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

// ---- Engine ----

type channelAutoResetSnapshot[T any] struct {
	value *T
	at    time.Time
}

// ChannelAutoResetEngine evaluates and runs automatic redemptions. One
// engine runs per process; the database lease keeps engines on different
// nodes from redeeming for the same channel at once.
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
	// asked on every tick.
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

// Evaluate runs one evaluation of ch. With dryRun nothing is redeemed or
// written, and cfg (when not nil) replaces the stored setting so an unsaved
// draft can be tried.
func (e *ChannelAutoResetEngine) Evaluate(ctx context.Context, ch *model.Channel, now time.Time, dryRun bool, draft *ChannelAutoResetConfig) ChannelAutoResetDecision {
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
	state := LoadChannelAutoResetState(ch)
	state.FiredAlarms = channelAutoResetRecentAlarms(state.FiredAlarms, now)

	if !dryRun && pruneChannelAutoResetAlarms(cfg.Rule, state.FiredAlarms, now) {
		e.saveConfig(ch.Id, cfg)
	}
	if ch.Status != common.ChannelStatusEnabled {
		decision.Reason = AutoResetReasonChannelDisabled
		e.recordSkip(ch.Id, &state, decision, dryRun)
		return decision
	}
	if cfg.Rule == nil {
		decision.Reason = AutoResetReasonNoRule
		return decision
	}

	facts := channelAutoResetFacts{now: now, fired: state.FiredAlarms}
	needUsage := channelAutoResetRuleHas(cfg.Rule, ChannelAutoResetCondFiveHour, ChannelAutoResetCondWeekly) || cfg.SkipIfResetWithinMinutes > 0
	if needUsage {
		usage, err := e.loadUsage(ctx, ch, now, false)
		if err != nil {
			decision.Reason = AutoResetReasonUsageMissing
			decision.Result = err.Error()
			e.recordSkip(ch.Id, &state, decision, dryRun)
			return decision
		}
		facts.usage = usage
		decision.Usage = usage.Windows
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
	// Alarms due at this evaluation fire once, whatever the outcome below.
	due := collectChannelAutoResetDue(cfg.Rule, facts)
	if !dryRun && len(due) > 0 {
		state.FiredAlarms = append(state.FiredAlarms, due...)
		pruneChannelAutoResetAlarms(cfg.Rule, state.FiredAlarms, now)
		e.saveConfig(ch.Id, cfg)
		e.saveState(ch.Id, state)
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
		e.recordSkip(ch.Id, &state, decision, dryRun)
		return decision
	}
	e.redeem(ctx, ch, cfg, facts, &decision)
	return decision
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

// redeem claims the channel lease, re-checks the guards against the state
// stored in the database, and redeems one credit.
func (e *ChannelAutoResetEngine) redeem(ctx context.Context, ch *model.Channel, cfg ChannelAutoResetConfig, facts channelAutoResetFacts, decision *ChannelAutoResetDecision) {
	now := facts.now
	owner := "autoreset_" + e.nodeId + "_" + common.GetRandomString(8)
	acquired, err := model.AcquireChannelAutoResetLease(ch.Id, owner, now, channelAutoResetLeaseTTL)
	if err != nil || !acquired {
		decision.Reason = AutoResetReasonBusy
		if err != nil {
			decision.Result = err.Error()
		}
		return
	}
	defer func() {
		if err := model.ReleaseChannelAutoResetLease(owner); err != nil {
			common.SysError(common.LogText("failed to release auto reset lease: channel_id=%d, error=%v", ch.Id, err))
		}
	}()
	// Subscribers of a bound channel may redeem by hand; share their lock.
	release, ok, err := AcquireSubscriptionResetLock(ch.Id)
	if err != nil || !ok {
		decision.Reason = AutoResetReasonBusy
		return
	}
	defer release()

	// Another node may have redeemed between the read and the lease.
	fresh, err := model.GetChannelById(ch.Id, true)
	if err != nil {
		decision.Reason = AutoResetReasonBusy
		decision.Result = err.Error()
		return
	}
	ch = fresh
	freshCfg := LoadChannelAutoResetConfig(ch)
	state := LoadChannelAutoResetState(ch)
	state.FiredAlarms = channelAutoResetRecentAlarms(state.FiredAlarms, now)
	if !freshCfg.Enabled || ch.Status != common.ChannelStatusEnabled {
		decision.Reason = AutoResetReasonDisabled
		return
	}
	if next, guard := ChannelAutoResetNextAllowed(freshCfg, state, now); guard != "" {
		decision.Reason = guard
		decision.NextAllowedAt = next.Unix()
		e.recordSkip(ch.Id, &state, *decision, false)
		return
	}

	credits, err := e.loadCredits(ctx, ch, now, true)
	if err != nil {
		decision.Reason = AutoResetReasonFailed
		decision.Result = "credits_unavailable"
		e.recordFailure(ch, freshCfg, &state, decision, ChannelAutoResetRedeemResult{Result: "credits_unavailable"}, now, facts.usage, nil)
		return
	}
	decision.Credits = credits
	if credits.Available <= 0 {
		decision.Reason = AutoResetReasonNoCredit
		state.BackoffUntil = now.Add(channelAutoResetNoCreditRecheck).Unix()
		e.recordSkip(ch.Id, &state, *decision, false)
		e.saveState(ch.Id, state)
		return
	}

	result := e.provider.Redeem(ctx, ch)
	decision.Result = result.Result
	state.LastAttemptAt = now.Unix()
	state.LastResult = result.Result
	state.LastSuccess = result.Success
	state.LastHits = decision.Hits
	state.LastSkipReason = ""
	if result.Success || result.MaybeConsumed {
		state.RedeemedAt = append(state.RedeemedAt, now.Unix())
	}
	cutoff := now.Add(-channelAutoResetHistoryKeep).Unix()
	state.RedeemedAt = slices.DeleteFunc(state.RedeemedAt, func(t int64) bool { return t <= cutoff })

	var after *ChannelAutoResetUsage
	if result.Success {
		decision.Reason = AutoResetReasonRedeemed
		state.FailCount = 0
		state.BackoffUntil = 0
		InvalidateSubscriptionUsage(ch.Id)
		e.mu.Lock()
		delete(e.credits, ch.Id)
		e.mu.Unlock()
		after, _ = e.loadUsage(ctx, ch, now, true)
		e.saveState(ch.Id, state)
		logger.LogInfo(ctx, common.LogText("auto reset credit redeemed: channel_id=%d, result=%s", ch.Id, result.Result))
		recordChannelAutoResetAudit(ch, freshCfg, decision, result, facts.usage, after, credits)
		return
	}
	e.recordFailure(ch, freshCfg, &state, decision, result, now, facts.usage, credits)
}

func (e *ChannelAutoResetEngine) recordFailure(ch *model.Channel, cfg ChannelAutoResetConfig, state *ChannelAutoResetState, decision *ChannelAutoResetDecision, result ChannelAutoResetRedeemResult, now time.Time, before *ChannelAutoResetUsage, credits *ChannelAutoResetCredits) {
	decision.Reason = AutoResetReasonFailed
	state.FailCount++
	state.LastAttemptAt = now.Unix()
	state.LastResult = result.Result
	state.LastSuccess = false
	state.LastHits = decision.Hits
	backoff := now.Add(channelAutoResetBackoff(state.FailCount))
	if result.RetryAfter.After(backoff) {
		backoff = result.RetryAfter
	}
	state.BackoffUntil = backoff.Unix()
	decision.NextAllowedAt = state.BackoffUntil
	e.saveState(ch.Id, *state)
	common.SysLog(common.LogText("auto reset credit failed: channel_id=%d, result=%s, failures=%d, backoff_until=%d", ch.Id, result.Result, state.FailCount, state.BackoffUntil))
	if credits != nil {
		recordChannelAutoResetAudit(ch, cfg, decision, result, before, nil, credits)
	}
}

// recordSkip keeps the skip reason in the persisted state when it changed, so
// the console shows it from any node without a write on every tick.
func (e *ChannelAutoResetEngine) recordSkip(channelId int, state *ChannelAutoResetState, decision ChannelAutoResetDecision, dryRun bool) {
	if dryRun || decision.Reason == "" || decision.Reason == state.LastSkipReason {
		return
	}
	state.LastSkipReason = decision.Reason
	state.LastSkipAt = decision.EvaluatedAt
	e.saveState(channelId, *state)
	if decision.Reason != AutoResetReasonNotMatched {
		common.SysLog(common.LogText("auto reset credit skipped: channel_id=%d, reason=%s", channelId, decision.Reason))
	}
}

func (e *ChannelAutoResetEngine) saveState(channelId int, state ChannelAutoResetState) {
	if err := model.UpdateChannelOtherInfoEntry(channelId, ChannelAutoResetStateKey, state); err != nil {
		common.SysError(common.LogText("failed to store auto reset state: channel_id=%d, error=%v", channelId, err))
	}
}

func (e *ChannelAutoResetEngine) saveConfig(channelId int, cfg ChannelAutoResetConfig) {
	if err := model.UpdateChannelOtherInfoEntry(channelId, ChannelAutoResetConfigKey, cfg); err != nil {
		common.SysError(common.LogText("failed to store auto reset setting: channel_id=%d, error=%v", channelId, err))
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
				engine.RunOnce(context.Background(), time.Now())
			}
		})
	})
}

// RunOnce evaluates every channel whose switch is on.
func (e *ChannelAutoResetEngine) RunOnce(ctx context.Context, now time.Time) {
	candidates, err := model.ListChannelAutoResetCandidates()
	if err != nil {
		common.SysError(common.LogText("failed to list auto reset channels: %v", err))
		return
	}
	enabled := map[int]bool{}
	for _, candidate := range candidates {
		if LoadChannelAutoResetConfig(candidate).Enabled {
			enabled[candidate.Id] = true
		}
	}
	e.mu.Lock()
	e.enabled = enabled
	e.mu.Unlock()
	for id := range enabled {
		ch, err := model.GetChannelById(id, true)
		if err != nil {
			continue
		}
		e.Evaluate(ctx, ch, now, false, nil)
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

// SetChannelAutoResetEnabled updates the in-memory switch after a save so the
// sample trigger follows it before the next tick.
func SetChannelAutoResetEnabled(channelId int, enabled bool) {
	engine := DefaultChannelAutoResetEngine()
	if engine == nil {
		return
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if enabled {
		engine.enabled[channelId] = true
	} else {
		delete(engine.enabled, channelId)
	}
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
