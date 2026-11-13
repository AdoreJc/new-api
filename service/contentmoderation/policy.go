package contentmoderation

import (
	"sync/atomic"

	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
)

// Action is what a scope policy wants done with a request in scope.
type Action string

const (
	// ActionBlock checks the input before billing and rejects it when flagged.
	ActionBlock Action = "block"
	// ActionReroute checks the input before billing and, when flagged, sends
	// the request to the rule's target model or group instead.
	ActionReroute Action = "reroute"
	// ActionObserve checks the input in the background; flagged inputs are
	// recorded and, when Dispose is set, go through disposal (violation
	// count, mail, disabling).
	ActionObserve Action = "observe"
	// ActionRecordOnly checks the input in the background and only records
	// it: no blocking, no disposal. Used for allowlisted users.
	ActionRecordOnly Action = "record_only"
)

// Subject is what a scope policy sees about a relay request. It is built
// before the request body is read.
type Subject struct {
	UserID  int
	TokenID int
	// Group is the group the request is billed to; for an "auto" token it is
	// the group the distributor chose, and RequestedGroup stays "auto".
	Group          string
	RequestedGroup string
	Model          string
	// Format is the relay format, matched by format conditions of the rules.
	Format string
}

// Decision is a scope policy's answer for one request.
type Decision struct {
	// Moderate is false for requests out of scope; nothing else is read then.
	Moderate bool
	// Mode is recorded on the event (observe or pre_block). A decision with
	// Moderate false and a Mode set means moderation is on but the request
	// is out of scope; it is counted as skipped.
	Mode   string
	Action Action
	// SampleRate is the percentage of in-scope inputs sent to the engine,
	// chosen by input hash. Keyword and hash blocklist checks ignore it.
	SampleRate int
	// Dispose counts a flagged input as a violation (and may mail or
	// disable the user). Inputs that are not disposed are stored as
	// allowlisted so they never count later either.
	Dispose bool
	// FailClosed blocks a synchronous check whose engine call failed.
	FailClosed bool
	// RuleID names the audit policy rule recorded on the event.
	RuleID string
	// KeywordMode and KeywordLists override the settings when set; nil lists
	// keep the configured ones.
	KeywordMode  string
	KeywordLists []string
	// Thresholds override single category thresholds.
	Thresholds map[string]float64
}

// synchronous reports whether the check must finish before billing.
func (d Decision) synchronous() bool {
	return d.Action == ActionBlock || d.Action == ActionReroute
}

// ScopePolicy decides whether and how a request is moderated. The scanning
// code (extraction, hash blocklist, keywords, engine, disposal) only follows
// the Decision; the audit policy rule table replaces the default policy with
// SetScopePolicy.
type ScopePolicy interface {
	Decide(subject Subject) Decision
}

// KeywordListUser is implemented by a ScopePolicy whose decisions may name
// keyword lists, so they are loaded even when the settings use none.
type KeywordListUser interface {
	UsesKeywordLists() bool
}

// ConfigScopePolicy is the policy of the content moderation settings alone:
// the mode, group and model filters, sample rate and allowlist.
type ConfigScopePolicy struct{}

func (ConfigScopePolicy) Decide(subject Subject) Decision {
	config := &content_moderation_setting.Current().Config
	if config.Mode == content_moderation_setting.ModeOff {
		return Decision{}
	}
	inGroup := config.IncludesGroup(subject.Group) ||
		(subject.RequestedGroup != "" && config.IncludesGroup(subject.RequestedGroup))
	if !inGroup || !config.IncludesModel(subject.Model) {
		return Decision{Mode: config.Mode}
	}
	decision := Decision{Moderate: true, Mode: config.Mode, Action: ActionObserve, SampleRate: config.SampleRate, Dispose: true}
	switch {
	case config.Allowlisted(subject.UserID):
		decision.Action, decision.Dispose = ActionRecordOnly, false
	case config.Mode == content_moderation_setting.ModePreBlock:
		decision.Action = ActionBlock
	}
	return decision
}

type policyHolder struct{ policy ScopePolicy }

var scopePolicy atomic.Pointer[policyHolder]

func init() { scopePolicy.Store(&policyHolder{policy: ConfigScopePolicy{}}) }

// SetScopePolicy replaces the policy used for every following request; nil
// restores the default ConfigScopePolicy.
func SetScopePolicy(policy ScopePolicy) {
	if policy == nil {
		policy = ConfigScopePolicy{}
	}
	scopePolicy.Store(&policyHolder{policy: policy})
}

// CurrentScopePolicy returns the policy in use.
func CurrentScopePolicy() ScopePolicy { return scopePolicy.Load().policy }
