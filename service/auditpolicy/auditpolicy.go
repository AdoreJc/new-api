// Package auditpolicy applies the audit policy rule table to relay requests.
// It is the only place the relay path calls prompt audit and content
// moderation: it resolves the request's rule once, starts background checks,
// runs the synchronous checks of both engines in parallel, and merges their
// verdicts. A content moderation block wins over everything else, then a
// prompt audit block, then an engine failure under a fail-closed rule, then a
// reroute.
package auditpolicy

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/contentmoderation"
	"github.com/QuantumNous/new-api/service/promptaudit"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/gin-gonic/gin"
)

// Error codes returned when prompt audit rejects a request.
const (
	ErrorCodePromptAuditBlocked     = types.ErrorCode("prompt_audit_blocked")
	ErrorCodePromptAuditUnavailable = types.ErrorCode("prompt_audit_unavailable")
)

// FormatResponsesWebSocket is the relay format rules use for Responses API
// turns over WebSocket.
const FormatResponsesWebSocket = "responses_websocket"

// evaluatedKey holds the result for the gin context, so a request is
// checked once whatever calls Evaluate again (retries never do).
const evaluatedKey = "audit_policy_evaluated"

// Reroute is where a request is sent instead of its own model or group.
type Reroute struct {
	RuleID string
	Model  string
	Group  string
}

// Result is what the relay path does next: stop with Err, switch to
// Reroute, or go on when both are nil.
type Result struct {
	Err     *types.NewAPIError
	Reroute *Reroute
}

type evaluated struct {
	key    string
	result Result
}

// Plan is the resolved rule for a subject and what each engine will do. The
// test endpoint of the admin page shows it without running a check.
type Plan struct {
	RuleID    string `json:"rule_id"`
	RuleName  string `json:"rule_name"`
	RuleIndex int    `json:"rule_index"`
	Saved     bool   `json:"saved"`
	// Allowlisted, Verified and Blocklisted are the subject's list places.
	Allowlisted bool `json:"allowlisted"`
	Verified    bool `json:"verified"`
	Blocklisted bool `json:"blocklisted"`
	// PromptAudit and ContentModeration are off, async (background, never
	// blocks) or sync (before billing).
	PromptAudit       string                              `json:"prompt_audit"`
	PromptAuditAction string                              `json:"prompt_audit_action"`
	PromptAuditFail   string                              `json:"prompt_audit_fail_policy"`
	ContentModeration string                              `json:"content_moderation"`
	ContentAction     string                              `json:"content_moderation_action"`
	ContentFail       string                              `json:"content_moderation_fail_policy"`
	Reroute           *audit_policy_setting.RerouteTarget `json:"reroute,omitempty"`
	// Notes explain why a configured action does not apply as written.
	Notes []string `json:"notes"`
}

const (
	engineOff   = "off"
	engineAsync = "async"
	engineSync  = "sync"
)

// Explain resolves the subject against the current rules without running a
// check.
func Explain(subject audit_policy_setting.Subject) Plan {
	snapshot := audit_policy_setting.Current()
	resolution := snapshot.Config.Resolve(subject)
	return plan(snapshot, resolution)
}

func plan(snapshot *audit_policy_setting.Snapshot, resolution audit_policy_setting.Resolution) Plan {
	rule := resolution.Rule
	result := Plan{
		RuleID: rule.ID, RuleName: rule.Name, RuleIndex: resolution.Index, Saved: snapshot.Saved,
		Allowlisted: resolution.Listing.Allowlisted, Verified: resolution.Listing.Verified, Blocklisted: resolution.Listing.Blocklisted,
		PromptAudit: engineOff, ContentModeration: engineOff, Notes: []string{},
	}
	pa := prompt_audit_setting.Current()
	if rule.PromptAudit.Enabled {
		result.PromptAuditFail = rule.PromptAudit.FailPolicy
		result.PromptAuditAction = effectiveAction(rule, rule.PromptAudit.Action, resolution.Listing.Verified)
		switch {
		case !pa.Config.Enabled && !pa.Degraded:
			result.Notes = append(result.Notes, "prompt audit is turned off")
		case promptAuditSynchronous(rule) && pa.Config.Mode == prompt_audit_setting.ModeBlocking:
			result.PromptAudit = engineSync
		default:
			result.PromptAudit = engineAsync
			if promptAuditSynchronous(rule) {
				result.Notes = append(result.Notes, "prompt audit mode is async_audit, so this rule only observes")
				result.PromptAuditAction = audit_policy_setting.ActionObserve
			}
		}
	}
	cm := content_moderation_setting.Current()
	if rule.ContentModeration.Enabled {
		result.ContentFail = rule.ContentModeration.FailPolicy
		decision := contentDecision(rule, resolution.Listing, cm.Config.Mode)
		switch {
		case !decision.Moderate:
			result.Notes = append(result.Notes, "content moderation is turned off")
		case decision.Action == contentmoderation.ActionBlock || decision.Action == contentmoderation.ActionReroute:
			result.ContentModeration = engineSync
			result.ContentAction = string(decision.Action)
		default:
			result.ContentModeration = engineAsync
			result.ContentAction = audit_policy_setting.ActionObserve
			if rule.ContentModeration.Action != audit_policy_setting.ActionObserve && cm.Config.Mode == content_moderation_setting.ModeObserve {
				result.Notes = append(result.Notes, "content moderation mode is observe, so this rule only observes")
			}
		}
	}
	if rule.UsesReroute() {
		if resolution.Listing.Verified {
			target := rule.Reroute
			result.Reroute = &target
		} else {
			result.Notes = append(result.Notes, "reroute applies only to verified allowlist users; this subject gets "+rule.RerouteFallback)
		}
	}
	return result
}

// effectiveAction is the action taken for this subject: a reroute applies
// only to verified allowlist users, everyone else gets the fallback.
func effectiveAction(rule *audit_policy_setting.Rule, action string, verified bool) string {
	if action == audit_policy_setting.ActionReroute && !verified {
		return rule.RerouteFallback
	}
	return action
}

// promptAuditSynchronous reports whether any prompt audit action of the rule
// must run before billing.
func promptAuditSynchronous(rule *audit_policy_setting.Rule) bool {
	if rule.PromptAudit.Action != audit_policy_setting.ActionObserve {
		return true
	}
	for _, action := range rule.PromptAudit.CategoryActions {
		if action == audit_policy_setting.ActionBlock || action == audit_policy_setting.ActionReroute {
			return true
		}
	}
	return false
}

// contentDecision turns the rule's content moderation settings into the
// engine's decision. The engine's mode is the master switch: off checks
// nothing, observe never blocks or reroutes.
func contentDecision(rule *audit_policy_setting.Rule, listing audit_policy_setting.Listing, mode string) contentmoderation.Decision {
	settings := rule.ContentModeration
	if mode == content_moderation_setting.ModeOff {
		return contentmoderation.Decision{}
	}
	if !settings.Enabled {
		// Moderation is on but this request is out of scope.
		return contentmoderation.Decision{Mode: mode}
	}
	decision := contentmoderation.Decision{
		Moderate: true, Mode: mode, SampleRate: settings.SampleRate, Dispose: settings.CountDisposal,
		FailClosed: settings.FailPolicy == audit_policy_setting.FailClosed, RuleID: rule.ID,
		KeywordMode: settings.KeywordMode, KeywordLists: settings.KeywordLists, Thresholds: settings.Thresholds,
	}
	action := effectiveAction(rule, settings.Action, listing.Verified)
	switch {
	case mode != content_moderation_setting.ModePreBlock || action == audit_policy_setting.ActionObserve:
		decision.Action = contentmoderation.ActionObserve
		if !settings.CountDisposal {
			decision.Action = contentmoderation.ActionRecordOnly
		}
	case action == audit_policy_setting.ActionReroute:
		decision.Action = contentmoderation.ActionReroute
	default:
		decision.Action = contentmoderation.ActionBlock
	}
	return decision
}

// contentPolicy is the rule table as content moderation's scope policy, so
// code that asks the engine's policy sees the same decisions.
type contentPolicy struct{}

func (contentPolicy) Decide(subject contentmoderation.Subject) contentmoderation.Decision {
	snapshot := audit_policy_setting.Current()
	resolution := snapshot.Config.Resolve(audit_policy_setting.Subject{
		UserID: subject.UserID, TokenID: subject.TokenID, Group: subject.Group, RequestedGroup: subject.RequestedGroup, Model: subject.Model,
	})
	return contentDecision(resolution.Rule, resolution.Listing, content_moderation_setting.Current().Config.Mode)
}

// UsesKeywordLists reports whether a rule moderates with a keyword mode of
// its own.
func (contentPolicy) UsesKeywordLists() bool {
	config := &audit_policy_setting.Current().Config
	for _, rule := range append([]audit_policy_setting.Rule{config.Default}, config.Rules...) {
		mode := rule.ContentModeration.KeywordMode
		if rule.ContentModeration.Enabled && mode != "" && mode != content_moderation_setting.KeywordModeAPIOnly {
			return true
		}
	}
	return false
}

func init() {
	contentmoderation.SetScopePolicy(contentPolicy{})
	promptaudit.SetDisposalHook(countPromptAuditViolation)
}

// countPromptAuditViolation counts a triggered prompt audit finding as a
// content moderation violation of the user.
func countPromptAuditViolation(event *model.PromptAuditEvent, email string) {
	contentmoderation.RecordViolation(model.ContentModerationEvent{
		CreatedAt: event.CreatedAt, RequestId: event.RequestId, UserId: event.UserId, Username: event.Username,
		TokenId: event.TokenId, TokenName: event.TokenName, UsingGroup: event.UsingGroup, ChannelId: event.ChannelId,
		RelayFormat: event.RelayFormat, Endpoint: event.Endpoint, ModelName: event.ModelName, Mode: event.Mode,
		Action: event.PolicyAction, HighestCategory: model.ContentModerationPromptAuditCategory, HighestScore: 1,
		InputHash: event.PromptHash, InputExcerpt: event.RedactedPreview, InputLength: event.PromptLength, RuleId: event.RuleId,
	}, email)
}

// SubjectOf builds the rule subject of a relay request.
func SubjectOf(c *gin.Context, info *relaycommon.RelayInfo, format string) audit_policy_setting.Subject {
	subject := contentmoderation.SubjectOf(c, info)
	return audit_policy_setting.Subject{
		UserID: subject.UserID, TokenID: subject.TokenID, Group: subject.Group,
		RequestedGroup: subject.RequestedGroup, Model: subject.Model, Format: format,
	}
}

// Evaluate checks a relay request once, before billing. format is the relay
// format rules match ("" uses info.RelayFormat). key identifies the request
// body: a second call on the same gin context with the same key returns the
// first result without checking again, and a different key (the next turn
// of a WebSocket session) is checked anew.
func Evaluate(c *gin.Context, info *relaycommon.RelayInfo, format string, key string) Result {
	if info == nil {
		return Result{}
	}
	if value, ok := c.Get(evaluatedKey); ok {
		if previous, ok := value.(evaluated); ok && previous.key == key {
			return previous.result
		}
	}
	result := evaluate(c, info, format)
	c.Set(evaluatedKey, evaluated{key: key, result: result})
	return result
}

func evaluate(c *gin.Context, info *relaycommon.RelayInfo, format string) Result {
	if format == "" {
		format = string(info.RelayFormat)
	}
	snapshot := audit_policy_setting.Current()
	subject := SubjectOf(c, info, format)
	resolution := snapshot.Config.Resolve(subject)
	rule := resolution.Rule
	pa := prompt_audit_setting.Current()

	paMode := engineOff
	protocol, covered := promptaudit.RelayProtocol(info.RelayFormat, info.RequestURLPath)
	if rule.PromptAudit.Enabled && covered && (pa.Config.Enabled || pa.Degraded) {
		paMode = engineAsync
		if promptAuditSynchronous(rule) && pa.Config.Mode == prompt_audit_setting.ModeBlocking {
			paMode = engineSync
		}
	} else if pa.Config.Enabled {
		promptaudit.CountSkipped()
	}
	cmSubject := contentmoderation.Subject{UserID: subject.UserID, TokenID: subject.TokenID, Group: subject.Group,
		RequestedGroup: subject.RequestedGroup, Model: subject.Model}
	cmDecision := contentDecision(rule, resolution.Listing, content_moderation_setting.Current().Config.Mode)

	var meta promptaudit.Meta
	if paMode != engineOff {
		meta = promptaudit.Meta{
			RequestID: c.GetString(common.RequestIdKey), UserID: info.UserId, UserEmail: info.UserEmail,
			Username: common.GetContextKeyString(c, constant.ContextKeyUserName), TokenID: info.TokenId,
			TokenName: c.GetString("token_name"), Group: subject.Group, ChannelID: common.GetContextKeyInt(c, constant.ContextKeyChannelId),
			RelayFormat: string(info.RelayFormat), Endpoint: info.RequestURLPath, Model: info.OriginModelName, Protocol: protocol,
		}
	}
	if paMode == engineAsync {
		promptaudit.Enqueue(c, meta, rule.ID, rule.PromptAudit)
	}

	var paVerdict promptaudit.Verdict
	var cmOutcome contentmoderation.Outcome
	if paMode == engineSync {
		// Create the body storage before the checks share the context.
		_, _ = common.GetBodyStorage(c)
		var wg sync.WaitGroup
		wg.Go(func() {
			paVerdict = promptaudit.Check(context.WithoutCancel(c.Request.Context()), c, meta, rule.ID, rule.PromptAudit)
		})
		cmOutcome = contentmoderation.Check(c, info, cmSubject, cmDecision)
		wg.Wait()
	} else {
		cmOutcome = contentmoderation.Check(c, info, cmSubject, cmDecision)
	}

	paAction := ""
	if paVerdict.Outcome == promptaudit.OutcomeTriggered {
		paAction = effectiveAction(rule, rule.PromptAudit.ActionFor(paVerdict.Triggered), resolution.Listing.Verified)
	}
	if event := paVerdict.Event; event != nil {
		event.PolicyAction = paAction
		if paAction == "" {
			event.PolicyAction = audit_policy_setting.ActionObserve
		}
		if paAction != "" && paAction != audit_policy_setting.ActionIgnore && rule.PromptAudit.CountDisposal {
			countPromptAuditViolation(event, info.UserEmail)
		}
		promptaudit.Record(event)
	}

	reroute := &Reroute{RuleID: rule.ID, Model: rule.Reroute.Model, Group: rule.Reroute.Group}
	switch {
	case cmOutcome == contentmoderation.OutcomeBlock:
		return Result{Err: contentmoderation.BlockError(c)}
	case paAction == audit_policy_setting.ActionBlock:
		return Result{Err: promptAuditError(c, ErrorCodePromptAuditBlocked, http.StatusForbidden, i18n.MsgPromptAuditBlocked, "prompt audit blocked the request")}
	case cmOutcome == contentmoderation.OutcomeUnavailable:
		return Result{Err: contentmoderation.UnavailableError(c)}
	case paVerdict.Outcome == promptaudit.OutcomeUnavailable && rule.PromptAudit.FailPolicy == audit_policy_setting.FailClosed:
		return Result{Err: promptAuditError(c, ErrorCodePromptAuditUnavailable, http.StatusServiceUnavailable, i18n.MsgAuditUnavailable, "prompt audit was unavailable and the rule fails closed")}
	case paAction == audit_policy_setting.ActionReroute, cmOutcome == contentmoderation.OutcomeReroute:
		return Result{Reroute: reroute}
	}
	return Result{}
}

// ErrorCodeRerouteFailed is returned when a reroute cannot be applied; the
// request is never sent to its original model instead.
const ErrorCodeRerouteFailed = types.ErrorCode("audit_reroute_failed")

// RerouteFailedError is the answer when a reroute cannot be applied.
func RerouteFailedError(c *gin.Context, reroute *Reroute, reason string) *types.NewAPIError {
	logger.LogWarn(c, common.LogText("audit policy rule %s could not reroute the request: %s", reroute.RuleID, reason))
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgAuditRerouteFailed)), ErrorCodeRerouteFailed,
		http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

// RerouteUnsupportedError refuses a reroute on a path that cannot change
// the model or channel, such as a Responses WebSocket connection.
func RerouteUnsupportedError(c *gin.Context, reroute *Reroute) *types.NewAPIError {
	return RerouteFailedError(c, reroute, "the connection is pinned to its model and channel")
}

// promptAuditError builds a rejection that names no category, so the reply
// does not help tune a prompt around the Guard.
func promptAuditError(c *gin.Context, code types.ErrorCode, status int, messageKey string, logLine string) *types.NewAPIError {
	service.RequestPolicy(c).AddEvent(service.PolicyEvent{ErrorCode: string(code), ErrorSource: "local",
		Decision: service.PolicyDecision{Action: "stop", Reason: "local_rejection", Source: "global"}, Health: "unchanged"})
	logger.LogWarn(c, logLine)
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, messageKey)), code, status, types.ErrOptionWithSkipRetry())
}
