package audit_policy_setting

import (
	"slices"
	"strconv"
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
		// Before the table, neither engine checked Responses WebSocket turns.
		match.Formats = slices.DeleteFunc(slices.Clone(validRelayFormats), func(format string) bool { return format == FormatResponsesWebSocket })
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
		// A request was in an engine's scope when its own group or, for an
		// "auto" token, "auto" matched. Both engines applied when each matched
		// by either name: the shared groups, or "auto" for one engine and the
		// distributor's group for the other.
		var both []Match
		if shared, ok := intersectGroups(paGroups, cmGroups); ok {
			both = append(both, Match{Groups: shared})
		}
		if paGroups != nil && cmGroups != nil {
			auto := []string{"auto"}
			if includesGroup(paGroups, "auto") {
				both = append(both, Match{RequestedGroups: auto, Groups: slices.Clone(cmGroups)})
			}
			if includesGroup(cmGroups, "auto") {
				both = append(both, Match{RequestedGroups: auto, Groups: slices.Clone(paGroups)})
			}
		}
		for index, match := range both {
			suffix := ""
			if index > 0 {
				suffix = "-" + strconv.Itoa(index+1)
			}
			if allowlisted {
				allowlistMatch := cmMatch(match.Groups, ListAllowlist)
				allowlistMatch.RequestedGroups = match.RequestedGroups
				add("legacy-both-allowlist"+suffix, "Prompt audit and content moderation (allowlist)", allowlistMatch, true, &recordOnly)
			}
			bothMatch := cmMatch(match.Groups, ListAny)
			bothMatch.RequestedGroups = match.RequestedGroups
			add("legacy-both"+suffix, "Prompt audit and content moderation", bothMatch, true, &contentModeration)
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

// includesGroup applies a legacy group scope (exact names and "prefix*").
func includesGroup(patterns []string, group string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool {
		prefix, wildcard := strings.CutSuffix(pattern, "*")
		return pattern == group || wildcard && strings.HasPrefix(group, prefix)
	})
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
