package contentmoderation

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
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
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
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
	s.moderate(context.Background(), content_moderation_setting.Current(), item, "", 0, false)
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

func TestKeywordListsMatchCaseInsensitivelyAndFollowChanges(t *testing.T) {
	setupDB(t)
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
		{"a banned phrase here", []string{"spam"}, ""},
		// The built-in default list is empty since the sensitive word check
		// was retired, and does not hide a hit in a later list.
		{"nothing to see", []string{"spam", content_moderation_setting.DefaultKeywordList}, ""},
		{"please buynow today", []string{content_moderation_setting.DefaultKeywordList, "spam"}, "spam"},
		{"buynow", []string{"missing-list"}, ""},
	}
	for _, test := range tests {
		list, hit := s.matchKeyword(test.text, test.lists)
		assert.Equal(t, test.want, list, test.text)
		assert.Equal(t, test.want != "", hit, test.text)
	}

	// A stored list change is picked up by the next reload.
	require.NoError(t, model.SaveContentModerationKeywordList("spam", []string{"discount"}, 200))
	require.NoError(t, s.reloadKeywords(false))
	_, hit := s.matchKeyword("please buynow today", []string{"spam"})
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
		// No field holds the matched keyword: apart from the redacted input
		// excerpt (kept as for every flagged event), it appears nowhere.
		assert.Contains(t, strings.ToLower(events[0].InputExcerpt), "buynow")
		withoutExcerpt := *events[0]
		withoutExcerpt.InputExcerpt = ""
		encoded, err := common.Marshal(withoutExcerpt)
		require.NoError(t, err)
		assert.NotContains(t, strings.ToLower(string(encoded)), "buynow")
	})

	t.Run("keyword_and_api blocks hits without the engine and sends the rest as api_only", func(t *testing.T) {
		harm := newModerationServer(t, okStatus, scoreBy("harm"))
		config := moderationConfig(content_moderation_setting.ModePreBlock, harm.URL)
		config.KeywordMode, config.KeywordLists = content_moderation_setting.KeywordModeKeywordAndAPI, []string{"spam"}
		config.HashBlocklistEnabled = true
		useConfig(t, config, []string{"k1"})
		require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ContentModerationFlaggedHash{}).Error)
		s := newService()
		require.NoError(t, s.reloadKeywords(false))

		c, _ := relayContext(chatBody("buynow please"))
		require.NotNil(t, s.checkRelay(c, chatInfo(8)), "a keyword hit is blocked")
		assert.Zero(t, harm.calls.Load(), "without an engine call")
		c, _ = relayContext(chatBody("plain question"))
		assert.Nil(t, s.checkRelay(c, chatInfo(8)))
		assert.Equal(t, int64(1), harm.calls.Load(), "other inputs go to the engine")
		c, _ = relayContext(chatBody("harm here"))
		require.NotNil(t, s.checkRelay(c, chatInfo(8)), "the engine still decides the rest")
		assert.Equal(t, int64(2), harm.calls.Load())
		drainRecords(s)
		c, _ = relayContext(chatBody("harm here"))
		require.NotNil(t, s.checkRelay(c, chatInfo(8)))
		assert.Equal(t, int64(2), harm.calls.Load(), "the hash blocklist is checked before the engine")
		drainRecords(s)

		hashes, err := model.ListContentModerationFlaggedHashes()
		require.NoError(t, err)
		assert.Len(t, hashes, 1, "only the engine hit is remembered, not the keyword hit")

		sampledOut := config
		sampledOut.SampleRate = 0
		useConfig(t, sampledOut, []string{"k1"})
		c, _ = relayContext(chatBody("another plain question"))
		assert.Nil(t, s.checkRelay(c, chatInfo(8)))
		assert.Equal(t, int64(2), harm.calls.Load(), "inputs without a keyword follow the sample rate")
	})

	t.Run("observe mode records keyword hits without blocking", func(t *testing.T) {
		config := moderationConfig(content_moderation_setting.ModeObserve, server.URL)
		config.KeywordMode, config.KeywordLists = content_moderation_setting.KeywordModeKeywordAndAPI, []string{"spam"}
		useConfig(t, config, []string{"k1"})
		require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ContentModerationEvent{}).Error)
		s := newService()
		require.NoError(t, s.reloadKeywords(false))
		c, _ := relayContext(chatBody("buynow please"))
		assert.Nil(t, s.checkRelay(c, chatInfo(8)))
		drainRecords(s)
		events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{}, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, model.ContentModerationActionAllow, events[0].Action)
		assert.Equal(t, "spam", events[0].KeywordList)
	})

	t.Run("keyword_gated_api sends only keyword hits to the engine, which decides", func(t *testing.T) {
		config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
		config.KeywordMode, config.KeywordLists = content_moderation_setting.KeywordModeKeywordGated, []string{"spam"}
		config.SampleRate, config.HashBlocklistEnabled = 0, true
		useConfig(t, config, []string{"k1"})
		require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ContentModerationEvent{}).Error)
		require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ContentModerationFlaggedHash{}).Error)
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
		c, _ = relayContext(chatBody("attack now"))
		require.NotNil(t, s.checkRelay(c, chatInfo(8)))
		assert.Equal(t, int64(2), server.calls.Load(), "a hit already flagged is answered from the hash blocklist")
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
		{"in scope", Subject{UserID: 1, Group: "vip-1", Model: "m"}, Decision{Moderate: true, Mode: "pre_block", Action: ActionBlock, SampleRate: 40, Dispose: true}},
		{"auto token routed to a matching group", Subject{UserID: 1, Group: "vip-2", RequestedGroup: "auto", Model: "m"}, Decision{Moderate: true, Mode: "pre_block", Action: ActionBlock, SampleRate: 40, Dispose: true}},
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

// reminderBody builds a request whose latest user message has the given text
// blocks, in the shape of the protocol.
func reminderBody(t *testing.T, protocol string, texts []string) []byte {
	t.Helper()
	blocks := make([]map[string]string, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, map[string]string{"type": "text", "text": text})
	}
	message := map[string]any{"role": "user", "content": blocks}
	var body any
	switch protocol {
	case ProtocolGemini:
		body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": blocks}}}
	case ProtocolResponses:
		body = map[string]any{"input": []any{message}}
	case ProtocolImages:
		body = map[string]any{"prompt": strings.Join(texts, "\n")}
	default:
		body = map[string]any{"messages": []any{message}}
	}
	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	return encoded
}

func reminderInfo(protocol string) *relaycommon.RelayInfo {
	info := chatInfo(7)
	switch protocol {
	case ProtocolClaude:
		info.RelayFormat, info.RequestURLPath = types.RelayFormatClaude, "/v1/messages"
	case ProtocolResponses:
		info.RelayFormat, info.RequestURLPath = types.RelayFormatOpenAIResponses, "/v1/responses"
	case ProtocolGemini:
		info.RelayFormat, info.RequestURLPath = types.RelayFormatGemini, "/v1beta/models/g:generateContent"
	case ProtocolImages:
		info.RelayFormat, info.RequestURLPath = types.RelayFormatOpenAIImage, "/v1/images/generations"
	}
	return info
}

// TestKeywordsSeeTextNextToReminders: the engine never receives text parts
// that carry a reminder block, but keywords are matched against them, so a
// keyword cannot hide before, after, beside or inside a reminder.
func TestKeywordsSeeTextNextToReminders(t *testing.T) {
	setupDB(t)
	require.NoError(t, i18n.Init())
	const word = "今晚打老虎"
	server := newModerationServer(t, okStatus, scoreBy("never-flagged"))
	require.NoError(t, model.SaveContentModerationKeywordList("tiger", []string{word}, 100))

	cases := map[string][]string{
		"plain":    {word},
		"prefix":   {"<system-reminder>context</system-reminder> " + word},
		"suffix":   {word + " <system-reminder>context</system-reminder>"},
		"separate": {"<system-reminder>context</system-reminder>", word},
		"inside":   {"<system-reminder>" + word + "</system-reminder>"},
	}
	modes := []string{content_moderation_setting.KeywordModeKeywordOnly, content_moderation_setting.KeywordModeAPIOnly,
		content_moderation_setting.KeywordModeKeywordAndAPI, content_moderation_setting.KeywordModeKeywordGated}
	for _, protocol := range []string{ProtocolClaude, ProtocolChat, ProtocolResponses, ProtocolGemini, ProtocolImages} {
		for name, texts := range cases {
			body := reminderBody(t, protocol, texts)
			// Reminder blocks are removed and the text around them kept; an
			// image prompt is the user's own text and is not filtered.
			wantSemantic := word
			if protocol == ProtocolImages {
				wantSemantic = joinParts(texts)
			} else if name == "inside" {
				wantSemantic = ""
			}
			require.Equal(t, wantSemantic, ExtractInput(protocol, body).Text, protocol+"/"+name)
			require.Contains(t, ExtractKeywordText(protocol, body), word, protocol+"/"+name)

			for _, mode := range modes {
				t.Run(protocol+"/"+name+"/"+mode, func(t *testing.T) {
					config := moderationConfig(content_moderation_setting.ModePreBlock, server.URL)
					config.KeywordMode, config.HashBlocklistEnabled = mode, true
					if mode != content_moderation_setting.KeywordModeAPIOnly {
						config.KeywordLists = []string{"tiger"}
					}
					useConfig(t, config, []string{"k1"})
					require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ContentModerationEvent{}).Error)
					s := newService()
					require.NoError(t, s.reloadKeywords(true))
					server.calls.Store(0)
					server.mu.Lock()
					server.inputs = nil
					server.mu.Unlock()

					c, _ := relayContext(string(body))
					apiErr := s.checkRelay(c, reminderInfo(protocol))
					drainRecords(s)
					// Hits are decided without the engine in keyword_only and
					// keyword_and_api; api_only and keyword_gated_api send the
					// reminder-free text when there is any.
					wantCalls := int64(0)
					if wantSemantic != "" && (mode == content_moderation_setting.KeywordModeAPIOnly || mode == content_moderation_setting.KeywordModeKeywordGated) {
						wantCalls = 1
					}
					assert.Equal(t, wantCalls, server.calls.Load())
					if wantCalls == 1 {
						var payload struct {
							Input string `json:"input"`
						}
						require.NoError(t, common.UnmarshalJsonStr(server.inputs[0], &payload))
						assert.Equal(t, wantSemantic, payload.Input, "the engine gets the reminder-free text")
					}
					switch mode {
					case content_moderation_setting.KeywordModeKeywordOnly, content_moderation_setting.KeywordModeKeywordAndAPI:
						require.NotNil(t, apiErr)
						events, _, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{}, 0, 10)
						require.NoError(t, err)
						require.Len(t, events, 1)
						assert.Equal(t, model.ContentModerationActionKeywordBlock, events[0].Action)
						assert.Equal(t, "tiger", events[0].KeywordList)
						hashes, err := model.ListContentModerationFlaggedHashes()
						require.NoError(t, err)
						assert.Empty(t, hashes, "keyword hits never enter the hash blocklist")
					case content_moderation_setting.KeywordModeAPIOnly:
						assert.Nil(t, apiErr)
						assert.Zero(t, s.metrics.keywordHits.Load())
					default:
						assert.Nil(t, apiErr, "the engine did not flag the input")
						assert.Equal(t, int64(1), s.metrics.keywordHits.Load())
					}
				})
			}
		}
	}
}

func TestKeywordTextSkipsWhatIsNotTheLatestUserMessage(t *testing.T) {
	for _, test := range []struct{ protocol, body string }{
		{ProtocolClaude, `{"messages":[{"role":"user","content":"old"},{"role":"user","content":[{"type":"tool_result","content":"今晚打老虎"}]}]}`},
		{ProtocolClaude, `{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"今晚打老虎"}]}`},
		{ProtocolChat, `{"messages":[{"role":"user","content":"old"},{"role":"tool","content":"今晚打老虎"}]}`},
		{ProtocolResponses, `{"input":[{"role":"user","content":"old"},{"type":"function_call_output","output":"今晚打老虎"}]}`},
		{ProtocolGemini, `{"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"model","parts":[{"text":"今晚打老虎"}]}]}`},
	} {
		assert.Empty(t, ExtractKeywordText(test.protocol, []byte(test.body)), test.body)
	}
}

func TestMultipartImageEditPrompt(t *testing.T) {
	setupDB(t)
	require.NoError(t, i18n.Init())
	server := newModerationServer(t, okStatus, scoreBy("attack"))
	useConfig(t, moderationConfig(content_moderation_setting.ModePreBlock, server.URL), []string{"k1"})
	s := newService()

	// form builds an image edit with a file part of fileBytes bytes followed
	// by the given prompt fields.
	form := func(fileBytes int, prompts ...string) (*bytes.Buffer, string) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		file, err := writer.CreateFormFile("image", "a.png")
		require.NoError(t, err)
		_, err = file.Write(bytes.Repeat([]byte{0x89}, fileBytes))
		require.NoError(t, err)
		require.NoError(t, writer.WriteField("prompt", prompts[0]))
		for _, prompt := range prompts[1:] {
			require.NoError(t, writer.WriteField("prompt", prompt))
		}
		require.NoError(t, writer.Close())
		return &body, writer.FormDataContentType()
	}
	info := reminderInfo(ProtocolImages)
	info.RequestURLPath = "/v1/images/edits"

	body, contentType := form(16, "a calm lake", "an attack scene")
	prompts, tooLarge := MultipartPrompts(contentType, bytes.NewReader(body.Bytes()))
	assert.Equal(t, []string{"a calm lake", "an attack scene"}, prompts, "every prompt field the upstream receives is read")
	assert.False(t, tooLarge)
	prompts, _ = MultipartPrompts("application/json", bytes.NewReader(body.Bytes()))
	assert.Empty(t, prompts)
	c, _ := relayContext(body.String())
	c.Request.Header.Set("Content-Type", contentType)
	require.NotNil(t, s.checkRelay(c, info), "a flagged second prompt field is blocked")
	assert.Equal(t, int64(1), server.calls.Load())
	server.mu.Lock()
	assert.Contains(t, server.inputs[0], "a calm lake an attack scene")
	server.mu.Unlock()

	// A file larger than the JSON body limit does not skip the prompt.
	previousLimit := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 128
	t.Cleanup(func() { constant.MaxRequestBodyMB = previousLimit })
	body, contentType = form(maxBodyBytes+1024, "an attack scene")
	c, _ = relayContext(body.String())
	c.Request.Header.Set("Content-Type", contentType)
	require.NotNil(t, s.checkRelay(c, info), "a large image does not hide the prompt")
	assert.Zero(t, s.metrics.tooLarge.Load())

	// Prompt text over the limit is handled like an oversized JSON body.
	half := strings.Repeat("x", maxPromptFieldBytes/2)
	body, contentType = form(16, half, half, "an attack scene")
	_, tooLarge = MultipartPrompts(contentType, bytes.NewReader(body.Bytes()))
	assert.True(t, tooLarge)
	calls := server.calls.Load()
	c, _ = relayContext(body.String())
	c.Request.Header.Set("Content-Type", contentType)
	assert.Nil(t, s.checkRelay(c, info))
	assert.Equal(t, int64(1), s.metrics.tooLarge.Load(), "an oversized prompt is counted as too large")
	assert.Equal(t, calls, server.calls.Load())
}

func TestEngineNeverFollowsRedirects(t *testing.T) {
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
	}))
	t.Cleanup(target.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/moderations", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)
	config := moderationConfig(content_moderation_setting.ModePreBlock, redirecting.URL)
	require.NoError(t, config.Normalize())
	_, _, err := (&engine{now: time.Now}).moderate(context.Background(), &content_moderation_setting.Snapshot{Config: config, Keys: []string{"k1"}}, Input{Text: "x"})
	var engineErr *engineError
	require.ErrorAs(t, err, &engineErr)
	assert.Equal(t, http.StatusTemporaryRedirect, engineErr.HTTPStatus)
	assert.Zero(t, leaked.Load(), "the key is never sent to the redirect target")
}

func TestDisposalLossIsCounted(t *testing.T) {
	s := newService()
	s.records = make(chan *record) // no recorder is reading
	s.disposalWait = 10 * time.Millisecond
	s.enqueueRecord(&record{event: &model.ContentModerationEvent{}, dispose: true})
	s.enqueueRecord(&record{event: &model.ContentModerationEvent{}})
	assert.Equal(t, int64(1), s.metrics.disposalDropped.Load())
	assert.Equal(t, int64(2), s.metrics.recordFailed.Load())
	assert.Equal(t, int64(1), s.runtime().DisposalDropped)
}

func TestKeywordListVersionChangesWithinOneSecond(t *testing.T) {
	setupDB(t)
	require.NoError(t, model.SaveContentModerationKeywordList("spam", []string{"a"}, 100))
	first, err := model.ContentModerationKeywordListsVersion()
	require.NoError(t, err)
	require.NoError(t, model.SaveContentModerationKeywordList("spam", []string{"b"}, 100))
	second, err := model.ContentModerationKeywordListsVersion()
	require.NoError(t, err)
	assert.NotEqual(t, first, second, "a second save in the same second is still a new version")
}

func TestHashBlocklistRetentionAndLimit(t *testing.T) {
	setupDB(t)
	for index, createdAt := range []int64{10, 20, 300, 400, 500} {
		require.NoError(t, model.AddContentModerationFlaggedHash(strings.Repeat(strconv.Itoa(index), 64), createdAt))
	}
	deleted, err := model.CleanupContentModerationFlaggedHashes(100, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted, "two are past retention, one more is over the limit")
	hashes, err := model.ListContentModerationFlaggedHashes()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{strings.Repeat("3", 64), strings.Repeat("4", 64)}, hashes, "the newest are kept")
}

func TestKeywordSearchTreatsWildcardsLiterally(t *testing.T) {
	setupDB(t)
	_, err := model.CreateContentModerationEvents([]*model.ContentModerationEvent{
		{InputExcerpt: "100% sure", CreatedAt: 1}, {InputExcerpt: "1000 sure", CreatedAt: 1},
		{InputExcerpt: "a_b", CreatedAt: 1}, {InputExcerpt: "axb", CreatedAt: 1},
	})
	require.NoError(t, err)
	for keyword, want := range map[string]int64{"100%": 1, "a_b": 1, "sure": 2} {
		_, total, err := model.ListContentModerationEvents(model.ContentModerationEventFilter{Keyword: keyword}, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, want, total, keyword)
	}
}

func TestKeywordExcerptSkipsLeadingReminders(t *testing.T) {
	longReminder := "<system-reminder>" + strings.Repeat("context ", 200) + "</system-reminder>"
	body := []byte(chatBody(longReminder + " cheap pills"))
	excerpt := Excerpt(keywordExcerptSource(ExtractInput(ProtocolChat, body).Text, ExtractKeywordText(ProtocolChat, body)))
	assert.Equal(t, "cheap pills", excerpt, "a leading reminder does not crowd out the user's words")

	inside := "<system-reminder>cheap pills</system-reminder>"
	assert.Equal(t, inside, keywordExcerptSource("", inside), "a keyword inside a reminder keeps the reminder text")

	assert.Len(t, []rune(Excerpt(strings.Repeat("token ", 100000))), maxExcerptRunes, "a large input is cut before redaction")
	assert.Equal(t, "日本", prefixRunes("日本語", 2), "the window is cut at a rune boundary")
}

func TestHashBlocklistLoadsOnlyWhenUsable(t *testing.T) {
	setupDB(t)
	config := content_moderation_setting.DefaultConfig()
	config.HashBlocklistEnabled = true
	assert.False(t, hashBlocklistInUse(&config), "off with the default policy: nothing reads the blocklist")
	SetScopePolicy(fixedPolicy{Decision{Moderate: true, Mode: "rule", Action: ActionBlock, SampleRate: 100}})
	t.Cleanup(func() { SetScopePolicy(nil) })
	assert.True(t, hashBlocklistInUse(&config), "a replaced policy may moderate while the global mode is off")
	SetScopePolicy(nil)
	config.Mode = content_moderation_setting.ModeObserve
	assert.True(t, hashBlocklistInUse(&config))
	config.HashBlocklistEnabled = false
	assert.False(t, hashBlocklistInUse(&config))

	s := newService()
	require.NoError(t, model.AddContentModerationFlaggedHash(strings.Repeat("a", 64), 100))
	require.NoError(t, s.refreshHashes())
	assert.True(t, s.hashListed(strings.Repeat("a", 64)))
	require.NoError(t, model.AddContentModerationFlaggedHash(strings.Repeat("b", 64), 200))
	require.NoError(t, s.refreshHashes())
	assert.True(t, s.hashListed(strings.Repeat("b", 64)), "a changed table is read again")
}
