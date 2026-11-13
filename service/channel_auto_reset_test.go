package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeAutoResetProvider stands in for the upstream usage, credit and redeem
// APIs; no test reaches a real upstream.
type fakeAutoResetProvider struct {
	mu          sync.Mutex
	usage       map[string]ChannelAutoResetWindow
	usageErr    error
	credits     ChannelAutoResetCredits
	results     []ChannelAutoResetRedeemResult
	redeemDelay time.Duration

	usageCalls    int
	upstreamCalls int
	creditCalls   int
	redeemCalls   int
}

func (p *fakeAutoResetProvider) LoadUsage(_ context.Context, _ *model.Channel, allowUpstream bool) (*ChannelAutoResetUsage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usageCalls++
	if allowUpstream {
		p.upstreamCalls++
	}
	if p.usageErr != nil {
		return nil, p.usageErr
	}
	return &ChannelAutoResetUsage{Windows: p.usage, FromUpstream: allowUpstream}, nil
}

func (p *fakeAutoResetProvider) LoadCredits(context.Context, *model.Channel) (*ChannelAutoResetCredits, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creditCalls++
	credits := p.credits
	return &credits, nil
}

func (p *fakeAutoResetProvider) Redeem(context.Context, *model.Channel) ChannelAutoResetRedeemResult {
	time.Sleep(p.redeemDelay)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.redeemCalls++
	if len(p.results) == 0 {
		return ChannelAutoResetRedeemResult{Result: "reset", Success: true}
	}
	result := p.results[0]
	p.results = p.results[1:]
	return result
}

func (p *fakeAutoResetProvider) calls() (usage, credits, redeem int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.usageCalls, p.creditCalls, p.redeemCalls
}

var autoResetTestNow = time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)

func autoResetTestConfig(rule ChannelAutoResetRule) ChannelAutoResetConfig {
	cfg := DefaultChannelAutoResetConfig()
	cfg.Enabled = true
	cfg.Rule = &rule
	cfg.UpdatedBy = 7
	cfg.UpdatedByRole = common.RoleRootUser
	return cfg
}

func fiveHourAtLeast(pct float64) ChannelAutoResetRule {
	return ChannelAutoResetRule{Type: ChannelAutoResetCondFiveHour, Threshold: pct}
}

// seedAutoResetChannel stores a Claude OAuth channel with cfg and state.
func seedAutoResetChannel(t *testing.T, cfg ChannelAutoResetConfig, state *ChannelAutoResetState) *model.Channel {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.AuditLog{}, &model.ChannelAutoResetState{}))
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM channels")
		model.DB.Exec("DELETE FROM channel_auto_reset_states")
		model.DB.Exec("DELETE FROM audit_logs")
	})
	ch := &model.Channel{Type: constant.ChannelTypeClaudeOAuth, Name: "auto-reset-test", Key: "{}", Status: common.ChannelStatusEnabled}
	ch.SetOtherInfo(map[string]any{ChannelAutoResetConfigKey: cfg})
	require.NoError(t, model.DB.Create(ch).Error)
	row := &model.ChannelAutoResetState{ChannelId: ch.Id, AutoEnabled: cfg.Enabled}
	if state != nil {
		row.AutoHistory = encodeChannelAutoResetJSON(state.RedeemedAt)
		row.LastRedeemAt = state.LastRedeemAt
		row.FailCount = state.FailCount
		row.BackoffUntil = state.BackoffUntil
		row.InFlightSince = state.InFlightSince
	}
	require.NoError(t, model.DB.Create(row).Error)
	return reloadAutoResetChannel(t, ch.Id)
}

func loadAutoResetState(t *testing.T, id int) ChannelAutoResetState {
	t.Helper()
	state, err := LoadChannelAutoResetState(id)
	require.NoError(t, err)
	return state
}

func reloadAutoResetChannel(t *testing.T, id int) *model.Channel {
	t.Helper()
	ch, err := model.GetChannelById(id, true)
	require.NoError(t, err)
	return ch
}

func TestEvaluateChannelAutoResetRule(t *testing.T) {
	now := autoResetTestNow
	expiry := now.Add(5 * time.Hour)
	facts := channelAutoResetFacts{
		now: now,
		usage: &ChannelAutoResetUsage{Windows: map[string]ChannelAutoResetWindow{
			"five_hour":                  {Utilization: 90, ResetsAt: now.Add(2 * time.Hour)},
			"seven_day":                  {Utilization: 60, ResetsAt: now.Add(48 * time.Hour)},
			"seven_day_overage_included": {Utilization: 99, ResetsAt: now.Add(-time.Minute)},
		}},
		credits: &ChannelAutoResetCredits{Available: 1, EarliestExpiry: &expiry},
		fired:   []string{"2026-11-12T07:58:00Z"},
	}
	weekly := func(window string, pct float64) ChannelAutoResetRule {
		return ChannelAutoResetRule{Type: ChannelAutoResetCondWeekly, Window: window, Threshold: pct}
	}
	expiring := func(hours float64) ChannelAutoResetRule {
		return ChannelAutoResetRule{Type: ChannelAutoResetCondExpiring, Hours: hours}
	}
	alarm := func(times ...string) ChannelAutoResetRule {
		return ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: times}
	}
	group := func(op string, children ...ChannelAutoResetRule) ChannelAutoResetRule {
		return ChannelAutoResetRule{Op: op, Children: children}
	}
	cases := []struct {
		name    string
		rule    ChannelAutoResetRule
		matched bool
		hits    int
	}{
		{"threshold equal matches", fiveHourAtLeast(90), true, 1},
		{"threshold just above does not", fiveHourAtLeast(90.1), false, 0},
		{"weekly default window", weekly("", 60), true, 1},
		{"window past its reset counts as zero", weekly("seven_day_overage_included", 1), false, 0},
		{"missing window does not match", weekly("seven_day_sonnet", 1), false, 0},
		{"and needs every child", group("and", fiveHourAtLeast(80), weekly("seven_day", 70)), false, 0},
		{"and lists every hit", group("and", fiveHourAtLeast(80), weekly("seven_day", 50)), true, 2},
		{"or needs one child", group("or", fiveHourAtLeast(95), weekly("seven_day", 50)), true, 1},
		{"empty group does not match", group("or"), false, 0},
		{"nested and inside or", group("or", group("and", fiveHourAtLeast(95), expiring(6)), group("and", fiveHourAtLeast(85), expiring(6))), true, 2},
		{"expiring at boundary", expiring(5), true, 1},
		{"expiring beyond window", expiring(4.9), false, 0},
		{"alarm due within grace", alarm("2026-11-12T07:55:00Z"), true, 1},
		{"alarm in the future", alarm("2026-11-12T08:01:00Z"), false, 0},
		{"alarm past grace", alarm("2026-11-12T07:29:00Z"), false, 0},
		{"alarm already fired", alarm("2026-11-12T07:58:00Z"), false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, hits := evaluateChannelAutoResetRule(&tc.rule, facts)
			assert.Equal(t, tc.matched, matched)
			assert.Len(t, hits, tc.hits)
		})
	}
}

func TestNormalizeChannelAutoResetConfig(t *testing.T) {
	now := autoResetTestNow
	deep := fiveHourAtLeast(50)
	for range 3 {
		deep = ChannelAutoResetRule{Op: "and", Children: []ChannelAutoResetRule{deep}}
	}
	wide := ChannelAutoResetRule{Op: "or"}
	for range 20 {
		wide.Children = append(wide.Children, fiveHourAtLeast(50))
	}
	cases := []struct {
		name        string
		channelType int
		rule        ChannelAutoResetRule
		wantErr     bool
	}{
		{"depth 4 rejected", constant.ChannelTypeClaudeOAuth, deep, true},
		{"21 nodes rejected", constant.ChannelTypeClaudeOAuth, wide, true},
		{"claude fable weekly window", constant.ChannelTypeClaudeOAuth, ChannelAutoResetRule{Type: ChannelAutoResetCondWeekly, Window: "seven_day_overage_included", Threshold: 80}, false},
		{"codex has no sonnet window", constant.ChannelTypeCodex, ChannelAutoResetRule{Type: ChannelAutoResetCondWeekly, Window: "seven_day_sonnet", Threshold: 80}, true},
		{"threshold above 100", constant.ChannelTypeCodex, fiveHourAtLeast(101), true},
		{"unknown condition", constant.ChannelTypeCodex, ChannelAutoResetRule{Type: "spend_usd"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeChannelAutoResetConfig(autoResetTestConfig(tc.rule), tc.channelType, now)
			assert.Equal(t, tc.wantErr, err != nil, "err: %v", err)
		})
	}

	t.Run("alarms in UTC, past ones dropped", func(t *testing.T) {
		cfg := autoResetTestConfig(ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{
			"2026-11-12T18:00:00+08:00", "2026-11-12T07:00:00Z", "2026-11-12T10:00:00Z",
		}})
		got, err := NormalizeChannelAutoResetConfig(cfg, constant.ChannelTypeClaudeOAuth, now)
		require.NoError(t, err)
		assert.Equal(t, []string{"2026-11-12T10:00:00Z"}, got.Rule.Times)
	})

	t.Run("switching on needs a condition", func(t *testing.T) {
		_, err := NormalizeChannelAutoResetConfig(autoResetTestConfig(ChannelAutoResetRule{Op: "or"}), constant.ChannelTypeClaudeOAuth, now)
		assert.Error(t, err)
	})
}

func TestChannelAutoResetAlarmFiresOnceAndExpires(t *testing.T) {
	now := autoResetTestNow
	cfg := autoResetTestConfig(ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{
		"2026-11-12T07:59:00Z", // due
		"2026-11-12T07:20:00Z", // missed by more than the grace
		"2026-11-13T07:00:00Z", // tomorrow
	}})
	ch := seedAutoResetChannel(t, cfg, nil)
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 40}}, credits: ChannelAutoResetCredits{Available: 1}}
	engine := NewChannelAutoResetEngine(provider)

	decision := engine.Evaluate(context.Background(), ch, now, false, nil)
	assert.Equal(t, AutoResetReasonRedeemed, decision.Reason)

	ch = reloadAutoResetChannel(t, ch.Id)
	assert.Equal(t, []string{"2026-11-13T07:00:00Z"}, LoadChannelAutoResetConfig(ch).Rule.Times)
	assert.Equal(t, []string{"2026-11-12T07:59:00Z"}, loadAutoResetState(t, ch.Id).FiredAlarms)

	// A stale save that brings the fired alarm back does not fire it again.
	stale := LoadChannelAutoResetConfig(ch)
	stale.Rule.Times = append(stale.Rule.Times, "2026-11-12T07:59:00Z")
	require.NoError(t, model.UpdateChannelOtherInfoEntry(ch.Id, ChannelAutoResetConfigKey, stale))
	decision = engine.Evaluate(context.Background(), reloadAutoResetChannel(t, ch.Id), now.Add(2*time.Hour), false, nil)
	assert.Equal(t, AutoResetReasonNotMatched, decision.Reason)
	_, _, redeems := provider.calls()
	assert.Equal(t, 1, redeems)
	assert.Equal(t, []string{"2026-11-13T07:00:00Z"}, LoadChannelAutoResetConfig(reloadAutoResetChannel(t, ch.Id)).Rule.Times)
}

func TestChannelAutoResetExpiringCreditTriggers(t *testing.T) {
	now := autoResetTestNow
	expiry := now.Add(3 * time.Hour)
	cfg := autoResetTestConfig(ChannelAutoResetRule{Type: ChannelAutoResetCondExpiring, Hours: 4})
	ch := seedAutoResetChannel(t, cfg, nil)
	// The 5-hour window resets in 5 minutes, but an expiring credit would be
	// lost anyway, so the natural-reset check is overridden by default.
	provider := &fakeAutoResetProvider{
		usage:   map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 70, ResetsAt: now.Add(5 * time.Minute)}},
		credits: ChannelAutoResetCredits{Available: 2, EarliestExpiry: &expiry},
	}
	decision := NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, now, false, nil)
	assert.Equal(t, AutoResetReasonRedeemed, decision.Reason)
	require.Len(t, decision.Hits, 1)
	assert.Equal(t, 3.0, decision.Hits[0].Value)
}

func TestChannelAutoResetGuards(t *testing.T) {
	now := autoResetTestNow
	expiry := now.Add(time.Hour)
	resetsSoon := map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 95, ResetsAt: now.Add(10 * time.Minute)}}
	resetsLater := map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 95, ResetsAt: now.Add(3 * time.Hour)}}
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	cases := []struct {
		name   string
		rule   ChannelAutoResetRule
		mutate func(*ChannelAutoResetConfig)
		state  ChannelAutoResetState
		usage  map[string]ChannelAutoResetWindow
		reason string
	}{
		{name: "min interval", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{LastRedeemAt: ago(30 * time.Minute)}, usage: resetsLater, reason: AutoResetReasonMinInterval},
		{name: "interval elapsed", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{RedeemedAt: []int64{ago(61 * time.Minute)}, LastRedeemAt: ago(61 * time.Minute)}, usage: resetsLater, reason: AutoResetReasonRedeemed},
		{name: "daily cap", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{RedeemedAt: []int64{ago(20 * time.Hour), ago(2 * time.Hour)}, LastRedeemAt: ago(2 * time.Hour)}, usage: resetsLater, reason: AutoResetReasonDailyCap},
		{name: "weekly cap", rule: fiveHourAtLeast(90), mutate: func(c *ChannelAutoResetConfig) { c.MaxPerWeek = 2 }, state: ChannelAutoResetState{RedeemedAt: []int64{ago(6 * 24 * time.Hour), ago(3 * 24 * time.Hour)}}, usage: resetsLater, reason: AutoResetReasonWeeklyCap},
		{name: "backoff", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{FailCount: 1, BackoffUntil: now.Add(time.Minute).Unix()}, usage: resetsLater, reason: AutoResetReasonBackoff},
		{name: "window resets soon", rule: fiveHourAtLeast(90), usage: resetsSoon, reason: AutoResetReasonResetsSoon},
		{name: "margin off", rule: fiveHourAtLeast(90), mutate: func(c *ChannelAutoResetConfig) { c.SkipIfResetWithinMinutes = 0 }, usage: resetsSoon, reason: AutoResetReasonRedeemed},
		{name: "alarm overrides margin", rule: ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{"2026-11-12T08:00:00Z"}}, usage: resetsSoon, reason: AutoResetReasonRedeemed},
		{name: "alarm without override", rule: ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{"2026-11-12T08:00:00Z"}}, mutate: func(c *ChannelAutoResetConfig) { c.AlarmIgnoresResetSoon = false }, usage: resetsSoon, reason: AutoResetReasonResetsSoon},
		{name: "expiring without override", rule: ChannelAutoResetRule{Type: ChannelAutoResetCondExpiring, Hours: 2}, mutate: func(c *ChannelAutoResetConfig) { c.ExpiringIgnoresResetSoon = false }, usage: resetsSoon, reason: AutoResetReasonResetsSoon},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := autoResetTestConfig(tc.rule)
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			ch := seedAutoResetChannel(t, cfg, &tc.state)
			provider := &fakeAutoResetProvider{usage: tc.usage, credits: ChannelAutoResetCredits{Available: 1, EarliestExpiry: &expiry}}
			decision := NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, now, false, nil)
			assert.Equal(t, tc.reason, decision.Reason)
			_, _, redeems := provider.calls()
			if tc.reason == AutoResetReasonRedeemed {
				assert.Equal(t, 1, redeems)
			} else {
				assert.Zero(t, redeems)
			}
		})
	}
}

func TestChannelAutoResetNoCreditWaits(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100}}}
	decision := NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, now, false, nil)
	assert.Equal(t, AutoResetReasonNoCredit, decision.Reason)
	_, _, redeems := provider.calls()
	assert.Zero(t, redeems)
	state := loadAutoResetState(t, ch.Id)
	assert.Equal(t, now.Add(channelAutoResetPause).Unix(), state.BackoffUntil)
	assert.Zero(t, state.FailCount)
}

// Two engines stand for two nodes sharing the database; whatever the
// interleaving, one credit is redeemed.
func TestChannelAutoResetRedeemsOnceAcrossNodes(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	provider := &fakeAutoResetProvider{
		usage:       map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100, ResetsAt: now.Add(4 * time.Hour)}},
		credits:     ChannelAutoResetCredits{Available: 3},
		redeemDelay: 50 * time.Millisecond,
	}
	nodes := []*ChannelAutoResetEngine{NewChannelAutoResetEngine(provider), NewChannelAutoResetEngine(provider)}
	decisions := make([]ChannelAutoResetDecision, len(nodes))
	var wg sync.WaitGroup
	for i, engine := range nodes {
		wg.Go(func() {
			decisions[i] = engine.Evaluate(context.Background(), ch, now, false, nil)
		})
	}
	wg.Wait()

	_, _, redeems := provider.calls()
	assert.Equal(t, 1, redeems)
	reasons := []string{decisions[0].Reason, decisions[1].Reason}
	assert.Contains(t, reasons, AutoResetReasonRedeemed)
	assert.Subset(t, []string{AutoResetReasonRedeemed, AutoResetReasonBusy, AutoResetReasonMinInterval, AutoResetReasonInFlight}, reasons)

	// A later evaluation from either node sees the stored redemption.
	decision := nodes[1].Evaluate(context.Background(), reloadAutoResetChannel(t, ch.Id), now.Add(time.Minute), false, nil)
	assert.Equal(t, AutoResetReasonMinInterval, decision.Reason)
	_, _, redeems = provider.calls()
	assert.Equal(t, 1, redeems)
}

func TestChannelAutoResetFailureBacksOff(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	provider := &fakeAutoResetProvider{
		usage:   map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100, ResetsAt: now.Add(4 * time.Hour)}},
		credits: ChannelAutoResetCredits{Available: 1},
		results: []ChannelAutoResetRedeemResult{
			{Result: "upstream_status_429"},
			{Result: "outcome_unknown", MaybeConsumed: true},
		},
	}
	engine := NewChannelAutoResetEngine(provider)
	run := func(at time.Time) ChannelAutoResetDecision {
		return engine.Evaluate(context.Background(), reloadAutoResetChannel(t, ch.Id), at, false, nil)
	}

	assert.Equal(t, AutoResetReasonFailed, run(now).Reason)
	state := loadAutoResetState(t, ch.Id)
	assert.Equal(t, 1, state.FailCount)
	assert.Equal(t, now.Add(5*time.Minute).Unix(), state.BackoffUntil)
	assert.Empty(t, state.RedeemedAt, "a definite rejection used no credit")

	assert.Equal(t, AutoResetReasonBackoff, run(now.Add(time.Minute)).Reason)

	second := now.Add(6 * time.Minute)
	assert.Equal(t, AutoResetReasonFailed, run(second).Reason)
	state = loadAutoResetState(t, ch.Id)
	assert.Equal(t, 2, state.FailCount)
	assert.Equal(t, second.Add(10*time.Minute).Unix(), state.BackoffUntil)
	assert.Equal(t, []int64{second.Unix()}, state.RedeemedAt, "an unknown outcome counts against the caps")
	_, _, redeems := provider.calls()
	assert.Equal(t, 2, redeems)
}

func TestChannelAutoResetDisabledDoesNothing(t *testing.T) {
	cfg := autoResetTestConfig(fiveHourAtLeast(1))
	cfg.Enabled = false
	ch := seedAutoResetChannel(t, cfg, nil)
	before := ch.OtherInfo
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100}}, credits: ChannelAutoResetCredits{Available: 1}}
	engine := NewChannelAutoResetEngine(provider)

	decision := engine.Evaluate(context.Background(), ch, autoResetTestNow, false, nil)
	assert.Equal(t, AutoResetReasonDisabled, decision.Reason)
	engine.RunOnce(context.Background())

	usage, credits, redeems := provider.calls()
	assert.Zero(t, usage+credits+redeems)
	assert.Equal(t, before, reloadAutoResetChannel(t, ch.Id).OtherInfo)
}

func TestChannelAutoResetDryRunAndUsageThrottle(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	before := ch.OtherInfo
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 92, ResetsAt: now.Add(time.Hour)}}, credits: ChannelAutoResetCredits{Available: 1}}
	engine := NewChannelAutoResetEngine(provider)

	decision := engine.Evaluate(context.Background(), ch, now, true, nil)
	assert.Equal(t, AutoResetReasonWouldRedeem, decision.Reason)
	assert.Equal(t, before, reloadAutoResetChannel(t, ch.Id).OtherInfo)

	// The snapshot is reused within 5 minutes; after a passive sample the
	// usage is read again, but upstream is not asked a second time.
	engine.Evaluate(context.Background(), ch, now.Add(time.Minute), true, nil)
	engine.ForgetUsage(ch.Id)
	engine.Evaluate(context.Background(), ch, now.Add(2*time.Minute), true, nil)
	provider.mu.Lock()
	assert.Equal(t, 2, provider.usageCalls)
	assert.Equal(t, 1, provider.upstreamCalls)
	provider.mu.Unlock()
	_, _, redeems := provider.calls()
	assert.Zero(t, redeems)
}

func TestChannelAutoResetAudit(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(ChannelAutoResetRule{Op: "or", Children: []ChannelAutoResetRule{
		fiveHourAtLeast(90),
		{Type: ChannelAutoResetCondWeekly, Window: "seven_day", Threshold: 99},
	}}), nil)
	provider := &fakeAutoResetProvider{
		usage:   map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 93.24, ResetsAt: now.Add(time.Hour)}, "seven_day": {Utilization: 40}},
		credits: ChannelAutoResetCredits{Available: 2},
	}
	require.Equal(t, AutoResetReasonRedeemed, NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, now, false, nil).Reason)

	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "channel.reset_credit_auto_redeem").Find(&logs).Error)
	require.Len(t, logs, 1)
	entry := logs[0]
	assert.Equal(t, 7, entry.UserId)
	assert.True(t, entry.Success)
	params := entry.Other.Op.Params
	encoded, err := common.Marshal(params)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"source": "auto",
		"channel_id": `+strconv.Itoa(ch.Id)+`,
		"provider": "claude",
		"result": "reset",
		"success": true,
		"matched": ["five_hour_used_pct 93.2>=90.0"],
		"usage_before": {"five_hour": 93.2, "seven_day": 40},
		"usage_after": {"five_hour": 93.2, "seven_day": 40},
		"credits_available_before": 2
	}`, string(encoded))
}

// gatedAutoResetProvider blocks the first LoadUsage or Redeem call until the
// test releases it.
type gatedAutoResetProvider struct {
	*fakeAutoResetProvider
	usageGate   chan struct{}
	usageIn     chan struct{}
	redeemGate  chan struct{}
	redeemIn    chan struct{}
	creditsGate chan struct{}
	creditsIn   chan struct{}
	usageOnce   sync.Once
	redeemOnce  sync.Once
	creditsOnce sync.Once
}

func (p *gatedAutoResetProvider) LoadCredits(ctx context.Context, ch *model.Channel) (*ChannelAutoResetCredits, error) {
	if p.creditsGate != nil {
		p.creditsOnce.Do(func() { close(p.creditsIn); <-p.creditsGate })
	}
	return p.fakeAutoResetProvider.LoadCredits(ctx, ch)
}

func (p *gatedAutoResetProvider) LoadUsage(ctx context.Context, ch *model.Channel, allowUpstream bool) (*ChannelAutoResetUsage, error) {
	if p.usageGate != nil {
		p.usageOnce.Do(func() { close(p.usageIn); <-p.usageGate })
	}
	return p.fakeAutoResetProvider.LoadUsage(ctx, ch, allowUpstream)
}

func (p *gatedAutoResetProvider) Redeem(ctx context.Context, ch *model.Channel) ChannelAutoResetRedeemResult {
	if p.redeemGate != nil {
		first := false
		p.redeemOnce.Do(func() { first = true })
		if first {
			close(p.redeemIn)
			<-p.redeemGate
		}
	}
	return p.fakeAutoResetProvider.Redeem(ctx, ch)
}

// An administrator switches automatic reset off while a tick is evaluating a
// due alarm: the tick neither redeems nor writes the old setting back.
func TestChannelAutoResetAdminDisableDuringEvaluation(t *testing.T) {
	now := autoResetTestNow
	cfg := autoResetTestConfig(ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{"2026-11-12T07:59:00Z", "2026-11-13T07:00:00Z"}})
	cfg.AlarmIgnoresResetSoon = false // the evaluation reads usage, where the test pauses it
	ch := seedAutoResetChannel(t, cfg, nil)
	provider := &gatedAutoResetProvider{
		fakeAutoResetProvider: &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 40}}, credits: ChannelAutoResetCredits{Available: 1}},
		usageGate:             make(chan struct{}),
		usageIn:               make(chan struct{}),
	}
	engine := NewChannelAutoResetEngine(provider)
	done := make(chan ChannelAutoResetDecision)
	go func() { done <- engine.Evaluate(context.Background(), ch, now, false, nil) }()
	<-provider.usageIn
	off := LoadChannelAutoResetConfig(ch)
	off.Enabled = false
	require.NoError(t, model.UpdateChannelOtherInfoEntry(ch.Id, ChannelAutoResetConfigKey, off))
	close(provider.usageGate)
	<-done

	_, _, redeems := provider.calls()
	assert.Zero(t, redeems)
	assert.False(t, LoadChannelAutoResetConfig(reloadAutoResetChannel(t, ch.Id)).Enabled)
}

// Node A is still waiting for the redemption answer when its lease runs out
// and node B takes the lease over: B must see A's redemption and not redeem.
func TestChannelAutoResetLeaseTakeoverDoesNotRedeemAgain(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	base := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100, ResetsAt: now.Add(4 * time.Hour)}}, credits: ChannelAutoResetCredits{Available: 3}}
	providerA := &gatedAutoResetProvider{fakeAutoResetProvider: base, redeemGate: make(chan struct{}), redeemIn: make(chan struct{})}
	nodeA, nodeB := NewChannelAutoResetEngine(providerA), NewChannelAutoResetEngine(base)
	done := make(chan ChannelAutoResetDecision)
	go func() { done <- nodeA.Evaluate(context.Background(), ch, now, false, nil) }()
	<-providerA.redeemIn

	decisionB := nodeB.Evaluate(context.Background(), reloadAutoResetChannel(t, ch.Id), now.Add(6*time.Minute), false, nil)
	close(providerA.redeemGate)
	<-done

	_, _, redeems := base.calls()
	assert.Equal(t, 1, redeems)
	assert.NotEqual(t, AutoResetReasonRedeemed, decisionB.Reason)
}

// A cached usage snapshot still matches, but usage read after the lease no
// longer does (for example after a manual reset elsewhere): nothing is redeemed.
func TestChannelAutoResetRechecksConditionsWithFreshUsage(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 95, ResetsAt: now.Add(4 * time.Hour)}}, credits: ChannelAutoResetCredits{Available: 1}}
	engine := NewChannelAutoResetEngine(provider)
	require.Equal(t, AutoResetReasonWouldRedeem, engine.Evaluate(context.Background(), ch, now, true, nil).Reason)

	provider.mu.Lock()
	provider.usage = map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 3, ResetsAt: now.Add(5 * time.Hour)}}
	provider.mu.Unlock()
	decision := engine.Evaluate(context.Background(), ch, now.Add(time.Minute), false, nil)

	assert.Equal(t, AutoResetReasonNotMatched, decision.Reason)
	_, _, redeems := provider.calls()
	assert.Zero(t, redeems)
}

// A manual or subscriber redemption holds the same lease while it runs and
// then counts against the minimum interval; one that used no credit does not.
func TestChannelAutoResetSharesLeaseWithManualRedemption(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100, ResetsAt: now.Add(4 * time.Hour)}}, credits: ChannelAutoResetCredits{Available: 2}}
	engine := NewChannelAutoResetEngine(provider)

	manual, err := BeginChannelRedemption(ch.Id, "manual")
	require.NoError(t, err)
	_, err = BeginChannelRedemption(ch.Id, "subscription")
	assert.ErrorIs(t, err, ErrChannelRedemptionBusy)
	assert.Contains(t, []string{AutoResetReasonInFlight, AutoResetReasonMinInterval}, engine.Evaluate(context.Background(), ch, now, false, nil).Reason)
	manual.Finish(true, nil)

	assert.Equal(t, AutoResetReasonMinInterval, engine.Evaluate(context.Background(), ch, now.Add(time.Minute), false, nil).Reason)
	state := loadAutoResetState(t, ch.Id)
	assert.Equal(t, "manual", state.LastRedeemSource)
	assert.Zero(t, state.InFlightSince)

	rejected, err := BeginChannelRedemption(ch.Id, "manual")
	require.NoError(t, err)
	rejected.Finish(false, nil)
	assert.Equal(t, state.LastRedeemAt, loadAutoResetState(t, ch.Id).LastRedeemAt, "a rejected redemption restores the previous record")
	_, _, redeems := provider.calls()
	assert.Zero(t, redeems)
}

// A node crashed after recording a redemption as in flight: it counts as a
// redemption, and the next lease holder resolves it once the interval passed.
func TestChannelAutoResetResolvesInterruptedRedemption(t *testing.T) {
	now := autoResetTestNow
	crashedAt := now.Add(-20 * time.Minute).Unix()
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), &ChannelAutoResetState{RedeemedAt: []int64{crashedAt}, LastRedeemAt: crashedAt, InFlightSince: crashedAt})
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100, ResetsAt: now.Add(4 * time.Hour)}}, credits: ChannelAutoResetCredits{Available: 2}}
	engine := NewChannelAutoResetEngine(provider)

	assert.Equal(t, AutoResetReasonMinInterval, engine.Evaluate(context.Background(), ch, now, false, nil).Reason)
	later := now.Add(45 * time.Minute)
	assert.Equal(t, AutoResetReasonRedeemed, engine.Evaluate(context.Background(), reloadAutoResetChannel(t, ch.Id), later, false, nil).Reason)

	state := loadAutoResetState(t, ch.Id)
	assert.Zero(t, state.InFlightSince)
	assert.Len(t, state.RedeemedAt, 2, "the interrupted redemption stays counted")
	_, _, redeems := provider.calls()
	assert.Equal(t, 1, redeems)
}

// A setting written around the dedicated endpoint is validated before use.
func TestChannelAutoResetSkipsInvalidStoredSetting(t *testing.T) {
	cfg := autoResetTestConfig(fiveHourAtLeast(500))
	ch := seedAutoResetChannel(t, cfg, nil)
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100}}, credits: ChannelAutoResetCredits{Available: 1}}

	decision := NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, autoResetTestNow, false, nil)

	assert.Equal(t, AutoResetReasonInvalidSetting, decision.Reason)
	usage, credits, redeems := provider.calls()
	assert.Zero(t, usage+credits+redeems)
	assert.Equal(t, AutoResetReasonInvalidSetting, loadAutoResetState(t, ch.Id).LastSkipReason)
}

// An alarm rule with the natural-reset override does not depend on usage, so
// it still fires while the usage API is down.
func TestChannelAutoResetAlarmFiresWithoutUsage(t *testing.T) {
	now := autoResetTestNow
	ch := seedAutoResetChannel(t, autoResetTestConfig(ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{"2026-11-12T07:58:00Z"}}), nil)
	provider := &fakeAutoResetProvider{usageErr: errors.New("usage API down"), credits: ChannelAutoResetCredits{Available: 1}}

	decision := NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, now, false, nil)

	assert.Equal(t, AutoResetReasonRedeemed, decision.Reason)
	usage, _, redeems := provider.calls()
	assert.Equal(t, 1, redeems)
	assert.Equal(t, 1, usage, "only the after-redemption read for the audit")
}

// Node A crashes while its alarm redemption is in flight (its Redeem never
// returns). With a minimum interval shorter than the alarm grace, node B
// evaluates after the interval: the alarm must not fire a second time.
func TestChannelAutoResetAlarmNotRefiredAfterCrash(t *testing.T) {
	alarmAt := autoResetTestNow
	cfg := autoResetTestConfig(ChannelAutoResetRule{Type: ChannelAutoResetCondAtTime, Times: []string{"2026-11-12T08:00:00Z"}})
	cfg.MinIntervalMinutes = 10
	ch := seedAutoResetChannel(t, cfg, nil)
	base := &fakeAutoResetProvider{credits: ChannelAutoResetCredits{Available: 3}}
	crashed := &gatedAutoResetProvider{fakeAutoResetProvider: base, redeemGate: make(chan struct{}), redeemIn: make(chan struct{})}
	t.Cleanup(func() { close(crashed.redeemGate) })
	go NewChannelAutoResetEngine(crashed).Evaluate(context.Background(), ch, alarmAt.Add(30*time.Second), false, nil)
	<-crashed.redeemIn

	decision := NewChannelAutoResetEngine(base).Evaluate(context.Background(), reloadAutoResetChannel(t, ch.Id), alarmAt.Add(11*time.Minute), false, nil)

	assert.NotEqual(t, AutoResetReasonRedeemed, decision.Reason)
	_, _, redeems := base.calls()
	assert.Zero(t, redeems, "node B sent nothing; node A's request is still blocked")
}

// A transient database error while the scheduler reads a channel leaves its
// shared state alone; only a deleted channel loses it.
func TestChannelAutoResetRunOnceKeepsStateOnReadError(t *testing.T) {
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), &ChannelAutoResetState{LastRedeemAt: autoResetTestNow.Unix()})
	require.NoError(t, model.DB.Create(&model.ChannelAutoResetState{ChannelId: ch.Id + 1000, AutoEnabled: true}).Error)
	callback := "test:fail_channel_read"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "channels" {
			tx.AddError(errors.New("database unavailable"))
		}
	}))
	engine := NewChannelAutoResetEngine(&fakeAutoResetProvider{})
	engine.RunOnce(context.Background())
	require.NoError(t, model.DB.Callback().Query().Remove(callback))

	state := loadAutoResetState(t, ch.Id)
	assert.Equal(t, autoResetTestNow.Unix(), state.LastRedeemAt)
	ids, err := model.ListChannelAutoResetEnabledIds()
	require.NoError(t, err)
	assert.Contains(t, ids, ch.Id)

	engine.RunOnce(context.Background())
	ids, err = model.ListChannelAutoResetEnabledIds()
	require.NoError(t, err)
	assert.NotContains(t, ids, ch.Id+1000, "the state of a deleted channel is removed")
	assert.Contains(t, ids, ch.Id)
}

// The switch is turned off while the lease holder reads credits upstream:
// the redemption is not sent.
func TestChannelAutoResetDisableDuringLeasedReads(t *testing.T) {
	now := autoResetTestNow
	cfg := autoResetTestConfig(fiveHourAtLeast(90))
	ch := seedAutoResetChannel(t, cfg, nil)
	provider := &gatedAutoResetProvider{
		fakeAutoResetProvider: &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 100, ResetsAt: now.Add(4 * time.Hour)}}, credits: ChannelAutoResetCredits{Available: 1}},
		creditsGate:           make(chan struct{}),
		creditsIn:             make(chan struct{}),
	}
	done := make(chan ChannelAutoResetDecision)
	go func() {
		done <- NewChannelAutoResetEngine(provider).Evaluate(context.Background(), ch, now, false, nil)
	}()
	<-provider.creditsIn
	off := cfg
	off.Enabled = false
	require.NoError(t, SaveChannelAutoResetConfig(ch.Id, off))
	close(provider.creditsGate)

	assert.NotEqual(t, AutoResetReasonRedeemed, (<-done).Reason)
	_, _, redeems := provider.calls()
	assert.Zero(t, redeems)
}

// A manual redemption outlives its lease and a subscriber redemption takes
// over. The late Finish of the first must not undo the second's record.
func TestChannelRedemptionLateFinishKeepsTakeover(t *testing.T) {
	ch := seedAutoResetChannel(t, autoResetTestConfig(fiveHourAtLeast(90)), nil)
	manual, err := BeginChannelRedemption(ch.Id, "manual")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.ChannelAutoResetState{}).Where("channel_id = ?", ch.Id).Update("lease_until", 1).Error)
	subscriber, err := BeginChannelRedemption(ch.Id, "subscription")
	require.NoError(t, err)

	manual.Finish(false, nil)
	state := loadAutoResetState(t, ch.Id)
	assert.Equal(t, "subscription", state.InFlightSource)
	assert.NotZero(t, state.LastRedeemAt)

	subscriber.Finish(true, nil)
	state = loadAutoResetState(t, ch.Id)
	assert.Equal(t, "subscription", state.LastRedeemSource)
	assert.NotZero(t, state.LastRedeemAt)
	assert.Zero(t, state.InFlightSince)
}

// Saving the same setting twice succeeds (MySQL reports an unchanged row as
// not affected; the compare-and-swap must not read that as contention).
func TestSaveChannelAutoResetConfigTwice(t *testing.T) {
	cfg := autoResetTestConfig(fiveHourAtLeast(90))
	ch := seedAutoResetChannel(t, cfg, nil)
	require.NoError(t, SaveChannelAutoResetConfig(ch.Id, cfg))
	require.NoError(t, SaveChannelAutoResetConfig(ch.Id, cfg))
}
