package controller

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type emailHTTPTransport func(*http.Request) (*http.Response, error)

func (transport emailHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func configureEmailDelivery(t *testing.T, options map[string]string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	original := common.OptionMap
	common.OptionMap = options
	common.OptionMapRWMutex.Unlock()
	originalTransport := http.DefaultTransport
	originalFrom, originalAccount, originalServer := common.SMTPFrom, common.SMTPAccount, common.SMTPServer
	common.SMTPFrom, common.SMTPAccount, common.SMTPServer = "", "", ""
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = original
		common.OptionMapRWMutex.Unlock()
		http.DefaultTransport = originalTransport
		common.SMTPFrom, common.SMTPAccount, common.SMTPServer = originalFrom, originalAccount, originalServer
	})
}

func TestResendDeliversThroughSelectedProvider(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "New API <sender@example.com>"})
	calls := 0
	http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://api.resend.com/emails", request.URL.String())
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		_, hasDeadline := request.Context().Deadline()
		assert.True(t, hasDeadline, "email delivery must have a bounded wait")
		var body struct {
			From    string   `json:"from"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			HTML    string   `json:"html"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &body))
		assert.Equal(t, `"New API" <sender@example.com>`, body.From)
		assert.Equal(t, []string{"one@example.com", "two@example.com"}, body.To)
		assert.Equal(t, "验证码", body.Subject)
		assert.Equal(t, "<p>123456</p>", body.HTML)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"email-test-id"}`)), Header: make(http.Header)}, nil
	})
	require.NoError(t, common.SendEmail("验证码", "one@example.com; two@example.com", "<p>123456</p>"))
	assert.Equal(t, 1, calls, "no SMTP configuration is needed and no duplicate request is made")
}

func TestResendFailuresDoNotRetryFallbackOrExposeSensitiveContent(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			calls := 0
			http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://untrusted.example/emails"}}, Body: io.NopCloser(strings.NewReader(`{"message":"test-key 123456 reset-token"}`))}, nil
			})
			err := common.SendEmail("verification", "one@example.com", "123456 reset-token")
			require.EqualError(t, err, "Resend email delivery failed (HTTP "+strconv.Itoa(status)+")")
			assert.Equal(t, 1, calls, "do not retry or follow redirects")
		})
	}
}

func TestResendRejectsIncompleteOrInvalidConfigurationBeforeSending(t *testing.T) {
	tests := []struct{ name, key, from, receiver string }{
		{"missing key", "", "sender@example.com", "one@example.com"},
		{"header injection", "key\r\nInjected: value", "sender@example.com", "one@example.com"},
		{"missing sender", "test-key", "", "one@example.com"},
		{"invalid sender", "test-key", "bad-address", "one@example.com"},
		{"invalid recipient", "test-key", "sender@example.com", "bad-address"},
		{"empty recipient", "test-key", "sender@example.com", ""},
		{"too many recipients", "test-key", "sender@example.com", strings.Repeat("one@example.com;", 50) + "two@example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": test.key, "ResendFrom": test.from})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid settings must not make a request")
				return nil, errors.New("unexpected request")
			})
			err := common.SendEmail("verification", test.receiver, "123456")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "123456")
			assert.NotContains(t, err.Error(), "test-key")
		})
	}
}

func TestResendRejectsMalformedSuccessAndSanitizesTransportErrors(t *testing.T) {
	for _, body := range []string{`{}`, `not json`, `{"id":false}`, `{"id":""}`} {
		t.Run(body, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			require.EqualError(t, common.SendEmail("verification", "one@example.com", "123456"), "Invalid Resend delivery response")
		})
	}
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
	http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("test-key 123456 reset-token") })
	require.EqualError(t, common.SendEmail("verification", "one@example.com", "123456"), "Resend request failed")
}

func TestSMTPAndLegacyConfigurationNeverUseResend(t *testing.T) {
	for _, provider := range []string{"", "smtp", "invalid"} {
		t.Run("provider="+provider, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": provider, "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("SMTP and unknown providers must not invoke Resend")
				return nil, errors.New("unexpected request")
			})
			require.Error(t, common.SendEmail("verification", "one@example.com", "123456"))
		})
	}
}

func TestEmailOptionsNeverReturnResendCredentials(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "private-email-credential", "ResendFrom": "sender@example.com"})
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(context)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "private-email-credential")
	assert.NotContains(t, response.Body.String(), "ResendApiKey")
	assert.Contains(t, response.Body.String(), "ResendFrom")
	assert.Contains(t, response.Body.String(), `"key":"ResendKeyConfigured","value":"true"`)
}

func TestEmailProviderOptionRejectsInvalidValues(t *testing.T) {
	for _, provider := range []string{"", "both", "SMTP", "OAuth", "smtp,resend"} {
		t.Run(provider, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp"})
			body, err := common.Marshal(OptionUpdateRequest{Key: "EmailProvider", Value: provider})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
			UpdateOption(context)
			assert.Contains(t, response.Body.String(), `"success":false`)
			assert.Contains(t, response.Body.String(), `"message_key":"Email sending method must be SMTP, Resend, or OAuth"`)
			assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])
		})
	}
}

func TestEmailProviderCannotActivateIncompleteResend(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"missing key":    {"ResendFrom": "sender@example.com"},
		"missing sender": {"ResendApiKey": "test-key"},
		"invalid sender": {"ResendApiKey": "test-key", "ResendFrom": "invalid"},
		"invalid key":    {"ResendApiKey": "key\r\nInjected: value", "ResendFrom": "sender@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			settings["EmailProvider"] = "smtp"
			configureEmailDelivery(t, settings)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"EmailProvider","value":"resend"}`))
			require.NotPanics(t, func() { UpdateOption(context) }, "invalid activation must fail before accessing storage")
			assert.Contains(t, response.Body.String(), `"success":false`)
			assert.Contains(t, response.Body.String(), `"message_key":"Resend `)
			assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])
			assert.NotContains(t, response.Body.String(), "test-key")
		})
	}
}

func TestResendSettingsChangesKeepActiveDeliveryWorking(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	previousDB, previousRedis := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = previousDB, previousRedis })

	tests := []struct {
		name, provider, key, value string
		saved                      bool
	}{
		{"active sender the parser rejects", "resend", "ResendFrom", "notify..team@example.com", false},
		{"active key cleared", "resend", "ResendApiKey", " ", false},
		{"active sender renamed", "resend", "ResendFrom", `"New API" <notify@example.com>`, true},
		{"inactive sender saved before its key", "smtp", "ResendFrom", "notify@example.com", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := map[string]string{"EmailProvider": test.provider, "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"}
			if test.provider == "smtp" {
				settings["ResendApiKey"] = ""
			}
			configureEmailDelivery(t, settings)
			before := settings[test.key]
			body, err := common.Marshal(OptionUpdateRequest{Key: test.key, Value: test.value})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
			UpdateOption(context)
			if test.saved {
				assert.Contains(t, response.Body.String(), `"success":true`)
				assert.Equal(t, test.value, common.OptionMap[test.key])
				return
			}
			assert.Contains(t, response.Body.String(), `"message_key":"Resend `)
			assert.Equal(t, before, common.OptionMap[test.key])
		})
	}
}

func TestEmailKeyStatusIsDerivedAndReadOnly(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp", "ResendKeyConfigured": "true"})
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(context)
	assert.Contains(t, response.Body.String(), `"key":"ResendKeyConfigured","value":"false"`)
	assert.NotContains(t, response.Body.String(), `"value":"true"`)

	response = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"ResendKeyConfigured","value":true}`))
	UpdateOption(context)
	assert.Contains(t, response.Body.String(), `"success":false`)
	assert.Contains(t, response.Body.String(), "read-only")
}

// fakeMailProvider serves the Google and Microsoft OAuth, profile and send
// endpoints the OAuth sending method calls.
type fakeMailProvider struct {
	mu         sync.Mutex
	tokenForms []url.Values
	tenants    []string
	sends      []fakeMailSend
	issue      func(form url.Values) (int, string)
	sendStatus int
	sendBody   string
	url        string
}

type fakeMailSend struct {
	path, authorization string
	body                []byte
}

func newFakeMailProvider(t *testing.T) *fakeMailProvider {
	t.Helper()
	provider := &fakeMailProvider{sendStatus: http.StatusAccepted}
	provider.issue = func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"access-1","expires_in":3600}`
	}
	token := func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		provider.mu.Lock()
		provider.tokenForms = append(provider.tokenForms, r.PostForm)
		if tenant := r.PathValue("tenant"); tenant != "" {
			provider.tenants = append(provider.tenants, tenant)
		}
		issue := provider.issue
		provider.mu.Unlock()
		status, body := issue(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	send := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		provider.mu.Lock()
		provider.sends = append(provider.sends, fakeMailSend{path: r.URL.Path, authorization: r.Header.Get("Authorization"), body: body})
		status, reply := provider.sendStatus, provider.sendBody
		provider.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /google/token", token)
	mux.HandleFunc("POST /ms/{tenant}/oauth2/v2.0/token", token)
	mux.HandleFunc("GET /google/userinfo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer access-1", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"email":"sender@gmail.example","email_verified":true}`)
	})
	mux.HandleFunc("GET /graph/me", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer access-1", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"mail":"sender@contoso.example","userPrincipalName":"upn@contoso.example"}`)
	})
	mux.HandleFunc("POST /gmail/send", send)
	mux.HandleFunc("POST /graph/me/sendMail", send)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	provider.url = server.URL
	t.Cleanup(common.SetEmailOAuthEndpointsForTest(common.EmailOAuthEndpoints{
		GoogleAuthorizeURL: server.URL + "/google/authorize",
		GoogleTokenURL:     server.URL + "/google/token",
		GoogleUserInfoURL:  server.URL + "/google/userinfo",
		GmailSendURL:       server.URL + "/gmail/send",
		MicrosoftLoginBase: server.URL + "/ms",
		MicrosoftGraphBase: server.URL + "/graph",
	}))
	return provider
}

func (provider *fakeMailProvider) counts() (tokens int, sends int) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.tokenForms), len(provider.sends)
}

// useEmailOptionsDatabase stores options in a file-backed SQLite database and
// writes the given rows, so refresh token rotation has a row to update.
func useEmailOptionsDatabase(t *testing.T, rows map[string]string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/options.db?_pragma=busy_timeout(30000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	for key, value := range rows {
		require.NoError(t, db.Create(&model.Option{Key: key, Value: value}).Error)
	}
	previousDB, previousRedis := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() {
		model.DB, common.RedisEnabled = previousDB, previousRedis
		if connection, err := db.DB(); err == nil {
			_ = connection.Close()
		}
	})
}

func oauthEmailOptions(provider string) map[string]string {
	options := map[string]string{
		"EmailProvider":                     "oauth",
		common.EmailOAuthProviderOption:     provider,
		common.EmailOAuthClientIdOption:     "client-id",
		common.EmailOAuthClientSecretOption: "client-secret",
		common.EmailOAuthTenantOption:       "contoso.example",
		common.EmailOAuthRefreshTokenOption: "refresh-1",
		common.EmailOAuthEmailOption:        "sender@gmail.example",
	}
	if provider == common.EmailOAuthProviderMicrosoft {
		options[common.EmailOAuthEmailOption] = "sender@contoso.example"
	}
	return options
}

func storedOption(t *testing.T, key string) string {
	t.Helper()
	var option model.Option
	require.NoError(t, model.DB.Where(&model.Option{Key: key}).First(&option).Error)
	return option.Value
}

func TestOAuthEmailDeliveryRequestFormats(t *testing.T) {
	const subject, content = "验证码 ✓", "<p>你好，验证码 123456</p>"
	for _, providerName := range []string{common.EmailOAuthProviderGoogle, common.EmailOAuthProviderMicrosoft} {
		t.Run(providerName, func(t *testing.T) {
			options := oauthEmailOptions(providerName)
			configureEmailDelivery(t, options)
			useEmailOptionsDatabase(t, options)
			previousName := common.SystemName
			common.SystemName = "New API"
			t.Cleanup(func() { common.SystemName = previousName })
			provider := newFakeMailProvider(t)

			require.NoError(t, common.SendEmail(subject, "one@example.com; two@example.com", content))
			require.NoError(t, common.SendEmail(subject, "one@example.com", content))

			tokens, sends := provider.counts()
			assert.Equal(t, 1, tokens, "the access token is cached between messages")
			require.Equal(t, 2, sends)
			form := provider.tokenForms[0]
			assert.Equal(t, "refresh_token", form.Get("grant_type"))
			assert.Equal(t, "refresh-1", form.Get("refresh_token"))
			assert.Equal(t, "client-id", form.Get("client_id"))
			assert.Equal(t, "client-secret", form.Get("client_secret"))
			first := provider.sends[0]
			assert.Equal(t, "Bearer access-1", first.authorization)
			if providerName == common.EmailOAuthProviderMicrosoft {
				assert.Equal(t, []string{"contoso.example"}, provider.tenants)
				assert.Contains(t, form.Get("scope"), "https://graph.microsoft.com/Mail.Send")
				assert.Contains(t, form.Get("scope"), "offline_access")
				assert.NotContains(t, form.Get("scope"), "Mail.Send.Shared", "without a sender address the grant keeps its original scopes")
				assert.Equal(t, "/graph/me/sendMail", first.path)
				var body struct {
					Message struct {
						Subject string `json:"subject"`
						Body    struct {
							ContentType string `json:"contentType"`
							Content     string `json:"content"`
						} `json:"body"`
						From struct {
							EmailAddress struct {
								Name    string `json:"name"`
								Address string `json:"address"`
							} `json:"emailAddress"`
						} `json:"from"`
						ToRecipients []struct {
							EmailAddress struct {
								Address string `json:"address"`
							} `json:"emailAddress"`
						} `json:"toRecipients"`
					} `json:"message"`
					SaveToSentItems bool `json:"saveToSentItems"`
				}
				require.NoError(t, common.Unmarshal(first.body, &body))
				assert.Equal(t, subject, body.Message.Subject)
				assert.Equal(t, "HTML", body.Message.Body.ContentType)
				assert.Equal(t, content, body.Message.Body.Content)
				assert.Equal(t, "New API", body.Message.From.EmailAddress.Name)
				assert.Equal(t, "sender@contoso.example", body.Message.From.EmailAddress.Address)
				require.Len(t, body.Message.ToRecipients, 2)
				assert.Equal(t, "one@example.com", body.Message.ToRecipients[0].EmailAddress.Address)
				assert.Equal(t, "two@example.com", body.Message.ToRecipients[1].EmailAddress.Address)
				return
			}
			assert.Equal(t, "/gmail/send", first.path)
			var body struct {
				Raw string `json:"raw"`
			}
			require.NoError(t, common.Unmarshal(first.body, &body))
			raw, err := base64.RawURLEncoding.DecodeString(body.Raw)
			require.NoError(t, err)
			message, err := mail.ReadMessage(bytes.NewReader(raw))
			require.NoError(t, err)
			decodedSubject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			require.NoError(t, err)
			assert.Equal(t, subject, decodedSubject)
			assert.Equal(t, `"New API" <sender@gmail.example>`, message.Header.Get("From"))
			assert.Equal(t, "<one@example.com>, <two@example.com>", message.Header.Get("To"))
			assert.Equal(t, "text/html; charset=UTF-8", message.Header.Get("Content-Type"))
			assert.Equal(t, "base64", message.Header.Get("Content-Transfer-Encoding"))
			encoded, err := io.ReadAll(message.Body)
			require.NoError(t, err)
			html, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(encoded), "\r\n", ""))
			require.NoError(t, err)
			assert.Equal(t, content, string(html))
		})
	}
}

func TestOAuthEmailGmailRecipientsMatchValidatedAddresses(t *testing.T) {
	options := oauthEmailOptions(common.EmailOAuthProviderGoogle)
	configureEmailDelivery(t, options)
	useEmailOptionsDatabase(t, options)
	provider := newFakeMailProvider(t)

	// The quoted local-part contains a comma; unquoted in the header it would
	// read as two recipients.
	require.NoError(t, common.SendEmail("verification", `"first,second"@example.com; two@example.com`, "123456"))

	_, sends := provider.counts()
	require.Equal(t, 1, sends)
	var body struct {
		Raw string `json:"raw"`
	}
	require.NoError(t, common.Unmarshal(provider.sends[0].body, &body))
	raw, err := base64.RawURLEncoding.DecodeString(body.Raw)
	require.NoError(t, err)
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	recipients, err := message.Header.AddressList("To")
	require.NoError(t, err)
	delivered := make([]string, len(recipients))
	for i, recipient := range recipients {
		delivered[i] = recipient.Address
	}
	validated := make([]string, 0, 2)
	for _, address := range []string{`"first,second"@example.com`, "two@example.com"} {
		parsed, err := mail.ParseAddress(address)
		require.NoError(t, err)
		validated = append(validated, parsed.Address)
	}
	assert.Equal(t, validated, delivered)
}

func TestOAuthEmailRotatedRefreshTokenIsSavedBeforeReuse(t *testing.T) {
	options := oauthEmailOptions(common.EmailOAuthProviderMicrosoft)
	configureEmailDelivery(t, options)
	useEmailOptionsDatabase(t, options)
	provider := newFakeMailProvider(t)
	// Microsoft rotates the refresh token on every refresh; expires_in 0
	// forces a refresh for every message.
	provider.issue = func(form url.Values) (int, string) {
		next := map[string]string{"refresh-1": "refresh-2", "refresh-2": "refresh-3"}[form.Get("refresh_token")]
		if next == "" {
			return http.StatusBadRequest, `{"error":"invalid_grant"}`
		}
		return http.StatusOK, `{"access_token":"access-1","expires_in":0,"refresh_token":"` + next + `"}`
	}
	previousStore, previousDelays := common.EmailOAuthRefreshTokenStore, common.EmailOAuthRotationRetryDelays
	common.EmailOAuthRotationRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		common.EmailOAuthRefreshTokenStore, common.EmailOAuthRotationRetryDelays = previousStore, previousDelays
	})

	// A transient write failure is retried.
	failures := 2
	common.EmailOAuthRefreshTokenStore = func(previous, next string) error {
		if failures > 0 {
			failures--
			return errors.New("database is locked")
		}
		return previousStore(previous, next)
	}
	require.NoError(t, common.SendEmail("first", "one@example.com", "<p>1</p>"))
	assert.Equal(t, "refresh-2", storedOption(t, common.EmailOAuthRefreshTokenOption))
	assert.Equal(t, "refresh-2", common.OptionMap[common.EmailOAuthRefreshTokenOption])

	// When every retry fails, the rotated token is kept in memory; the next
	// message uses it, not the invalidated one, and saves it.
	common.EmailOAuthRefreshTokenStore = func(string, string) error { return errors.New("database is locked") }
	require.NoError(t, common.SendEmail("second", "one@example.com", "<p>2</p>"))
	assert.Equal(t, "refresh-2", storedOption(t, common.EmailOAuthRefreshTokenOption))
	common.EmailOAuthRefreshTokenStore = previousStore
	provider.issue = func(form url.Values) (int, string) {
		if form.Get("refresh_token") != "refresh-3" {
			return http.StatusBadRequest, `{"error":"invalid_grant"}`
		}
		return http.StatusOK, `{"access_token":"access-1","expires_in":3600,"refresh_token":"refresh-4"}`
	}
	require.NoError(t, common.SendEmail("third", "one@example.com", "<p>3</p>"))
	assert.Equal(t, "refresh-4", storedOption(t, common.EmailOAuthRefreshTokenOption))
	assert.Equal(t, "refresh-4", common.OptionMap[common.EmailOAuthRefreshTokenOption])
	_, sends := provider.counts()
	assert.Equal(t, 3, sends)
}

func TestOAuthEmailRotationDoesNotOverwriteNewerAuthorization(t *testing.T) {
	options := oauthEmailOptions(common.EmailOAuthProviderMicrosoft)
	configureEmailDelivery(t, options)
	useEmailOptionsDatabase(t, options)
	require.NoError(t, model.SaveEmailOAuthAuthorization(common.EmailOAuthGrant{RefreshToken: "refresh-new-grant", Email: "sender@contoso.example"}))
	assert.ErrorIs(t, model.ReplaceEmailOAuthRefreshToken("refresh-1", "refresh-2"), common.ErrEmailOAuthAuthorizationChanged)
	assert.Equal(t, "refresh-new-grant", storedOption(t, common.EmailOAuthRefreshTokenOption))
	assert.Equal(t, "refresh-new-grant", common.OptionMap[common.EmailOAuthRefreshTokenOption])
}

func TestOAuthEmailInvalidGrantAsksForReauthorization(t *testing.T) {
	for _, providerName := range []string{common.EmailOAuthProviderGoogle, common.EmailOAuthProviderMicrosoft} {
		t.Run(providerName, func(t *testing.T) {
			options := oauthEmailOptions(providerName)
			configureEmailDelivery(t, options)
			useEmailOptionsDatabase(t, options)
			provider := newFakeMailProvider(t)
			provider.issue = func(url.Values) (int, string) {
				return http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh-1 was revoked"}`
			}
			err := common.SendEmail("verification", "one@example.com", "123456")
			require.EqualError(t, err, "The sender mailbox authorization has expired or was revoked. Re-authorize it in the email settings")
			_, sends := provider.counts()
			assert.Zero(t, sends)
		})
	}
}

func TestOAuthEmailFailuresDoNotFallBackToOtherMethods(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var smtpConnections atomic.Int32
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			smtpConnections.Add(1)
			_ = connection.Close()
		}
	}()
	smtpPort := listener.Addr().(*net.TCPAddr).Port

	tests := []struct {
		name, refreshToken, expected string
		tokenStatus, sendStatus      int
	}{
		{"not authorized", "", "The sender mailbox is not authorized", http.StatusOK, http.StatusAccepted},
		{"token endpoint unavailable", "refresh-1", "OAuth email request failed", http.StatusServiceUnavailable, http.StatusAccepted},
		{"send rejected", "refresh-1", "OAuth email delivery failed (HTTP 500)", http.StatusOK, http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := oauthEmailOptions(common.EmailOAuthProviderGoogle)
			options[common.EmailOAuthRefreshTokenOption] = test.refreshToken
			options["ResendApiKey"], options["ResendFrom"] = "test-key", "sender@example.com"
			configureEmailDelivery(t, options)
			useEmailOptionsDatabase(t, options)
			previousPort := common.SMTPPort
			common.SMTPServer, common.SMTPPort, common.SMTPAccount, common.SMTPFrom = "127.0.0.1", smtpPort, "smtp@example.com", "smtp@example.com"
			t.Cleanup(func() { common.SMTPPort = previousPort })
			provider := newFakeMailProvider(t)
			provider.sendStatus = test.sendStatus
			provider.issue = func(url.Values) (int, string) {
				return test.tokenStatus, `{"access_token":"access-1","expires_in":3600}`
			}
			defaultTransport := http.DefaultTransport
			http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host == "api.resend.com" {
					t.Error("OAuth failures must not fall back to Resend")
				}
				return defaultTransport.RoundTrip(request)
			})

			err := common.SendEmail("verification", "one@example.com", "123456")
			require.EqualError(t, err, test.expected)
			assert.NotContains(t, err.Error(), "123456")
			assert.Zero(t, smtpConnections.Load(), "OAuth failures must not fall back to SMTP")
		})
	}
}

// sentMIMEMessage decodes the Gmail API message of the given send.
func sentMIMEMessage(t *testing.T, send fakeMailSend) *mail.Message {
	t.Helper()
	var body struct {
		Raw string `json:"raw"`
	}
	require.NoError(t, common.Unmarshal(send.body, &body))
	raw, err := base64.RawURLEncoding.DecodeString(body.Raw)
	require.NoError(t, err)
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	return message
}

func TestOAuthEmailSendsFromConfiguredSenderAddress(t *testing.T) {
	const sharedScopes = "https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/Mail.Send.Shared https://graph.microsoft.com/User.Read"
	previousName := common.SystemName
	common.SystemName = "New API"
	t.Cleanup(func() { common.SystemName = previousName })

	t.Run("gmail alias is the From address and Message-ID domain", func(t *testing.T) {
		options := oauthEmailOptions(common.EmailOAuthProviderGoogle)
		options[common.EmailOAuthFromOption] = "notify@alias.example"
		configureEmailDelivery(t, options)
		useEmailOptionsDatabase(t, options)
		provider := newFakeMailProvider(t)

		require.NoError(t, common.SendEmail("verification", "one@example.com", "123456"))

		require.Len(t, provider.sends, 1)
		assert.Equal(t, "/gmail/send", provider.sends[0].path)
		message := sentMIMEMessage(t, provider.sends[0])
		assert.Equal(t, `"New API" <notify@alias.example>`, message.Header.Get("From"))
		assert.True(t, strings.HasSuffix(message.Header.Get("Message-ID"), "@alias.example>"), message.Header.Get("Message-ID"))
		assert.Empty(t, provider.tokenForms[0].Get("scope"), "Google refreshes never carry a scope")
	})

	t.Run("microsoft shared mailbox is message.from on /me/sendMail", func(t *testing.T) {
		options := oauthEmailOptions(common.EmailOAuthProviderMicrosoft)
		options[common.EmailOAuthFromOption] = "shared@contoso.example"
		options[common.EmailOAuthScopesOption] = sharedScopes
		configureEmailDelivery(t, options)
		useEmailOptionsDatabase(t, options)
		provider := newFakeMailProvider(t)

		require.NoError(t, common.SendEmail("verification", "one@example.com", "123456"))

		require.Len(t, provider.sends, 1)
		assert.Equal(t, "/graph/me/sendMail", provider.sends[0].path)
		var body struct {
			Message struct {
				From struct {
					EmailAddress struct {
						Name    string `json:"name"`
						Address string `json:"address"`
					} `json:"emailAddress"`
				} `json:"from"`
			} `json:"message"`
		}
		require.NoError(t, common.Unmarshal(provider.sends[0].body, &body))
		assert.Equal(t, "New API", body.Message.From.EmailAddress.Name)
		assert.Equal(t, "shared@contoso.example", body.Message.From.EmailAddress.Address)
		assert.Contains(t, strings.Fields(provider.tokenForms[0].Get("scope")), "https://graph.microsoft.com/Mail.Send.Shared",
			"the refresh asks for the scopes the grant received")
	})

	t.Run("microsoft authorized mailbox in another case is not a shared mailbox", func(t *testing.T) {
		options := oauthEmailOptions(common.EmailOAuthProviderMicrosoft)
		options[common.EmailOAuthFromOption] = "Sender@Contoso.example"
		configureEmailDelivery(t, options)
		useEmailOptionsDatabase(t, options)
		provider := newFakeMailProvider(t)

		require.NoError(t, common.SendEmail("verification", "one@example.com", "123456"))
		_, sends := provider.counts()
		assert.Equal(t, 1, sends)
	})
}

func TestOAuthEmailSharedMailboxNeverFallsBackToTheAuthorizedMailbox(t *testing.T) {
	t.Run("grant without Mail.Send.Shared asks for re-authorization", func(t *testing.T) {
		options := oauthEmailOptions(common.EmailOAuthProviderMicrosoft)
		options[common.EmailOAuthFromOption] = "shared@contoso.example"
		options[common.EmailOAuthScopesOption] = "https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/User.Read"
		configureEmailDelivery(t, options)
		useEmailOptionsDatabase(t, options)
		provider := newFakeMailProvider(t)

		err := common.SendEmail("verification", "one@example.com", "123456")
		require.ErrorIs(t, err, common.ErrEmailOAuthSharedReauthorize)
		tokens, sends := provider.counts()
		assert.Zero(t, tokens)
		assert.Zero(t, sends)
	})

	for name, reply := range map[string]struct {
		status int
		body   string
	}{
		"send as denied": {http.StatusForbidden, `{"error":{"code":"ErrorSendAsDenied","message":"The user account which was used to submit this request does not have the right to send mail on behalf of the specified sending account."}}`},
		"other 403":      {http.StatusForbidden, `{"error":{"code":"ErrorAccessDenied","message":"Access is denied."}}`},
	} {
		t.Run(name, func(t *testing.T) {
			options := oauthEmailOptions(common.EmailOAuthProviderMicrosoft)
			options[common.EmailOAuthFromOption] = "shared@contoso.example"
			options[common.EmailOAuthScopesOption] = "Mail.Send Mail.Send.Shared User.Read"
			configureEmailDelivery(t, options)
			useEmailOptionsDatabase(t, options)
			provider := newFakeMailProvider(t)
			provider.sendStatus, provider.sendBody = reply.status, reply.body

			err := common.SendEmail("verification", "one@example.com", "123456")
			require.EqualError(t, err, "The authorized account has no Send As or Send on Behalf permission for shared@contoso.example")
			var message *common.Message
			require.ErrorAs(t, err, &message)
			assert.Equal(t, "shared@contoso.example", message.Params["sender"])
			_, sends := provider.counts()
			assert.Equal(t, 1, sends, "the message is not retried from the authorized mailbox")
		})
	}

	t.Run("invalid stored sender is refused", func(t *testing.T) {
		options := oauthEmailOptions(common.EmailOAuthProviderGoogle)
		options[common.EmailOAuthFromOption] = "a@example.com, b@example.com"
		configureEmailDelivery(t, options)
		useEmailOptionsDatabase(t, options)
		provider := newFakeMailProvider(t)

		require.ErrorIs(t, common.SendEmail("verification", "one@example.com", "123456"), common.ErrEmailOAuthSenderInvalid)
		_, sends := provider.counts()
		assert.Zero(t, sends)
	})
}
