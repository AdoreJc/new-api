package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const emailOAuthTestRedirectURI = "https://console.example/system-settings/operations/email"

// setupEmailOAuthAuthorization prepares a signed-in user, an OAuth app that
// is not yet authorized, and a fake provider.
func setupEmailOAuthAuthorization(t *testing.T, providerName string) (service.AuthIdentity, *fakeMailProvider) {
	t.Helper()
	user, identity := setupSecurityEnrollmentTest(t)
	options := oauthEmailOptions(providerName)
	options["EmailProvider"] = "smtp"
	options[common.EmailOAuthRefreshTokenOption], options[common.EmailOAuthEmailOption] = "", ""
	configureEmailDelivery(t, options)
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://console.example/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })
	provider := newFakeMailProvider(t)
	provider.issue = func(form url.Values) (int, string) {
		if form.Get("grant_type") != "authorization_code" {
			return http.StatusBadRequest, `{"error":"unsupported_grant_type"}`
		}
		return http.StatusOK, `{"access_token":"access-1","expires_in":3600,"refresh_token":"refresh-granted"}`
	}
	require.NotZero(t, user.Id)
	return identity, provider
}

func startEmailOAuth(t *testing.T, identity service.AuthIdentity) url.Values {
	t.Helper()
	response := securityEnrollmentRequest(http.MethodPost, "/api/option/email_oauth/authorize", "", "", identity, StartEmailOAuthAuthorization)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	var data struct {
		AuthorizationURL string `json:"authorization_url"`
		RedirectURI      string `json:"redirect_uri"`
	}
	require.NoError(t, common.Unmarshal(result.Data, &data))
	assert.Equal(t, emailOAuthTestRedirectURI, data.RedirectURI)
	authorizationURL, err := url.Parse(data.AuthorizationURL)
	require.NoError(t, err)
	query := authorizationURL.Query()
	query.Set("path", authorizationURL.Path)
	return query
}

func completeEmailOAuth(identity service.AuthIdentity, body string) *httptest.ResponseRecorder {
	return securityEnrollmentRequest(http.MethodPost, "/api/option/email_oauth/callback", body, "", identity, EmailOAuthCallback)
}

func TestEmailOAuthAuthorizationStoresMailboxOnce(t *testing.T) {
	for _, test := range []struct {
		provider, path, email string
		scopes                []string
	}{
		{common.EmailOAuthProviderGoogle, "/google/authorize", "sender@gmail.example", []string{"https://www.googleapis.com/auth/gmail.send", "email"}},
		{common.EmailOAuthProviderMicrosoft, "/ms/contoso.example/oauth2/v2.0/authorize", "sender@contoso.example", []string{"https://graph.microsoft.com/Mail.Send", "offline_access"}},
	} {
		t.Run(test.provider, func(t *testing.T) {
			identity, provider := setupEmailOAuthAuthorization(t, test.provider)
			authorization := startEmailOAuth(t, identity)
			assert.Equal(t, test.path, authorization.Get("path"))
			assert.Equal(t, "client-id", authorization.Get("client_id"))
			assert.Equal(t, "code", authorization.Get("response_type"))
			assert.Equal(t, emailOAuthTestRedirectURI, authorization.Get("redirect_uri"))
			assert.Equal(t, "S256", authorization.Get("code_challenge_method"))
			for _, scope := range test.scopes {
				assert.Contains(t, strings.Fields(authorization.Get("scope")), scope)
			}
			if test.provider == common.EmailOAuthProviderGoogle {
				assert.Equal(t, "offline", authorization.Get("access_type"))
			}
			state := authorization.Get("state")
			require.NotEmpty(t, state)

			body := `{"state":"` + state + `","code":"auth-code"}`
			response := completeEmailOAuth(identity, body)
			require.Contains(t, response.Body.String(), `"success":true`, response.Body.String())
			assert.Contains(t, response.Body.String(), test.email)
			require.Len(t, provider.tokenForms, 1)
			form := provider.tokenForms[0]
			assert.Equal(t, "auth-code", form.Get("code"))
			assert.Equal(t, emailOAuthTestRedirectURI, form.Get("redirect_uri"))
			assert.Equal(t, "client-secret", form.Get("client_secret"))
			challenge := sha256.Sum256([]byte(form.Get("code_verifier")))
			assert.Equal(t, authorization.Get("code_challenge"), base64.RawURLEncoding.EncodeToString(challenge[:]), "the verifier must match the challenge")
			assert.Equal(t, "refresh-granted", common.OptionMap[common.EmailOAuthRefreshTokenOption])
			assert.Equal(t, test.email, common.OptionMap[common.EmailOAuthEmailOption])
			assert.Equal(t, "refresh-granted", storedOption(t, common.EmailOAuthRefreshTokenOption))
			assert.Equal(t, test.email, storedOption(t, common.EmailOAuthEmailOption))

			// The console sees the mailbox but never the secret or the token.
			options := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(options)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
			GetOptions(context)
			assert.NotContains(t, options.Body.String(), "refresh-granted")
			assert.NotContains(t, options.Body.String(), "client-secret")
			assert.NotContains(t, options.Body.String(), common.EmailOAuthRefreshTokenOption)
			assert.Contains(t, options.Body.String(), `"key":"EmailOAuthEmail","value":"`+test.email+`"`)
			assert.Contains(t, options.Body.String(), `"key":"EmailOAuthSecretConfigured","value":"true"`)

			// Replaying the same state is rejected without a second exchange.
			replay := completeEmailOAuth(identity, body)
			assert.Equal(t, http.StatusForbidden, replay.Code)
			assert.Len(t, provider.tokenForms, 1)
		})
	}
}

func TestEmailOAuthCallbackRejectsUnusableState(t *testing.T) {
	tests := []struct {
		name  string
		state func(t *testing.T, identity service.AuthIdentity) (service.AuthIdentity, string)
	}{
		{"missing", func(_ *testing.T, identity service.AuthIdentity) (service.AuthIdentity, string) { return identity, "" }},
		{"unknown", func(_ *testing.T, identity service.AuthIdentity) (service.AuthIdentity, string) {
			return identity, "not-a-real-state"
		}},
		{"expired", func(t *testing.T, identity service.AuthIdentity) (service.AuthIdentity, string) {
			state := startEmailOAuth(t, identity).Get("state")
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeEmailOAuth).Update("expires_at", time.Now().Add(-time.Minute)).Error)
			return identity, state
		}},
		{"other session", func(t *testing.T, identity service.AuthIdentity) (service.AuthIdentity, string) {
			state := startEmailOAuth(t, identity).Get("state")
			bundle, err := service.CreateLoginSession(identity.UserID, "password", "127.0.0.1", "other-session")
			require.NoError(t, err)
			other, err := service.ParseAccessToken(bundle.AccessToken)
			require.NoError(t, err)
			return other, state
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, provider := setupEmailOAuthAuthorization(t, common.EmailOAuthProviderGoogle)
			caller, state := test.state(t, identity)
			response := completeEmailOAuth(caller, `{"state":"`+state+`","code":"auth-code"}`)
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.Contains(t, response.Body.String(), `"message_key":"The authorization request is invalid or has expired. Start the authorization again"`)
			assert.Empty(t, provider.tokenForms, "no code exchange for a rejected state")
			assert.Empty(t, common.OptionMap[common.EmailOAuthRefreshTokenOption])
		})
	}

	t.Run("provider error consumes the state", func(t *testing.T) {
		identity, provider := setupEmailOAuthAuthorization(t, common.EmailOAuthProviderGoogle)
		state := startEmailOAuth(t, identity).Get("state")
		denied := completeEmailOAuth(identity, `{"state":"`+state+`","error":"access_denied"}`)
		assert.Contains(t, denied.Body.String(), `"message_key":"The mailbox authorization was denied or cancelled"`)
		retry := completeEmailOAuth(identity, `{"state":"`+state+`","code":"auth-code"}`)
		assert.Equal(t, http.StatusForbidden, retry.Code)
		assert.Empty(t, provider.tokenForms)
	})
}

func TestEmailOAuthEndpointsRequireRoot(t *testing.T) {
	setupEmailOAuthAuthorization(t, common.EmailOAuthProviderGoogle)
	common.OptionMap[common.EmailOAuthRefreshTokenOption], common.OptionMap[common.EmailOAuthEmailOption] = "refresh-1", "sender@gmail.example"
	var user model.User
	require.NoError(t, model.DB.First(&user).Error)
	require.Equal(t, common.RoleCommonUser, user.Role)
	bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "non-admin")
	require.NoError(t, err)
	router := gin.New()
	option := router.Group("/api/option", middleware.RootAuth())
	option.POST("/email_oauth/authorize", StartEmailOAuthAuthorization)
	option.POST("/email_oauth/callback", EmailOAuthCallback)
	option.DELETE("/email_oauth/authorization", RevokeEmailOAuthAuthorization)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/option/email_oauth/authorize"},
		{http.MethodPost, "/api/option/email_oauth/callback"},
		{http.MethodDelete, "/api/option/email_oauth/authorization"},
	} {
		for _, bearer := range []string{"", bundle.AccessToken} {
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"state":"x","code":"y"}`))
			if bearer != "" {
				request.Header.Set("Authorization", "Bearer "+bearer)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.Code, route.path)
		}
	}
	var flows int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeEmailOAuth).Count(&flows).Error)
	assert.Zero(t, flows)
	assert.Equal(t, "refresh-1", common.OptionMap[common.EmailOAuthRefreshTokenOption])
}

func TestEmailOAuthOptionRulesKeepDeliveryWorking(t *testing.T) {
	identity, _ := setupEmailOAuthAuthorization(t, common.EmailOAuthProviderGoogle)
	update := func(key string, value any) string {
		body, err := common.Marshal(OptionUpdateRequest{Key: key, Value: value})
		require.NoError(t, err)
		return securityEnrollmentRequest(http.MethodPut, "/api/option/", string(body), "", identity, UpdateOption).Body.String()
	}
	revoke := func() string {
		return securityEnrollmentRequest(http.MethodDelete, "/api/option/email_oauth/authorization", "", "", identity, RevokeEmailOAuthAuthorization).Body.String()
	}

	assert.Contains(t, update("EmailProvider", "oauth"), `"message_key":"Authorize the sender mailbox before enabling OAuth sending"`)
	for _, key := range []string{common.EmailOAuthRefreshTokenOption, common.EmailOAuthEmailOption, common.EmailOAuthSecretConfiguredOption} {
		assert.Contains(t, update(key, "attacker@example.com"), `"message_key":"This setting is managed by the mailbox authorization flow"`, key)
	}
	assert.Contains(t, update(common.EmailOAuthProviderOption, "yahoo"), `"message_key":"The OAuth app settings are incomplete or invalid"`)
	assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])

	require.NoError(t, model.SaveEmailOAuthAuthorization("refresh-0", "sender@gmail.example"))
	// A rotation is saved only over the token it replaces.
	assert.ErrorIs(t, model.ReplaceEmailOAuthRefreshToken("stale", "refresh-x"), common.ErrEmailOAuthAuthorizationChanged)
	require.NoError(t, model.ReplaceEmailOAuthRefreshToken("refresh-0", "refresh-1"))
	assert.Equal(t, "refresh-1", storedOption(t, common.EmailOAuthRefreshTokenOption))
	assert.Contains(t, update(common.EmailOAuthClientIdOption, "other-client"), `"message_key":"Remove the mailbox authorization before changing the OAuth app"`)
	assert.Contains(t, update(common.EmailOAuthClientSecretOption, "rotated-secret"), `"success":true`, "a secret can rotate without re-authorizing")
	assert.Contains(t, update("EmailProvider", "oauth"), `"success":true`)
	assert.Contains(t, update(common.EmailOAuthClientSecretOption, " "), `"message_key":"The OAuth app settings are incomplete or invalid"`)
	assert.Contains(t, revoke(), `"message_key":"Select another email sending method before removing the mailbox authorization"`)
	assert.Equal(t, "refresh-1", storedOption(t, common.EmailOAuthRefreshTokenOption))

	assert.Contains(t, update("EmailProvider", "smtp"), `"success":true`)
	assert.Contains(t, revoke(), `"success":true`)
	assert.Empty(t, storedOption(t, common.EmailOAuthRefreshTokenOption))
	assert.Empty(t, common.OptionMap[common.EmailOAuthEmailOption])
	assert.Contains(t, update(common.EmailOAuthClientIdOption, "other-client"), `"success":true`)
}
