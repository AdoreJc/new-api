package audit_policy_setting

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"
)

// Derive builds the rule table equivalent to the legacy scope settings of
// both engines: prompt audit's group scope, risk categories and jailbreak
// mapping, and content moderation's mode, group scope, model filter, sample
// rate and allowlist. A request is checked by the same engines, with the same
// actions, as before the table existed.
//
// paDegraded means the stored prompt audit setting could not be read while it
// asked for blocking; every group is then treated as in scope for blocking so
// the failure policy decides, instead of silently auditing nothing.
func Derive(pa prompt_audit_setting.Config, paDegraded bool, cm content_moderation_setting.Config) Config {
	config := Config{Rules: []Rule{}, Allowlist: []ListEntry{}, Blocklist: []ListEntry{}}
	paOn := pa.Enabled || paDegraded
	cmOn := cm.Mode != content_moderation_setting.ModeOff
	var paGroups, cmGroups []string
	if !pa.AllGroups && !paDegraded {
		paGroups = pa.Groups
	}
	if !cm.AllGroups {
		cmGroups = cm.Groups
	}
	promptAudit := derivedPromptAudit(pa, paDegraded)
	contentModeration := ContentModeration{
		Enabled: true, Action: ActionObserve, SampleRate: cm.SampleRate, FailPolicy: FailOpen, CountDisposal: true,
	}
	if cm.Mode == content_moderation_setting.ModePreBlock {
		contentModeration.Action = ActionBlock
	}
	// Allowlisted users were checked in the background and only recorded.
	recordOnly := ContentModeration{Enabled: true, Action: ActionObserve, SampleRate: cm.SampleRate, FailPolicy: FailOpen}
	var models, excludeModels []string
	switch cm.ModelFilter.Type {
	case content_moderation_setting.ModelFilterInclude:
		models = cm.ModelFilter.Models
	case content_moderation_setting.ModelFilterExclude:
		excludeModels = cm.ModelFilter.Models
	}
	for _, id := range cm.AllowlistUserIDs {
		config.Allowlist = append(config.Allowlist, ListEntry{UserID: id, Note: "content moderation allowlist"})
	}
	add := func(id, name string, match Match, withPA bool, cmRule *ContentModeration) {
		rule := Rule{ID: id, Name: name, Enabled: true, Match: match}
		if withPA {
			rule.PromptAudit = promptAudit
		}
		if cmRule != nil {
			rule.ContentModeration = *cmRule
		}
		config.Rules = append(config.Rules, rule)
	}
	cmMatch := func(groups []string, list string) Match {
		return Match{Groups: slices.Clone(groups), Models: slices.Clone(models), ExcludeModels: slices.Clone(excludeModels), List: list}
	}
	allowlisted := len(cm.AllowlistUserIDs) > 0
	if paOn && cmOn {
		if both, ok := intersectGroups(paGroups, cmGroups); ok {
			if allowlisted {
				add("legacy-both-allowlist", "Prompt audit and content moderation (allowlist)", cmMatch(both, ListAllowlist), true, &recordOnly)
			}
			add("legacy-both", "Prompt audit and content moderation", cmMatch(both, ListAny), true, &contentModeration)
		}
	}
	if paOn {
		add("legacy-prompt-audit", "Prompt audit", Match{Groups: slices.Clone(paGroups)}, true, nil)
	}
	if cmOn {
		if allowlisted {
			add("legacy-content-moderation-allowlist", "Content moderation (allowlist)", cmMatch(cmGroups, ListAllowlist), false, &recordOnly)
		}
		add("legacy-content-moderation", "Content moderation", cmMatch(cmGroups, ListAny), false, &contentModeration)
	}
	_ = config.Normalize()
	return config
}

// derivedPromptAudit expresses the enabled risk categories and the jailbreak
// mapping as category trigger levels: PII and self-harm trigger on a
// Controversial verdict, jailbreak too when strict mapping is on, and every
// other enabled category on Unsafe. Disabled categories never trigger.
func derivedPromptAudit(pa prompt_audit_setting.Config, degraded bool) PromptAudit {
	rule := PromptAudit{Enabled: true, MinLevel: LevelUnsafe, Action: ActionObserve, FailPolicy: pa.FailPolicy,
		CategoryLevels: map[string]string{}, CategoryActions: map[string]string{}}
	if pa.Mode == prompt_audit_setting.ModeBlocking || degraded {
		rule.Action = ActionBlock
	}
	for _, category := range prompt_audit_setting.ScannerIDs {
		switch {
		case !slices.Contains(pa.Scanners, category):
			rule.CategoryLevels[category] = LevelOff
		case category == "pii" || category == "suicide_and_self_harm" || (category == "jailbreak" && pa.StrictJailbreakMapping):
			rule.CategoryLevels[category] = LevelControversial
		}
	}
	return rule
}

// intersectGroups intersects two legacy group scopes, each a list of exact
// names and "prefix*" patterns, where nil means every group. It reports false
// when no group is in both.
func intersectGroups(a, b []string) ([]string, bool) {
	if a == nil {
		return slices.Clone(b), true
	}
	if b == nil {
		return slices.Clone(a), true
	}
	result := make([]string, 0)
	for _, left := range a {
		for _, right := range b {
			pattern, ok := intersectPattern(left, right)
			if ok && !slices.Contains(result, pattern) {
				result = append(result, pattern)
			}
		}
	}
	return result, len(result) > 0
}

func intersectPattern(a, b string) (string, bool) {
	aPrefix, aWild := strings.CutSuffix(a, "*")
	bPrefix, bWild := strings.CutSuffix(b, "*")
	switch {
	case !aWild && !bWild:
		return a, a == b
	case aWild && bWild:
		if strings.HasPrefix(aPrefix, bPrefix) {
			return a, true
		}
		return b, strings.HasPrefix(bPrefix, aPrefix)
	case aWild:
		return b, strings.HasPrefix(b, aPrefix)
	default:
		return a, strings.HasPrefix(a, bPrefix)
	}
}
