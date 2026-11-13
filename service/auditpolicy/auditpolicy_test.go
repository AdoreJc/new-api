package auditpolicy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/contentmoderation"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usePromptAudit(t *testing.T, config prompt_audit_setting.Config) {
	t.Helper()
	previous := prompt_audit_setting.Current()
	encoded, err := common.Marshal(config)
	require.NoError(t, err)
	prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, string(encoded))
	t.Cleanup(func() {
		encoded, _ := common.Marshal(previous.Config)
		prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, string(encoded))
	})
}

func useContentModeration(t *testing.T, config content_moderation_setting.Config, keys []string) {
	t.Helper()
	require.NoError(t, config.Normalize())
	previous := content_moderation_setting.Current()
	encoded, err := common.Marshal(config)
	require.NoError(t, err)
	encodedKeys, err := common.Marshal(keys)
	require.NoError(t, err)
	content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(encoded))
	content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, string(encodedKeys))
	t.Cleanup(func() {
		encoded, _ := common.Marshal(previous.Config)
		encodedKeys, _ := common.Marshal(previous.Keys)
		content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(encoded))
		content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, string(encodedKeys))
	})
}

func usePolicy(t *testing.T, config audit_policy_setting.Config) {
	t.Helper()
	require.NoError(t, config.Normalize())
	encoded, err := common.Marshal(config)
	require.NoError(t, err)
	audit_policy_setting.ApplyOption(audit_policy_setting.OptionKey, string(encoded))
	require.True(t, audit_policy_setting.Current().Saved)
	t.Cleanup(func() { audit_policy_setting.ApplyOption(audit_policy_setting.OptionKey, "") })
}

func guardEndpoint(t *testing.T, answer string, delay time.Duration) (prompt_audit_setting.Endpoint, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(delay)
		encoded, _ := common.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)
	return prompt_audit_setting.Endpoint{ID: "g1", BaseURL: server.URL, Model: "guard", TimeoutMS: 200, Enabled: true}, calls
}

func blockingConfig(endpoint prompt_audit_setting.Endpoint) prompt_audit_setting.Config {
	config := prompt_audit_setting.DefaultConfig()
	config.Enabled, config.Mode = true, prompt_audit_setting.ModeBlocking
	config.Endpoints = []prompt_audit_setting.Endpoint{endpoint}
	return config
}

func relayRequest(text string, userID int) (*gin.Context, *relaycommon.RelayInfo) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"gpt-x","messages":[{"role":"user","content":"` + text + `"}]}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Set(common.RequestIdKey, "req-1")
	return c, &relaycommon.RelayInfo{UserId: userID, TokenId: 3, UsingGroup: "vip", OriginModelName: "gpt-x",
		RelayFormat: types.RelayFormatOpenAI, RequestURLPath: "/v1/chat/completions"}
}

func TestRuleMatching(t *testing.T) {
	config := audit_policy_setting.Config{
		Rules: []audit_policy_setting.Rule{
			{ID: "off", Enabled: false, Match: audit_policy_setting.Match{Groups: []string{"*"}}},
			{ID: "blocked-users", Enabled: true, Match: audit_policy_setting.Match{List: audit_policy_setting.ListBlocklist}},
			{ID: "verified", Enabled: true, Match: audit_policy_setting.Match{List: audit_policy_setting.ListVerified}},
			{ID: "thinking", Enabled: true, Match: audit_policy_setting.Match{Models: []string{"claude-*-thinking"}}},
			{ID: "oauth", Enabled: true, Match: audit_policy_setting.Match{Groups: []string{"claude-ch*", "codex-ch*"}, ExcludeModels: []string{"*-mini"}}},
			{ID: "ws", Enabled: true, Match: audit_policy_setting.Match{Formats: []string{FormatResponsesWebSocket}}},
			{ID: "token", Enabled: true, Match: audit_policy_setting.Match{TokenIDs: []int{77}}},
		},
		Allowlist: []audit_policy_setting.ListEntry{{UserID: 5, Verified: true}, {UserID: 6}, {UserID: 8, TokenID: 80, Verified: true}},
		Blocklist: []audit_policy_setting.ListEntry{{UserID: 9}},
	}
	require.NoError(t, config.Normalize())
	tests := []struct {
		name    string
		subject audit_policy_setting.Subject
		want    string
	}{
		{"a blocklisted user matches before everything below", audit_policy_setting.Subject{UserID: 9, Group: "claude-ch1"}, "blocked-users"},
		{"a verified allowlist user", audit_policy_setting.Subject{UserID: 5, Group: "claude-ch1"}, "verified"},
		{"an allowlist user who is not verified", audit_policy_setting.Subject{UserID: 6, Group: "claude-ch1", Model: "opus"}, "oauth"},
		{"a verified token entry covers only that token", audit_policy_setting.Subject{UserID: 8, TokenID: 80, Group: "x"}, "verified"},
		{"another token of the same user", audit_policy_setting.Subject{UserID: 8, TokenID: 81, Group: "x"}, audit_policy_setting.DefaultRuleID},
		{"a wildcard inside a model pattern", audit_policy_setting.Subject{UserID: 1, Group: "x", Model: "claude-opus-thinking"}, "thinking"},
		{"a group prefix", audit_policy_setting.Subject{UserID: 1, Group: "codex-ch3", Model: "gpt-5"}, "oauth"},
		{"an excluded model falls through", audit_policy_setting.Subject{UserID: 1, Group: "codex-ch3", Model: "gpt-5-mini"}, audit_policy_setting.DefaultRuleID},
		{"an auto token matches by its requested group", audit_policy_setting.Subject{UserID: 1, Group: "default", RequestedGroup: "claude-ch-auto"}, "oauth"},
		{"a relay format", audit_policy_setting.Subject{UserID: 1, Group: "x", Format: FormatResponsesWebSocket}, "ws"},
		{"a token id", audit_policy_setting.Subject{UserID: 1, TokenID: 77, Group: "x"}, "token"},
		{"nothing matches", audit_policy_setting.Subject{UserID: 1, Group: "default", Model: "gpt-5"}, audit_policy_setting.DefaultRuleID},
	}
	for _, test := range tests {
		assert.Equal(t, test.want, config.Resolve(test.subject).Rule.ID, test.name)
	}
	assert.True(t, audit_policy_setting.Glob("*", ""))
	assert.True(t, audit_policy_setting.Glob("a*b*c", "a-b-b-c"))
	assert.False(t, audit_policy_setting.Glob("a*b", "a-b-c"))
}

func TestRuleValidation(t *testing.T) {
	valid := func() audit_policy_setting.Config {
		return audit_policy_setting.Config{Rules: []audit_policy_setting.Rule{{ID: "r", Enabled: true}}}
	}
	tests := []struct {
		name   string
		mutate func(*audit_policy_setting.Config)
	}{
		{"duplicate ids", func(c *audit_policy_setting.Config) { c.Rules = append(c.Rules, c.Rules[0]) }},
		{"the default id is reserved", func(c *audit_policy_setting.Config) { c.Rules[0].ID = audit_policy_setting.DefaultRuleID }},
		{"a reroute without a target", func(c *audit_policy_setting.Config) {
			c.Rules[0].PromptAudit = audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionReroute}
		}},
		{"a reroute fallback that reroutes", func(c *audit_policy_setting.Config) { c.Rules[0].RerouteFallback = audit_policy_setting.ActionReroute }},
		{"an unknown category", func(c *audit_policy_setting.Config) {
			c.Rules[0].PromptAudit.CategoryLevels = map[string]string{"nope": audit_policy_setting.LevelUnsafe}
		}},
		{"an unknown format", func(c *audit_policy_setting.Config) { c.Rules[0].Match.Formats = []string{"smtp"} }},
		{"a blocklist entry without a user", func(c *audit_policy_setting.Config) { c.Blocklist = []audit_policy_setting.ListEntry{{TokenID: 3}} }},
	}
	for _, test := range tests {
		config := valid()
		test.mutate(&config)
		assert.Error(t, config.Normalize(), test.name)
	}
	config := valid()
	require.NoError(t, config.Normalize())
	assert.Equal(t, audit_policy_setting.ActionObserve, config.Rules[0].PromptAudit.Action, "observe is the default action")
	assert.Equal(t, audit_policy_setting.FailOpen, config.Rules[0].PromptAudit.FailPolicy, "fail-open is the default")
	assert.Equal(t, audit_policy_setting.FailOpen, config.Rules[0].ContentModeration.FailPolicy)
}

// TestDerivedRulesKeepLegacyScopes compares the derived rules with the legacy
// scope checks of both engines over a grid of groups, models and users.
func TestDerivedRulesKeepLegacyScopes(t *testing.T) {
	endpoint, _ := guardEndpoint(t, "Safety: Safe\nCategories: None", 0)
	pa := blockingConfig(endpoint)
	pa.Mode = prompt_audit_setting.ModeAsync
	cm := content_moderation_setting.DefaultConfig()
	scopes := []struct {
		name  string
		setup func()
	}{
		{"production: prompt audit on every group, moderation off", func() {}},
		{"both on with overlapping prefixes, a model filter and an allowlist", func() {
			pa.AllGroups, pa.Groups = false, []string{"claude-ch*", "vip"}
			cm.Mode, cm.AllGroups, cm.Groups = content_moderation_setting.ModePreBlock, false, []string{"claude-*", "default"}
			cm.ModelFilter = content_moderation_setting.ModelFilter{Type: content_moderation_setting.ModelFilterExclude, Models: []string{"free"}}
			cm.AllowlistUserIDs = []int{9}
		}},
		{"moderation observing an include list, prompt audit off", func() {
			pa.Enabled = false
			cm.Mode, cm.AllGroups = content_moderation_setting.ModeObserve, true
			cm.ModelFilter = content_moderation_setting.ModelFilter{Type: content_moderation_setting.ModelFilterInclude, Models: []string{"paid"}}
		}},
	}
	groups := []string{"default", "vip", "claude-ch1", "claude-x", "codex-ch1"}
	models := []string{"paid", "free"}
	for _, scope := range scopes {
		scope.setup()
		require.NoError(t, pa.Normalize())
		require.NoError(t, cm.Normalize())
		usePromptAudit(t, pa)
		useContentModeration(t, cm, []string{"k"})
		config := audit_policy_setting.Current().Config
		for _, group := range groups {
			for _, model := range models {
				for _, user := range []int{1, 9} {
					subject := audit_policy_setting.Subject{UserID: user, Group: group, Model: model}
					resolution := config.Resolve(subject)
					legacyPA := pa.Enabled && pa.IncludesGroup(group)
					assert.Equal(t, legacyPA, resolution.Rule.PromptAudit.Enabled, "%s: prompt audit for %+v", scope.name, subject)
					if legacyPA {
						assert.Equal(t, audit_policy_setting.ActionObserve, resolution.Rule.PromptAudit.Action)
					}
					legacyCM := contentmoderation.ConfigScopePolicy{}.Decide(contentmoderation.Subject{UserID: user, Group: group, Model: model})
					derivedCM := contentDecision(resolution.Rule, resolution.Listing, cm.Mode)
					assert.Equal(t, legacyCM.Moderate, derivedCM.Moderate, "%s: moderation for %+v", scope.name, subject)
					assert.Equal(t, legacyCM.Action, derivedCM.Action, "%s: moderation action for %+v", scope.name, subject)
					assert.Equal(t, legacyCM.Dispose, derivedCM.Dispose, "%s: disposal for %+v", scope.name, subject)
					assert.Equal(t, legacyCM.SampleRate, derivedCM.SampleRate)
				}
			}
		}
	}
}

func TestPromptAuditCategoryActions(t *testing.T) {
	rule := audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionBlock,
		CategoryActions: map[string]string{"politically_sensitive_topics": audit_policy_setting.ActionIgnore, "copyright_violation": audit_policy_setting.ActionObserve}}
	assert.Equal(t, audit_policy_setting.ActionBlock, rule.ActionFor([]string{"violent"}))
	assert.Equal(t, audit_policy_setting.ActionIgnore, rule.ActionFor([]string{"politically_sensitive_topics"}))
	assert.Equal(t, audit_policy_setting.ActionObserve, rule.ActionFor([]string{"copyright_violation", "politically_sensitive_topics"}))
	assert.Equal(t, audit_policy_setting.ActionBlock, rule.ActionFor([]string{"copyright_violation", "violent"}), "the most severe action wins")
	assert.Equal(t, audit_policy_setting.ActionBlock, rule.ActionFor(nil), "an unknown category takes the rule action")
}

func blockRule(failPolicy string) audit_policy_setting.Config {
	return audit_policy_setting.Config{Rules: []audit_policy_setting.Rule{{
		ID: "vip", Enabled: true, Match: audit_policy_setting.Match{Groups: []string{"vip"}},
		PromptAudit: audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionBlock, FailPolicy: failPolicy},
	}}}
}

func TestEvaluateBlocksOnlyInBlockingMode(t *testing.T) {
	require.NoError(t, i18n.Init())
	endpoint, calls := guardEndpoint(t, "Safety: Unsafe\nCategories: Violent", 0)
	config := blockingConfig(endpoint)
	usePromptAudit(t, config)
	usePolicy(t, blockRule(audit_policy_setting.FailOpen))

	c, info := relayRequest("attack", 1)
	result := Evaluate(c, info, "", "")
	require.NotNil(t, result.Err)
	assert.Equal(t, http.StatusForbidden, result.Err.StatusCode)
	assert.Equal(t, ErrorCodePromptAuditBlocked, result.Err.GetErrorCode())
	assert.NotContains(t, strings.ToLower(result.Err.Error()), "violent", "the reply names no category")
	assert.Equal(t, int64(1), calls.Load())

	again := Evaluate(c, info, "", "")
	assert.Same(t, result.Err, again.Err, "the same request is never checked twice")
	assert.Equal(t, int64(1), calls.Load())

	config.Mode = prompt_audit_setting.ModeAsync
	usePromptAudit(t, config)
	c, info = relayRequest("attack", 1)
	assert.Nil(t, Evaluate(c, info, "", "").Err, "async_audit mode never blocks, whatever the rule says")
	plan := Explain(audit_policy_setting.Subject{UserID: 1, Group: "vip"})
	assert.Equal(t, engineAsync, plan.PromptAudit)
	assert.Equal(t, audit_policy_setting.ActionObserve, plan.PromptAuditAction)
}

func TestEvaluateFailurePolicy(t *testing.T) {
	require.NoError(t, i18n.Init())
	endpoint, _ := guardEndpoint(t, "Safety: Unsafe\nCategories: Violent", 500*time.Millisecond)
	usePromptAudit(t, blockingConfig(endpoint))

	usePolicy(t, blockRule(audit_policy_setting.FailOpen))
	c, info := relayRequest("attack", 1)
	assert.Nil(t, Evaluate(c, info, "", "").Err, "a Guard timeout lets the request through when failing open")

	usePolicy(t, blockRule(audit_policy_setting.FailClosed))
	c, info = relayRequest("attack", 1)
	result := Evaluate(c, info, "", "")
	require.NotNil(t, result.Err, "a Guard timeout rejects the request when failing closed")
	assert.Equal(t, http.StatusServiceUnavailable, result.Err.StatusCode)
	assert.Equal(t, ErrorCodePromptAuditUnavailable, result.Err.GetErrorCode())

	// A stored blocking setting that cannot be read is not "audit nothing":
	// the derived rule applies the stored failure policy.
	audit_policy_setting.ApplyOption(audit_policy_setting.OptionKey, "")
	prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, `{"enabled":true,"mode":"blocking","fail_policy":"closed","worker_count":0}`)
	require.True(t, prompt_audit_setting.Current().Degraded)
	c, info = relayRequest("hello", 1)
	result = Evaluate(c, info, "", "")
	require.NotNil(t, result.Err)
	assert.Equal(t, ErrorCodePromptAuditUnavailable, result.Err.GetErrorCode())
}

func TestEvaluateRerouteNeedsAVerifiedUser(t *testing.T) {
	require.NoError(t, i18n.Init())
	endpoint, _ := guardEndpoint(t, "Safety: Unsafe\nCategories: Jailbreak", 0)
	usePromptAudit(t, blockingConfig(endpoint))
	usePolicy(t, audit_policy_setting.Config{
		Rules: []audit_policy_setting.Rule{{
			ID: "reroute", Enabled: true, Match: audit_policy_setting.Match{Groups: []string{"vip"}},
			PromptAudit:     audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionReroute},
			Reroute:         audit_policy_setting.RerouteTarget{Model: "safe-model"},
			RerouteFallback: audit_policy_setting.ActionBlock,
		}},
		Allowlist: []audit_policy_setting.ListEntry{{UserID: 5, Verified: true}, {UserID: 6}},
	})

	c, info := relayRequest("attack", 5)
	result := Evaluate(c, info, "", "")
	require.Nil(t, result.Err)
	require.NotNil(t, result.Reroute)
	assert.Equal(t, "safe-model", result.Reroute.Model)

	for _, user := range []int{6, 7} {
		c, info = relayRequest("attack", user)
		result = Evaluate(c, info, "", "")
		assert.Nil(t, result.Reroute, "user %d is not verified and cannot be rerouted", user)
		require.NotNil(t, result.Err, "user %d gets the fallback", user)
		assert.Equal(t, ErrorCodePromptAuditBlocked, result.Err.GetErrorCode())
	}
}

func TestEvaluateContentModerationBlockWins(t *testing.T) {
	require.NoError(t, i18n.Init())
	endpoint, guardCalls := guardEndpoint(t, "Safety: Unsafe\nCategories: Violent", 0)
	usePromptAudit(t, blockingConfig(endpoint))
	var moderationCalls atomic.Int64
	moderation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		moderationCalls.Add(1)
		encoded, _ := common.Marshal(map[string]any{"results": []any{map[string]any{"category_scores": map[string]float64{"violence": 0.99}}}})
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(moderation.Close)
	cm := content_moderation_setting.DefaultConfig()
	cm.Mode, cm.BaseURL, cm.RetryCount = content_moderation_setting.ModePreBlock, moderation.URL, 0
	useContentModeration(t, cm, []string{"k"})
	policy := blockRule(audit_policy_setting.FailOpen)
	policy.Rules[0].ContentModeration = audit_policy_setting.ContentModeration{Enabled: true, Action: audit_policy_setting.ActionBlock, SampleRate: 100}
	usePolicy(t, policy)

	c, info := relayRequest("attack", 1)
	result := Evaluate(c, info, "", "")
	require.NotNil(t, result.Err)
	assert.Equal(t, contentmoderation.ErrorCodeBlocked, result.Err.GetErrorCode(), "a content moderation block takes priority")
	assert.Equal(t, int64(1), guardCalls.Load(), "both engines ran, in parallel")
	assert.Equal(t, int64(1), moderationCalls.Load())
}

func TestEvaluateChecksEachWebSocketTurn(t *testing.T) {
	require.NoError(t, i18n.Init())
	// Unsafe answers are never cached as safe, so every check reaches the Guard.
	endpoint, calls := guardEndpoint(t, "Safety: Unsafe\nCategories: Violent", 0)
	usePromptAudit(t, blockingConfig(endpoint))
	usePolicy(t, blockRule(audit_policy_setting.FailOpen))
	c, info := relayRequest("first", 1)
	assert.NotNil(t, Evaluate(c, info, FormatResponsesWebSocket, "turn-1").Err)
	assert.NotNil(t, Evaluate(c, info, FormatResponsesWebSocket, "turn-1").Err)
	assert.Equal(t, int64(1), calls.Load(), "a repeated turn is checked once")
	assert.NotNil(t, Evaluate(c, info, FormatResponsesWebSocket, "turn-2").Err)
	assert.Equal(t, int64(2), calls.Load(), "the next turn is checked again")
}
