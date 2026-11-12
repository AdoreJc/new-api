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
	// ActionObserve checks the input in the background; flagged inputs are
	// recorded and go through disposal (violation count, mail, disabling).
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
}

// ScopePolicy decides whether and how a request is moderated. The scanning
// code (extraction, hash blocklist, keywords, engine, disposal) only follows
// the Decision, so a rule-table policy can replace the default one with
// SetScopePolicy without touching it.
type ScopePolicy interface {
	Decide(subject Subject) Decision
}

// ConfigScopePolicy is the default policy: the mode, group and model
// filters, sample rate and allowlist of the content moderation settings.
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
	decision := Decision{Moderate: true, Mode: config.Mode, Action: ActionObserve, SampleRate: config.SampleRate}
	switch {
	case config.Allowlisted(subject.UserID):
		decision.Action = ActionRecordOnly
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

func currentScopePolicy() ScopePolicy { return scopePolicy.Load().policy }
