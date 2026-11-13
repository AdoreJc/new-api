package service

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, model.DB.AutoMigrate(&model.AuditLog{}))
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM channels")
		model.DB.Exec("DELETE FROM system_task_locks")
		model.DB.Exec("DELETE FROM audit_logs")
	})
	ch := &model.Channel{Type: constant.ChannelTypeClaudeOAuth, Name: "auto-reset-test", Key: "{}", Status: common.ChannelStatusEnabled}
	info := map[string]any{ChannelAutoResetConfigKey: cfg}
	if state != nil {
		info[ChannelAutoResetStateKey] = *state
	}
	ch.SetOtherInfo(info)
	require.NoError(t, model.DB.Create(ch).Error)
	return reloadAutoResetChannel(t, ch.Id)
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
		{"alarm past grace", alarm("2026-11-12T07:49:00Z"), false, 0},
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
		"2026-11-12T07:40:00Z", // missed by more than the grace
		"2026-11-13T07:00:00Z", // tomorrow
	}})
	ch := seedAutoResetChannel(t, cfg, nil)
	provider := &fakeAutoResetProvider{usage: map[string]ChannelAutoResetWindow{"five_hour": {Utilization: 40}}, credits: ChannelAutoResetCredits{Available: 1}}
	engine := NewChannelAutoResetEngine(provider)

	decision := engine.Evaluate(context.Background(), ch, now, false, nil)
	assert.Equal(t, AutoResetReasonRedeemed, decision.Reason)

	ch = reloadAutoResetChannel(t, ch.Id)
	assert.Equal(t, []string{"2026-11-13T07:00:00Z"}, LoadChannelAutoResetConfig(ch).Rule.Times)
	assert.Equal(t, []string{"2026-11-12T07:59:00Z"}, LoadChannelAutoResetState(ch).FiredAlarms)

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
		{name: "min interval", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{RedeemedAt: []int64{ago(30 * time.Minute)}}, usage: resetsLater, reason: AutoResetReasonMinInterval},
		{name: "interval elapsed", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{RedeemedAt: []int64{ago(61 * time.Minute)}}, usage: resetsLater, reason: AutoResetReasonRedeemed},
		{name: "daily cap", rule: fiveHourAtLeast(90), state: ChannelAutoResetState{RedeemedAt: []int64{ago(20 * time.Hour), ago(2 * time.Hour)}}, usage: resetsLater, reason: AutoResetReasonDailyCap},
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
	state := LoadChannelAutoResetState(reloadAutoResetChannel(t, ch.Id))
	assert.Equal(t, now.Add(channelAutoResetNoCreditRecheck).Unix(), state.BackoffUntil)
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
	assert.Subset(t, []string{AutoResetReasonRedeemed, AutoResetReasonBusy, AutoResetReasonMinInterval}, reasons)

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
	state := LoadChannelAutoResetState(reloadAutoResetChannel(t, ch.Id))
	assert.Equal(t, 1, state.FailCount)
	assert.Equal(t, now.Add(5*time.Minute).Unix(), state.BackoffUntil)
	assert.Empty(t, state.RedeemedAt, "a definite rejection used no credit")

	assert.Equal(t, AutoResetReasonBackoff, run(now.Add(time.Minute)).Reason)

	second := now.Add(6 * time.Minute)
	assert.Equal(t, AutoResetReasonFailed, run(second).Reason)
	state = LoadChannelAutoResetState(reloadAutoResetChannel(t, ch.Id))
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
	engine.RunOnce(context.Background(), autoResetTestNow)

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
