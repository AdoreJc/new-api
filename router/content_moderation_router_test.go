package router

import (
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
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
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
