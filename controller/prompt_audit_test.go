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
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPromptAuditTest(t *testing.T) service.AuthIdentity {
	t.Helper()
	_, identity := setupSecurityEnrollmentTest(t)
	previous := prompt_audit_setting.Current()
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	for key, value := range previousMap {
		common.OptionMap[key] = value
	}
	for _, key := range []string{prompt_audit_setting.OptionKey, prompt_audit_setting.TokensOptionKey} {
		common.OptionMap[key] = ""
	}
	common.OptionMapRWMutex.Unlock()
	prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, "")
	prompt_audit_setting.ApplyOption(prompt_audit_setting.TokensOptionKey, "")
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
		configJSON, _ := common.Marshal(previous.Config)
		tokensJSON, _ := common.Marshal(previous.Tokens)
		prompt_audit_setting.ApplyOption(prompt_audit_setting.OptionKey, string(configJSON))
		prompt_audit_setting.ApplyOption(prompt_audit_setting.TokensOptionKey, string(tokensJSON))
	})
	return identity
}

func savePromptAuditConfig(t *testing.T, identity service.AuthIdentity, baseURL string, version int64, tokenChanges string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"expected_config_version":` + common.Interface2String(version) + `,"token_changes":` + tokenChanges + `,
		"config":{"enabled":true,"mode":"async_audit","all_groups":false,"groups":["claude-ch*","default"],
		"scanners":["jailbreak","pii"],"store_full_prompt":true,"retention_days":30,"worker_count":2,"queue_capacity":100,
		"endpoints":[{"id":"g1","name":"local","base_url":"` + baseURL + `","model":"guard","enabled":true}]}}`
	return securityEnrollmentRequest(http.MethodPut, "/api/prompt_audit/config", body, "", identity, UpdatePromptAuditConfig)
}

func TestPromptAuditOptionsAreOnlyWritableThroughTheDedicatedAPI(t *testing.T) {
	identity := setupPromptAuditTest(t)
	for _, key := range []string{prompt_audit_setting.OptionKey, prompt_audit_setting.TokensOptionKey} {
		body, err := common.Marshal(OptionUpdateRequest{Key: key, Value: `{"enabled":true}`})
		require.NoError(t, err)
		response := securityEnrollmentRequest(http.MethodPut, "/api/option/", string(body), "", identity, UpdateOption)
		assert.Contains(t, response.Body.String(), `"message_key":"This setting is managed by the prompt audit settings page"`, key)
	}
	assert.False(t, prompt_audit_setting.Current().Config.Enabled)
	assert.Error(t, model.UpdateOptionsBulk(map[string]string{prompt_audit_setting.TokensOptionKey: `{"g1":"x"}`}))
}

func TestPromptAuditConfigNeverReturnsGuardTokens(t *testing.T) {
	identity := setupPromptAuditTest(t)
	const secret = "guard-secret-value"

	saved := savePromptAuditConfig(t, identity, "http://127.0.0.1:11434/v1", 0, `{"g1":{"value":"`+secret+`"}}`)
	require.Contains(t, saved.Body.String(), `"success":true`, saved.Body.String())
	assert.NotContains(t, saved.Body.String(), secret)
	assert.Contains(t, saved.Body.String(), `"has_token":{"g1":true}`)
	snapshot := prompt_audit_setting.Current()
	assert.True(t, snapshot.Config.Enabled)
	assert.Equal(t, int64(1), snapshot.Config.ConfigVersion)
	assert.Equal(t, "http://127.0.0.1:11434", snapshot.Config.Endpoints[0].BaseURL)
	assert.Equal(t, secret, snapshot.Tokens["g1"])

	for _, response := range []*httptest.ResponseRecorder{
		securityEnrollmentRequest(http.MethodGet, "/api/prompt_audit/config", "", "", identity, GetPromptAuditConfig),
		securityEnrollmentRequest(http.MethodGet, "/api/option/", "", "", identity, GetOptions),
	} {
		assert.Contains(t, response.Body.String(), `"success":true`)
		assert.NotContains(t, response.Body.String(), secret)
		assert.NotContains(t, response.Body.String(), prompt_audit_setting.TokensOptionKey)
	}

	stale := savePromptAuditConfig(t, identity, "http://127.0.0.1:11434", 0, `{}`)
	assert.Equal(t, http.StatusConflict, stale.Code)

	kept := savePromptAuditConfig(t, identity, "http://127.0.0.1:11434", 1, `{}`)
	require.Contains(t, kept.Body.String(), `"success":true`)
	assert.Equal(t, secret, prompt_audit_setting.Current().Tokens["g1"], "same address keeps the stored token")

	moved := savePromptAuditConfig(t, identity, "http://10.0.0.9:8000", 2, `{}`)
	require.Contains(t, moved.Body.String(), `"success":true`)
	assert.Contains(t, moved.Body.String(), `"has_token":{"g1":false}`)
	assert.Empty(t, prompt_audit_setting.Current().Tokens["g1"], "a new address drops the stored token")
	assert.Equal(t, int64(3), prompt_audit_setting.Current().Config.ConfigVersion)
}

func TestPromptAuditRoutesRequireRoot(t *testing.T) {
	setupPromptAuditTest(t)
	sessionFor := func(role int) string {
		password, err := common.Password2Hash("prompt-audit-password")
		require.NoError(t, err)
		user := &model.User{Username: "pa-user-" + common.Interface2String(role), Password: password, Role: role,
			Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "pa-aff-" + common.Interface2String(role)}
		require.NoError(t, model.DB.Create(user).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "prompt-audit")
		require.NoError(t, err)
		return bundle.AccessToken
	}
	router := gin.New()
	group := router.Group("/api/prompt_audit", middleware.RootAuth())
	group.GET("/config", GetPromptAuditConfig)
	group.PUT("/config", UpdatePromptAuditConfig)
	group.POST("/endpoints/probe", ProbePromptAuditEndpoint)
	group.GET("/runtime", GetPromptAuditRuntime)
	group.GET("/events", GetPromptAuditEvents)
	group.GET("/events/:id", GetPromptAuditEvent)
	group.DELETE("/events/:id", DeletePromptAuditEvent)
	group.POST("/events/batch-delete", BatchDeletePromptAuditEvents)
	group.POST("/events/delete-preview", PreviewPromptAuditEventDeletion)
	group.POST("/events/delete-by-filter", DeletePromptAuditEventsByFilter)
	send := func(method, path, bearer string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	admin := sessionFor(common.RoleAdminUser)
	for _, route := range router.Routes() {
		path := strings.Replace(route.Path, ":id", "1", 1)
		for _, bearer := range []string{"", admin} {
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, send(route.Method, path, bearer).Code, route.Method+" "+route.Path)
		}
	}
	root := send(http.MethodGet, "/api/prompt_audit/config", sessionFor(common.RoleRootUser))
	assert.Equal(t, http.StatusOK, root.Code)
	assert.Contains(t, root.Body.String(), `"success":true`)
}
