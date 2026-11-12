package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/contentaudit"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func TestGeminiRelayHandsLongRunningPredictionToPluginRoute(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "veo-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "veotestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	source, err := plugins.Source("google")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	_, err = registry.RegisterFactory(source, jsplugin.Options{Key: "google"})
	require.NoError(t, err)
	// The plugin route authenticates as in production, then names itself.
	handlers := func(generation *jsplugin.RoutingGeneration, group jsplugin.RouteGroup) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, group)
		return []gin.HandlerFunc{production[0], production[1], production[2], func(c *gin.Context) {
			c.String(http.StatusOK, "plugin %s for user %d", group.Bindings[0].Plugin.Meta.Key, c.GetInt("id"))
		}}
	}
	require.NoError(t, registry.SetGenerationPreparer(newPluginGenerationBuilder(engine.Routes(), nil, handlers).prepare))
	require.Empty(t, registry.RoutingErrors())
	previousRegistry := defaultPluginDispatcher.registry
	defaultPluginDispatcher.registry = registry
	t.Cleanup(func() { defaultPluginDispatcher.registry = previousRegistry })
	engine.NoRoute(defaultPluginDispatcher.dispatch)

	for _, testCase := range []struct {
		method   string
		path     string
		toPlugin bool
	}{
		{method: http.MethodPost, path: "/v1beta/models/veo-3.1-generate-preview:predictLongRunning", toPlugin: true},
		{method: http.MethodGet, path: "/v1beta/models/veo-3.1-generate-preview/operations/task_sample", toPlugin: true},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.5-flash:generateContent"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(`{}`))
			request.Header.Set("x-goog-api-key", "veotestkey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if testCase.toPlugin {
				assert.Equal(t, fmt.Sprintf("plugin google for user %d", user.Id), recorder.Body.String())
				return
			}
			// The relay looks for a Gemini channel serving the model; there is none.
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"model_not_found"`)
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}

// TestRelayRoutesAreClassifiedForContentAudit lists every relay POST route with
// the relay format it enters controller.Relay with, or why it bypasses the
// relay entry. A new route fails here until it is classified.
func TestRelayRoutesAreClassifiedForContentAudit(t *testing.T) {
	const (
		notImplemented = "answered with not implemented"
		task           = "asynchronous task submission, not the relay entry"
	)
	relayFormats := map[string]types.RelayFormat{
		"POST /pg/chat/completions":          types.RelayFormatOpenAI,
		"POST /v1/chat/completions":          types.RelayFormatOpenAI,
		"POST /v1/completions":               types.RelayFormatOpenAI,
		"POST /v1/moderations":               types.RelayFormatOpenAI,
		"POST /v1/messages":                  types.RelayFormatClaude,
		"POST /v1/responses":                 types.RelayFormatOpenAIResponses,
		"POST /v1/responses/compact":         types.RelayFormatOpenAIResponsesCompaction,
		"POST /v1/models/*path":              types.RelayFormatGemini,
		"POST /v1beta/models/*path":          types.RelayFormatGemini,
		"POST /v1/engines/:model/embeddings": types.RelayFormatGemini, // embedding paths are skipped at the entry
		"POST /v1/images/generations":        types.RelayFormatOpenAIImage,
		"POST /v1/images/edits":              types.RelayFormatOpenAIImage,
		"POST /v1/edits":                     types.RelayFormatOpenAIImage,
		"POST /v1/embeddings":                types.RelayFormatEmbedding,
		"POST /v1/rerank":                    types.RelayFormatRerank,
		"POST /v1/audio/speech":              types.RelayFormatOpenAIAudio,
		"POST /v1/audio/transcriptions":      types.RelayFormatOpenAIAudio,
		"POST /v1/audio/translations":        types.RelayFormatOpenAIAudio,
		"POST /v1/alpha/search":              types.RelayFormatOpenAIAlphaSearch,
		"POST /v1/tools/search":              types.RelayFormatDeepSeekTools,
		"POST /v1/tools/fetch":               types.RelayFormatDeepSeekTools,
		"GET /v1/realtime":                   types.RelayFormatOpenAIRealtime,
	}
	bypassReasons := map[string]string{
		"GET /v1/responses":               "Responses WebSocket turns are billed per turn outside the relay entry",
		"POST /v1/images/variations":      notImplemented,
		"POST /v1/files":                  notImplemented,
		"POST /v1/fine-tunes":             notImplemented,
		"POST /v1/fine-tunes/:id/cancel":  notImplemented,
		"POST /v1/tasks/:key":             task,
		"POST /v1/video/generations":      task,
		"POST /v1/videos":                 task,
		"POST /v1/videos/:video_id/remix": task,
	}

	engine := gin.New()
	SetRelayRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	SetVideoRouter(engine)
	SetTaskRouter(engine)
	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		registered[key] = true
		if route.Method != http.MethodPost {
			continue
		}
		if strings.HasPrefix(route.Path, "/mj/") || strings.HasPrefix(route.Path, "/:mode/mj/") {
			continue // Midjourney submissions bypass the relay entry
		}
		_, relayed := relayFormats[key]
		_, bypassed := bypassReasons[key]
		assert.True(t, relayed || bypassed, "classify %s for content audit", key)
	}
	for key, format := range relayFormats {
		assert.True(t, registered[key], "%s is no longer registered", key)
		assert.True(t, contentaudit.IsAuditedFormat(format) || contentaudit.ExemptReason(format) != "", "%s format %s has no audit classification", key, format)
	}
	for key := range bypassReasons {
		assert.True(t, registered[key], "%s is no longer registered", key)
	}
	assert.NotEmpty(t, contentaudit.ExemptReason(types.RelayFormatMjProxy))
	assert.NotEmpty(t, contentaudit.ExemptReason(types.RelayFormatTask))
}
