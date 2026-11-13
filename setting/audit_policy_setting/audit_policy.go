// Package audit_policy_setting holds the rule table shared by prompt audit and
// content moderation: which requests each engine checks and what happens when
// it finds something. Rules are matched from top to bottom and the first
// enabled rule that matches wins; the default rule applies when none does.
//
// Until an administrator saves a table, the rules are derived from the
// legacy scope settings of both engines (see Derive), so an upgrade keeps
// the behavior of the stored configuration.
package audit_policy_setting

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"
)

// OptionKey stores the saved rule table as JSON.
const OptionKey = "AuditPolicySetting"

// FormatResponsesWebSocket is the relay format of Responses API turns over
// WebSocket.
const FormatResponsesWebSocket = "responses_websocket"

// Actions taken when an engine finds something. ActionIgnore is only valid as
// a per-category override and drops the category from the decision.
const (
	ActionObserve = "observe"
	ActionBlock   = "block"
	ActionReroute = "reroute"
	ActionIgnore  = "ignore"
)

// Failure policies when an engine cannot answer a synchronous check.
const (
	FailOpen   = "open"
	FailClosed = "closed"
)

// Prompt audit trigger levels: the lowest Guard safety label at which a
// category triggers the rule's action. LevelOff never triggers.
const (
	LevelControversial = "controversial"
	LevelUnsafe        = "unsafe"
	LevelOff           = "off"
)

// List conditions of a rule.
const (
	ListAny       = ""
	ListAllowlist = "allowlist"
	// ListVerified matches allowlist entries marked as verified, the only
	// users a reroute applies to.
	ListVerified  = "verified"
	ListBlocklist = "blocklist"
	// ListUnlisted matches users on neither list.
	ListUnlisted = "unlisted"
)

const (
	MaxRules          = 200
	MaxPatterns       = 200
	MaxListEntries    = 5000
	MaxIDsPerRule     = 1000
	MaxPatternLength  = 255
	MaxRuleIDLength   = 64
	MaxRuleNameLength = 128
	MaxNoteLength     = 255
	DefaultRuleID     = "default"
)

var (
	validActions       = []string{ActionObserve, ActionBlock, ActionReroute}
	validLevels        = []string{LevelControversial, LevelUnsafe, LevelOff}
	validLists         = []string{ListAny, ListAllowlist, ListVerified, ListBlocklist, ListUnlisted}
	validFailPolicies  = []string{FailOpen, FailClosed}
	validKeywordModes  = []string{content_moderation_setting.KeywordModeAPIOnly, content_moderation_setting.KeywordModeKeywordOnly, content_moderation_setting.KeywordModeKeywordAndAPI, content_moderation_setting.KeywordModeKeywordGated}
	validRelayFormats  = []string{"openai", "claude", "gemini", "openai_responses", "openai_responses_compaction", "openai_image", FormatResponsesWebSocket}
	actionSeverityRank = map[string]int{ActionIgnore: 0, ActionObserve: 1, ActionReroute: 2, ActionBlock: 3}
)

// Match lists the conditions of a rule; all set conditions must hold. Within
// one list any entry may match. Group and model patterns may use "*" for any
// run of characters, for example "claude-ch*" or "*-thinking".
type Match struct {
	Groups []string `json:"groups"`
	// RequestedGroups match only the group the token asked for, such as
	// "auto", never the group the distributor chose for it.
	RequestedGroups []string `json:"requested_groups"`
	Models          []string `json:"models"`
	ExcludeModels   []string `json:"exclude_models"`
	UserIDs         []int    `json:"user_ids"`
	TokenIDs        []int    `json:"token_ids"`
	List            string   `json:"list"`
	// Formats are relay formats; "responses_websocket" is the Responses API
	// over WebSocket.
	Formats []string `json:"formats"`
}

// PromptAudit is a rule's prompt audit settings.
type PromptAudit struct {
	Enabled bool `json:"enabled"`
	// ScanScope overrides the engine's scan scope; empty keeps it.
	ScanScope string `json:"scan_scope"`
	// MinLevel is the trigger level of categories without their own level.
	MinLevel string `json:"min_level"`
	// CategoryLevels sets the trigger level of single categories.
	CategoryLevels map[string]string `json:"category_levels"`
	// Action is taken when a category triggers; CategoryActions overrides it
	// for single categories (the most severe triggered action wins).
	Action          string            `json:"action"`
	CategoryActions map[string]string `json:"category_actions"`
	FailPolicy      string            `json:"fail_policy"`
	// CountDisposal counts a triggered verdict as a violation for content
	// moderation's automatic disabling.
	CountDisposal bool `json:"count_disposal"`
}

// ContentModeration is a rule's content moderation settings.
type ContentModeration struct {
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"`
	// SampleRate is the percentage of inputs sent to the engine.
	SampleRate int `json:"sample_rate"`
	// KeywordMode and KeywordLists override the engine settings when set.
	KeywordMode  string   `json:"keyword_mode"`
	KeywordLists []string `json:"keyword_lists"`
	// Thresholds override single category thresholds of the engine.
	Thresholds    map[string]float64 `json:"thresholds"`
	FailPolicy    string             `json:"fail_policy"`
	CountDisposal bool               `json:"count_disposal"`
}

type RerouteTarget struct {
	Model string `json:"model"`
	Group string `json:"group"`
}

type Rule struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Enabled           bool              `json:"enabled"`
	Match             Match             `json:"match"`
	PromptAudit       PromptAudit       `json:"prompt_audit"`
	ContentModeration ContentModeration `json:"content_moderation"`
	// Reroute is used by the reroute action. It applies only to verified
	// allowlist users; for anyone else RerouteFallback (observe or block) is
	// taken instead.
	Reroute         RerouteTarget `json:"reroute"`
	RerouteFallback string        `json:"reroute_fallback"`
	// Degraded marks a stored rule whose engine settings could not be read.
	// Its engines run nothing; where it asked to block or reroute, the
	// request is handled as if the engine were unavailable, so its failure
	// policy decides.
	Degraded bool `json:"degraded,omitempty"`
}

// ListEntry names a user, or a single token of a user, on a list.
type ListEntry struct {
	UserID   int    `json:"user_id"`
	TokenID  int    `json:"token_id"`
	Verified bool   `json:"verified"`
	Note     string `json:"note"`
}

type Config struct {
	Rules         []Rule      `json:"rules"`
	Default       Rule        `json:"default"`
	Allowlist     []ListEntry `json:"allowlist"`
	Blocklist     []ListEntry `json:"blocklist"`
	ConfigVersion int64       `json:"config_version"`
	UpdatedAt     int64       `json:"updated_at"`
	UpdatedBy     int         `json:"updated_by"`
	// Derived marks a stored reset: no table is saved and the rules are
	// derived again, but the version keeps counting from here.
	Derived bool `json:"derived,omitempty"`
}

// MaxStoredBytes bounds the stored table.
const MaxStoredBytes = 1 << 20

// Snapshot is the immutable runtime view. Saved is false while the rules are
// derived from the legacy engine settings.
type Snapshot struct {
	Config Config
	Saved  bool
	// Degraded is set when the stored table could not be read as a whole;
	// Problems says what was dropped or kept degraded.
	Degraded bool
	Problems []string
}

// derivedSnapshot caches the rules derived from one pair of engine snapshots.
type derivedSnapshot struct {
	pa       *prompt_audit_setting.Snapshot
	cm       *content_moderation_setting.Snapshot
	version  int64
	snapshot *Snapshot
}

var (
	// saved is the saved table, or nil when none is saved.
	saved   atomic.Pointer[Snapshot]
	derived atomic.Pointer[derivedSnapshot]
	// derivedVersion is the version of a reset table, so derived rules keep
	// counting up and an editor holding an older version cannot overwrite.
	derivedVersion atomic.Int64
)

// Current returns the active snapshot: the saved table, or the rules derived
// from the current engine settings. Callers must not modify it.
func Current() *Snapshot {
	if snapshot := saved.Load(); snapshot != nil {
		return snapshot
	}
	pa, cm, version := prompt_audit_setting.Current(), content_moderation_setting.Current(), derivedVersion.Load()
	if cached := derived.Load(); cached != nil && cached.pa == pa && cached.cm == cm && cached.version == version {
		return cached.snapshot
	}
	snapshot := &Snapshot{Config: Derive(pa.Config, pa.Degraded, cm.Config)}
	snapshot.Config.ConfigVersion = version
	derived.Store(&derivedSnapshot{pa: pa, cm: cm, version: version, snapshot: snapshot})
	return snapshot
}

func IsOptionKey(key string) bool { return key == OptionKey }

// ApplyOption installs a saved table after an option write or a periodic sync.
// An empty value removes the saved table and returns to derived rules. A value
// that cannot be read keeps the previous rules, so a broken write never
// widens or narrows what is checked.
func ApplyOption(key, value string) {
	if key != OptionKey {
		return
	}
	if strings.TrimSpace(value) == "" {
		saved.Store(nil)
		derivedVersion.Store(0)
		return
	}
	config, err := ParseConfig(value)
	if err == nil && config.Derived {
		saved.Store(nil)
		derivedVersion.Store(config.ConfigVersion)
		return
	}
	if err == nil {
		saved.Store(&Snapshot{Config: config, Saved: true})
		return
	}
	config, problems := ParseDegraded(value)
	common.SysError(common.LogText("audit policy setting is invalid, degraded rules in use: %s (%s)", err.Error(), strings.Join(problems, "; ")))
	saved.Store(&Snapshot{Config: config, Saved: true, Degraded: true, Problems: problems})
}

// ParseDegraded reads a stored table that failed validation as far as it
// can. Valid rules are kept. A rule whose match conditions are valid but
// whose engine settings are not is kept as a degraded rule that carries only
// its blocking intent; a rule whose conditions cannot be read is dropped.
// Invalid list entries are dropped. When the value is not even readable
// JSON, a degraded default rule keeps a blocking intent found in the text,
// so the failure policy decides instead of nothing being checked.
func ParseDegraded(value string) (Config, []string) {
	var raw Config
	problems := []string{}
	if err := common.UnmarshalJsonStr(value, &raw); err != nil {
		problems = append(problems, "the stored table is not readable")
		config := Config{Rules: []Rule{}, Allowlist: []ListEntry{}, Blocklist: []ListEntry{}}
		config.Default = degradedRule(Rule{ID: DefaultRuleID}, blockingIntentIn(value))
		return config, problems
	}
	config := Config{Rules: []Rule{}, Allowlist: []ListEntry{}, Blocklist: []ListEntry{}, ConfigVersion: raw.ConfigVersion,
		UpdatedAt: raw.UpdatedAt, UpdatedBy: raw.UpdatedBy}
	ids := []string{DefaultRuleID}
	for index, rule := range raw.Rules {
		if len(config.Rules) >= MaxRules {
			problems = append(problems, "rules beyond the limit were dropped")
			break
		}
		original := rule.clone()
		if err := rule.normalize(false); err == nil && !slices.Contains(ids, rule.ID) {
			ids = append(ids, rule.ID)
			config.Rules = append(config.Rules, rule)
			continue
		}
		kept := Rule{ID: strings.TrimSpace(original.ID), Name: original.Name, Enabled: original.Enabled, Match: original.Match}
		if kept.ID == "" || len(kept.ID) > MaxRuleIDLength || slices.Contains(ids, kept.ID) {
			kept.ID = "invalid-" + strconv.Itoa(index+1)
		}
		if utf8.RuneCountInString(kept.Name) > MaxRuleNameLength {
			kept.Name = ""
		}
		if err := kept.normalize(false); err != nil {
			problems = append(problems, "rule "+strconv.Itoa(index+1)+" was dropped: its conditions are invalid")
			continue
		}
		ids = append(ids, kept.ID)
		problems = append(problems, "rule "+kept.ID+" is degraded: its engine settings are invalid")
		config.Rules = append(config.Rules, degradedRule(kept, intentOf(original)))
	}
	defaultRule := raw.Default.clone()
	defaultRule.ID, defaultRule.Enabled, defaultRule.Match = DefaultRuleID, true, Match{}
	if err := defaultRule.normalize(true); err != nil {
		problems = append(problems, "the default rule is degraded: its engine settings are invalid")
		defaultRule = degradedRule(Rule{ID: DefaultRuleID}, intentOf(raw.Default))
	}
	config.Default = defaultRule
	for _, list := range []struct {
		name    string
		entries []ListEntry
		target  *[]ListEntry
	}{{"allowlist", raw.Allowlist, &config.Allowlist}, {"blocklist", raw.Blocklist, &config.Blocklist}} {
		for _, entry := range list.entries {
			normalized, err := normalizeEntries([]ListEntry{entry})
			if err != nil {
				problems = append(problems, "an invalid "+list.name+" entry was dropped")
				continue
			}
			if !slices.ContainsFunc(*list.target, func(e ListEntry) bool { return e.UserID == entry.UserID && e.TokenID == entry.TokenID }) {
				*list.target = append(*list.target, normalized...)
			}
		}
	}
	for index := range config.Blocklist {
		config.Blocklist[index].Verified = false
	}
	return config, problems
}

// failIntent is what a degraded rule keeps of an engine: whether it asked to
// block or reroute, and whether it failed closed.
type failIntent struct {
	promptAudit, contentModeration             bool
	promptAuditClosed, contentModerationClosed bool
}

func intentOf(rule Rule) failIntent {
	blocks := func(action string) bool { return action == ActionBlock || action == ActionReroute }
	return failIntent{
		promptAudit:             rule.PromptAudit.Enabled && (blocks(rule.PromptAudit.Action) || slices.ContainsFunc(slices.Collect(maps.Values(rule.PromptAudit.CategoryActions)), blocks)),
		promptAuditClosed:       rule.PromptAudit.FailPolicy == FailClosed,
		contentModeration:       rule.ContentModeration.Enabled && blocks(rule.ContentModeration.Action),
		contentModerationClosed: rule.ContentModeration.FailPolicy == FailClosed,
	}
}

// blockingIntentIn guesses the intent of an unreadable table from its text:
// any block or reroute action, and a closed failure policy anywhere.
func blockingIntentIn(value string) failIntent {
	compact := strings.Join(strings.Fields(value), "")
	blocks := strings.Contains(compact, `"action":"block"`) || strings.Contains(compact, `"action":"reroute"`)
	closed := strings.Contains(compact, `"fail_policy":"closed"`)
	return failIntent{promptAudit: blocks, contentModeration: blocks, promptAuditClosed: closed, contentModerationClosed: closed}
}

func degradedRule(rule Rule, intent failIntent) Rule {
	rule.Degraded = true
	rule.PromptAudit = PromptAudit{Enabled: intent.promptAudit, Action: ActionBlock, FailPolicy: FailOpen}
	if intent.promptAuditClosed {
		rule.PromptAudit.FailPolicy = FailClosed
	}
	rule.ContentModeration = ContentModeration{Enabled: intent.contentModeration, Action: ActionBlock, FailPolicy: FailOpen}
	if intent.contentModerationClosed {
		rule.ContentModeration.FailPolicy = FailClosed
	}
	if !intent.promptAudit {
		rule.PromptAudit.Action = ActionObserve
	}
	if !intent.contentModeration {
		rule.ContentModeration.Action = ActionObserve
	}
	_ = rule.PromptAudit.normalize()
	_ = rule.ContentModeration.normalize()
	return rule
}

// ParseConfig decodes and validates a stored table.
func ParseConfig(value string) (Config, error) {
	var config Config
	if err := common.UnmarshalJsonStr(value, &config); err != nil {
		return Config{}, err
	}
	if config.Derived {
		return Config{Derived: true, ConfigVersion: config.ConfigVersion}, nil
	}
	if err := config.Normalize(); err != nil {
		return Config{}, err
	}
	return config, nil
}

var (
	ErrTooManyRules        = common.NewMessage("An audit policy holds at most 200 rules")
	ErrInvalidRule         = common.NewMessage("Audit policy rule ids must be unique, non-empty and at most 64 characters")
	ErrRuleNameTooLong     = common.NewMessage("Audit policy rule names are at most 128 characters")
	ErrNoteTooLong         = common.NewMessage("Audit policy list notes are at most 255 characters")
	ErrPolicyTooLarge      = common.NewMessage("The audit policy is too large to save; it must stay under 1 MB")
	ErrDegradedRulesRemain = common.NewMessage("Edit or delete the rules that could not be read before saving the audit policy")
	ErrInvalidPattern      = common.NewMessage("Audit policy group and model patterns are invalid")
	ErrInvalidIDs          = common.NewMessage("Audit policy user and token IDs must be positive")
	ErrInvalidList         = common.NewMessage("Audit policy list condition is invalid")
	ErrInvalidFormat       = common.NewMessage("Audit policy relay format is invalid")
	ErrInvalidAction       = common.NewMessage("Audit policy action is invalid")
	ErrInvalidLevel        = common.NewMessage("Audit policy trigger level or category is invalid")
	ErrInvalidScanScope    = common.NewMessage("Prompt audit scan scope is invalid")
	ErrInvalidFail         = common.NewMessage("Audit policy failure policy is invalid")
	ErrInvalidReroute      = common.NewMessage("A reroute rule needs a target model or group, and its fallback must be observe or block")
	ErrInvalidListEntry    = common.NewMessage("Audit policy list entries need a positive user ID")
	ErrInvalidSampling     = common.NewMessage("Content moderation sample rate must be between 0 and 100")
	ErrInvalidThreshold    = common.NewMessage("Content moderation thresholds must be between 0 and 1")
	ErrInvalidKeywords     = common.NewMessage("Content moderation keyword mode or list names are invalid")
)

// Normalize trims, deduplicates and validates the table in place.
func (c *Config) Normalize() error {
	c.Derived = false
	if len(c.Rules) > MaxRules {
		return ErrTooManyRules
	}
	ids := make([]string, 0, len(c.Rules)+1)
	for index := range c.Rules {
		rule := &c.Rules[index]
		if err := rule.normalize(false); err != nil {
			return err
		}
		if slices.Contains(ids, rule.ID) || rule.ID == DefaultRuleID {
			return ErrInvalidRule
		}
		ids = append(ids, rule.ID)
	}
	c.Default.ID = DefaultRuleID
	c.Default.Enabled = true
	c.Default.Match = Match{}
	if err := c.Default.normalize(true); err != nil {
		return err
	}
	var err error
	if c.Allowlist, err = normalizeEntries(c.Allowlist); err != nil {
		return err
	}
	if c.Blocklist, err = normalizeEntries(c.Blocklist); err != nil {
		return err
	}
	for index := range c.Blocklist {
		c.Blocklist[index].Verified = false
	}
	return nil
}

func normalizeEntries(entries []ListEntry) ([]ListEntry, error) {
	if len(entries) > MaxListEntries {
		return nil, ErrInvalidListEntry
	}
	normalized := make([]ListEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Note = strings.TrimSpace(entry.Note)
		if entry.UserID <= 0 || entry.TokenID < 0 {
			return nil, ErrInvalidListEntry
		}
		if utf8.RuneCountInString(entry.Note) > MaxNoteLength {
			return nil, ErrNoteTooLong
		}
		if slices.ContainsFunc(normalized, func(e ListEntry) bool { return e.UserID == entry.UserID && e.TokenID == entry.TokenID }) {
			continue
		}
		normalized = append(normalized, entry)
	}
	return normalized, nil
}

func (r *Rule) normalize(isDefault bool) error {
	// Only ParseDegraded marks rules degraded; a saved rule never is.
	r.Degraded = false
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	if r.ID == "" || len(r.ID) > MaxRuleIDLength {
		return ErrInvalidRule
	}
	if utf8.RuneCountInString(r.Name) > MaxRuleNameLength {
		return ErrRuleNameTooLong
	}
	if !isDefault {
		var err error
		if r.Match.Groups, err = normalizePatterns(r.Match.Groups); err != nil {
			return err
		}
		if r.Match.RequestedGroups, err = normalizePatterns(r.Match.RequestedGroups); err != nil {
			return err
		}
		if r.Match.Models, err = normalizePatterns(r.Match.Models); err != nil {
			return err
		}
		if r.Match.ExcludeModels, err = normalizePatterns(r.Match.ExcludeModels); err != nil {
			return err
		}
		if r.Match.UserIDs, err = normalizeIDs(r.Match.UserIDs); err != nil {
			return err
		}
		if r.Match.TokenIDs, err = normalizeIDs(r.Match.TokenIDs); err != nil {
			return err
		}
		if !slices.Contains(validLists, r.Match.List) {
			return ErrInvalidList
		}
		formats := make([]string, 0, len(r.Match.Formats))
		for _, format := range r.Match.Formats {
			format = strings.TrimSpace(format)
			if !slices.Contains(validRelayFormats, format) {
				return ErrInvalidFormat
			}
			if !slices.Contains(formats, format) {
				formats = append(formats, format)
			}
		}
		r.Match.Formats = formats
	}
	if err := r.PromptAudit.normalize(); err != nil {
		return err
	}
	if err := r.ContentModeration.normalize(); err != nil {
		return err
	}
	if r.RerouteFallback == "" {
		r.RerouteFallback = ActionObserve
	}
	r.Reroute.Model = strings.TrimSpace(r.Reroute.Model)
	r.Reroute.Group = strings.TrimSpace(r.Reroute.Group)
	if r.RerouteFallback != ActionObserve && r.RerouteFallback != ActionBlock ||
		len(r.Reroute.Model) > MaxPatternLength || len(r.Reroute.Group) > 64 ||
		strings.Contains(r.Reroute.Model, "*") || strings.Contains(r.Reroute.Group, "*") {
		return ErrInvalidReroute
	}
	if r.UsesReroute() && r.Reroute.Model == "" && r.Reroute.Group == "" {
		return ErrInvalidReroute
	}
	return nil
}

// UsesReroute reports whether any action of the rule is a reroute.
func (r *Rule) UsesReroute() bool {
	if r.PromptAudit.Enabled && (r.PromptAudit.Action == ActionReroute || slices.Contains(slices.Collect(maps.Values(r.PromptAudit.CategoryActions)), ActionReroute)) {
		return true
	}
	return r.ContentModeration.Enabled && r.ContentModeration.Action == ActionReroute
}

func normalizePatterns(patterns []string) ([]string, error) {
	if len(patterns) > MaxPatterns {
		return nil, ErrInvalidPattern
	}
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || slices.Contains(normalized, pattern) {
			continue
		}
		if len(pattern) > MaxPatternLength || strings.ContainsAny(pattern, "\t\r\n,") || strings.Trim(pattern, "*") == "" && pattern != "*" {
			return nil, ErrInvalidPattern
		}
		normalized = append(normalized, pattern)
	}
	return normalized, nil
}

func normalizeIDs(ids []int) ([]int, error) {
	if len(ids) > MaxIDsPerRule {
		return nil, ErrInvalidIDs
	}
	normalized := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrInvalidIDs
		}
		if !slices.Contains(normalized, id) {
			normalized = append(normalized, id)
		}
	}
	slices.Sort(normalized)
	return normalized, nil
}

func (p *PromptAudit) normalize() error {
	if p.Action == "" {
		p.Action = ActionObserve
	}
	if p.MinLevel == "" {
		p.MinLevel = LevelUnsafe
	}
	if p.FailPolicy == "" {
		p.FailPolicy = FailOpen
	}
	if !slices.Contains(validActions, p.Action) {
		return ErrInvalidAction
	}
	if p.MinLevel != LevelControversial && p.MinLevel != LevelUnsafe {
		return ErrInvalidLevel
	}
	if !slices.Contains(validFailPolicies, p.FailPolicy) {
		return ErrInvalidFail
	}
	if p.ScanScope != "" && !slices.Contains(prompt_audit_setting.ScanScopes, p.ScanScope) {
		return ErrInvalidScanScope
	}
	if p.CategoryLevels == nil {
		p.CategoryLevels = map[string]string{}
	}
	for category, level := range p.CategoryLevels {
		if !slices.Contains(prompt_audit_setting.ScannerIDs, category) || !slices.Contains(validLevels, level) {
			return ErrInvalidLevel
		}
	}
	if p.CategoryActions == nil {
		p.CategoryActions = map[string]string{}
	}
	for category, action := range p.CategoryActions {
		if !slices.Contains(prompt_audit_setting.ScannerIDs, category) || (action != ActionIgnore && !slices.Contains(validActions, action)) {
			return ErrInvalidAction
		}
	}
	return nil
}

func (m *ContentModeration) normalize() error {
	if m.Action == "" {
		m.Action = ActionObserve
	}
	if m.FailPolicy == "" {
		m.FailPolicy = FailOpen
	}
	if !slices.Contains(validActions, m.Action) {
		return ErrInvalidAction
	}
	if !slices.Contains(validFailPolicies, m.FailPolicy) {
		return ErrInvalidFail
	}
	if m.SampleRate < 0 || m.SampleRate > 100 {
		return ErrInvalidSampling
	}
	if m.KeywordMode != "" && !slices.Contains(validKeywordModes, m.KeywordMode) {
		return ErrInvalidKeywords
	}
	lists := make([]string, 0, len(m.KeywordLists))
	for _, name := range m.KeywordLists {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(lists, name) {
			continue
		}
		if !content_moderation_setting.ValidKeywordListName(name) {
			return ErrInvalidKeywords
		}
		lists = append(lists, name)
	}
	if len(lists) > content_moderation_setting.MaxKeywordLists {
		return ErrInvalidKeywords
	}
	if m.KeywordLists != nil || len(lists) > 0 {
		m.KeywordLists = lists
	}
	if m.KeywordMode != "" && m.KeywordMode != content_moderation_setting.KeywordModeAPIOnly && m.KeywordLists != nil && len(m.KeywordLists) == 0 {
		return ErrInvalidKeywords
	}
	for category, value := range m.Thresholds {
		if !slices.Contains(content_moderation_setting.Categories, category) || value < 0 || value > 1 {
			return ErrInvalidThreshold
		}
	}
	return nil
}

// Subject is what the rule table sees about a request.
type Subject struct {
	UserID  int
	TokenID int
	// Group is the group the request is billed to; for an "auto" token it is
	// the group the distributor chose, and RequestedGroup stays "auto".
	Group          string
	RequestedGroup string
	Model          string
	Format         string
	// AnyFormat matches every format condition. Only the admin test panel
	// sets it, for a request whose format was left open; relay requests
	// always match by their own format.
	AnyFormat bool
}

// Listing is a subject's place on the lists.
type Listing struct {
	Allowlisted bool
	Verified    bool
	Blocklisted bool
}

// Resolution is the rule that applies to a subject.
type Resolution struct {
	Rule *Rule
	// Index is the rule's position, or -1 for the default rule.
	Index   int
	Listing Listing
	// AnyFormat repeats Subject.AnyFormat for the caller.
	AnyFormat bool
}

// Resolve returns the first enabled rule that matches the subject, or the
// default rule.
func (c *Config) Resolve(subject Subject) Resolution {
	listing := Listing{}
	if entry, ok := findEntry(c.Allowlist, subject); ok {
		listing.Allowlisted, listing.Verified = true, entry.Verified
	}
	_, listing.Blocklisted = findEntry(c.Blocklist, subject)
	for index := range c.Rules {
		rule := &c.Rules[index]
		if rule.Enabled && rule.Match.matches(subject, listing) {
			return Resolution{Rule: rule, Index: index, Listing: listing, AnyFormat: subject.AnyFormat}
		}
	}
	return Resolution{Rule: &c.Default, Index: -1, Listing: listing, AnyFormat: subject.AnyFormat}
}

// findEntry finds a token entry first, then a whole-user entry.
func findEntry(entries []ListEntry, subject Subject) (ListEntry, bool) {
	var userEntry *ListEntry
	for index := range entries {
		entry := &entries[index]
		if entry.UserID != subject.UserID {
			continue
		}
		if entry.TokenID != 0 && entry.TokenID == subject.TokenID {
			return *entry, true
		}
		if entry.TokenID == 0 && userEntry == nil {
			userEntry = entry
		}
	}
	if userEntry != nil {
		return *userEntry, true
	}
	return ListEntry{}, false
}

func (m *Match) matches(subject Subject, listing Listing) bool {
	if len(m.Groups) > 0 && !MatchesAny(m.Groups, subject.Group) &&
		(subject.RequestedGroup == "" || !MatchesAny(m.Groups, subject.RequestedGroup)) {
		return false
	}
	if len(m.RequestedGroups) > 0 && !MatchesAny(m.RequestedGroups, subject.RequestedGroup) {
		return false
	}
	if len(m.Models) > 0 && !MatchesAny(m.Models, subject.Model) {
		return false
	}
	if MatchesAny(m.ExcludeModels, subject.Model) {
		return false
	}
	if len(m.UserIDs) > 0 && !slices.Contains(m.UserIDs, subject.UserID) {
		return false
	}
	if len(m.TokenIDs) > 0 && !slices.Contains(m.TokenIDs, subject.TokenID) {
		return false
	}
	if len(m.Formats) > 0 && !subject.AnyFormat && !slices.Contains(m.Formats, subject.Format) {
		return false
	}
	switch m.List {
	case ListAllowlist:
		return listing.Allowlisted
	case ListVerified:
		return listing.Verified
	case ListBlocklist:
		return listing.Blocklisted
	case ListUnlisted:
		return !listing.Allowlisted && !listing.Blocklisted
	}
	return true
}

// MatchesAny reports whether value matches one of the patterns.
func MatchesAny(patterns []string, value string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool { return Glob(pattern, value) })
}

// Glob matches value against a pattern where "*" stands for any run of
// characters, including none. Every other character matches itself.
func Glob(pattern, value string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(value, part)
		if index < 0 {
			return false
		}
		value = value[index+len(part):]
	}
	return strings.HasSuffix(value, last)
}

// Level returns the trigger level of a prompt audit category.
func (p *PromptAudit) Level(category string) string {
	if level, ok := p.CategoryLevels[category]; ok {
		return level
	}
	return p.MinLevel
}

// ActionFor returns the action for the triggered categories: the most severe
// per-category action, or the rule action when none of them has one. It
// returns ActionIgnore when every triggered category is ignored.
func (p *PromptAudit) ActionFor(triggered []string) string {
	if len(triggered) == 0 {
		return p.Action
	}
	action := ActionIgnore
	for _, category := range triggered {
		candidate, ok := p.CategoryActions[category]
		if !ok {
			candidate = p.Action
		}
		if actionSeverityRank[candidate] > actionSeverityRank[action] {
			action = candidate
		}
	}
	return action
}

// Clone returns a deep copy.
func (c Config) Clone() Config {
	clone := c
	clone.Rules = make([]Rule, len(c.Rules))
	for index, rule := range c.Rules {
		clone.Rules[index] = rule.clone()
	}
	clone.Default = c.Default.clone()
	clone.Allowlist = slices.Clone(c.Allowlist)
	clone.Blocklist = slices.Clone(c.Blocklist)
	return clone
}

func (r Rule) clone() Rule {
	clone := r
	clone.Match.Groups = slices.Clone(r.Match.Groups)
	clone.Match.RequestedGroups = slices.Clone(r.Match.RequestedGroups)
	clone.Match.Models = slices.Clone(r.Match.Models)
	clone.Match.ExcludeModels = slices.Clone(r.Match.ExcludeModels)
	clone.Match.UserIDs = slices.Clone(r.Match.UserIDs)
	clone.Match.TokenIDs = slices.Clone(r.Match.TokenIDs)
	clone.Match.Formats = slices.Clone(r.Match.Formats)
	clone.PromptAudit.CategoryLevels = maps.Clone(r.PromptAudit.CategoryLevels)
	clone.PromptAudit.CategoryActions = maps.Clone(r.PromptAudit.CategoryActions)
	clone.ContentModeration.KeywordLists = slices.Clone(r.ContentModeration.KeywordLists)
	clone.ContentModeration.Thresholds = maps.Clone(r.ContentModeration.Thresholds)
	return clone
}
