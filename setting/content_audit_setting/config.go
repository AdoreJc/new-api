// Package content_audit_setting holds the content audit configuration and its
// hot-reloaded snapshot. The configuration lives in two options: a
// non-sensitive JSON document and a separate map of Guard endpoint tokens.
// Neither may be written through the generic option API.
package content_audit_setting

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const (
	ConfigOptionKey = "ContentAuditSetting"
	// TokensOptionKey ends with "Secret" so the generic option listing never
	// returns it.
	TokensOptionKey = "ContentAuditGuardTokensSecret"
)

const (
	DefaultWorkerCount    = 2
	MaxWorkerCount        = 8
	DefaultQueueCapacity  = 1000
	MaxQueueCapacity      = 20000
	DefaultQueueMaxMB     = 64
	MaxQueueMaxMB         = 1024
	DefaultTimeoutMS      = 3000
	MinTimeoutMS          = 100
	MaxTimeoutMS          = 30000
	DefaultMaxChunkRunes  = 4000
	MinMaxChunkRunes      = 128
	MaxMaxChunkRunes      = 100000
	DefaultMaxBodyMB      = 8
	MaxMaxBodyMB          = 64
	DefaultRetentionDays  = 30
	MaxRetentionDays      = 365
	MaxEndpoints          = 8
	maxGroupPatterns      = 200
	maxEndpointNameRunes  = 64
	maxEndpointModelRunes = 128
)

// ScannerIDs are the Qwen3Guard risk categories an administrator can enable.
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

// DefaultGroups covers the shared subscription groups.
var DefaultGroups = []string{"claude-ch*", "codex-ch*", "oauth-ch*"}

type Endpoint struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	Enabled bool   `json:"enabled"`
}

type Config struct {
	Enabled       bool       `json:"enabled"`
	AllGroups     bool       `json:"all_groups"`
	Groups        []string   `json:"groups"`
	Scanners      []string   `json:"scanners"`
	WorkerCount   int        `json:"worker_count"`
	QueueCapacity int        `json:"queue_capacity"`
	QueueMaxMB    int        `json:"queue_max_mb"`
	TimeoutMS     int        `json:"timeout_ms"`
	MaxChunkRunes int        `json:"max_chunk_runes"`
	MaxBodyMB     int        `json:"max_body_mb"`
	RetentionDays int        `json:"retention_days"`
	Endpoints     []Endpoint `json:"endpoints"`
	ConfigVersion int64      `json:"config_version"`
	UpdatedAt     int64      `json:"updated_at"`
	UpdatedBy     int        `json:"updated_by"`
}

// Snapshot is the immutable runtime view of both options.
type Snapshot struct {
	Config Config
	// Tokens maps endpoint id to its bearer token.
	Tokens map[string]string
	// LoadError is set when the stored configuration could not be applied; the
	// snapshot then falls back to the disabled default.
	LoadError string
}

func DefaultConfig() Config {
	return Config{
		Groups:        slices.Clone(DefaultGroups),
		Scanners:      slices.Clone(ScannerIDs),
		WorkerCount:   DefaultWorkerCount,
		QueueCapacity: DefaultQueueCapacity,
		QueueMaxMB:    DefaultQueueMaxMB,
		TimeoutMS:     DefaultTimeoutMS,
		MaxChunkRunes: DefaultMaxChunkRunes,
		MaxBodyMB:     DefaultMaxBodyMB,
		RetentionDays: DefaultRetentionDays,
		Endpoints:     []Endpoint{},
	}
}

var (
	snapshot  atomic.Pointer[Snapshot]
	rawConfig atomic.Pointer[string]
	rawTokens atomic.Pointer[string]
	listeners atomic.Pointer[[]func(*Snapshot)]
)

func init() {
	snapshot.Store(&Snapshot{Config: DefaultConfig(), Tokens: map[string]string{}})
}

// Current returns the active snapshot. It never returns nil.
func Current() *Snapshot {
	return snapshot.Load()
}

// OnChange registers a callback invoked after every snapshot replacement.
func OnChange(listener func(*Snapshot)) {
	for {
		previous := listeners.Load()
		var next []func(*Snapshot)
		if previous != nil {
			next = slices.Clone(*previous)
		}
		next = append(next, listener)
		if listeners.CompareAndSwap(previous, &next) {
			return
		}
	}
}

// IsOption reports whether key belongs to the content audit configuration.
func IsOption(key string) bool {
	return key == ConfigOptionKey || key == TokensOptionKey
}

// ApplyOption updates the snapshot from one stored option value. It is called
// from the option map loader, so it must not touch the option map itself.
func ApplyOption(key, value string) {
	switch key {
	case ConfigOptionKey:
		rawConfig.Store(&value)
	case TokensOptionKey:
		rawTokens.Store(&value)
	default:
		return
	}
	rebuild()
}

func rebuild() {
	next := &Snapshot{Config: DefaultConfig(), Tokens: map[string]string{}}
	if raw := rawConfig.Load(); raw != nil && strings.TrimSpace(*raw) != "" {
		cfg, err := ParseConfig(*raw)
		if err != nil {
			next.LoadError = err.Error()
		} else {
			next.Config = cfg
		}
	}
	if raw := rawTokens.Load(); raw != nil && strings.TrimSpace(*raw) != "" {
		tokens, err := ParseTokens(*raw)
		if err != nil && next.LoadError == "" {
			next.LoadError = "guard tokens are invalid"
		}
		if err == nil {
			next.Tokens = tokens
		}
	}
	if next.LoadError != "" {
		// A broken configuration never turns auditing on.
		next.Config.Enabled = false
	}
	snapshot.Store(next)
	if callbacks := listeners.Load(); callbacks != nil {
		for _, callback := range *callbacks {
			callback(next)
		}
	}
}

// ParseConfig decodes and validates a stored configuration document.
func ParseConfig(raw string) (Config, error) {
	cfg := DefaultConfig()
	if err := common.UnmarshalJsonStr(raw, &cfg); err != nil {
		return Config{}, errors.New("configuration is not valid JSON")
	}
	normalized, err := Normalize(cfg)
	if err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func ParseTokens(raw string) (map[string]string, error) {
	tokens := map[string]string{}
	if err := common.UnmarshalJsonStr(raw, &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

// Normalize trims, deduplicates and validates a configuration. Zero numeric
// fields take their defaults.
func Normalize(cfg Config) (Config, error) {
	cfg.WorkerCount = defaultIfZero(cfg.WorkerCount, DefaultWorkerCount)
	cfg.QueueCapacity = defaultIfZero(cfg.QueueCapacity, DefaultQueueCapacity)
	cfg.QueueMaxMB = defaultIfZero(cfg.QueueMaxMB, DefaultQueueMaxMB)
	cfg.TimeoutMS = defaultIfZero(cfg.TimeoutMS, DefaultTimeoutMS)
	cfg.MaxChunkRunes = defaultIfZero(cfg.MaxChunkRunes, DefaultMaxChunkRunes)
	cfg.MaxBodyMB = defaultIfZero(cfg.MaxBodyMB, DefaultMaxBodyMB)
	cfg.RetentionDays = defaultIfZero(cfg.RetentionDays, DefaultRetentionDays)

	bounds := []struct {
		name          string
		value, lo, hi int
	}{
		{"worker_count", cfg.WorkerCount, 1, MaxWorkerCount},
		{"queue_capacity", cfg.QueueCapacity, 1, MaxQueueCapacity},
		{"queue_max_mb", cfg.QueueMaxMB, 1, MaxQueueMaxMB},
		{"timeout_ms", cfg.TimeoutMS, MinTimeoutMS, MaxTimeoutMS},
		{"max_chunk_runes", cfg.MaxChunkRunes, MinMaxChunkRunes, MaxMaxChunkRunes},
		{"max_body_mb", cfg.MaxBodyMB, 1, MaxMaxBodyMB},
		{"retention_days", cfg.RetentionDays, 1, MaxRetentionDays},
	}
	for _, bound := range bounds {
		if bound.value < bound.lo || bound.value > bound.hi {
			return Config{}, fmt.Errorf("%s must be between %d and %d", bound.name, bound.lo, bound.hi)
		}
	}

	groups := make([]string, 0, len(cfg.Groups))
	for _, group := range cfg.Groups {
		group = strings.TrimSpace(group)
		if group == "" || slices.Contains(groups, group) {
			continue
		}
		if strings.Contains(strings.TrimSuffix(group, "*"), "*") || group == "*" {
			return Config{}, fmt.Errorf("group pattern %q may only end with a single *", group)
		}
		groups = append(groups, group)
	}
	if len(groups) > maxGroupPatterns {
		return Config{}, fmt.Errorf("at most %d group patterns are allowed", maxGroupPatterns)
	}
	cfg.Groups = groups

	scanners := make([]string, 0, len(cfg.Scanners))
	for _, scanner := range cfg.Scanners {
		scanner = strings.TrimSpace(scanner)
		if !slices.Contains(ScannerIDs, scanner) {
			return Config{}, fmt.Errorf("unknown scanner %q", scanner)
		}
		if !slices.Contains(scanners, scanner) {
			scanners = append(scanners, scanner)
		}
	}
	cfg.Scanners = scanners

	if len(cfg.Endpoints) > MaxEndpoints {
		return Config{}, fmt.Errorf("at most %d guard endpoints are allowed", MaxEndpoints)
	}
	endpoints := make([]Endpoint, 0, len(cfg.Endpoints))
	for _, endpoint := range cfg.Endpoints {
		endpoint.ID = strings.TrimSpace(endpoint.ID)
		endpoint.Name = strings.TrimSpace(endpoint.Name)
		endpoint.Model = strings.TrimSpace(endpoint.Model)
		if !validEndpointID(endpoint.ID) {
			return Config{}, errors.New("guard endpoint id must be 1-64 letters, digits, '-' or '_'")
		}
		if slices.ContainsFunc(endpoints, func(existing Endpoint) bool { return existing.ID == endpoint.ID }) {
			return Config{}, fmt.Errorf("duplicate guard endpoint id %q", endpoint.ID)
		}
		baseURL, err := NormalizeBaseURL(endpoint.BaseURL)
		if err != nil {
			return Config{}, err
		}
		endpoint.BaseURL = baseURL
		if endpoint.Model == "" || len([]rune(endpoint.Model)) > maxEndpointModelRunes {
			return Config{}, fmt.Errorf("guard endpoint %q needs a model name of at most %d characters", endpoint.ID, maxEndpointModelRunes)
		}
		if len([]rune(endpoint.Name)) > maxEndpointNameRunes {
			return Config{}, fmt.Errorf("guard endpoint name may be at most %d characters", maxEndpointNameRunes)
		}
		endpoints = append(endpoints, endpoint)
	}
	cfg.Endpoints = endpoints
	if cfg.Enabled && !cfg.HasEnabledEndpoint() {
		return Config{}, errors.New("enable at least one guard endpoint before turning content audit on")
	}
	return cfg, nil
}

// NormalizeBaseURL accepts an http(s) origin with an optional path. User
// info, query and fragment are refused, and a trailing /v1 is dropped because
// the client appends the API path itself.
func NormalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("guard endpoint base URL is invalid")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("guard endpoint base URL must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", errors.New("guard endpoint base URL must not contain credentials, a query or a fragment")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.EqualFold(path, "/v1") {
		path = ""
	}
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// GroupInScope reports whether a request group is covered by the configuration.
// A pattern ending in * matches by prefix.
func (cfg *Config) GroupInScope(group string) bool {
	if cfg.AllGroups {
		return true
	}
	for _, pattern := range cfg.Groups {
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
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

// HasEnabledEndpoint reports whether at least one Guard endpoint can be used.
func (cfg *Config) HasEnabledEndpoint() bool {
	return slices.ContainsFunc(cfg.Endpoints, func(endpoint Endpoint) bool { return endpoint.Enabled })
}

func validEndpointID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func defaultIfZero(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}
