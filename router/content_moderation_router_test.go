package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/contentmoderation"
	"github.com/QuantumNous/new-api/service/promptaudit"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContentModerationCoversEveryRelayPostRoute lists every POST route of
// the relay routers. Routes that reach controller.Relay with a user-written
// prompt must be moderated; every other route needs a stated reason. A new
// route fails this test until it is classified here.
func TestContentModerationCoversEveryRelayPostRoute(t *testing.T) {
	moderated := map[string]types.RelayFormat{
		"/v1/messages":                     types.RelayFormatClaude,
		"/v1/chat/completions":             types.RelayFormatOpenAI,
		"/v1/completions":                  types.RelayFormatOpenAI,
		"/pg/chat/completions":             types.RelayFormatOpenAI, // controller.Playground calls Relay
		"/v1/responses":                    types.RelayFormatOpenAIResponses,
		"/v1/responses/compact":            types.RelayFormatOpenAIResponsesCompaction,
		"/v1/edits":                        types.RelayFormatOpenAIImage,
		"/v1/images/generations":           types.RelayFormatOpenAIImage,
		"/v1/images/edits":                 types.RelayFormatOpenAIImage,
		"/v1/models/*path":                 types.RelayFormatGemini,
		"/v1beta/models/*path":             types.RelayFormatGemini,
		"/v1/engines/:model/embeddings":    "", // Gemini format, but an embedding request
		"/v1/embeddings":                   "",
		"/v1/moderations":                  "",
		"/v1/rerank":                       "",
		"/v1/audio/transcriptions":         "",
		"/v1/audio/translations":           "",
		"/v1/audio/speech":                 "",
		"/v1/alpha/search":                 "",
		"/v1/tools/search":                 "",
		"/v1/tools/fetch":                  "",
		"/v1/images/variations":            "",
		"/v1/files":                        "",
		"/v1/fine-tunes":                   "",
		"/v1/fine-tunes/:id/cancel":        "",
		"/v1/videos":                       "",
		"/v1/video/generations":            "",
		"/v1/videos/:video_id/remix":       "",
		"/kling/v1/videos/text2video":      "",
		"/kling/v1/videos/image2video":     "",
		"/jimeng/":                         "",
		"/mj/submit/action":                "",
		"/mj/submit/shorten":               "",
		"/mj/submit/modal":                 "",
		"/mj/submit/imagine":               "",
		"/mj/submit/change":                "",
		"/mj/submit/simple-change":         "",
		"/mj/submit/describe":              "",
		"/mj/submit/blend":                 "",
		"/mj/submit/edits":                 "",
		"/mj/submit/video":                 "",
		"/mj/notify":                       "",
		"/mj/task/list-by-condition":       "",
		"/mj/insight-face/swap":            "",
		"/mj/submit/upload-discord-images": "",
	}
	// Reasons for routes that are not moderated. Prefix entries cover route
	// families.
	exempt := map[string]string{
		"/v1/engines/:model/embeddings": "embedding input, no generation",
		"/v1/embeddings":                "embedding input, no generation",
		"/v1/moderations":               "itself a moderation call",
		"/v1/rerank":                    "documents to rank, no generation",
		"/v1/audio/":                    "audio upload or speech of given text; no chat prompt",
		"/v1/alpha/search":              "search query for a tool, no generation",
		"/v1/tools/":                    "search and fetch tools, no generation",
		"/v1/images/variations":         "not implemented",
		"/v1/files":                     "not implemented",
		"/v1/fine-tunes":                "not implemented",
		"/v1/video":                     "task submission, does not go through controller.Relay",
		"/kling/":                       "task submission, does not go through controller.Relay",
		"/jimeng/":                      "task submission, does not go through controller.Relay",
		"/mj/":                          "Midjourney submission, does not go through controller.Relay",
		"/:mode/mj/":                    "Midjourney submission, does not go through controller.Relay",
	}
	exemptReason := func(path string) string {
		for prefix, reason := range exempt {
			if path == prefix || strings.HasPrefix(path, prefix) {
				return reason
			}
		}
		return ""
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetRelayRouter(engine)
	SetVideoRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	for _, route := range engine.Routes() {
		if route.Method != http.MethodPost {
			continue
		}
		format, known := moderated[route.Path]
		if !known && strings.HasPrefix(route.Path, "/:mode/mj/") {
			format, known = "", true
		}
		if !assert.True(t, known, "classify POST %s for content moderation", route.Path) {
			continue
		}
		if format == "" {
			assert.NotEmpty(t, exemptReason(route.Path), "POST %s needs a reason to be exempt", route.Path)
			continue
		}
		requestPath := strings.ReplaceAll(route.Path, "*path", "g:generateContent")
		protocol, covered := contentmoderation.RelayProtocol(format, requestPath)
		assert.True(t, covered, "POST %s must be moderated", route.Path)
		assert.NotEmpty(t, protocol)
		protocol, covered = promptaudit.RelayProtocol(format, requestPath)
		assert.True(t, covered, "POST %s must be prompt audited", route.Path)
		assert.NotEmpty(t, protocol)
	}
}

// TestContentModerationBlockHappensBeforeBilling sends a flagged chat request
// through the real relay router: it is answered 403 with no upstream call, no
// quota change and no consume log, while a clean request reaches upstream.
func TestContentModerationBlockHappensBeforeBilling(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	// Performance metrics record a successful relay from a background
	// goroutine that would outlive this test; they are not under test here.
	previousPerfMetrics := strconv.FormatBool(perf_metrics_setting.GetSetting().Enabled)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": previousPerfMetrics}))
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.ContentModerationEvent{}, &model.ContentModerationFlaggedHash{}))

	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer upstream.Close()
	var moderationCalls atomic.Int64
	moderation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		moderationCalls.Add(1)
		var payload struct {
			Input string `json:"input"`
		}
		_ = common.DecodeJson(r.Body, &payload)
		score := 0.01
		if strings.Contains(payload.Input, "forbidden") {
			score = 0.99
		}
		encoded, _ := common.Marshal(map[string]any{"results": []any{map[string]any{"category_scores": map[string]float64{"violence": score}}}})
		_, _ = w.Write(encoded)
	}))
	defer moderation.Close()

	user := model.User{Username: "moderated-user", Status: common.UserStatusEnabled, Group: "default", Quota: 500000, AffCode: "aff-moderated"}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "moderationkey", Status: common.TokenStatusEnabled,
		ExpiredTime: -1, UnlimitedQuota: true, Name: "moderation"}).Error)
	baseURL := upstream.URL
	channel := model.Channel{Type: constant.ChannelTypeOpenAI, Key: "upstream-key", Status: common.ChannelStatusEnabled,
		Name: "mock", Models: "gpt-4o-mini", Group: "default", BaseURL: &baseURL}
	require.NoError(t, channel.Insert())

	config := content_moderation_setting.DefaultConfig()
	config.Mode, config.BaseURL, config.RetryCount = content_moderation_setting.ModePreBlock, moderation.URL, 0
	require.NoError(t, config.Normalize())
	configJSON, err := common.Marshal(config)
	require.NoError(t, err)
	previous := content_moderation_setting.Current()
	content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(configJSON))
	content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, `["moderation-key"]`)
	t.Cleanup(func() {
		configJSON, _ := common.Marshal(previous.Config)
		keysJSON, _ := common.Marshal(previous.Keys)
		content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(configJSON))
		content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, string(keysJSON))
	})

	previousRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o-mini":0.075}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(previousRatios) })

	engine := gin.New()
	SetRelayRouter(engine)
	send := func(text string) *httptest.ResponseRecorder {
		body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"` + text + `"}]}`
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer sk-moderationkey")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	blocked := send("something forbidden")
	require.Equal(t, http.StatusForbidden, blocked.Code, blocked.Body.String())
	assert.Contains(t, blocked.Body.String(), `"code":"content_moderation_blocked"`)
	assert.NotContains(t, blocked.Body.String(), "violence")
	assert.Equal(t, int64(1), moderationCalls.Load())
	assert.Zero(t, upstreamCalls.Load(), "a blocked request never reaches upstream")
	var reloaded model.User
	require.NoError(t, model.DB.First(&reloaded, user.Id).Error)
	assert.Equal(t, user.Quota, reloaded.Quota, "a blocked request is not pre-charged")
	assert.Zero(t, reloaded.UsedQuota)
	var consumeLogs int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs, "a blocked request writes no consume log")

	allowed := send("hello")
	assert.Equal(t, int64(2), moderationCalls.Load())
	assert.Equal(t, int64(1), upstreamCalls.Load(), "a clean request reaches upstream: %s", allowed.Body.String())
}

// auditRelayFixture is a user with an unlimited token, mock upstream
// channels for the given models, and a mock Guard that answers Unsafe for
// prompts containing "forbidden".
type auditRelayFixture struct {
	engine        *gin.Engine
	user          model.User
	upstreamCalls *atomic.Int64
	lastModel     *atomic.Value
}

func newAuditRelayFixture(t *testing.T, policy audit_policy_setting.Config, models ...string) auditRelayFixture {
	t.Helper()
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	previousPerfMetrics := strconv.FormatBool(perf_metrics_setting.GetSetting().Enabled)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": previousPerfMetrics}))
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.PromptAuditEvent{},
		&model.ContentModerationEvent{}, &model.ContentModerationFlaggedHash{}))

	fixture := auditRelayFixture{upstreamCalls: &atomic.Int64{}, lastModel: &atomic.Value{}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.upstreamCalls.Add(1)
		var payload struct {
			Model string `json:"model"`
		}
		_ = common.DecodeJson(r.Body, &payload)
		fixture.lastModel.Store(payload.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"` + payload.Model + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(upstream.Close)
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		answer := "Safety: Safe\nCategories: None"
		if strings.Contains(string(body), "forbidden") {
			answer = "Safety: Unsafe\nCategories: Violent"
		}
		encoded, _ := common.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(guard.Close)

	fixture.user = model.User{Username: "audited-user", Status: common.UserStatusEnabled, Group: "default", Quota: 500000, AffCode: "aff-audited"}
	require.NoError(t, model.DB.Create(&fixture.user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: fixture.user.Id, Key: "auditkey", Status: common.TokenStatusEnabled,
		ExpiredTime: -1, UnlimitedQuota: true, Name: "audit"}).Error)
	baseURL := upstream.URL
	for _, name := range models {
		channel := model.Channel{Type: constant.ChannelTypeOpenAI, Key: "upstream-key", Status: common.ChannelStatusEnabled,
			Name: "mock-" + name, Models: name, Group: "default", BaseURL: &baseURL}
		require.NoError(t, channel.Insert())
	}

	pa := prompt_audit_setting.DefaultConfig()
	pa.Enabled, pa.Mode = true, prompt_audit_setting.ModeBlocking
	pa.Endpoints = []prompt_audit_setting.Endpoint{{ID: "g1", BaseURL: guard.URL, Model: "guard", TimeoutMS: 2000, Enabled: true}}
	require.NoError(t, pa.Normalize())
	previousPA := prompt_audit_setting.Current().Config
	paJSON, err := common.Marshal(pa)
	require.NoError(t, err)
	prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, string(paJSON))
	for index := range policy.Allowlist {
		if policy.Allowlist[index].UserID == 0 {
			policy.Allowlist[index].UserID = fixture.user.Id
		}
	}
	require.NoError(t, policy.Normalize())
	policyJSON, err := common.Marshal(policy)
	require.NoError(t, err)
	audit_policy_setting.ApplyOption(audit_policy_setting.OptionKey, string(policyJSON))
	t.Cleanup(func() {
		previousJSON, _ := common.Marshal(previousPA)
		prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, string(previousJSON))
		audit_policy_setting.ApplyOption(audit_policy_setting.OptionKey, "")
	})

	previousRatios := ratio_setting.ModelRatio2JSONString()
	ratios := map[string]float64{}
	for _, name := range models {
		ratios[name] = 0.075
	}
	ratiosJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratiosJSON)))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(previousRatios) })

	fixture.engine = gin.New()
	SetRelayRouter(fixture.engine)
	SetTaskPluginProtocolRouter(fixture.engine)
	return fixture
}

func (f auditRelayFixture) send(path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-auditkey")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.engine.ServeHTTP(recorder, request)
	return recorder
}

// TestPromptAuditBlockHappensBeforeBilling sends flagged requests through
// the real relay router in blocking mode: each is answered with a JSON error
// in its protocol's shape, even when streaming was asked for, with no
// upstream call, no quota change and no consume log.
func TestPromptAuditBlockHappensBeforeBilling(t *testing.T) {
	policy := audit_policy_setting.Config{Default: audit_policy_setting.Rule{
		PromptAudit: audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionBlock},
	}}
	fixture := newAuditRelayFixture(t, policy, "gpt-4o-mini")

	blocked := fixture.send("/v1/chat/completions", `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"something forbidden"}]}`)
	require.Equal(t, http.StatusForbidden, blocked.Code, blocked.Body.String())
	assert.True(t, strings.HasPrefix(blocked.Header().Get("Content-Type"), "application/json"), "a blocked stream request gets JSON, not SSE")
	assert.NotContains(t, blocked.Body.String(), "data:")
	assert.Contains(t, blocked.Body.String(), `"code":"prompt_audit_blocked"`)
	assert.NotContains(t, strings.ToLower(blocked.Body.String()), "violent", "the reply names no category")

	claude := fixture.send("/v1/messages", `{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"something forbidden"}]}`)
	require.Equal(t, http.StatusForbidden, claude.Code, claude.Body.String())
	assert.Contains(t, claude.Body.String(), `"type":"error"`, "Claude requests get a Claude error body")
	assert.NotContains(t, strings.ToLower(claude.Body.String()), "violent")

	assert.Zero(t, fixture.upstreamCalls.Load(), "a blocked request never reaches upstream")
	var reloaded model.User
	require.NoError(t, model.DB.First(&reloaded, fixture.user.Id).Error)
	assert.Equal(t, fixture.user.Quota, reloaded.Quota, "a blocked request is not pre-charged")
	assert.Zero(t, reloaded.UsedQuota)
	var consumeLogs int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs, "a blocked request writes no consume log")

	allowed := fixture.send("/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	assert.Equal(t, http.StatusOK, allowed.Code, allowed.Body.String())
	assert.Equal(t, int64(1), fixture.upstreamCalls.Load())
}

// TestAuditRerouteBillsAndLogsTheActualModel reroutes a flagged request of
// a verified allowlist user: upstream receives the target model, and the
// consume log names it and records the requested one. A user who is not
// verified gets the rule's fallback instead.
func TestAuditRerouteBillsAndLogsTheActualModel(t *testing.T) {
	policy := audit_policy_setting.Config{
		Rules: []audit_policy_setting.Rule{{
			ID: "reroute-listed", Enabled: true, Match: audit_policy_setting.Match{List: audit_policy_setting.ListAllowlist},
			PromptAudit:     audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionReroute},
			Reroute:         audit_policy_setting.RerouteTarget{Model: "safe-model"},
			RerouteFallback: audit_policy_setting.ActionBlock,
		}},
		// UserID 0 is filled with the fixture user.
		Allowlist: []audit_policy_setting.ListEntry{{Verified: true}},
	}
	fixture := newAuditRelayFixture(t, policy, "gpt-4o-mini", "safe-model")

	rerouted := fixture.send("/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"something forbidden"}]}`)
	require.Equal(t, http.StatusOK, rerouted.Code, rerouted.Body.String())
	assert.Equal(t, "safe-model", fixture.lastModel.Load(), "upstream receives the target model")
	var log model.Log
	require.NoError(t, model.DB.Where("type = ?", model.LogTypeConsume).Order("id desc").First(&log).Error)
	assert.Equal(t, "safe-model", log.ModelName, "billing and the log use the actual model")
	assert.Contains(t, log.Other, `"from_model":"gpt-4o-mini"`)
	assert.Contains(t, log.Other, `"to_model":"safe-model"`)

	unverified := policy
	unverified.Allowlist = []audit_policy_setting.ListEntry{{UserID: fixture.user.Id, Verified: false}}
	require.NoError(t, unverified.Normalize())
	encoded, err := common.Marshal(unverified)
	require.NoError(t, err)
	audit_policy_setting.ApplyOption(audit_policy_setting.OptionKey, string(encoded))
	calls := fixture.upstreamCalls.Load()
	fallback := fixture.send("/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"something forbidden"}]}`)
	assert.Equal(t, http.StatusForbidden, fallback.Code, "a user who is not verified cannot be rerouted: %s", fallback.Body.String())
	assert.Equal(t, calls, fixture.upstreamCalls.Load())
}

func verifiedReroutePolicy(target audit_policy_setting.RerouteTarget) audit_policy_setting.Config {
	return audit_policy_setting.Config{
		Rules: []audit_policy_setting.Rule{{
			ID: "reroute-listed", Enabled: true, Match: audit_policy_setting.Match{List: audit_policy_setting.ListVerified},
			PromptAudit:     audit_policy_setting.PromptAudit{Enabled: true, Action: audit_policy_setting.ActionReroute},
			Reroute:         target,
			RerouteFallback: audit_policy_setting.ActionBlock,
		}},
		// UserID 0 is filled with the fixture user.
		Allowlist: []audit_policy_setting.ListEntry{{Verified: true}},
	}
}

// addAuditChannel adds a channel answering from its own mock upstream and
// returns that upstream's call counter.
func addAuditChannel(t *testing.T, name, models, group string, priority int64, status int, setting string) *atomic.Int64 {
	t.Helper()
	calls := &atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"safe-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(upstream.Close)
	baseURL := upstream.URL
	channel := model.Channel{Type: constant.ChannelTypeOpenAI, Key: "upstream-key", Status: common.ChannelStatusEnabled,
		Name: name, Models: models, Group: group, BaseURL: &baseURL, Priority: &priority}
	if setting != "" {
		channel.Setting = &setting
	}
	require.NoError(t, channel.Insert())
	return calls
}

// TestAuditRerouteRetriesNeverUsePassThroughChannels reroutes to a model
// whose preferred channel fails and whose only other channel forwards the
// client body unchanged: the retry never sends the original body there.
func TestAuditRerouteRetriesNeverUsePassThroughChannels(t *testing.T) {
	fixture := newAuditRelayFixture(t, verifiedReroutePolicy(audit_policy_setting.RerouteTarget{Model: "safe-model"}), "gpt-4o-mini")
	previousRetries := common.RetryTimes
	common.RetryTimes = 3
	t.Cleanup(func() { common.RetryTimes = previousRetries })
	failing := addAuditChannel(t, "safe-failing", "safe-model", "default", 10, http.StatusInternalServerError, "")
	passThrough := addAuditChannel(t, "safe-pass-through", "safe-model", "default", 0, http.StatusOK, `{"pass_through_body_enabled":true}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o-mini":0.075,"safe-model":0.075}`))

	response := fixture.send("/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"something forbidden"}]}`)
	assert.NotEqual(t, http.StatusOK, response.Code, response.Body.String())
	assert.Positive(t, failing.Load(), "the rerouted request went to the target's channel")
	assert.Zero(t, passThrough.Load(), "no attempt used a channel that forwards the original body")
	assert.Zero(t, fixture.upstreamCalls.Load(), "nothing reached the original model")
}

// TestAuditRerouteToAnotherGroupBillsThatGroup reroutes a flagged request
// of a token in the user's own group to another group: the request runs and
// is logged in the target group. The "auto" token case is covered by
// TestAuditRerouteMovesAnAutoTokenToTheTargetGroup in the controller tests.
func TestAuditRerouteToAnotherGroupBillsThatGroup(t *testing.T) {
	fixture := newAuditRelayFixture(t, verifiedReroutePolicy(audit_policy_setting.RerouteTarget{Group: "vip"}), "gpt-4o-mini")
	vip := addAuditChannel(t, "vip", "gpt-4o-mini", "vip", 0, http.StatusOK, "")

	response := fixture.send("/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"something forbidden"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int64(1), vip.Load())
	assert.Zero(t, fixture.upstreamCalls.Load())
	var log model.Log
	require.NoError(t, model.DB.Where("type = ?", model.LogTypeConsume).Order("id desc").First(&log).Error)
	assert.Equal(t, "vip", log.Group, "the request is billed and logged in the target group")
	assert.Contains(t, log.Other, `"to_group":"vip"`)
}

// TestAuditRerouteCoversEveryPromptFormat reroutes flagged Claude,
// Responses and Gemini requests: upstream receives the target model each
// time.
func TestAuditRerouteCoversEveryPromptFormat(t *testing.T) {
	fixture := newAuditRelayFixture(t, verifiedReroutePolicy(audit_policy_setting.RerouteTarget{Model: "safe-model"}), "gpt-4o-mini", "safe-model")
	requests := []struct{ name, path, body string }{
		{"claude", "/v1/messages", `{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"something forbidden"}]}`},
		{"responses", "/v1/responses", `{"model":"gpt-4o-mini","input":"something forbidden"}`},
		{"gemini", "/v1beta/models/gpt-4o-mini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"something forbidden"}]}]}`},
	}
	for _, request := range requests {
		fixture.lastModel.Store("")
		response := fixture.send(request.path, request.body)
		assert.Equal(t, "safe-model", fixture.lastModel.Load(), "%s: upstream receives the target model (%d %s)", request.name, response.Code, response.Body.String())
	}
}
