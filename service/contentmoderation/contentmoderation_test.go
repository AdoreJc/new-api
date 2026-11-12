package contentmoderation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestExtractInputTakesOnlyTheLatestUserMessage(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
		text     string
		images   []string
	}{
		{
			name: "chat completions keeps the last user message only", protocol: ProtocolChat,
			body: `{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"old question"},
				{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"new  question"},
				{"type":"image_url","image_url":{"url":"https://img.example/a.png"}},{"type":"image_url","image_url":{"url":"https://img.example/b.png"}}]}]}`,
			text: "new question", images: []string{"https://img.example/a.png"},
		},
		{
			name: "chat completions ending with a tool result yields nothing", protocol: ProtocolChat,
			body: `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"result"}]}`,
		},
		{
			name: "claude skips reminder blocks, tool results and trailing system messages", protocol: ProtocolClaude,
			body: `{"system":"sys","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>ctx</system-reminder>"},
				{"type":"tool_result","content":"tool output"},{"type":"text","text":"real ask"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},{"role":"system","content":"late"}]}`,
			text: "real ask", images: []string{"data:image/png;base64,AAAA"},
		},
		{
			name: "claude assistant prefill yields nothing", protocol: ProtocolClaude,
			body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"Sure"}]}`,
		},
		{
			name: "responses string input", protocol: ProtocolResponses,
			body: `{"instructions":"sys","input":"plain input"}`, text: "plain input",
		},
		{
			name: "responses last message item", protocol: ProtocolResponses,
			body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"first"}]},{"role":"assistant","content":[{"type":"output_text","text":"x"}]},
				{"role":"user","content":[{"type":"input_text","text":"second"},{"type":"input_image","image_url":"data:image/jpeg;base64,BBBB"}]}]}`,
			text: "second", images: []string{"data:image/jpeg;base64,BBBB"},
		},
		{
			name: "responses ending with a function call output yields nothing", protocol: ProtocolResponses,
			body: `{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"42"}]}`,
		},
		{
			name: "gemini last user content", protocol: ProtocolGemini,
			body: `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"model","parts":[{"text":"x"}]},
				{"role":"user","parts":[{"text":"gemini ask"},{"inlineData":{"mimeType":"image/webp","data":"CCCC"}}]}]}`,
			text: "gemini ask", images: []string{"data:image/webp;base64,CCCC"},
		},
		{
			name: "image prompt", protocol: ProtocolImages,
			body: `{"model":"img","prompt":"draw a cat","image":"data:image/png;base64,DDDD"}`, text: "draw a cat",
		},
		{
			name: "legacy completions take the last prompt", protocol: ProtocolCompletions,
			body: `{"prompt":["first","last prompt"]}`, text: "last prompt",
		},
		{name: "invalid JSON", protocol: ProtocolChat, body: `{"messages":`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := ExtractInput(test.protocol, []byte(test.body))
			assert.Equal(t, test.text, input.Text)
			assert.ElementsMatch(t, test.images, input.Images)
		})
	}

	long := strings.Repeat("字", maxInputRunes+50)
	input := ExtractInput(ProtocolChat, []byte(`{"messages":[{"role":"user","content":"`+long+`"}]}`))
	assert.Equal(t, maxInputRunes, len([]rune(input.Text)), "input is capped at 12000 characters")
}

func TestRelayProtocolCoverage(t *testing.T) {
	tests := []struct {
		format   types.RelayFormat
		path     string
		protocol string
	}{
		{types.RelayFormatOpenAI, "/v1/chat/completions", ProtocolChat},
		{types.RelayFormatOpenAI, "/pg/chat/completions", ProtocolChat},
		{types.RelayFormatOpenAI, "/v1/completions", ProtocolCompletions},
		{types.RelayFormatClaude, "/v1/messages", ProtocolClaude},
		{types.RelayFormatOpenAIResponses, "/v1/responses", ProtocolResponses},
		{types.RelayFormatOpenAIResponsesCompaction, "/v1/responses/compact", ProtocolResponses},
		{types.RelayFormatGemini, "/v1beta/models/g:generateContent?alt=sse", ProtocolGemini},
		{types.RelayFormatOpenAIImage, "/v1/images/generations", ProtocolImages},
		{types.RelayFormatOpenAI, "/v1/moderations", ""},
		{types.RelayFormatGemini, "/v1beta/models/g:embedContent", ""},
		{types.RelayFormatEmbedding, "/v1/embeddings", ""},
		{types.RelayFormatRerank, "/v1/rerank", ""},
		{types.RelayFormatOpenAIAudio, "/v1/audio/speech", ""},
		{types.RelayFormatOpenAIRealtime, "/v1/realtime", ""},
	}
	for _, test := range tests {
		protocol, covered := RelayProtocol(test.format, test.path)
		assert.Equal(t, test.protocol, protocol, test.path)
		assert.Equal(t, test.protocol != "", covered, test.path)
	}
}

func TestEvaluateUsesInclusiveThresholds(t *testing.T) {
	thresholds := content_moderation_setting.DefaultThresholds()
	flagged, highest, score := evaluate(map[string]float64{"hate": 0.65, "violence": 0.2}, thresholds)
	assert.True(t, flagged, "a score equal to the threshold is flagged")
	assert.Equal(t, "hate", highest)
	assert.InDelta(t, 0.65, score, 1e-9)

	flagged, highest, _ = evaluate(map[string]float64{"harassment": 0.97, "violence": 0.5}, thresholds)
	assert.False(t, flagged)
	assert.Equal(t, "harassment", highest)

	flagged, _, _ = evaluate(map[string]float64{"unknown-category": 1}, thresholds)
	assert.False(t, flagged, "categories without a threshold never flag")
}

// moderationServer answers with the scores returned by scores(input) and the
// HTTP status chosen per key by status.
type moderationServer struct {
	*httptest.Server
	calls  atomic.Int64
	mu     sync.Mutex
	inputs []string
	keys   []string
}

func newModerationServer(t *testing.T, status func(key string) int, scores func(input string) map[string]float64) *moderationServer {
	t.Helper()
	server := &moderationServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.calls.Add(1)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Input any `json:"input"`
		}
		_ = common.Unmarshal(raw, &payload)
		text, _ := payload.Input.(string)
		server.mu.Lock()
		server.inputs = append(server.inputs, string(raw))
		server.keys = append(server.keys, key)
		server.mu.Unlock()
		if code := status(key); code != http.StatusOK {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"message":"echo ` + key + `"}}`))
			return
		}
		encoded, _ := common.Marshal(map[string]any{"model": "omni-moderation-test", "results": []any{map[string]any{"category_scores": scores(text)}}})
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)
	return server
}

func okStatus(string) int { return http.StatusOK }

func scoreBy(word string) func(string) map[string]float64 {
	return func(text string) map[string]float64 {
		if strings.Contains(text, word) {
			return map[string]float64{"violence": 0.99, "hate": 0.1}
		}
		return map[string]float64{"violence": 0.01}
	}
}

func useConfig(t *testing.T, config content_moderation_setting.Config, keys []string) {
	t.Helper()
	previous := content_moderation_setting.Current()
	require.NoError(t, config.Normalize())
	configJSON, err := common.Marshal(config)
	require.NoError(t, err)
	keysJSON, err := common.Marshal(keys)
	require.NoError(t, err)
	content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(configJSON))
	content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, string(keysJSON))
	t.Cleanup(func() {
		configJSON, _ := common.Marshal(previous.Config)
		keysJSON, _ := common.Marshal(previous.Keys)
		content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(configJSON))
		content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, string(keysJSON))
	})
}

func moderationConfig(mode, baseURL string) content_moderation_setting.Config {
	config := content_moderation_setting.DefaultConfig()
	config.Mode, config.BaseURL, config.TimeoutMS, config.RetryCount = mode, baseURL, 500, 0
	return config
}

func TestEngineFreezesKeysByHTTPStatus(t *testing.T) {
	statuses := map[string]int{"k-rate": http.StatusTooManyRequests, "k-auth": http.StatusUnauthorized, "k-err": http.StatusBadGateway, "k-ok": http.StatusOK}
	server := newModerationServer(t, func(key string) int { return statuses[key] }, scoreBy("x"))
	now := time.Unix(1_700_000_000, 0)
	e := &engine{now: func() time.Time { return now }}
	config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.RetryCount = 3
	require.NoError(t, config.Normalize())
	snapshot := &content_moderation_setting.Snapshot{Config: config, Keys: []string{"k-rate", "k-auth", "k-err", "k-ok"}}

	result, attempts, err := e.moderate(context.Background(), snapshot, Input{Text: "hello"})
	require.NoError(t, err, "failing keys are skipped until a healthy one answers")
	assert.Equal(t, 4, attempts)
	assert.NotNil(t, result)

	byID := map[string]KeyStatus{}
	for _, status := range e.keyStatuses(snapshot.Keys) {
		byID[status.ID] = status
	}
	frozenFor := func(key string) time.Duration {
		return time.Unix(byID[content_moderation_setting.KeyID(key)].FrozenUntil, 0).Sub(now)
	}
	assert.Equal(t, time.Minute, frozenFor("k-rate"))
	assert.Equal(t, 10*time.Minute, frozenFor("k-auth"))
	assert.Equal(t, 10*time.Second, frozenFor("k-err"))
	assert.Equal(t, "healthy", byID[content_moderation_setting.KeyID("k-ok")].Status)
	for _, status := range byID {
		assert.NotContains(t, status.LastError, "echo", "upstream bodies never reach key status")
	}

	server.calls.Store(0)
	_, _, err = e.moderate(context.Background(), snapshot, Input{Text: "hello"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), server.calls.Load(), "frozen keys are not called")

	now = now.Add(11 * time.Second)
	e.cursor.Store(2)
	server.calls.Store(0)
	_, _, err = e.moderate(context.Background(), snapshot, Input{Text: "hello"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), server.calls.Load(), "a key frozen for ten seconds is tried again afterwards")

	badRequest := newModerationServer(t, func(string) int { return http.StatusBadRequest }, scoreBy("x"))
	config.BaseURL = badRequest.URL
	_, attempts, err = (&engine{now: time.Now}).moderate(context.Background(), &content_moderation_setting.Snapshot{Config: config, Keys: []string{"a", "b"}}, Input{Text: "x"})
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "a 400 rejects the input, so no other key is tried")
}

// setupDB opens SQLite by default. With TEST_SECURITY_DIALECT=mysql or
// postgres it uses TEST_MYSQL_DSN / TEST_POSTGRES_DSN and drops its tables
// afterwards.
func setupDB(t *testing.T) {
	t.Helper()
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	var db *gorm.DB
	var err error
	switch os.Getenv("TEST_SECURITY_DIALECT") {
	case "mysql":
		db, err = gorm.Open(mysql.Open(os.Getenv("TEST_MYSQL_DSN")), config)
	case "postgres":
		db, err = gorm.Open(postgres.Open(os.Getenv("TEST_POSTGRES_DSN")), config)
	default:
		db, err = gorm.Open(sqlite.Open(t.TempDir()+"/moderation.db"), config)
	}
	require.NoError(t, err)
	tables := []any{&model.ContentModerationEvent{}, &model.ContentModerationFlaggedHash{}, &model.ContentModerationKeywordList{}, &model.User{}, &model.UserSession{}, &model.AuditLog{}}
	require.NoError(t, db.Migrator().DropTable(tables...))
	require.NoError(t, db.AutoMigrate(tables...))
	previousDB, previousLogDB, previousRedis := model.DB, model.LOG_DB, common.RedisEnabled
	model.DB, model.LOG_DB, common.RedisEnabled = db, db, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled = previousDB, previousLogDB, previousRedis
		_ = db.Migrator().DropTable(tables...)
		if connection, err := db.DB(); err == nil {
			_ = connection.Close()
		}
	})
}

// countingBody records whether the request body was read.
type countingBody struct {
	io.Reader
	reads atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.Reader.Read(p)
}

func relayContext(body string) (*gin.Context, *countingBody) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	reader := &countingBody{Reader: strings.NewReader(body)}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reader)
	c.Request.ContentLength = int64(len(body))
	c.Set(common.RequestIdKey, "req-1")
	c.Set("token_name", "key-a")
	return c, reader
}

func chatInfo(userID int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{UserId: userID, TokenId: 3, UsingGroup: "default", OriginModelName: "gpt-x",
		RelayFormat: types.RelayFormatOpenAI, RequestURLPath: "/v1/chat/completions"}
}

func chatBody(text string) string {
	return `{"model":"gpt-x","messages":[{"role":"user","content":"` + text + `"}]}`
}

// drainRecords stores every queued event the way the recorder would.
func drainRecords(s *Service) {
	for {
		select {
		case item := <-s.records:
			if item.dispose {
				s.dispose(item)
				continue
			}
			_, _ = model.CreateContentModerationEvents([]*model.ContentModerationEvent{item.event})
		default:
			return
		}
	}
}

func TestCheckRelayDoesNothingWhileOff(t *testing.T) {
	useConfig(t, content_moderation_setting.DefaultConfig(), []string{"k"})
	s := newService()
	c, body := relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)))
	assert.Zero(t, body.reads.Load(), "the request body is not read while moderation is off")
	assert.Zero(t, s.metrics.checked.Load()+s.metrics.skipped.Load())
}

func TestCheckRelayPreBlock(t *testing.T) {
	setupDB(t)
	require.NoError(t, i18n.Init())
	server := newModerationServer(t, okStatus, scoreBy("attack"))
	config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.HashBlocklistEnabled = true
	useConfig(t, config, []string{"k1"})
	s := newService()

	c, _ := relayContext(chatBody("hello there"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "a passing input continues")

	c, _ = relayContext(chatBody("attack now"))
	apiErr := s.checkRelay(c, chatInfo(7))
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Equal(t, ErrorCodeBlocked, apiErr.GetErrorCode())
	assert.NotContains(t, apiErr.Error(), "violence", "the reply names no category")
	assert.Equal(t, int64(2), server.calls.Load())

	drainRecords(s)
	events, total, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{}, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total, "passing inputs are not stored by default")
	event := events[0]
	assert.True(t, event.Flagged)
	assert.Equal(t, model.ContentModerationActionBlock, event.Action)
	assert.Equal(t, "violence", event.HighestCategory)
	assert.Equal(t, 1, event.ViolationCount)
	assert.Equal(t, "req-1", event.RequestId)
	assert.Contains(t, event.CategoryScores, `"violence":0.99`)

	c, _ = relayContext(chatBody("attack now"))
	apiErr = s.checkRelay(c, chatInfo(8))
	require.NotNil(t, apiErr, "the same input is blocked again from the hash blocklist")
	assert.Equal(t, int64(2), server.calls.Load(), "a blocklisted input does not call the engine")
	drainRecords(s)
	hashes, err := model.ListContentModerationFlaggedHashes()
	require.NoError(t, err)
	assert.Len(t, hashes, 1)
	count, err := model.CountContentModerationViolations(8, 0)
	require.NoError(t, err)
	assert.Zero(t, count, "hash blocklist events do not count toward disabling")

	observe := moderationConfig(content_moderation_setting.ModeObserve, server.URL)
	observe.HashBlocklistEnabled = true
	useConfig(t, observe, []string{"k1"})
	c, _ = relayContext(chatBody("attack now"))
	assert.Nil(t, s.checkRelay(c, chatInfo(9)), "observe mode only records a blocklisted input")
	drainRecords(s)
	count, err = model.CountContentModerationViolations(9, 0)
	require.NoError(t, err)
	assert.Zero(t, count, "a recorded blocklist match does not count toward disabling either")
}

func TestCheckRelayFailsOpen(t *testing.T) {
	setupDB(t)
	server := newModerationServer(t, func(string) int { return http.StatusInternalServerError }, scoreBy("attack"))
	config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.RecordNonHits = true
	useConfig(t, config, []string{"k1"})
	s := newService()

	c, _ := relayContext(chatBody("attack now"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "an engine failure lets the request through")
	assert.Equal(t, int64(1), s.metrics.failed.Load())

	useConfig(t, moderationConfig(content_moderation_setting.ModePreBlock, "http://127.0.0.1:1"), []string{"k1"})
	c, _ = relayContext(chatBody("attack now"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "an unreachable engine lets the request through")
	assert.Equal(t, int64(2), s.metrics.failed.Load())

	drainRecords(s)
	events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{Action: model.ContentModerationActionError}, 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, events, "failed calls are recorded when non-hits are recorded")
	assert.False(t, events[0].Flagged)
	assert.NotContains(t, events[0].Error, "k1")
}

func TestCheckRelayScopeSamplingAndAllowlist(t *testing.T) {
	setupDB(t)
	server := newModerationServer(t, okStatus, scoreBy("attack"))
	s := newService()

	config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.AllGroups, config.Groups = false, []string{"vip*"}
	useConfig(t, config, []string{"k1"})
	c, _ := relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "groups outside the scope are not checked")

	config = moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.ModelFilter = content_moderation_setting.ModelFilter{Type: content_moderation_setting.ModelFilterExclude, Models: []string{"gpt-x"}}
	useConfig(t, config, []string{"k1"})
	c, _ = relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "excluded models are not checked")

	config = moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.SampleRate = 0
	useConfig(t, config, []string{"k1"})
	c, _ = relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "a zero sample rate checks nothing")
	assert.Zero(t, server.calls.Load())
	assert.Equal(t, int64(2), s.metrics.skipped.Load())
	assert.Equal(t, int64(1), s.metrics.sampledOut.Load())

	config = moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
	config.AllowlistUserIDs = []int{7}
	useConfig(t, config, []string{"k1"})
	c, _ = relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)), "allowlisted users are never blocked")
	require.Equal(t, int64(1), s.depth.Load(), "allowlisted users are checked in the background")
	item := <-s.queue
	s.check(context.Background(), content_moderation_setting.Current(), item, "", 0, false)
	s.release(item)
	drainRecords(s)
	events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{}, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.True(t, events[0].Allowlisted)
	assert.Equal(t, model.ContentModerationActionAllow, events[0].Action)
	count, err := model.CountContentModerationViolations(7, 0)
	require.NoError(t, err)
	assert.Zero(t, count, "allowlisted events do not count toward disabling")
}

func TestObserveModeQueuesAndRecords(t *testing.T) {
	setupDB(t)
	server := newModerationServer(t, okStatus, scoreBy("attack"))
	config := moderationConfig(content_moderation_setting.ModeObserve, server.URL)
	config.QueueSize = 1
	useConfig(t, config, []string{"k1"})
	s := newService()

	c, _ := relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)))
	c, _ = relayContext(chatBody("attack again"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)))
	assert.Zero(t, server.calls.Load(), "observe mode never calls the engine on the request path")
	assert.Equal(t, int64(1), s.metrics.enqueued.Load())
	assert.Equal(t, int64(1), s.metrics.dropped.Load(), "a full queue drops instead of waiting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.worker(ctx)
	go s.recorder(ctx)
	require.Eventually(t, func() bool { return s.metrics.recorded.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{Flagged: "true"}, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, content_moderation_setting.ModeObserve, events[0].Mode)
	assert.Equal(t, model.ContentModerationActionAllow, events[0].Action, "observe mode only records")
	assert.Zero(t, s.depth.Load())
}

func TestDisposalDisablesUserAtThresholdWithinWindow(t *testing.T) {
	setupDB(t)
	user := &model.User{Username: "u1", Password: "x", AffCode: "aff-u1", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default"}
	admin := &model.User{Username: "a1", Password: "x", AffCode: "aff-a1", Status: common.UserStatusEnabled, Role: common.RoleAdminUser, Group: "default"}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(admin).Error)
	s := newService()
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	config := moderationConfig(content_moderation_setting.ModePreBlock, "http://127.0.0.1:1")
	config.AutoDisableEnabled, config.DisableThreshold, config.ViolationWindowHours = true, 3, 24
	require.NoError(t, config.Normalize())

	flagged := func(userID int, at time.Time) *record {
		return &record{event: &model.ContentModerationEvent{UserId: userID, Flagged: true, CreatedAt: at.Unix(),
			Action: model.ContentModerationActionBlock, RequestId: "r"}, dispose: true, config: config}
	}
	// One violation outside the window and one inside: the next is the second.
	s.dispose(flagged(user.Id, now.Add(-30*time.Hour)))
	s.dispose(flagged(user.Id, now.Add(-time.Hour)))
	item := flagged(user.Id, now)
	s.dispose(item)
	assert.Equal(t, 2, item.event.ViolationCount, "violations outside the window are not counted")
	reloaded, err := model.GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.UserStatusEnabled, reloaded.Status)

	item = flagged(user.Id, now)
	s.dispose(item)
	assert.Equal(t, 3, item.event.ViolationCount)
	assert.True(t, item.event.AutoDisabled)
	reloaded, err = model.GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.UserStatusDisabled, reloaded.Status, "the user is disabled at the threshold")
	var audits []model.AuditLog
	require.NoError(t, model.DB.Where("action = ?", "content_moderation.auto_disable").Find(&audits).Error)
	require.Len(t, audits, 1, "automatic disabling is audit-logged")
	assert.Equal(t, user.Id, audits[0].UserId)

	// Re-enabled by an administrator, the user starts from zero.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusEnabled).Error)
	now = now.Add(time.Minute)
	item = flagged(user.Id, now)
	s.dispose(item)
	assert.Equal(t, 1, item.event.ViolationCount, "the count restarts after an automatic disable")

	for range 3 {
		s.dispose(flagged(admin.Id, now))
	}
	reloaded, err = model.GetUserById(admin.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.UserStatusEnabled, reloaded.Status, "administrators are never disabled automatically")
}

func TestCleanupKeepsHitsLongerThanNonHits(t *testing.T) {
	setupDB(t)
	useConfig(t, content_moderation_setting.DefaultConfig(), nil)
	s := newService()
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	day := int64(24 * 3600)
	events := []*model.ContentModerationEvent{
		{Flagged: true, CreatedAt: now.Unix() - 181*day},
		{Flagged: true, CreatedAt: now.Unix() - 179*day},
		{Flagged: false, CreatedAt: now.Unix() - 4*day},
		{Flagged: false, CreatedAt: now.Unix() - 2*day},
	}
	_, err := model.CreateContentModerationEvents(events)
	require.NoError(t, err)
	deleted, err := s.cleanup()
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	remaining, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{}, 0, 10)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	for _, event := range remaining {
		assert.Contains(t, []int64{events[1].Id, events[3].Id}, event.Id)
	}
}

func TestExcerptRedactsSecrets(t *testing.T) {
	excerpt := Excerpt("call https://evil.example/x?y=1 with sk-proj-abcdefghijklmnop and mail me@example.com, api_key=hunter22secret")
	for _, secret := range []string{"evil.example", "abcdefghijklmnop", "me@example.com", "hunter22secret"} {
		assert.NotContains(t, excerpt, secret)
	}
	assert.Len(t, []rune(Excerpt(strings.Repeat("a b ", 200))), maxExcerptRunes)
}

func useSensitiveWords(t *testing.T, words string) {
	t.Helper()
	previous := setting.SensitiveWordsToString()
	setting.SensitiveWordsFromString(words)
	t.Cleanup(func() { setting.SensitiveWordsFromString(previous) })
}

func TestKeywordListsMatchCaseInsensitivelyAndFollowChanges(t *testing.T) {
	setupDB(t)
	useSensitiveWords(t, "Banned Phrase")
	s := newService()
	require.NoError(t, model.SaveContentModerationKeywordList("spam", []string{"BuyNow", "免费领取"}, 100))
	require.NoError(t, s.reloadKeywords(false))

	tests := []struct {
		text  string
		lists []string
		want  string
	}{
		{"please buynow today", []string{"spam"}, "spam"},
		{"现在免费领取吧", []string{"spam"}, "spam"},
		{"a banned phrase here", []string{"spam", content_moderation_setting.DefaultKeywordList}, content_moderation_setting.DefaultKeywordList},
		{"a banned phrase here", []string{"spam"}, ""},
		{"nothing to see", []string{"spam", content_moderation_setting.DefaultKeywordList}, ""},
		{"buynow", []string{"missing-list"}, ""},
	}
	for _, test := range tests {
		list, hit := s.matchKeyword(test.text, test.lists)
		assert.Equal(t, test.want, list, test.text)
		assert.Equal(t, test.want != "", hit, test.text)
	}

	// The default list reads the global sensitive words in place, so an
	// edit there applies at once without a reload.
	setting.SensitiveWordsFromString("other words")
	_, hit := s.matchKeyword("a banned phrase here", []string{content_moderation_setting.DefaultKeywordList})
	assert.False(t, hit)

	// A stored list change is picked up by the next reload.
	require.NoError(t, model.SaveContentModerationKeywordList("spam", []string{"discount"}, 200))
	require.NoError(t, s.reloadKeywords(false))
	_, hit = s.matchKeyword("please buynow today", []string{"spam"})
	assert.False(t, hit)
	list, _ := s.matchKeyword("big DISCOUNT", []string{"spam"})
	assert.Equal(t, "spam", list)
}

func TestNormalizeKeywordsEnforcesLimits(t *testing.T) {
	words, err := content_moderation_setting.NormalizeKeywords(" a \n\nB\nb\n c ")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "B", "c"}, words, "blanks and case-insensitive duplicates are dropped")

	_, err = content_moderation_setting.NormalizeKeywords(strings.Repeat("x", content_moderation_setting.MaxKeywordRunes+1))
	assert.Error(t, err)
	many := make([]string, content_moderation_setting.MaxKeywordsPerList+1)
	for index := range many {
		many[index] = "w" + strconv.Itoa(index)
	}
	_, err = content_moderation_setting.NormalizeKeywords(strings.Join(many, "\n"))
	assert.Error(t, err)
}

func TestKeywordModes(t *testing.T) {
	setupDB(t)
	require.NoError(t, i18n.Init())
	useSensitiveWords(t, "")
	server := newModerationServer(t, okStatus, scoreBy("attack"))
	require.NoError(t, model.SaveContentModerationKeywordList("spam", []string{"buynow", "attack"}, 100))

	t.Run("keyword_only blocks without an engine call and records the list name", func(t *testing.T) {
		config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
		config.KeywordMode, config.KeywordLists = content_moderation_setting.KeywordModeKeywordOnly, []string{"spam"}
		useConfig(t, config, nil)
		s := newService()
		require.NoError(t, s.reloadKeywords(false))
		server.calls.Store(0)

		c, _ := relayContext(chatBody("clean question"))
		assert.Nil(t, s.checkRelay(c, chatInfo(7)), "no keyword, no engine: the request continues")
		c, _ = relayContext(chatBody("BUYNOW cheap"))
		apiErr := s.checkRelay(c, chatInfo(7))
		require.NotNil(t, apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
		assert.Zero(t, server.calls.Load(), "keyword_only never calls the engine")

		drainRecords(s)
		events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{Action: model.ContentModerationActionKeywordBlock}, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "spam", events[0].KeywordList)
		assert.Equal(t, model.ContentModerationKeywordCategory, events[0].HighestCategory)
		assert.Equal(t, 1, events[0].ViolationCount, "keyword blocks count toward disabling")
		encoded, err := common.Marshal(events[0])
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), `"buynow"`, "the matched keyword is not stored as such")
	})

	t.Run("keyword_and_api sends only keyword hits to the engine, which decides", func(t *testing.T) {
		config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
		config.KeywordMode, config.KeywordLists = content_moderation_setting.KeywordModeKeywordAndAPI, []string{"spam"}
		config.SampleRate = 0
		useConfig(t, config, []string{"k1"})
		s := newService()
		require.NoError(t, s.reloadKeywords(false))
		server.calls.Store(0)

		c, _ := relayContext(chatBody("buynow please"))
		assert.Nil(t, s.checkRelay(c, chatInfo(8)), "the engine clears a keyword hit")
		assert.Equal(t, int64(1), server.calls.Load(), "a keyword hit is reviewed even with a zero sample rate")
		c, _ = relayContext(chatBody("attack now"))
		require.NotNil(t, s.checkRelay(c, chatInfo(8)), "the engine confirms a keyword hit")
		assert.Equal(t, int64(2), server.calls.Load())
		c, _ = relayContext(chatBody("plain question"))
		assert.Nil(t, s.checkRelay(c, chatInfo(8)))
		assert.Equal(t, int64(2), server.calls.Load(), "inputs without a keyword skip the engine")
		drainRecords(s)
		events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{Action: model.ContentModerationActionBlock}, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "spam", events[0].KeywordList)
		assert.Equal(t, "violence", events[0].HighestCategory)
	})
}

// fixedPolicy decides the same for every request.
type fixedPolicy struct{ decision Decision }

func (p fixedPolicy) Decide(Subject) Decision { return p.decision }

func TestScanFollowsAReplacedScopePolicy(t *testing.T) {
	setupDB(t)
	require.NoError(t, i18n.Init())
	server := newModerationServer(t, okStatus, scoreBy("attack"))
	// The settings say observe, but the policy says block for this request.
	useConfig(t, moderationConfig(content_moderation_setting.ModeObserve, server.URL), []string{"k1"})
	SetScopePolicy(fixedPolicy{Decision{Moderate: true, Mode: "rule", Action: ActionBlock, SampleRate: 100}})
	t.Cleanup(func() { SetScopePolicy(nil) })
	s := newService()

	c, _ := relayContext(chatBody("attack"))
	require.NotNil(t, s.checkRelay(c, chatInfo(7)), "the policy action decides blocking")
	drainRecords(s)
	events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{}, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "rule", events[0].Mode)

	SetScopePolicy(fixedPolicy{Decision{}})
	c, body := relayContext(chatBody("attack"))
	assert.Nil(t, s.checkRelay(c, chatInfo(7)))
	assert.Zero(t, body.reads.Load(), "out of scope: the body is not read")
}

func TestConfigScopePolicy(t *testing.T) {
	config := moderationConfig(content_moderation_setting.ModePreBlock, "http://127.0.0.1:1")
	config.AllGroups, config.Groups = false, []string{"vip*"}
	config.ModelFilter = content_moderation_setting.ModelFilter{Type: content_moderation_setting.ModelFilterExclude, Models: []string{"free-model"}}
	config.AllowlistUserIDs, config.SampleRate = []int{9}, 40
	useConfig(t, config, nil)
	policy := ConfigScopePolicy{}
	tests := []struct {
		name    string
		subject Subject
		want    Decision
	}{
		{"in scope", Subject{UserID: 1, Group: "vip-1", Model: "m"}, Decision{Moderate: true, Mode: "pre_block", Action: ActionBlock, SampleRate: 40}},
		{"auto token routed to a matching group", Subject{UserID: 1, Group: "vip-2", RequestedGroup: "auto", Model: "m"}, Decision{Moderate: true, Mode: "pre_block", Action: ActionBlock, SampleRate: 40}},
		{"allowlisted user", Subject{UserID: 9, Group: "vip-1", Model: "m"}, Decision{Moderate: true, Mode: "pre_block", Action: ActionRecordOnly, SampleRate: 40}},
		{"group out of scope", Subject{UserID: 1, Group: "default", Model: "m"}, Decision{Mode: "pre_block"}},
		{"excluded model", Subject{UserID: 1, Group: "vip-1", Model: "free-model"}, Decision{Mode: "pre_block"}},
	}
	for _, test := range tests {
		assert.Equal(t, test.want, policy.Decide(test.subject), test.name)
	}
	useConfig(t, content_moderation_setting.DefaultConfig(), nil)
	assert.Equal(t, Decision{}, policy.Decide(Subject{UserID: 1, Group: "vip-1", Model: "m"}), "off")
}
