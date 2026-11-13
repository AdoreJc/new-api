package promptaudit

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
)

// Issue is one finding of an event in reviewer terms. Title and Action are
// English source strings the console translates.
type Issue struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	// Severity is critical, high, medium or low.
	Severity string `json:"severity"`
	Action   string `json:"action"`
	// Chunks are the 1-based scan positions of the chunks that named it.
	Chunks []int `json:"chunks"`
}

var issueCatalog = map[string]struct{ title, severity string }{
	"violent":                       {"Violence", "high"},
	"non_violent_illegal_acts":      {"Non-violent illegal acts", "high"},
	"sexual_content_or_sexual_acts": {"Sexual content", "high"},
	"pii":                           {"Personal information", "high"},
	"suicide_and_self_harm":         {"Suicide and self-harm", "critical"},
	"unethical_acts":                {"Unethical acts", "medium"},
	"politically_sensitive_topics":  {"Politically sensitive topics", "medium"},
	"copyright_violation":           {"Copyright violation", "low"},
	"jailbreak":                     {"Jailbreak or prompt injection", "critical"},
}

// SummarizeIssues lists an event's categories, unknown ones last, with the
// action the event records.
func SummarizeIssues(event *model.PromptAuditEvent) []Issue {
	var categories, unknown []string
	_ = common.UnmarshalJsonStr(event.Categories, &categories)
	_ = common.UnmarshalJsonStr(event.UnknownCategories, &unknown)
	var hits []chunkHit
	_ = common.UnmarshalJsonStr(event.HitChunks, &hits)
	action := issueAction(event)
	issues := make([]Issue, 0, len(categories)+len(unknown))
	for _, category := range append(categories, unknown...) {
		issue := Issue{Category: category, Title: "Unrecognized category", Severity: "medium", Action: action, Chunks: []int{}}
		if entry, ok := issueCatalog[category]; ok {
			issue.Title, issue.Severity = entry.title, entry.severity
		}
		for _, hit := range hits {
			if slices.Contains(hit.Categories, category) || strings.HasPrefix(category, "unknown:") && len(hit.Categories) == 0 {
				issue.Chunks = append(issue.Chunks, hit.Index)
			}
		}
		issues = append(issues, issue)
	}
	return issues
}

func issueAction(event *model.PromptAuditEvent) string {
	switch event.PolicyAction {
	case audit_policy_setting.ActionBlock:
		return "Blocked"
	case audit_policy_setting.ActionReroute:
		return "Rerouted"
	case audit_policy_setting.ActionIgnore:
		return "Ignored by rule"
	}
	if event.Decision == DecisionCritical {
		return "Recorded, would trigger"
	}
	return "Recorded for review"
}
