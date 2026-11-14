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
//
// An optional sender address (EmailOAuthFrom) sends from another mailbox:
//   - Google: the MIME From header. It must be a verified "Send mail as"
//     alias of the authorized account; listing aliases would need a
//     restricted Gmail scope, so the address is entered by hand.
//   - Microsoft: message.from on POST /me/sendMail, which needs the delegated
//     Mail.Send.Shared scope (work or school accounts only) and Send As or
//     Send on Behalf rights on that mailbox. /users/{from}/sendMail is not
//     used because it also needs Full Access to the mailbox.
//
// Mail.Send.Shared is requested only for a sender address other than the
// authorized mailbox, so existing grants and personal accounts keep the
// original scopes. While OAuth sends, a sender the grant cannot use is
// refused when it is saved; the console authorizes first, then saves.
const (
	EmailOAuthProviderGoogle    = "google"
	EmailOAuthProviderMicrosoft = "microsoft"
	EmailOAuthDefaultTenant     = "common"

	EmailOAuthProviderOption     = "EmailOAuthProvider"
	EmailOAuthClientIdOption     = "EmailOAuthClientId"
	EmailOAuthClientSecretOption = "EmailOAuthClientSecret"
	EmailOAuthTenantOption       = "EmailOAuthTenant"
	EmailOAuthFromOption         = "EmailOAuthFrom"
	EmailOAuthRefreshTokenOption = "EmailOAuthRefreshToken"
	EmailOAuthEmailOption        = "EmailOAuthEmail"
	// EmailOAuthScopesOption holds the scopes the stored grant received. It
	// is written by the authorization flow together with the refresh token.
	EmailOAuthScopesOption = "EmailOAuthScopes"
	// EmailOAuthSecretConfiguredOption and EmailOAuthSharedSendGrantedOption
	// are derived for the console only.
	EmailOAuthSecretConfiguredOption  = "EmailOAuthSecretConfigured"
	EmailOAuthSharedSendGrantedOption = "EmailOAuthSharedSendGranted"
	// EmailOAuthRedirectURIOption shows the console the redirect URI the
	// authorization flow sends, so the one registered with the provider is
	// the one in use whichever website address the console was opened on.
	EmailOAuthRedirectURIOption = "EmailOAuthRedirectURI"
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
	emailOAuthGoogleScope          = "openid email https://www.googleapis.com/auth/gmail.send"
	emailOAuthMicrosoftScope       = "offline_access https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/User.Read"
	emailOAuthMicrosoftSharedScope = emailOAuthMicrosoftScope + " https://graph.microsoft.com/Mail.Send.Shared"
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
	ErrEmailOAuthSenderInvalid = NewMessage("Enter a single sender email address, or leave it blank to send from the authorized mailbox")
	// ErrEmailOAuthSharedReauthorize is returned instead of sending from the
	// authorized mailbox when the grant cannot send from the shared one.
	ErrEmailOAuthSharedReauthorize = NewMessage("Re-authorize the mailbox to send from the shared mailbox")
	// ErrEmailOAuthSenderNeedsGrant refuses to save such a sender address.
	ErrEmailOAuthSenderNeedsGrant = NewMessage("Re-authorize the mailbox with this sender address entered before saving it")
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
	From         string
	Scopes       string
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
	if err := ValidateEmailOAuthSender(options[EmailOAuthFromOption]); err != nil {
		return err
	}
	if strings.TrimSpace(options[EmailOAuthRefreshTokenOption]) == "" || strings.TrimSpace(options[EmailOAuthEmailOption]) == "" {
		return NewMessage("Authorize the sender mailbox before enabling OAuth sending")
	}
	return ValidateEmailOAuthSenderGrant(
		strings.TrimSpace(options[EmailOAuthProviderOption]),
		options[EmailOAuthFromOption],
		options[EmailOAuthEmailOption],
		options[EmailOAuthScopesOption],
	)
}

// emailOAuthSenderNeedsShared reports whether sending from sender through
// mailbox needs Mail.Send.Shared. With no mailbox authorized yet, any
// sender may turn out to be another mailbox.
func emailOAuthSenderNeedsShared(provider, sender, mailbox string) bool {
	sender, mailbox = strings.TrimSpace(sender), strings.TrimSpace(mailbox)
	return provider == EmailOAuthProviderMicrosoft && sender != "" && (mailbox == "" || !strings.EqualFold(sender, mailbox))
}

// ValidateEmailOAuthSenderGrant refuses a sender address the authorized
// grant cannot send from, so an active OAuth method keeps sending.
func ValidateEmailOAuthSenderGrant(provider, sender, mailbox, scopes string) error {
	if strings.TrimSpace(mailbox) == "" || !emailOAuthSenderNeedsShared(provider, sender, mailbox) || EmailOAuthSharedSendGranted(scopes) {
		return nil
	}
	return ErrEmailOAuthSenderNeedsGrant
}

// ValidateEmailOAuthSender accepts an empty value (send from the authorized
// mailbox) or exactly one bare address, which ends up in a From header.
func ValidateEmailOAuthSender(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 254 || strings.ContainsAny(value, "\r\n\x00") {
		return ErrEmailOAuthSenderInvalid
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return ErrEmailOAuthSenderInvalid
	}
	return nil
}

// EmailOAuthRequestedScope is the scope a new authorization asks for. The
// shared-mailbox scope is added only when one of senders (the saved and the
// pending address) is not the currently authorized mailbox.
func EmailOAuthRequestedScope(provider, mailbox string, senders ...string) string {
	if provider != EmailOAuthProviderMicrosoft {
		return emailOAuthGoogleScope
	}
	for _, sender := range senders {
		if emailOAuthSenderNeedsShared(provider, sender, mailbox) {
			return emailOAuthMicrosoftSharedScope
		}
	}
	return emailOAuthMicrosoftScope
}

// EmailOAuthSharedSendGranted reports whether a Microsoft grant's scopes
// include Mail.Send.Shared. Token responses may list a scope as a full or
// short, URL-encoded or plain name in any case.
func EmailOAuthSharedSendGranted(scopes string) bool {
	if decoded, err := url.QueryUnescape(scopes); err == nil {
		scopes = decoded
	}
	for scope := range strings.FieldsSeq(strings.ToLower(scopes)) {
		if strings.TrimPrefix(scope, "https://graph.microsoft.com/") == "mail.send.shared" {
			return true
		}
	}
	return false
}

// emailOAuthRefreshScope asks a Microsoft refresh for the scopes the grant
// received: a refresh may not ask for more than the original authorization.
func emailOAuthRefreshScope(scopes string) string {
	if EmailOAuthSharedSendGranted(scopes) {
		return emailOAuthMicrosoftSharedScope
	}
	return emailOAuthMicrosoftScope
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

// EmailOAuthAuthorizeURL builds the provider consent URL for the code flow;
// scope comes from EmailOAuthRequestedScope.
func EmailOAuthAuthorizeURL(app EmailOAuthApp, redirectURI, state, challenge, scope string) (string, error) {
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
		"scope":                 {scope},
	}
	base := emailOAuthEndpoints.GoogleAuthorizeURL
	if app.Provider == EmailOAuthProviderMicrosoft {
		base = microsoftEndpoint(app.Tenant, "authorize")
		query.Set("response_mode", "query")
		query.Set("prompt", "select_account")
	} else {
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
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// postEmailOAuthToken calls the token endpoint. Response bodies are never
// logged: they carry tokens on success and may echo request data on failure.
// Microsoft requests carry a scope; Google ones never do.
func postEmailOAuthToken(ctx context.Context, app EmailOAuthApp, form url.Values, microsoftScope string) (*emailOAuthTokenResponse, error) {
	form.Set("client_id", app.ClientID)
	form.Set("client_secret", app.ClientSecret)
	if app.Provider == EmailOAuthProviderMicrosoft {
		form.Set("scope", microsoftScope)
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

// EmailOAuthGrant is the result of a completed authorization.
type EmailOAuthGrant struct {
	RefreshToken string
	Email        string
	// Scopes the access token is valid for; when the provider omits them,
	// the token is for the requested scopes.
	Scopes string
}

// ExchangeEmailOAuthCode redeems an authorization code requested with scope
// and returns the grant with the authorized mailbox address.
func ExchangeEmailOAuthCode(ctx context.Context, app EmailOAuthApp, code, verifier, redirectURI, scope string) (EmailOAuthGrant, error) {
	if err := ValidateEmailOAuthApp(app); err != nil {
		return EmailOAuthGrant{}, err
	}
	token, err := postEmailOAuthToken(ctx, app, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}, scope)
	if err != nil {
		return EmailOAuthGrant{}, err
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		SysError(LogText("email oauth authorization returned no refresh token: provider=%s", app.Provider))
		return EmailOAuthGrant{}, ErrEmailOAuthRequestFailed
	}
	email, err := fetchEmailOAuthMailbox(ctx, app.Provider, token.AccessToken)
	if err != nil {
		return EmailOAuthGrant{}, err
	}
	scopes := strings.TrimSpace(token.Scope)
	if scopes == "" {
		scopes = scope
	}
	return EmailOAuthGrant{RefreshToken: token.RefreshToken, Email: email, Scopes: scopes}, nil
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
	}, emailOAuthRefreshScope(config.Scopes))
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
		From:          strings.TrimSpace(OptionMap[EmailOAuthFromOption]),
		Scopes:        strings.TrimSpace(OptionMap[EmailOAuthScopesOption]),
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

// SendOAuthEmail sends through the authorized mailbox, from the configured
// sender address when one is set. It never falls back to the authorized
// mailbox when the sender address cannot be used.
func SendOAuthEmail(subject, receiver, content string) error {
	config := readEmailOAuthConfig()
	if err := ValidateEmailOAuthApp(config.EmailOAuthApp); err != nil {
		return err
	}
	if config.RefreshToken == "" || config.Email == "" {
		return ErrEmailOAuthNotAuthorized
	}
	if err := ValidateEmailOAuthSender(config.From); err != nil {
		return err
	}
	fromAddress := config.Email
	if config.From != "" {
		fromAddress = config.From
	}
	sharedMailbox := !strings.EqualFold(fromAddress, config.Email)
	if ValidateEmailOAuthSenderGrant(config.Provider, config.From, config.Email, config.Scopes) != nil {
		return ErrEmailOAuthSharedReauthorize
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
	sender := (&mail.Address{Name: SystemName, Address: fromAddress}).String()
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
		// A shared mailbox goes in from; Graph sets sender itself.
		var from graphAddress
		from.EmailAddress.Name = SystemName
		from.EmailAddress.Address = fromAddress
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
		// Gmail sends from a verified "Send mail as" alias of the account.
		raw := buildEmailOAuthMIME(sender, fromAddress, to, subject, content)
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
		// Graph answers 403 ErrorSendAsDenied when the signed-in user has
		// neither Send As nor Send on Behalf on the from mailbox. Other
		// refusals of a shared-mailbox send only name the address.
		if config.Provider == EmailOAuthProviderMicrosoft && fmt.Sprint(failure.Error.Code) == "ErrorSendAsDenied" {
			return NewMessage("The authorized account has no Send As or Send on Behalf permission for {{sender}}", map[string]any{"sender": fromAddress})
		}
		if sharedMailbox && response.StatusCode == http.StatusForbidden {
			return NewMessage("Sending from {{sender}} was refused (HTTP {{status}})", map[string]any{"sender": fromAddress, "status": response.StatusCode})
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
	// Re-quote each address: a parsed quoted local-part such as "a,b"@x is
	// returned unquoted, and Gmail parses this header to find recipients.
	recipients := make([]string, len(to))
	for i, address := range to {
		recipients[i] = (&mail.Address{Address: address}).String()
	}
	builder.WriteString("To: " + strings.Join(recipients, ", ") + "\r\n")
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
