package controller

import (
	"context"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// The provider redirects the browser back to the email settings page, which
// posts code and state to EmailOAuthCallback. The callback cannot be a
// server-side GET: session cookies are SameSite=Strict, so a cross-site
// redirect carries no session to bind the state to.
const emailOAuthCallbackPath = "/system-settings/operations/email"

// emailOAuthFlowPayload is server-side state of one authorization attempt.
// The OAuth app is snapshotted so a callback cannot complete against
// settings changed after the attempt started.
type emailOAuthFlowPayload struct {
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirect_uri"`
	ClientID    string `json:"client_id"`
	Tenant      string `json:"tenant"`
	// Scope is what the consent page asked for; the code is redeemed with it.
	Scope string `json:"scope"`
}

func emailOAuthRedirectURI() string {
	address := strings.TrimRight(strings.TrimSpace(system_setting.ServerAddress), "/")
	if address == "" {
		return ""
	}
	return address + emailOAuthCallbackPath
}

func recordEmailOAuthAudit(c *gin.Context, change string, provider string, success bool) {
	recordManageAudit(c, "option.update", map[string]any{
		"key": "EmailOAuthAuthorization", "change": change, "provider": provider, "success": success,
	})
}

// StartEmailOAuthAuthorization creates a one-time state bound to the
// administrator's browser session and returns the provider consent URL.
func StartEmailOAuthAuthorization(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("Sign in to the console in a browser to authorize the sender mailbox"))
		return
	}
	app := common.CurrentEmailOAuthApp()
	if err := common.ValidateEmailOAuthApp(app); err != nil {
		common.ApiError(c, err)
		return
	}
	redirectURI := emailOAuthRedirectURI()
	if redirectURI == "" {
		common.ApiErrorT(c, "Set the server address before authorizing the sender mailbox")
		return
	}
	verifier, challenge, err := common.EmailOAuthPKCE()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.OptionMapRWMutex.RLock()
	sender := common.OptionMap[common.EmailOAuthFromOption]
	common.OptionMapRWMutex.RUnlock()
	scope := common.EmailOAuthRequestedScope(app.Provider, sender)
	payload, err := common.Marshal(emailOAuthFlowPayload{Verifier: verifier, RedirectURI: redirectURI, ClientID: app.ClientID, Tenant: app.Tenant, Scope: scope})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	expiresAt := time.Now().Add(oauthAuthFlowTTL)
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeEmailOAuth,
		Provider:  app.Provider,
		UserId:    identity.UserID,
		SessionId: identity.SessionID,
		Payload:   string(payload),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	authorizationURL, err := common.EmailOAuthAuthorizeURL(app, redirectURI, state, challenge, scope)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"authorization_url": authorizationURL,
		"redirect_uri":      redirectURI,
		"expires_at":        expiresAt.Unix(),
	})
}

type emailOAuthCallbackRequest struct {
	State string `json:"state"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

// EmailOAuthCallback completes an authorization started by the same
// administrator session. The state is consumed before anything else, so it
// works once even when the provider reported an error or the exchange fails.
func EmailOAuthCallback(c *gin.Context) {
	var request emailOAuthCallbackRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("Sign in to the console in a browser to authorize the sender mailbox"))
		return
	}
	invalidState := common.NewMessage("The authorization request is invalid or has expired. Start the authorization again")
	state := strings.TrimSpace(request.State)
	if state == "" {
		common.ApiErrorStatus(c, http.StatusForbidden, invalidState)
		return
	}
	flow, err := model.ConsumeAuthFlow(state, model.AuthFlowMatch{
		Purpose:   model.AuthFlowPurposeEmailOAuth,
		UserId:    identity.UserID,
		SessionId: identity.SessionID,
	})
	if err != nil {
		common.ApiErrorStatus(c, http.StatusForbidden, invalidState)
		return
	}
	success := false
	defer func() { recordEmailOAuthAudit(c, "authorize", flow.Provider, success) }()
	if request.Error != "" {
		common.ApiErrorT(c, "The mailbox authorization was denied or cancelled")
		return
	}
	var payload emailOAuthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil || payload.Verifier == "" {
		common.ApiErrorStatus(c, http.StatusForbidden, invalidState)
		return
	}
	app := common.CurrentEmailOAuthApp()
	if app.Provider != flow.Provider || app.ClientID != payload.ClientID || app.Tenant != payload.Tenant {
		common.ApiErrorT(c, "The OAuth app settings changed during authorization. Start the authorization again")
		return
	}
	code := strings.TrimSpace(request.Code)
	if code == "" {
		common.ApiErrorT(c, "Mailbox authorization failed. Try again")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	scope := payload.Scope
	if scope == "" { // A flow started before the scope was recorded.
		scope = common.EmailOAuthRequestedScope(app.Provider, "")
	}
	grant, err := common.ExchangeEmailOAuthCode(ctx, app, code, payload.Verifier, payload.RedirectURI, scope)
	if err != nil {
		common.ApiErrorT(c, "Mailbox authorization failed. Try again")
		return
	}
	if err := model.SaveEmailOAuthAuthorization(grant); err != nil {
		common.SysError(common.LogText("failed to save the email oauth authorization: %v", err))
		common.ApiErrorT(c, "Mailbox authorization failed. Try again")
		return
	}
	success = true
	common.ApiSuccess(c, gin.H{"email": grant.Email, "provider": flow.Provider})
}

// SendEmailOAuthTestMessage sends a test message through the OAuth mailbox,
// from the configured sender address, to the authorized mailbox itself. It
// works before OAuth is the active sending method, so the sender permission
// can be checked first.
func SendEmailOAuthTestMessage(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	mailbox := strings.TrimSpace(common.OptionMap[common.EmailOAuthEmailOption])
	sender := strings.TrimSpace(common.OptionMap[common.EmailOAuthFromOption])
	common.OptionMapRWMutex.RUnlock()
	if mailbox == "" {
		common.ApiError(c, common.ErrEmailOAuthNotAuthorized)
		return
	}
	if sender == "" {
		sender = mailbox
	}
	subject := i18n.T(c, i18n.MsgEmailOAuthTestSubject, map[string]any{"SystemName": common.SystemName})
	content := i18n.T(c, i18n.MsgEmailOAuthTestBody, map[string]any{"Sender": sender, "Mailbox": mailbox})
	err := common.SendOAuthEmail(subject, mailbox, "<p>"+html.EscapeString(content)+"</p>")
	recordManageAudit(c, "option.update", map[string]any{
		"key": "EmailOAuthAuthorization", "change": "test", "provider": common.CurrentEmailOAuthApp().Provider, "success": err == nil,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"to": mailbox, "from": sender})
}

// RevokeEmailOAuthAuthorization forgets the stored refresh token and mailbox.
// The grant itself is removed in the provider's account settings.
func RevokeEmailOAuthAuthorization(c *gin.Context) {
	provider := common.CurrentEmailOAuthApp().Provider
	if err := model.ClearEmailOAuthAuthorization(); err != nil {
		recordEmailOAuthAudit(c, "revoke", provider, false)
		common.ApiError(c, err)
		return
	}
	recordEmailOAuthAudit(c, "revoke", provider, true)
	common.ApiSuccess(c, nil)
}
