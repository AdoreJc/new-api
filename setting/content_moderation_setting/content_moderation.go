// Package content_moderation_setting holds the content moderation
// configuration stored in the options table and the in-process snapshot read
// on the relay path.
package content_moderation_setting

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	// OptionKey stores the non-secret configuration as JSON.
	OptionKey = "ContentModerationSetting"
	// KeysOptionKey stores the moderation API keys as a JSON array. The Secret
	// suffix keeps it out of GET /api/option.
	KeysOptionKey = "ContentModerationAPIKeysSecret"

	ModeOff      = "off"
	ModeObserve  = "observe"
	ModePreBlock = "pre_block"

	KeywordModeAPIOnly       = "api_only"
	KeywordModeKeywordOnly   = "keyword_only"
	KeywordModeKeywordAndAPI = "keyword_and_api"
	KeywordModeKeywordGated  = "keyword_gated_api"

	// DefaultKeywordList names the stored list that an upgrade fills with the
	// words of the retired sensitive word check, which used to be read in
	// place under this name. It is an ordinary list otherwise.
	DefaultKeywordList    = "default"
	MaxKeywordLists       = 50
	MaxKeywordsPerList    = 10000
	MaxKeywordRunes       = 200
	MaxKeywordListNameLen = 64

	ModelFilterAll     = "all"
	ModelFilterInclude = "include"
	ModelFilterExclude = "exclude"

	DefaultBaseURL             = "https://api.openai.com"
	DefaultModel               = "omni-moderation-latest"
	DefaultTimeoutMS           = 3000
	MinTimeoutMS               = 100
	MaxTimeoutMS               = 30000
	DefaultRetryCount          = 2
	MaxRetryCount              = 5
	DefaultWorkerCount         = 4
	MaxWorkerCount             = 32
	DefaultQueueSize           = 1024
	MaxQueueSize               = 10000
	DefaultHitRetentionDays    = 180
	MaxHitRetentionDays        = 3650
	DefaultNonHitRetentionDays = 3
	MaxNonHitRetentionDays     = 3
	DefaultDisableThreshold    = 10
	MaxDisableThreshold        = 10000
	DefaultViolationWindowHrs  = 720
	MaxViolationWindowHrs      = 24 * 365
	MaxGroups                  = 200
	MaxModels                  = 1000
	MaxAllowlistUsers          = 1000
	MaxAPIKeys                 = 32
	MaxAPIKeyLength            = 512
)

// Categories are the OpenAI moderation categories in display order.
var Categories = []string{
	"harassment",
	"harassment/threatening",
	"hate",
	"hate/threatening",
	"illicit",
	"illicit/violent",
	"self-harm",
	"self-harm/intent",
	"self-harm/instructions",
	"sexual",
	"sexual/minors",
	"violence",
	"violence/graphic",
}

// DefaultThresholds are the per-category scores at or above which an input is
// flagged; the values follow the original risk control defaults.
func DefaultThresholds() map[string]float64 {
	return map[string]float64{
		"harassment":             0.98,
		"harassment/threatening": 0.90,
		"hate":                   0.65,
		"hate/threatening":       0.65,
		"illicit":                0.95,
		"illicit/violent":        0.95,
		"self-harm":              0.65,
		"self-harm/intent":       0.85,
		"self-harm/instructions": 0.65,
		"sexual":                 0.65,
		"sexual/minors":          0.65,
		"violence":               0.95,
		"violence/graphic":       0.95,
	}
}

type ModelFilter struct {
	Type   string   `json:"type"`
	Models []string `json:"models"`
}

type Config struct {
	Mode    string `json:"mode"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// TimeoutMS bounds one moderation call; RetryCount more calls may follow
	// with the next usable key.
	TimeoutMS  int `json:"timeout_ms"`
	RetryCount int `json:"retry_count"`
	// IncludeImages sends at most one image of the latest user message along
	// with its text. Models named text-moderation-* never receive images.
	IncludeImages bool `json:"include_images"`
	// SampleRate is the percentage of inputs checked, chosen by input hash so
	// the same input always gets the same answer.
	SampleRate int `json:"sample_rate"`
	// AllGroups checks every group; otherwise Groups lists exact group names
	// or prefixes ending in "*".
	AllGroups   bool               `json:"all_groups"`
	Groups      []string           `json:"groups"`
	ModelFilter ModelFilter        `json:"model_filter"`
	Thresholds  map[string]float64 `json:"thresholds"`
	// WorkerCount and QueueSize size the in-memory queue used by observe mode
	// and by allowlisted users.
	WorkerCount int `json:"worker_count"`
	QueueSize   int `json:"queue_size"`
	// RecordNonHits also stores inputs that passed and failed calls; they are
	// kept for at most NonHitRetentionDays.
	RecordNonHits       bool `json:"record_non_hits"`
	HitRetentionDays    int  `json:"hit_retention_days"`
	NonHitRetentionDays int  `json:"non_hit_retention_days"`
	// HashBlocklistEnabled remembers the hash of every flagged input; the same
	// input is then blocked (pre_block) or recorded (observe) without a call.
	HashBlocklistEnabled bool `json:"hash_blocklist_enabled"`
	// EmailOnHit mails the user on each flagged input (at most once per hour).
	EmailOnHit bool `json:"email_on_hit"`
	// AutoDisableEnabled disables a user once DisableThreshold flagged inputs
	// fall within ViolationWindowHours since the user was last disabled.
	AutoDisableEnabled   bool `json:"auto_disable_enabled"`
	DisableThreshold     int  `json:"disable_threshold"`
	ViolationWindowHours int  `json:"violation_window_hours"`
	// KeywordMode, for the keywords of KeywordLists:
	//   - api_only: no keyword check; inputs go to the engine.
	//   - keyword_only: an input with a keyword is flagged without an engine
	//     call; other inputs are not checked.
	//   - keyword_and_api: an input with a keyword is flagged without an
	//     engine call; other inputs go to the engine as in api_only.
	//   - keyword_gated_api: only inputs with a keyword go to the engine,
	//     which decides; other inputs are not checked.
	KeywordMode  string   `json:"keyword_mode"`
	KeywordLists []string `json:"keyword_lists"`
	// AllowlistUserIDs are only recorded: never blocked, mailed or disabled.
	AllowlistUserIDs []int `json:"allowlist_user_ids"`
	ConfigVersion    int64 `json:"config_version"`
	UpdatedAt        int64 `json:"updated_at"`
	UpdatedBy        int   `json:"updated_by"`
}

// Snapshot is the immutable runtime view: configuration plus API keys.
type Snapshot struct {
	Config Config
	Keys   []string
}

var current atomic.Pointer[Snapshot]

func init() {
	current.Store(&Snapshot{Config: DefaultConfig(), Keys: []string{}})
}

func DefaultConfig() Config {
	return Config{
		Mode:                 ModeOff,
		BaseURL:              DefaultBaseURL,
		Model:                DefaultModel,
		TimeoutMS:            DefaultTimeoutMS,
		RetryCount:           DefaultRetryCount,
		IncludeImages:        true,
		SampleRate:           100,
		AllGroups:            true,
		Groups:               []string{},
		ModelFilter:          ModelFilter{Type: ModelFilterAll, Models: []string{}},
		Thresholds:           DefaultThresholds(),
		WorkerCount:          DefaultWorkerCount,
		QueueSize:            DefaultQueueSize,
		HitRetentionDays:     DefaultHitRetentionDays,
		NonHitRetentionDays:  DefaultNonHitRetentionDays,
		DisableThreshold:     DefaultDisableThreshold,
		ViolationWindowHours: DefaultViolationWindowHrs,
		KeywordMode:          KeywordModeAPIOnly,
		KeywordLists:         []string{},
		AllowlistUserIDs:     []int{},
	}
}

// Current returns the active snapshot. Callers must not modify it.
func Current() *Snapshot { return current.Load() }

func IsOptionKey(key string) bool { return key == OptionKey || key == KeysOptionKey }

// ApplyOption refreshes the snapshot after an option write or a periodic sync.
// An unreadable stored configuration falls back to the defaults (mode off) so
// a broken option never moderates with half-parsed settings.
func ApplyOption(key, value string) {
	previous := Current()
	next := &Snapshot{Config: previous.Config, Keys: previous.Keys}
	switch key {
	case OptionKey:
		config, err := ParseConfig(value)
		if err != nil {
			common.SysError(common.LogText("content moderation setting is invalid, moderation disabled: %s", err.Error()))
			config = DefaultConfig()
		}
		next.Config = config
	case KeysOptionKey:
		next.Keys = ParseKeys(value)
	default:
		return
	}
	current.Store(next)
}

// ParseConfig decodes a stored configuration over the defaults, so keys added
// later keep their default values for configurations saved before them.
func ParseConfig(value string) (Config, error) {
	config := DefaultConfig()
	if strings.TrimSpace(value) == "" {
		return config, nil
	}
	if err := common.UnmarshalJsonStr(value, &config); err != nil {
		return DefaultConfig(), err
	}
	if err := config.Normalize(); err != nil {
		return DefaultConfig(), err
	}
	return config, nil
}

// ParseKeys decodes stored API keys, dropping blanks and duplicates.
func ParseKeys(value string) []string {
	var keys []string
	if strings.TrimSpace(value) == "" || common.UnmarshalJsonStr(value, &keys) != nil {
		return []string{}
	}
	return NormalizeKeys(keys)
}

// NormalizeKeys trims keys and removes blanks and duplicates, keeping order.
func NormalizeKeys(keys []string) []string {
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" && !slices.Contains(normalized, key) {
			normalized = append(normalized, key)
		}
	}
	return normalized
}

// KeyID identifies an API key without revealing any of it: the first twelve
// hex digits of its SHA-256 digest.
func KeyID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])[:12]
}

var (
	ErrInvalidMode        = common.NewMessage("Content moderation mode is invalid")
	ErrGroupsRequired     = common.NewMessage("Select at least one group or moderate all groups")
	ErrInvalidGroup       = common.NewMessage("Content moderation group names are invalid")
	ErrInvalidModelFilter = common.NewMessage("Content moderation model filter is invalid")
	ErrInvalidThreshold   = common.NewMessage("Content moderation thresholds must be between 0 and 1")
	ErrInvalidLimits      = common.NewMessage("Content moderation numeric settings are out of range")
	ErrInvalidModel       = common.NewMessage("Content moderation model name is invalid")
	ErrInvalidAllowlist   = common.NewMessage("Content moderation allowlist must contain positive user IDs")
	ErrInvalidBaseURL     = common.NewMessage("Moderation API address must be an http or https URL without credentials, query, or fragment")
	ErrInvalidAPIKey      = common.NewMessage("Moderation API keys are invalid")
	ErrInvalidKeywordMode = common.NewMessage("Content moderation keyword mode is invalid")
	ErrInvalidKeywordList = common.NewMessage("Keyword list names may use letters, digits, dot, dash and underscore, up to 64 characters")
	ErrKeywordListsNeeded = common.NewMessage("Select at least one keyword list for the keyword mode")
	ErrInvalidKeywords    = common.NewMessage("A keyword list holds at most 10000 keywords of up to 200 characters each")
)

// Normalize trims, deduplicates and validates the configuration in place.
func (c *Config) Normalize() error {
	if c.Mode == "" {
		c.Mode = ModeOff
	}
	if c.Mode != ModeOff && c.Mode != ModeObserve && c.Mode != ModePreBlock {
		return ErrInvalidMode
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return err
	}
	c.BaseURL = baseURL
	c.Model = strings.TrimSpace(c.Model)
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if len(c.Model) > 255 || strings.ContainsAny(c.Model, " \t\r\n") {
		return ErrInvalidModel
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = DefaultTimeoutMS
	}
	if c.WorkerCount == 0 {
		c.WorkerCount = DefaultWorkerCount
	}
	if c.QueueSize == 0 {
		c.QueueSize = DefaultQueueSize
	}
	if c.HitRetentionDays == 0 {
		c.HitRetentionDays = DefaultHitRetentionDays
	}
	if c.NonHitRetentionDays == 0 {
		c.NonHitRetentionDays = DefaultNonHitRetentionDays
	}
	if c.DisableThreshold == 0 {
		c.DisableThreshold = DefaultDisableThreshold
	}
	if c.ViolationWindowHours == 0 {
		c.ViolationWindowHours = DefaultViolationWindowHrs
	}
	if c.TimeoutMS < MinTimeoutMS || c.TimeoutMS > MaxTimeoutMS ||
		c.RetryCount < 0 || c.RetryCount > MaxRetryCount ||
		c.SampleRate < 0 || c.SampleRate > 100 ||
		c.WorkerCount < 1 || c.WorkerCount > MaxWorkerCount ||
		c.QueueSize < 1 || c.QueueSize > MaxQueueSize ||
		c.HitRetentionDays < 1 || c.HitRetentionDays > MaxHitRetentionDays ||
		c.NonHitRetentionDays < 1 || c.NonHitRetentionDays > MaxNonHitRetentionDays ||
		c.DisableThreshold < 1 || c.DisableThreshold > MaxDisableThreshold ||
		c.ViolationWindowHours < 1 || c.ViolationWindowHours > MaxViolationWindowHrs {
		return ErrInvalidLimits
	}
	groups := make([]string, 0, len(c.Groups))
	for _, group := range c.Groups {
		group = strings.TrimSpace(group)
		if group == "" || slices.Contains(groups, group) {
			continue
		}
		name, _ := strings.CutSuffix(group, "*")
		if name == "" || strings.ContainsAny(name, "* \t\r\n,") || len(group) > 64 {
			return ErrInvalidGroup
		}
		groups = append(groups, group)
	}
	if len(groups) > MaxGroups {
		return ErrInvalidGroup
	}
	c.Groups = groups
	if !c.AllGroups && len(c.Groups) == 0 {
		return ErrGroupsRequired
	}
	if c.ModelFilter.Type == "" {
		c.ModelFilter.Type = ModelFilterAll
	}
	if c.ModelFilter.Type != ModelFilterAll && c.ModelFilter.Type != ModelFilterInclude && c.ModelFilter.Type != ModelFilterExclude {
		return ErrInvalidModelFilter
	}
	models := make([]string, 0, len(c.ModelFilter.Models))
	for _, name := range c.ModelFilter.Models {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(models, name) {
			continue
		}
		if len(name) > 255 {
			return ErrInvalidModelFilter
		}
		models = append(models, name)
	}
	if len(models) > MaxModels || (c.ModelFilter.Type != ModelFilterAll && len(models) == 0) {
		return ErrInvalidModelFilter
	}
	c.ModelFilter.Models = models
	thresholds := DefaultThresholds()
	for category, value := range c.Thresholds {
		if !slices.Contains(Categories, category) {
			return ErrInvalidThreshold
		}
		if value < 0 || value > 1 {
			return ErrInvalidThreshold
		}
		thresholds[category] = value
	}
	c.Thresholds = thresholds
	allowlist := make([]int, 0, len(c.AllowlistUserIDs))
	for _, id := range c.AllowlistUserIDs {
		if id <= 0 {
			return ErrInvalidAllowlist
		}
		if !slices.Contains(allowlist, id) {
			allowlist = append(allowlist, id)
		}
	}
	if len(allowlist) > MaxAllowlistUsers {
		return ErrInvalidAllowlist
	}
	slices.Sort(allowlist)
	c.AllowlistUserIDs = allowlist
	if c.KeywordMode == "" {
		c.KeywordMode = KeywordModeAPIOnly
	}
	if !slices.Contains([]string{KeywordModeAPIOnly, KeywordModeKeywordOnly, KeywordModeKeywordAndAPI, KeywordModeKeywordGated}, c.KeywordMode) {
		return ErrInvalidKeywordMode
	}
	lists := make([]string, 0, len(c.KeywordLists))
	for _, name := range c.KeywordLists {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(lists, name) {
			continue
		}
		if !ValidKeywordListName(name) {
			return ErrInvalidKeywordList
		}
		lists = append(lists, name)
	}
	if len(lists) > MaxKeywordLists {
		return ErrInvalidKeywordList
	}
	if c.KeywordMode != KeywordModeAPIOnly && len(lists) == 0 {
		return ErrKeywordListsNeeded
	}
	c.KeywordLists = lists
	return nil
}

// ValidKeywordListName accepts 1 to 64 letters, digits, '.', '-' and '_'.
func ValidKeywordListName(name string) bool {
	if name == "" || len(name) > MaxKeywordListNameLen {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// NormalizeKeywords splits a list typed one keyword per line, trims it and
// drops blanks and case-insensitive duplicates, keeping order.
func NormalizeKeywords(raw string) ([]string, error) {
	keywords := make([]string, 0)
	seen := map[string]struct{}{}
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > MaxKeywordRunes {
			return nil, ErrInvalidKeywords
		}
		key := strings.ToLower(line)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keywords = append(keywords, line)
	}
	if len(keywords) > MaxKeywordsPerList {
		return nil, ErrInvalidKeywords
	}
	return keywords, nil
}

// Clone returns a copy that shares no slices or maps with c.
func (c Config) Clone() Config {
	clone := c
	clone.Groups = slices.Clone(c.Groups)
	clone.ModelFilter.Models = slices.Clone(c.ModelFilter.Models)
	clone.Thresholds = maps.Clone(c.Thresholds)
	clone.AllowlistUserIDs = slices.Clone(c.AllowlistUserIDs)
	clone.KeywordLists = slices.Clone(c.KeywordLists)
	return clone
}

// IncludesGroup reports whether requests using the group are in scope.
func (c *Config) IncludesGroup(group string) bool {
	if c.AllGroups {
		return true
	}
	for _, pattern := range c.Groups {
		if prefix, isPrefix := strings.CutSuffix(pattern, "*"); isPrefix {
			if strings.HasPrefix(group, prefix) {
				return true
			}
			continue
		}
		if pattern == group {
			return true
		}
	}
	return false
}

// IncludesModel applies the model include or exclude list.
func (c *Config) IncludesModel(model string) bool {
	switch c.ModelFilter.Type {
	case ModelFilterInclude:
		return slices.Contains(c.ModelFilter.Models, model)
	case ModelFilterExclude:
		return !slices.Contains(c.ModelFilter.Models, model)
	}
	return true
}

// Allowlisted reports whether the user is only recorded.
func (c *Config) Allowlisted(userID int) bool {
	_, found := slices.BinarySearch(c.AllowlistUserIDs, userID)
	return found
}

// SupportsImages reports whether the configured model accepts image input.
func (c *Config) SupportsImages() bool {
	return c.IncludeImages && !strings.HasPrefix(c.Model, "text-moderation")
}

// NormalizeBaseURL accepts an http(s) origin with an optional path, rejects
// credentials, query and fragment, and drops a trailing "/v1". Only root
// administrators can set it, and self-hosted engines may sit on private
// addresses, so no SSRF address filtering is applied here.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return "", ErrInvalidBaseURL
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidBaseURL
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", ErrInvalidBaseURL
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.EqualFold(path, "/v1") {
		path = ""
	}
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}
