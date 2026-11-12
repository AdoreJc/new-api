package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupContentModerationTest(t *testing.T) service.AuthIdentity {
	t.Helper()
	_, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationEvent{}, &model.ContentModerationFlaggedHash{}, &model.ContentModerationKeywordList{}))
	previous := content_moderation_setting.Current()
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	for key, value := range previousMap {
		common.OptionMap[key] = value
	}
	common.OptionMap[content_moderation_setting.OptionKey] = ""
	common.OptionMap[content_moderation_setting.KeysOptionKey] = ""
	common.OptionMapRWMutex.Unlock()
	content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, "")
	content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, "")
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
		configJSON, _ := common.Marshal(previous.Config)
		keysJSON, _ := common.Marshal(previous.Keys)
		content_moderation_setting.ApplyOption(content_moderation_setting.OptionKey, string(configJSON))
		content_moderation_setting.ApplyOption(content_moderation_setting.KeysOptionKey, string(keysJSON))
	})
	return identity
}

func saveContentModerationConfig(identity service.AuthIdentity, baseURL string, version int64, keyChanges string) *httptest.ResponseRecorder {
	body := `{"expected_config_version":` + common.Interface2String(version) + `,"key_changes":` + keyChanges + `,
		"config":{"mode":"pre_block","base_url":"` + baseURL + `","model":"omni-moderation-latest","all_groups":true,
		"thresholds":{"hate":0.5},"allowlist_user_ids":[9,3,9]}}`
	return securityEnrollmentRequest(http.MethodPut, "/api/content_moderation/config", body, "", identity, UpdateContentModerationConfig)
}

func TestContentModerationConfigKeepsKeysSecret(t *testing.T) {
	identity := setupContentModerationTest(t)
	const secret = "sk-moderation-secret-value"

	for _, key := range []string{content_moderation_setting.OptionKey, content_moderation_setting.KeysOptionKey} {
		body, err := common.Marshal(OptionUpdateRequest{Key: key, Value: `["x"]`})
		require.NoError(t, err)
		response := securityEnrollmentRequest(http.MethodPut, "/api/option/", string(body), "", identity, UpdateOption)
		assert.Contains(t, response.Body.String(), `"message_key":"This setting is managed by the content moderation settings page"`, key)
	}

	missing := saveContentModerationConfig(identity, "https://api.openai.com", 0, `{}`)
	assert.Contains(t, missing.Body.String(), "Add at least one moderation API key", "an active mode needs a key")

	saved := saveContentModerationConfig(identity, "https://api.openai.com/v1", 0, `{"add":["`+secret+`"]}`)
	require.Contains(t, saved.Body.String(), `"success":true`, saved.Body.String())
	assert.NotContains(t, saved.Body.String(), secret)
	assert.Contains(t, saved.Body.String(), `"id":"`+content_moderation_setting.KeyID(secret)+`"`)
	snapshot := content_moderation_setting.Current()
	assert.Equal(t, content_moderation_setting.ModePreBlock, snapshot.Config.Mode)
	assert.Equal(t, "https://api.openai.com", snapshot.Config.BaseURL)
	assert.Equal(t, []int{3, 9}, snapshot.Config.AllowlistUserIDs)
	assert.InDelta(t, 0.5, snapshot.Config.Thresholds["hate"], 1e-9)
	assert.InDelta(t, 0.98, snapshot.Config.Thresholds["harassment"], 1e-9, "unset categories keep their defaults")
	assert.Equal(t, 100, snapshot.Config.SampleRate, "fields left out of the request keep their defaults")
	assert.True(t, snapshot.Config.IncludeImages)
	assert.Equal(t, []string{secret}, snapshot.Keys)

	for _, response := range []*httptest.ResponseRecorder{
		securityEnrollmentRequest(http.MethodGet, "/api/content_moderation/config", "", "", identity, GetContentModerationConfig),
		securityEnrollmentRequest(http.MethodGet, "/api/content_moderation/runtime", "", "", identity, GetContentModerationRuntime),
		securityEnrollmentRequest(http.MethodGet, "/api/option/", "", "", identity, GetOptions),
	} {
		assert.Contains(t, response.Body.String(), `"success":true`)
		assert.NotContains(t, response.Body.String(), secret)
		assert.NotContains(t, response.Body.String(), content_moderation_setting.KeysOptionKey)
	}

	stale := saveContentModerationConfig(identity, "https://api.openai.com", 0, `{}`)
	assert.Equal(t, http.StatusConflict, stale.Code)

	kept := saveContentModerationConfig(identity, "https://api.openai.com", 1, `{}`)
	require.Contains(t, kept.Body.String(), `"success":true`)
	assert.Equal(t, []string{secret}, content_moderation_setting.Current().Keys, "the same address keeps the stored keys")

	moved := saveContentModerationConfig(identity, "https://moderation.example", 2, `{"add":["sk-new-key-value"]}`)
	require.Contains(t, moved.Body.String(), `"success":true`, moved.Body.String())
	assert.Equal(t, []string{"sk-new-key-value"}, content_moderation_setting.Current().Keys, "a new address drops the stored keys")

	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "option.update").Find(&audits).Error)
	require.NotEmpty(t, audits)
	for _, audit := range audits {
		encoded, err := common.Marshal(audit)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), secret, "audit logs never contain keys")
	}
}

func TestContentModerationRoutesRequireRoot(t *testing.T) {
	setupContentModerationTest(t)
	sessionFor := func(role int) string {
		password, err := common.Password2Hash("content-moderation-password")
		require.NoError(t, err)
		user := &model.User{Username: "cm-user-" + common.Interface2String(role), Password: password, Role: role,
			Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "cm-aff-" + common.Interface2String(role)}
		require.NoError(t, model.DB.Create(user).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "content-moderation")
		require.NoError(t, err)
		return bundle.AccessToken
	}
	router := gin.New()
	group := router.Group("/api/content_moderation", middleware.RootAuth())
	group.GET("/config", GetContentModerationConfig)
	group.PUT("/config", UpdateContentModerationConfig)
	group.POST("/allowlist", UpdateContentModerationAllowlist)
	group.POST("/probe", ProbeContentModerationEngine)
	group.GET("/runtime", GetContentModerationRuntime)
	group.GET("/events", GetContentModerationEvents)
	group.GET("/events/:id", GetContentModerationEvent)
	group.DELETE("/events/:id", DeleteContentModerationEvent)
	group.POST("/events/batch-delete", BatchDeleteContentModerationEvents)
	group.POST("/events/delete-preview", PreviewContentModerationEventDeletion)
	group.POST("/events/delete-by-filter", DeleteContentModerationEventsByFilter)
	group.DELETE("/hashes/:hash", DeleteContentModerationHash)
	group.DELETE("/hashes", ClearContentModerationHashes)
	group.GET("/keyword_lists", GetContentModerationKeywordLists)
	group.GET("/keyword_lists/:name", GetContentModerationKeywordList)
	group.PUT("/keyword_lists/:name", SaveContentModerationKeywordList)
	group.DELETE("/keyword_lists/:name", DeleteContentModerationKeywordList)
	send := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	admin := sessionFor(common.RoleAdminUser)
	for _, route := range router.Routes() {
		path := strings.NewReplacer(":id", "1", ":hash", strings.Repeat("a", 64), ":name", "spam").Replace(route.Path)
		for _, bearer := range []string{"", admin} {
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, send(route.Method, path, bearer, `{}`).Code, route.Method+" "+route.Path)
		}
	}
	root := sessionFor(common.RoleRootUser)
	response := send(http.MethodPost, "/api/content_moderation/allowlist", root, `{"user_id":42,"action":"add"}`)
	require.Contains(t, response.Body.String(), `"success":true`, response.Body.String())
	assert.True(t, content_moderation_setting.Current().Config.Allowlisted(42))
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "content_moderation.allowlist_add").Find(&audits).Error)
	assert.Len(t, audits, 1, "allowlist changes are audit-logged")

	saved := send(http.MethodPut, "/api/content_moderation/keyword_lists/spam", root, `{"words":"BuyNow\nbuynow\nfree gift\n"}`)
	require.Contains(t, saved.Body.String(), `"count":2`, saved.Body.String())
	lists := send(http.MethodGet, "/api/content_moderation/keyword_lists", root, "")
	assert.Contains(t, lists.Body.String(), `"name":"default"`)
	assert.Contains(t, lists.Body.String(), `"name":"spam","count":2`)
	assert.NotContains(t, lists.Body.String(), "BuyNow", "the summary carries no keywords")
	builtin := send(http.MethodPut, "/api/content_moderation/keyword_lists/default", root, `{"words":"x"}`)
	assert.Equal(t, http.StatusBadRequest, builtin.Code, "the default list is edited as sensitive words, not here")
	require.NoError(t, model.LOG_DB.Where("action = ?", "content_moderation.keyword_list_save").Find(&audits).Error)
	require.Len(t, audits, 1)
	encoded, err := common.Marshal(audits[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "BuyNow", "the audit log records the list size, not its keywords")
}
