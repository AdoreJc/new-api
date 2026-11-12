package common

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OAuth sender mailbox ("EmailProvider" = "oauth"). One mailbox is authorized
// by an administrator through the authorization code flow with PKCE; mail is
// then sent through the provider's HTTP API with a short-lived access token:
//   - Google: Gmail API users.messages.send, scope gmail.send.
//   - Microsoft: Graph POST /me/sendMail, scopes Mail.Send and offline_access.
//
// The HTTP APIs are used instead of SMTP XOAUTH2 because many Microsoft 365
// tenants disable SMTP AUTH, and gmail.send is narrower than the
// https://mail.google.com/ scope SMTP needs.
const (
	EmailOAuthProviderGoogle    = "google"
	EmailOAuthProviderMicrosoft = "microsoft"
	EmailOAuthDefaultTenant     = "common"

	EmailOAuthProviderOption     = "EmailOAuthProvider"
	EmailOAuthClientIdOption     = "EmailOAuthClientId"
	EmailOAuthClientSecretOption = "EmailOAuthClientSecret"
	EmailOAuthTenantOption       = "EmailOAuthTenant"
	EmailOAuthRefreshTokenOption = "EmailOAuthRefreshToken"
	EmailOAuthEmailOption        = "EmailOAuthEmail"
	// EmailOAuthSecretConfiguredOption is derived for the console only.
	EmailOAuthSecretConfiguredOption = "EmailOAuthSecretConfigured"
)

// EmailOAuthEndpoints holds every provider URL so tests can point them at a
// local server. Production code never changes them.
type EmailOAuthEndpoints struct {
	GoogleAuthorizeURL string
	GoogleTokenURL     string
	GoogleUserInfoURL  string
	GmailSendURL       string
	MicrosoftLoginBase string // {base}/{tenant}/oauth2/v2.0/{authorize|token}
	MicrosoftGraphBase string
}

var DefaultEmailOAuthEndpoints = EmailOAuthEndpoints{
	GoogleAuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
	GoogleTokenURL:     "https://oauth2.googleapis.com/token",
	GoogleUserInfoURL:  "https://openidconnect.googleapis.com/v1/userinfo",
	GmailSendURL:       "https://gmail.googleapis.com/gmail/v1/users/me/messages/send",
	MicrosoftLoginBase: "https://login.microsoftonline.com",
	MicrosoftGraphBase: "https://graph.microsoft.com/v1.0",
}

var emailOAuthEndpoints = DefaultEmailOAuthEndpoints

// SetEmailOAuthEndpointsForTest swaps the provider URLs and returns a restore
// function. It also drops the cached access token.
func SetEmailOAuthEndpointsForTest(endpoints EmailOAuthEndpoints) func() {
	emailOAuthState.Lock()
	previous := emailOAuthEndpoints
	emailOAuthEndpoints = endpoints
	emailOAuthState.cached = emailOAuthAccessToken{}
	emailOAuthState.pending = emailOAuthRotation{}
	emailOAuthState.Unlock()
	return func() {
		emailOAuthState.Lock()
		emailOAuthEndpoints = previous
		emailOAuthState.cached = emailOAuthAccessToken{}
		emailOAuthState.pending = emailOAuthRotation{}
		emailOAuthState.Unlock()
	}
}

const (
	emailOAuthGoogleScope    = "openid email https://www.googleapis.com/auth/gmail.send"
	emailOAuthMicrosoftScope = "offline_access https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/User.Read"
)

var emailOAuthTenantPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,63}$`)

var emailOAuthHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var (
	ErrEmailOAuthNotAuthorized = NewMessage("The sender mailbox is not authorized")
	ErrEmailOAuthReauthorize   = NewMessage("The sender mailbox authorization has expired or was revoked. Re-authorize it in the email settings")
	ErrEmailOAuthAppInvalid    = NewMessage("The OAuth app settings are incomplete or invalid")
	ErrEmailOAuthRequestFailed = NewMessage("OAuth email request failed")
	// ErrEmailOAuthAuthorizationChanged is returned by the rotation store when
	// the stored refresh token is no longer the one that was refreshed, for
	// example after a re-authorization. The rotated token is then discarded.
	ErrEmailOAuthAuthorizationChanged = errors.New("email oauth authorization changed")
)

// EmailOAuthRefreshTokenStore persists a rotated refresh token only when the
// stored token still equals previous. The model package installs it.
var EmailOAuthRefreshTokenStore func(previous, next string) error

// EmailOAuthRotationRetryDelays bounds the time spent saving a rotated token
// before it is kept in memory and saved again on the next send.
var EmailOAuthRotationRetryDelays = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, time.Second, 2 * time.Second}

// EmailOAuthApp is the administrator's OAuth client registration.
type EmailOAuthApp struct {
	Provider     string
	ClientID     string
	ClientSecret string
	Tenant       string
}

type emailOAuthConfig struct {
	EmailOAuthApp
	RefreshToken string
	Email        string
}

// CurrentEmailOAuthApp reads the OAuth app settings from the option map.
func CurrentEmailOAuthApp() EmailOAuthApp {
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	return emailOAuthAppFromMap(OptionMap)
}

func emailOAuthAppFromMap(options map[string]string) EmailOAuthApp {
	tenant := strings.TrimSpace(options[EmailOAuthTenantOption])
	if tenant == "" {
		tenant = EmailOAuthDefaultTenant
	}
	return EmailOAuthApp{
		Provider:     strings.TrimSpace(options[EmailOAuthProviderOption]),
		ClientID:     strings.TrimSpace(options[EmailOAuthClientIdOption]),
		ClientSecret: strings.TrimSpace(options[EmailOAuthClientSecretOption]),
		Tenant:       tenant,
	}
}

// ValidateEmailOAuthApp checks local settings only; it never contacts the provider.
func ValidateEmailOAuthApp(app EmailOAuthApp) error {
	if app.Provider != EmailOAuthProviderGoogle && app.Provider != EmailOAuthProviderMicrosoft {
		return ErrEmailOAuthAppInvalid
	}
	if app.ClientID == "" || len(app.ClientID) > 256 || strings.ContainsAny(app.ClientID, "\r\n\x00 ") {
		return ErrEmailOAuthAppInvalid
	}
	if app.ClientSecret == "" || len(app.ClientSecret) > 1024 || strings.ContainsAny(app.ClientSecret, "\r\n\x00") {
		return ErrEmailOAuthAppInvalid
	}
	if app.Provider == EmailOAuthProviderMicrosoft && !emailOAuthTenantPattern.MatchString(app.Tenant) {
		return ErrEmailOAuthAppInvalid
	}
	return nil
}

// ValidateEmailOAuthActivation is the rule for selecting OAuth as the sending
// method: the app is complete and a mailbox is authorized.
func ValidateEmailOAuthActivation(options map[string]string) error {
	if err := ValidateEmailOAuthApp(emailOAuthAppFromMap(options)); err != nil {
		return err
	}
	if strings.TrimSpace(options[EmailOAuthRefreshTokenOption]) == "" || strings.TrimSpace(options[EmailOAuthEmailOption]) == "" {
		return NewMessage("Authorize the sender mailbox before enabling OAuth sending")
	}
	return nil
}

// EmailOAuthPKCE is a fresh code verifier and its S256 challenge.
func EmailOAuthPKCE() (verifier string, challenge string, err error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func microsoftEndpoint(tenant, name string) string {
	return strings.TrimRight(emailOAuthEndpoints.MicrosoftLoginBase, "/") + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/" + name
}

func emailOAuthTokenURL(app EmailOAuthApp) string {
	if app.Provider == EmailOAuthProviderMicrosoft {
		return microsoftEndpoint(app.Tenant, "token")
	}
	return emailOAuthEndpoints.GoogleTokenURL
}

// EmailOAuthAuthorizeURL builds the provider consent URL for the code flow.
func EmailOAuthAuthorizeURL(app EmailOAuthApp, redirectURI, state, challenge string) (string, error) {
	if err := ValidateEmailOAuthApp(app); err != nil {
		return "", err
	}
	query := url.Values{
		"client_id":             {app.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	base := emailOAuthEndpoints.GoogleAuthorizeURL
	if app.Provider == EmailOAuthProviderMicrosoft {
		base = microsoftEndpoint(app.Tenant, "authorize")
		query.Set("scope", emailOAuthMicrosoftScope)
		query.Set("response_mode", "query")
		query.Set("prompt", "select_account")
	} else {
		query.Set("scope", emailOAuthGoogleScope)
		// A refresh token is only issued for offline access, and Google
		// repeats it on later grants only when consent is shown again.
		query.Set("access_type", "offline")
		query.Set("prompt", "consent")
		query.Set("include_granted_scopes", "false")
	}
	return base + "?" + query.Encode(), nil
}

type emailOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

// postEmailOAuthToken calls the token endpoint. Response bodies are never
// logged: they carry tokens on success and may echo request data on failure.
func postEmailOAuthToken(ctx context.Context, app EmailOAuthApp, form url.Values) (*emailOAuthTokenResponse, error) {
	form.Set("client_id", app.ClientID)
	form.Set("client_secret", app.ClientSecret)
	if app.Provider == EmailOAuthProviderMicrosoft {
		form.Set("scope", emailOAuthMicrosoftScope)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, emailOAuthTokenURL(app), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ErrEmailOAuthRequestFailed
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := emailOAuthHTTPClient.Do(request)
	if err != nil {
		SysError(LogText("email oauth token request failed: provider=%s", app.Provider))
		return nil, ErrEmailOAuthRequestFailed
	}
	defer response.Body.Close()
	var token emailOAuthTokenResponse
	decodeErr := DecodeJson(io.LimitReader(response.Body, 64*1024), &token)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if token.Error == "invalid_grant" {
			return nil, ErrEmailOAuthReauthorize
		}
		SysError(LogText("email oauth token endpoint rejected the request: provider=%s status=%d error=%q", app.Provider, response.StatusCode, token.Error))
		return nil, ErrEmailOAuthRequestFailed
	}
	if decodeErr != nil || strings.TrimSpace(token.AccessToken) == "" {
		SysError(LogText("email oauth token endpoint returned an invalid response: provider=%s", app.Provider))
		return nil, ErrEmailOAuthRequestFailed
	}
	return &token, nil
}

// ExchangeEmailOAuthCode redeems an authorization code and returns the
// refresh token and the authorized mailbox address.
func ExchangeEmailOAuthCode(ctx context.Context, app EmailOAuthApp, code, verifier, redirectURI string) (refreshToken string, email string, err error) {
	if err := ValidateEmailOAuthApp(app); err != nil {
		return "", "", err
	}
	token, err := postEmailOAuthToken(ctx, app, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		SysError(LogText("email oauth authorization returned no refresh token: provider=%s", app.Provider))
		return "", "", ErrEmailOAuthRequestFailed
	}
	email, err = fetchEmailOAuthMailbox(ctx, app.Provider, token.AccessToken)
	if err != nil {
		return "", "", err
	}
	return token.RefreshToken, email, nil
}

func fetchEmailOAuthMailbox(ctx context.Context, provider, accessToken string) (string, error) {
	endpoint := emailOAuthEndpoints.GoogleUserInfoURL
	if provider == EmailOAuthProviderMicrosoft {
		endpoint = strings.TrimRight(emailOAuthEndpoints.MicrosoftGraphBase, "/") + "/me?$select=mail,userPrincipalName"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", ErrEmailOAuthRequestFailed
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := emailOAuthHTTPClient.Do(request)
	if err != nil {
		SysError(LogText("email oauth mailbox lookup failed: provider=%s", provider))
		return "", ErrEmailOAuthRequestFailed
	}
	defer response.Body.Close()
	var profile struct {
		Email             string `json:"email"`
		EmailVerified     *bool  `json:"email_verified"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if response.StatusCode != http.StatusOK || DecodeJson(io.LimitReader(response.Body, 64*1024), &profile) != nil {
		SysError(LogText("email oauth mailbox lookup was rejected: provider=%s status=%d", provider, response.StatusCode))
		return "", ErrEmailOAuthRequestFailed
	}
	address := profile.Email
	if provider == EmailOAuthProviderMicrosoft {
		address = profile.Mail
		if address == "" {
			address = profile.UserPrincipalName
		}
	} else if profile.EmailVerified != nil && !*profile.EmailVerified {
		address = ""
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(address))
	if err != nil || parsed.Name != "" {
		SysError(LogText("email oauth mailbox lookup returned no usable address: provider=%s", provider))
		return "", ErrEmailOAuthRequestFailed
	}
	return parsed.Address, nil
}

type emailOAuthAccessToken struct {
	key       string
	token     string
	expiresAt time.Time
}

// emailOAuthRotation keeps a rotated refresh token that could not be saved
// yet. While the stored token still equals previous, current is used instead.
type emailOAuthRotation struct {
	previous string
	current  string
}

var emailOAuthState struct {
	sync.Mutex
	cached  emailOAuthAccessToken
	pending emailOAuthRotation
}

func emailOAuthCacheKey(config emailOAuthConfig) string {
	sum := sha256.Sum256([]byte(config.Provider + "\x00" + config.ClientID + "\x00" + config.Tenant + "\x00" + config.RefreshToken))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ResetEmailOAuthTokenCache drops the cached access token and any unsaved
// rotation, for example after the authorization is replaced or removed.
func ResetEmailOAuthTokenCache() {
	emailOAuthState.Lock()
	emailOAuthState.cached = emailOAuthAccessToken{}
	emailOAuthState.pending = emailOAuthRotation{}
	emailOAuthState.Unlock()
}

// emailOAuthAccessTokenFor returns a cached access token or refreshes one.
// Refreshes are serialized so a rotating refresh token is used only once.
func emailOAuthAccessTokenFor(ctx context.Context, config emailOAuthConfig) (string, error) {
	emailOAuthState.Lock()
	defer emailOAuthState.Unlock()

	stored := config.RefreshToken
	pending := emailOAuthState.pending
	if pending.current != "" {
		if pending.previous != stored {
			// The authorization was replaced or removed meanwhile.
			emailOAuthState.pending = emailOAuthRotation{}
		} else {
			config.RefreshToken = pending.current
			if persistEmailOAuthRefreshToken(stored, pending.current, nil) {
				emailOAuthState.pending = emailOAuthRotation{}
				stored = pending.current
			}
		}
	}

	key := emailOAuthCacheKey(config)
	if emailOAuthState.cached.key == key && time.Now().Before(emailOAuthState.cached.expiresAt) {
		return emailOAuthState.cached.token, nil
	}
	token, err := postEmailOAuthToken(ctx, config.EmailOAuthApp, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {config.RefreshToken},
	})
	if err != nil {
		emailOAuthState.cached = emailOAuthAccessToken{}
		return "", err
	}
	if token.RefreshToken != "" && token.RefreshToken != config.RefreshToken {
		// The previous refresh token may already be invalid, so the new one
		// must survive a failed write: retry, then keep it in memory.
		if persistEmailOAuthRefreshToken(stored, token.RefreshToken, EmailOAuthRotationRetryDelays) {
			emailOAuthState.pending = emailOAuthRotation{}
		} else {
			emailOAuthState.pending = emailOAuthRotation{previous: stored, current: token.RefreshToken}
		}
		config.RefreshToken = token.RefreshToken
	}
	lifetime := time.Duration(max(token.ExpiresIn, 0)) * time.Second
	expiresAt := time.Now().Add(lifetime - time.Minute)
	emailOAuthState.cached = emailOAuthAccessToken{key: emailOAuthCacheKey(config), token: token.AccessToken, expiresAt: expiresAt}
	return token.AccessToken, nil
}

// persistEmailOAuthRefreshToken reports whether nothing remains to be saved:
// the write succeeded, or the stored authorization changed and the rotated
// token belongs to a grant that is no longer used.
func persistEmailOAuthRefreshToken(previous, next string, retryDelays []time.Duration) bool {
	if EmailOAuthRefreshTokenStore == nil {
		SysError(LogText("email oauth refresh token rotation cannot be saved: no store"))
		return false
	}
	for attempt := 0; ; attempt++ {
		err := EmailOAuthRefreshTokenStore(previous, next)
		if err == nil || errors.Is(err, ErrEmailOAuthAuthorizationChanged) {
			return true
		}
		if attempt >= len(retryDelays) {
			SysError(LogText("failed to save the rotated email oauth refresh token; it is kept in memory and saved on the next send: %v", err))
			return false
		}
		time.Sleep(retryDelays[attempt])
	}
}

func readEmailOAuthConfig() emailOAuthConfig {
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	return emailOAuthConfig{
		EmailOAuthApp: emailOAuthAppFromMap(OptionMap),
		RefreshToken:  strings.TrimSpace(OptionMap[EmailOAuthRefreshTokenOption]),
		Email:         strings.TrimSpace(OptionMap[EmailOAuthEmailOption]),
	}
}

func parseEmailRecipients(receiver string) ([]string, error) {
	to := make([]string, 0)
	for address := range strings.SplitSeq(receiver, ";") {
		recipient, err := mail.ParseAddress(strings.TrimSpace(address))
		if err != nil || len(to) >= 50 {
			return nil, NewMessage("Invalid email address")
		}
		to = append(to, recipient.Address)
	}
	return to, nil
}

func sendOAuthEmail(subject, receiver, content string) error {
	config := readEmailOAuthConfig()
	if err := ValidateEmailOAuthApp(config.EmailOAuthApp); err != nil {
		return err
	}
	if config.RefreshToken == "" || config.Email == "" {
		return ErrEmailOAuthNotAuthorized
	}
	to, err := parseEmailRecipients(receiver)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	accessToken, err := emailOAuthAccessTokenFor(ctx, config)
	if err != nil {
		return err
	}
	sender := (&mail.Address{Name: SystemName, Address: config.Email}).String()
	var endpoint string
	var payload []byte
	if config.Provider == EmailOAuthProviderMicrosoft {
		endpoint = strings.TrimRight(emailOAuthEndpoints.MicrosoftGraphBase, "/") + "/me/sendMail"
		type graphAddress struct {
			EmailAddress struct {
				Name    string `json:"name,omitempty"`
				Address string `json:"address"`
			} `json:"emailAddress"`
		}
		recipients := make([]graphAddress, len(to))
		for i, address := range to {
			recipients[i].EmailAddress.Address = address
		}
		var from graphAddress
		from.EmailAddress.Name = SystemName
		from.EmailAddress.Address = config.Email
		var message struct {
			Message struct {
				Subject string `json:"subject"`
				Body    struct {
					ContentType string `json:"contentType"`
					Content     string `json:"content"`
				} `json:"body"`
				From         graphAddress   `json:"from"`
				ToRecipients []graphAddress `json:"toRecipients"`
			} `json:"message"`
			SaveToSentItems bool `json:"saveToSentItems"`
		}
		message.Message.Subject = subject
		message.Message.Body.ContentType = "HTML"
		message.Message.Body.Content = content
		message.Message.From = from
		message.Message.ToRecipients = recipients
		payload, err = Marshal(message)
	} else {
		endpoint = emailOAuthEndpoints.GmailSendURL
		raw := buildEmailOAuthMIME(sender, config.Email, to, subject, content)
		payload, err = Marshal(map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)})
	}
	if err != nil {
		return ErrEmailOAuthRequestFailed
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ErrEmailOAuthRequestFailed
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := emailOAuthHTTPClient.Do(request)
	if err != nil {
		SysError(LogText("oauth email request failed: provider=%s", config.Provider))
		return ErrEmailOAuthRequestFailed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code    any    `json:"code"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = DecodeJson(io.LimitReader(response.Body, 4096), &failure)
		SysError(LogText("oauth email was rejected: provider=%s status=%d code=%v reason=%q", config.Provider, response.StatusCode, failure.Error.Code, failure.Error.Status))
		if response.StatusCode == http.StatusUnauthorized {
			// The cached access token was revoked early; the next send refreshes.
			emailOAuthState.Lock()
			emailOAuthState.cached = emailOAuthAccessToken{}
			emailOAuthState.Unlock()
		}
		return NewMessage("OAuth email delivery failed (HTTP {{status}})", map[string]any{"status": response.StatusCode})
	}
	return nil
}

// buildEmailOAuthMIME renders an RFC 5322 HTML message for the Gmail API.
func buildEmailOAuthMIME(sender, senderAddress string, to []string, subject, content string) []byte {
	domain := "localhost"
	if _, after, ok := strings.Cut(senderAddress, "@"); ok {
		domain = after
	}
	var builder strings.Builder
	builder.WriteString("From: " + sender + "\r\n")
	builder.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	builder.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	builder.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	builder.WriteString(fmt.Sprintf("Message-ID: <%d.%s@%s>\r\n", time.Now().UnixNano(), GetRandomString(12), domain))
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	builder.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	for len(encoded) > 76 {
		builder.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	builder.WriteString(encoded + "\r\n")
	return []byte(builder.String())
}
