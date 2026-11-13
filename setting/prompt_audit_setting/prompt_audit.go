// Package prompt_audit_setting holds the prompt audit configuration stored in
// the options table and the in-process snapshot read on the relay path.
package prompt_audit_setting

import (
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const (
	// OptionKey stores the non-secret configuration as JSON.
	OptionKey = "PromptAuditSetting"
	// TokensOptionKey stores Guard bearer tokens as a JSON object keyed by
	// endpoint id. The Secret suffix keeps it out of GET /api/option.
	TokensOptionKey = "PromptAuditGuardTokensSecret"

	// ModeAsync audits in the background and never blocks. ModeBlocking lets
	// audit policy rules check requests before billing and block or reroute
	// them; rules whose action is observe still audit in the background.
	ModeAsync    = "async_audit"
	ModeBlocking = "blocking"

	// Failure policies of a synchronous check when the Guard cannot answer.
	FailOpen   = "open"
	FailClosed = "closed"

	// GlobalConcurrency and EndpointConcurrency bound synchronous checks in
	// flight; a full bulkhead counts as the Guard being unavailable.
	GlobalConcurrency   = 64
	EndpointConcurrency = 16

	DefaultWorkerCount   = 2
	MaxWorkerCount       = 8
	DefaultQueueCapacity = 1024
	MaxQueueCapacity     = 10000
	DefaultRetentionDays = 30
	MaxRetentionDays     = 365
	DefaultTimeoutMS     = 3000
	MinTimeoutMS         = 100
	MaxTimeoutMS         = 30000
	DefaultInputLimit    = 4000
	MinInputLimit        = 128
	MaxInputLimit        = 100000
	MaxEndpoints         = 8
	MaxGroups            = 200
	// DefaultMaxChunks bounds Guard calls per request: the latest user turn
	// first, then earlier text from newest to oldest.
	DefaultMaxChunks  = 8
	MaxMaxChunks      = 100
	DefaultGuardModel = "sileader/qwen3guard:0.6b"

	// Scan scopes choose which extracted text is sent to the Guard. Stored
	// prompts and hashes always cover the whole request.
	ScanScopeLatestUser = "latest_user"
	ScanScopeUser       = "user"
	ScanScopeAll        = "all"
	DefaultScanScope    = ScanScopeLatestUser
)

// ScanScopes lists the valid scan scopes in display order.
var ScanScopes = []string{ScanScopeLatestUser, ScanScopeUser, ScanScopeAll}

// ScannerIDs are the Qwen3Guard risk categories in display order.
var ScannerIDs = []string{
	"violent",
	"non_violent_illegal_acts",
	"sexual_content_or_sexual_acts",
	"pii",
	"suicide_and_self_harm",
	"unethical_acts",
	"politically_sensitive_topics",
	"copyright_violation",
	"jailbreak",
}

type Endpoint struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	TimeoutMS  int    `json:"timeout_ms"`
	InputLimit int    `json:"input_limit"`
	Enabled    bool   `json:"enabled"`
}

type Config struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	// AllGroups audits every group; otherwise Groups lists exact group names
	// or prefixes ending in "*" (for example "claude-ch*").
	AllGroups       bool     `json:"all_groups"`
	Groups          []string `json:"groups"`
	Scanners        []string `json:"scanners"`
	StoreFullPrompt bool     `json:"store_full_prompt"`
	RetentionDays   int      `json:"retention_days"`
	WorkerCount     int      `json:"worker_count"`
	QueueCapacity   int      `json:"queue_capacity"`
	// MaxChunksPerRequest limits how many chunks of one request are sent to
	// the Guard; chunks recently judged safe do not count.
	MaxChunksPerRequest int `json:"max_chunks_per_request"`
	// ScanScope is latest_user (only the newest user message), user (every
	// user message) or all (system, developer, assistant and tool text too).
	ScanScope string `json:"scan_scope"`
	// StrictJailbreakMapping records a Controversial jailbreak verdict as
	// critical, as before; by default it is a flagged warning.
	StrictJailbreakMapping bool `json:"strict_jailbreak_mapping"`
	// FailPolicy is the failure policy of the rules derived from these
	// settings while no audit policy table is saved.
	FailPolicy string `json:"fail_policy"`
	// StorePassEvents also stores results the Guard judged safe.
	StorePassEvents bool       `json:"store_pass_events"`
	Endpoints       []Endpoint `json:"endpoints"`
	ConfigVersion   int64      `json:"config_version"`
	UpdatedAt       int64      `json:"updated_at"`
	UpdatedBy       int        `json:"updated_by"`
}

// Snapshot is the immutable runtime view: configuration plus Guard tokens.
// Degraded means the stored configuration could not be read while it asked
// for blocking: the engine stays off, and blocking checks it should have run
// follow their failure policy instead of passing silently.
type Snapshot struct {
	Config   Config
	Tokens   map[string]string
	Degraded bool
}

var current atomic.Pointer[Snapshot]

func init() {
	current.Store(&Snapshot{Config: DefaultConfig(), Tokens: map[string]string{}})
}

func DefaultConfig() Config {
	return Config{
		Mode:                ModeAsync,
		AllGroups:           true,
		Groups:              []string{},
		Scanners:            slices.Clone(ScannerIDs),
		StoreFullPrompt:     true,
		RetentionDays:       DefaultRetentionDays,
		WorkerCount:         DefaultWorkerCount,
		QueueCapacity:       DefaultQueueCapacity,
		MaxChunksPerRequest: DefaultMaxChunks,
		ScanScope:           DefaultScanScope,
		FailPolicy:          FailOpen,
		Endpoints:           []Endpoint{},
	}
}

// Current returns the active snapshot. Callers must not modify it.
func Current() *Snapshot { return current.Load() }

func IsOptionKey(key string) bool { return key == OptionKey || key == TokensOptionKey }

// ApplyOption refreshes the snapshot after an option write or a periodic sync.
// An unreadable stored value falls back to a disabled configuration so a
// broken option never audits with half-parsed settings.
func ApplyOption(key, value string) {
	previous := Current()
	next := &Snapshot{Config: previous.Config, Tokens: previous.Tokens, Degraded: previous.Degraded}
	switch key {
	case OptionKey:
		config, err := ParseConfig(value)
		next.Degraded = false
		if err != nil {
			common.SysError(common.LogText("prompt audit setting is invalid, auditing disabled: %s", err.Error()))
			config = DefaultConfig()
			// Keep the stored intent: a broken blocking setting must not turn
			// into "audit nothing" without the failure policy deciding.
			var intent struct {
				Enabled    bool   `json:"enabled"`
				Mode       string `json:"mode"`
				FailPolicy string `json:"fail_policy"`
			}
			if common.UnmarshalJsonStr(value, &intent) == nil && intent.Enabled && intent.Mode == ModeBlocking {
				next.Degraded = true
				config.Mode = ModeBlocking
				if intent.FailPolicy == FailClosed {
					config.FailPolicy = FailClosed
				}
			}
		}
		next.Config = config
	case TokensOptionKey:
		next.Tokens = ParseTokens(value)
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

func ParseTokens(value string) map[string]string {
	tokens := map[string]string{}
	if strings.TrimSpace(value) == "" {
		return tokens
	}
	if err := common.UnmarshalJsonStr(value, &tokens); err != nil {
		return map[string]string{}
	}
	return tokens
}

var (
	ErrInvalidMode          = common.NewMessage("Prompt audit mode is invalid")
	ErrInvalidScanScope     = common.NewMessage("Prompt audit scan scope is invalid")
	ErrInvalidFailPolicy    = common.NewMessage("Prompt audit failure policy is invalid")
	ErrGroupsRequired       = common.NewMessage("Select at least one group or audit all groups")
	ErrInvalidGroup         = common.NewMessage("Prompt audit group names are invalid")
	ErrInvalidScanner       = common.NewMessage("Prompt audit risk categories are invalid")
	ErrInvalidLimits        = common.NewMessage("Prompt audit worker, queue, or retention settings are out of range")
	ErrInvalidEndpoint      = common.NewMessage("Prompt audit Guard endpoint settings are invalid")
	ErrInvalidBaseURL       = common.NewMessage("Guard endpoint address must be an http or https URL without credentials, query, or fragment")
	ErrEnabledEndpointNeeds = common.NewMessage("Enable at least one Guard endpoint before turning on prompt audit")
)

// Normalize trims, deduplicates and validates the configuration in place.
func (c *Config) Normalize() error {
	if c.Mode == "" {
		c.Mode = ModeAsync
	}
	if c.Mode != ModeAsync && c.Mode != ModeBlocking {
		return ErrInvalidMode
	}
	if c.FailPolicy == "" {
		c.FailPolicy = FailOpen
	}
	if c.FailPolicy != FailOpen && c.FailPolicy != FailClosed {
		return ErrInvalidFailPolicy
	}
	if c.ScanScope == "" {
		c.ScanScope = DefaultScanScope
	}
	if !slices.Contains(ScanScopes, c.ScanScope) {
		return ErrInvalidScanScope
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
	scanners := make([]string, 0, len(c.Scanners))
	for _, id := range ScannerIDs {
		if slices.Contains(c.Scanners, id) {
			scanners = append(scanners, id)
		}
	}
	if len(scanners) != len(c.Scanners) {
		for _, id := range c.Scanners {
			if !slices.Contains(ScannerIDs, id) {
				return ErrInvalidScanner
			}
		}
	}
	c.Scanners = scanners
	if c.MaxChunksPerRequest == 0 {
		c.MaxChunksPerRequest = DefaultMaxChunks
	}
	if c.MaxChunksPerRequest < 1 || c.MaxChunksPerRequest > MaxMaxChunks ||
		c.WorkerCount < 1 || c.WorkerCount > MaxWorkerCount ||
		c.QueueCapacity < 1 || c.QueueCapacity > MaxQueueCapacity ||
		c.RetentionDays < 1 || c.RetentionDays > MaxRetentionDays {
		return ErrInvalidLimits
	}
	if len(c.Endpoints) > MaxEndpoints {
		return ErrInvalidEndpoint
	}
	ids := make([]string, 0, len(c.Endpoints))
	hasEnabled := false
	for index := range c.Endpoints {
		endpoint := &c.Endpoints[index]
		endpoint.ID = strings.TrimSpace(endpoint.ID)
		endpoint.Name = strings.TrimSpace(endpoint.Name)
		endpoint.Model = strings.TrimSpace(endpoint.Model)
		if endpoint.ID == "" || len(endpoint.ID) > 64 || slices.Contains(ids, endpoint.ID) ||
			len(endpoint.Name) > 128 || endpoint.Model == "" || len(endpoint.Model) > 255 {
			return ErrInvalidEndpoint
		}
		ids = append(ids, endpoint.ID)
		baseURL, err := NormalizeBaseURL(endpoint.BaseURL)
		if err != nil {
			return err
		}
		endpoint.BaseURL = baseURL
		if endpoint.TimeoutMS == 0 {
			endpoint.TimeoutMS = DefaultTimeoutMS
		}
		if endpoint.InputLimit == 0 {
			endpoint.InputLimit = DefaultInputLimit
		}
		if endpoint.TimeoutMS < MinTimeoutMS || endpoint.TimeoutMS > MaxTimeoutMS ||
			endpoint.InputLimit < MinInputLimit || endpoint.InputLimit > MaxInputLimit {
			return ErrInvalidEndpoint
		}
		hasEnabled = hasEnabled || endpoint.Enabled
	}
	if c.Enabled && !hasEnabled {
		return ErrEnabledEndpointNeeds
	}
	return nil
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

// EnabledEndpoints returns the endpoints used for scanning, in failover order.
func (c *Config) EnabledEndpoints() []Endpoint {
	endpoints := make([]Endpoint, 0, len(c.Endpoints))
	for _, endpoint := range c.Endpoints {
		if endpoint.Enabled {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// NormalizeBaseURL accepts an http(s) origin with an optional path, rejects
// credentials, query and fragment, and drops a trailing "/v1". Guard nodes are
// expected on private or loopback addresses, so no SSRF address filtering is
// applied here; only root administrators can configure them.
func NormalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
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
