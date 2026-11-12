package service

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// subscriptionOAuthResponseHeaders are the upstream response headers a
// subscription OAuth channel (Claude OAuth, Codex) passes to its clients. The
// upstream answers of these channels describe a whole subscription account
// that many users may share, so anything outside this list stays on the
// gateway. Lower-case names.
var subscriptionOAuthResponseHeaders = map[string]struct{}{
	"content-type":     {},
	"content-encoding": {},
	"content-language": {},
	"cache-control":    {},
	"etag":             {},
	"last-modified":    {},
	"expires":          {},
	"vary":             {},
	"date":             {},
	"location":         {},
	"www-authenticate": {},
	"request-id":       {},
	"x-request-id":     {},
	// Codex clients read these to avoid counting reasoning tokens twice and to
	// keep the turn state of a conversation.
	"x-reasoning-included": {},
	"x-codex-turn-state":   {},
}

// isSubscriptionOAuthChannel reports whether the channel serving the request
// is a subscription OAuth channel, whose upstream answers carry the usage
// limits and identity of the shared account.
func isSubscriptionOAuthChannel(c *gin.Context) bool {
	switch common.GetContextKeyInt(c, constant.ContextKeyChannelType) {
	case constant.ChannelTypeClaudeOAuth, constant.ChannelTypeCodex:
		return true
	}
	return false
}

// isSubscriptionLimitHeaderForClient reports whether a usage-limit header
// (lower-case name) of the bound account may be shown to the user the account
// is bound to.
func isSubscriptionLimitHeaderForClient(name string) bool {
	if name == "retry-after" || strings.HasPrefix(name, "anthropic-ratelimit-unified-") {
		return true
	}
	rest, ok := strings.CutPrefix(name, "x-codex-")
	if !ok {
		return false
	}
	return strings.HasPrefix(rest, "primary-") ||
		strings.HasPrefix(rest, "secondary-") ||
		strings.HasPrefix(rest, "credits-") ||
		strings.HasSuffix(rest, "-limit-name") ||
		rest == "rate-limit-reached-type"
}

// WriteSubscriptionLimitHeaders gives the user a subscription binding serves
// the usage-limit headers of the bound account, as RecordSubscriptionLimitHeaders
// kept them. They are written only while the request is still served by the
// binding on the bound channel and that channel holds a single account, which
// is the same scope the bound user's own usage view has. It must run before
// the response status is written.
func WriteSubscriptionLimitHeaders(c *gin.Context) {
	if c == nil || c.Writer == nil || !operation_setting.SubscriptionRateLimitHeaderPassthrough {
		return
	}
	route, _ := common.GetContextKeyType[*relaycommon.SubscriptionRoute](c, constant.ContextKeySubscriptionRoute)
	if !route.Active() || common.GetContextKeyInt(c, constant.ContextKeyChannelId) != route.ChannelId {
		return
	}
	if common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) || !isSubscriptionOAuthChannel(c) {
		return
	}
	headers, _ := common.GetContextKeyType[http.Header](c, constant.ContextKeyUpstreamRateLimitHeaders)
	for name, values := range headers {
		if !isSubscriptionLimitHeaderForClient(strings.ToLower(name)) {
			continue
		}
		c.Writer.Header().Del(name)
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
}
