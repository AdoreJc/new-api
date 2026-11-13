package promptaudit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"
)

// Decisions, risk levels and actions recorded on events.
const (
	DecisionPass     = "pass"
	DecisionFlag     = "flag"
	DecisionCritical = "critical"

	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"

	ActionAllow = "Allow"
	ActionWarn  = "Warn"
	ActionBlock = "Block"

	errorCodeUnavailable     = "guard_unavailable"
	errorCodeInvalidResponse = "guard_invalid_response"

	maxGuardResponseBytes = 256 * 1024
)

var categoryAliases = map[string]string{
	"violence": "violent", "non violent illegal acts": "non_violent_illegal_acts",
	"sexual content or sexual acts": "sexual_content_or_sexual_acts", "sexual": "sexual_content_or_sexual_acts",
	"personal identifying information": "pii", "personal identifiable information": "pii",
	"suicide self harm": "suicide_and_self_harm", "suicide and self harm": "suicide_and_self_harm",
	"unethical": "unethical_acts", "political": "politically_sensitive_topics",
	"copyright": "copyright_violation", "prompt injection": "jailbreak",
}

// guardError classifies a failed Guard call. Retryable errors (network,
// timeout, 429, 5xx) move on to the next endpoint and may be retried.
type guardError struct {
	Code       string
	HTTPStatus int
	Retryable  bool
	Timeout    bool
	Cause      error
}

func (e *guardError) Error() string {
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.HTTPStatus)
	}
	return e.Code
}

func (e *guardError) Unwrap() error { return e.Cause }

// guardResult is the normalized verdict for one chunk or a whole prompt.
type guardResult struct {
	Safety            string
	Decision          string
	RiskLevel         string
	Action            string
	Categories        []string
	MatchedScanners   []string
	UnknownCategories []string
	GuardEndpointID   string
	ScannerVersion    string
	ChunkTotal        int
	ChunksScanned     int
	LatencyMS         int
	// Incomplete marks findings kept after a later chunk failed to scan.
	Incomplete bool
	// Hits locates every chunk the Guard did not pass.
	Hits []chunkHit
	// Triggered lists the categories that made the verdict critical. It is
	// empty for a critical verdict caused by an unknown or missing category.
	Triggered []string
}

// chunkHit records where a finding came from without storing its text: the
// chunk's 1-based position in scan order (1 is the start of the latest user
// turn) and the roles whose text the chunk contains.
type chunkHit struct {
	Index      int      `json:"index"`
	Roles      []string `json:"roles"`
	Decision   string   `json:"decision"`
	Categories []string `json:"categories,omitempty"`
}

func normalizeCategory(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("_", " ", "&", " and ", "/", " ", "-", " ").Replace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	if canonical, ok := categoryAliases[normalized]; ok {
		return canonical
	}
	return strings.ReplaceAll(normalized, " ", "_")
}

// parseQwen3Guard reads the "Safety:" and "Categories:" lines of a Qwen3Guard
// answer and applies the rule's trigger levels. Safe allows. Controversial
// warns, unless a category whose level is controversial matched, which is
// critical. Unsafe is critical when a category that is not off matched, an
// unknown category was named, or no category was named; otherwise it is a
// high-risk warning. Triggered lists the categories that made it critical.
func parseQwen3Guard(content string, rule *audit_policy_setting.PromptAudit) (*guardResult, error) {
	var safety, categoryLine string
	for line := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "safety:"):
			if safety != "" {
				return nil, &guardError{Code: errorCodeInvalidResponse}
			}
			safety = strings.ToLower(strings.TrimSpace(line[len("safety:"):]))
		case strings.HasPrefix(lower, "categories:"):
			if categoryLine != "" {
				return nil, &guardError{Code: errorCodeInvalidResponse}
			}
			categoryLine = strings.TrimSpace(line[len("categories:"):])
		}
	}
	if (safety != "safe" && safety != "controversial" && safety != "unsafe") || categoryLine == "" {
		return nil, &guardError{Code: errorCodeInvalidResponse}
	}
	result := &guardResult{Safety: safety, Decision: DecisionPass, RiskLevel: RiskLow, Action: ActionAllow}
	known := map[string]struct{}{}
	unknown := map[string]struct{}{}
	for raw := range strings.SplitSeq(categoryLine, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.EqualFold(raw, "none") || strings.EqualFold(raw, "n/a") {
			continue
		}
		category := normalizeCategory(raw)
		if slices.Contains(prompt_audit_setting.ScannerIDs, category) {
			known[category] = struct{}{}
			continue
		}
		digest := sha256.Sum256([]byte(category))
		unknown[fmt.Sprintf("unknown:%x", digest[:8])] = struct{}{}
	}
	result.Categories = orderedCategories(known)
	result.UnknownCategories = orderedCategories(unknown)
	for _, category := range result.Categories {
		if rule.Level(category) != audit_policy_setting.LevelOff {
			result.MatchedScanners = append(result.MatchedScanners, category)
		}
	}
	switch safety {
	case "controversial":
		result.Decision, result.RiskLevel, result.Action = DecisionFlag, RiskMedium, ActionWarn
		for _, category := range result.MatchedScanners {
			if rule.Level(category) == audit_policy_setting.LevelControversial {
				result.Triggered = append(result.Triggered, category)
			}
		}
		if len(result.Triggered) > 0 {
			result.Decision, result.RiskLevel, result.Action = DecisionCritical, RiskCritical, ActionBlock
		}
	case "unsafe":
		result.Decision, result.RiskLevel, result.Action = DecisionFlag, RiskHigh, ActionWarn
		if len(result.MatchedScanners) > 0 || len(result.UnknownCategories) > 0 || len(result.Categories) == 0 {
			result.Decision, result.RiskLevel, result.Action = DecisionCritical, RiskCritical, ActionBlock
			result.Triggered = slices.Clone(result.MatchedScanners)
		}
	}
	return result, nil
}

// orderedCategories lists known categories in catalog order, then the rest sorted.
func orderedCategories(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, id := range prompt_audit_setting.ScannerIDs {
		if _, ok := values[id]; ok {
			result = append(result, id)
		}
	}
	rest := make([]string, 0, len(values)-len(result))
	for value := range values {
		if !slices.Contains(result, value) {
			rest = append(rest, value)
		}
	}
	slices.Sort(rest)
	return append(result, rest...)
}

func decisionSeverity(decision string) int {
	switch decision {
	case DecisionCritical:
		return 3
	case DecisionFlag:
		return 2
	}
	return 1
}

// aggregateResults merges chunk verdicts: the most severe chunk decides, and
// categories are the union of all chunks.
func aggregateResults(results []*guardResult) *guardResult {
	aggregated := &guardResult{Decision: DecisionPass, RiskLevel: RiskLow, Action: ActionAllow, ChunkTotal: len(results)}
	categories, matched, unknown, triggered := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, result := range results {
		if aggregated.GuardEndpointID == "" || decisionSeverity(result.Decision) > decisionSeverity(aggregated.Decision) {
			aggregated.Decision, aggregated.RiskLevel, aggregated.Action = result.Decision, result.RiskLevel, result.Action
			aggregated.Safety, aggregated.GuardEndpointID, aggregated.ScannerVersion = result.Safety, result.GuardEndpointID, result.ScannerVersion
		}
		for _, value := range result.Categories {
			categories[value] = struct{}{}
		}
		for _, value := range result.MatchedScanners {
			matched[value] = struct{}{}
		}
		for _, value := range result.UnknownCategories {
			unknown[value] = struct{}{}
		}
		for _, value := range result.Triggered {
			triggered[value] = struct{}{}
		}
	}
	aggregated.Triggered = orderedCategories(triggered)
	aggregated.Categories = orderedCategories(categories)
	aggregated.MatchedScanners = orderedCategories(matched)
	aggregated.UnknownCategories = orderedCategories(unknown)
	return aggregated
}

// scanChunk is one Guard input and the roles of the text it covers.
type scanChunk struct {
	text  string
	roles []string
}

// chunks cuts the part into chunks of at most limit runes. Boundaries start
// from the beginning, so a resent conversation keeps the same chunks for its
// unchanged history.
func (p scanPart) chunks(limit int) []scanChunk {
	runes := []rune(p.text)
	chunks := make([]scanChunk, 0, len(runes)/limit+1)
	for start := 0; start < len(runes); start += limit {
		end := min(start+limit, len(runes))
		roles := make([]string, 0, 1)
		for _, span := range p.spans {
			if span.start < end && span.end > start && !slices.Contains(roles, span.role) {
				roles = append(roles, span.role)
			}
		}
		chunks = append(chunks, scanChunk{text: string(runes[start:end]), roles: roles})
	}
	return chunks
}

// scanOrder lists the chunks to scan: the latest user turn first, then the
// rest of the transcript from newest to oldest.
func scanOrder(latest, rest []scanChunk) []scanChunk {
	ordered := make([]scanChunk, 0, len(latest)+len(rest))
	ordered = append(ordered, latest...)
	for index := len(rest) - 1; index >= 0; index-- {
		ordered = append(ordered, rest[index])
	}
	return ordered
}

const (
	safeChunkCacheSize = 8192
	safeChunkTTL       = time.Hour
)

// safeChunkCache remembers chunks the Guard recently judged safe, so agent
// sessions that resend their whole history are not rescanned every turn.
// Only safe verdicts are cached; the key includes the configuration version.
type safeChunkCache struct {
	mu      sync.Mutex
	expires map[[sha256.Size]byte]time.Time
	order   [][sha256.Size]byte
}

func safeChunkKey(version int64, chunk string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strconv.FormatInt(version, 10) + "\x00" + chunk))
}

func (c *safeChunkCache) has(key [sha256.Size]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.expires[key]
	return ok && now.Before(expiry)
}

func (c *safeChunkCache) add(key [sha256.Size]byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expires == nil {
		c.expires = make(map[[sha256.Size]byte]time.Time, safeChunkCacheSize)
	}
	if _, exists := c.expires[key]; !exists {
		if len(c.order) >= safeChunkCacheSize {
			delete(c.expires, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, key)
	}
	c.expires[key] = now.Add(safeChunkTTL)
}

type cachedHTTPClient struct {
	key    string
	client *http.Client
}

// guardClient calls OpenAI-compatible chat completion endpoints serving
// Qwen3Guard. It keeps one HTTP client per endpoint id and replaces it, closing
// idle connections, when the endpoint's address or timeout changes.
type guardClient struct {
	mu         sync.Mutex
	clients    map[string]cachedHTTPClient
	safeChunks safeChunkCache
	// endpointSlots bound synchronous calls in flight per endpoint id.
	endpointSlots map[string]chan struct{}
}

// errBulkheadFull is returned when an endpoint already serves its share of
// synchronous checks; the next endpoint is tried as after a network error.
var errBulkheadFull = &guardError{Code: errorCodeUnavailable, Retryable: true}

// acquireEndpoint takes a synchronous call slot of the endpoint without
// waiting. The returned function releases it.
func (g *guardClient) acquireEndpoint(id string) (func(), bool) {
	g.mu.Lock()
	if g.endpointSlots == nil {
		g.endpointSlots = map[string]chan struct{}{}
	}
	slots, ok := g.endpointSlots[id]
	if !ok {
		slots = make(chan struct{}, prompt_audit_setting.EndpointConcurrency)
		g.endpointSlots[id] = slots
	}
	g.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

func newHTTPClient(endpoint prompt_audit_setting.Endpoint) *http.Client {
	timeout := time.Duration(endpoint.TimeoutMS) * time.Millisecond
	transport := &http.Transport{
		// Guard nodes are administrator-owned and usually on a private network;
		// do not route their traffic through an environment proxy.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   prompt_audit_setting.MaxWorkerCount,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func (g *guardClient) httpClient(endpoint prompt_audit_setting.Endpoint) *http.Client {
	key := fmt.Sprintf("%s|%d", endpoint.BaseURL, endpoint.TimeoutMS)
	g.mu.Lock()
	defer g.mu.Unlock()
	if cached, ok := g.clients[endpoint.ID]; ok {
		if cached.key == key {
			return cached.client
		}
		cached.client.CloseIdleConnections()
	}
	if g.clients == nil {
		g.clients = map[string]cachedHTTPClient{}
	}
	client := newHTTPClient(endpoint)
	g.clients[endpoint.ID] = cachedHTTPClient{key: key, client: client}
	return client
}

// scan sends one chunk to one endpoint.
func (g *guardClient) scan(ctx context.Context, endpoint prompt_audit_setting.Endpoint, token, chunk string, rule *audit_policy_setting.PromptAudit) (*guardResult, error) {
	return scanWith(ctx, g.httpClient(endpoint), endpoint, token, chunk, rule)
}

func scanWith(ctx context.Context, client *http.Client, endpoint prompt_audit_setting.Endpoint, token, chunk string, rule *audit_policy_setting.PromptAudit) (*guardResult, error) {
	payload, err := common.Marshal(map[string]any{
		"model":       endpoint.Model,
		"messages":    []map[string]string{{"role": "user", "content": chunk}},
		"temperature": 0,
		"max_tokens":  64,
		"seed":        42,
	})
	if err != nil {
		return nil, &guardError{Code: errorCodeInvalidResponse, Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.BaseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, &guardError{Code: errorCodeUnavailable, Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		var netErr net.Error
		timeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
		return nil, &guardError{Code: errorCodeUnavailable, Retryable: true, Timeout: timeout, Cause: err}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return nil, &guardError{Code: errorCodeUnavailable, HTTPStatus: response.StatusCode, Retryable: retryable}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGuardResponseBytes+1))
	if err != nil {
		return nil, &guardError{Code: errorCodeUnavailable, Retryable: true, Cause: err}
	}
	if len(body) > maxGuardResponseBytes {
		return nil, &guardError{Code: errorCodeInvalidResponse}
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := common.Unmarshal(body, &envelope); err != nil || len(envelope.Choices) == 0 {
		return nil, &guardError{Code: errorCodeInvalidResponse, Cause: err}
	}
	result, err := parseQwen3Guard(envelope.Choices[0].Message.Content, rule)
	if err != nil {
		return nil, err
	}
	result.GuardEndpointID = endpoint.ID
	result.ScannerVersion = endpoint.Model
	return result, nil
}

// scanRequest is what one evaluation needs besides the text.
type scanRequest struct {
	snapshot    *prompt_audit_setting.Snapshot
	rule        *audit_policy_setting.PromptAudit
	retryDelays []time.Duration
	// synchronous calls hold a per-endpoint bulkhead slot.
	synchronous bool
}

// scanChunk tries the enabled endpoints in order, moving on only after a
// retryable error. When every endpoint failed retryably it waits and tries
// the same chunk again, once per retry delay.
func (g *guardClient) scanChunk(ctx context.Context, request scanRequest, endpoints []prompt_audit_setting.Endpoint, chunk string, metrics *Metrics) (*guardResult, error) {
	for attempt := 0; ; attempt++ {
		var lastErr error
		for index, endpoint := range endpoints {
			var result *guardResult
			var err error
			if request.synchronous {
				if release, acquired := g.acquireEndpoint(endpoint.ID); acquired {
					result, err = g.scan(ctx, endpoint, request.snapshot.Tokens[endpoint.ID], chunk, request.rule)
					release()
				} else {
					metrics.bulkheadFull.Add(1)
					err = errBulkheadFull
				}
			} else {
				result, err = g.scan(ctx, endpoint, request.snapshot.Tokens[endpoint.ID], chunk, request.rule)
			}
			if err == nil {
				return result, nil
			}
			lastErr = err
			var guardErr *guardError
			if !errors.As(err, &guardErr) || !guardErr.Retryable {
				return nil, err
			}
			if index < len(endpoints)-1 {
				metrics.failovers.Add(1)
			}
		}
		if attempt >= len(request.retryDelays) {
			return nil, lastErr
		}
		metrics.retries.Add(1)
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(request.retryDelays[attempt]):
		}
	}
}

// evaluate scans the latest user turn first and then earlier text from newest
// to oldest. Chunks judged safe recently are skipped, and at most
// MaxChunksPerRequest chunks go to the Guard; the rest are left unscanned and
// counted. It stops at the first chunk that would block.
func (g *guardClient) evaluate(ctx context.Context, request scanRequest, latest, rest scanPart, metrics *Metrics) (*guardResult, error) {
	snapshot := request.snapshot
	endpoints := snapshot.Config.EnabledEndpoints()
	if len(endpoints) == 0 {
		return nil, &guardError{Code: errorCodeUnavailable}
	}
	inputLimit := endpoints[0].InputLimit
	for _, endpoint := range endpoints[1:] {
		inputLimit = min(inputLimit, endpoint.InputLimit)
	}
	started := time.Now()
	chunks := scanOrder(latest.chunks(inputLimit), rest.chunks(inputLimit))
	budget := snapshot.Config.MaxChunksPerRequest
	results := make([]*guardResult, 0, min(len(chunks), budget))
	var hits []chunkHit
	scanned := 0
	for index, chunk := range chunks {
		key := safeChunkKey(snapshot.Config.ConfigVersion, chunk.text)
		if g.safeChunks.has(key, started) {
			metrics.chunksCached.Add(1)
			continue
		}
		if scanned >= budget {
			metrics.chunksOverBudget.Add(int64(len(chunks) - index))
			break
		}
		scanned++
		result, err := g.scanChunk(ctx, request, endpoints, chunk.text, metrics)
		if err != nil {
			// Keep what earlier chunks found instead of losing it with the error.
			if !slices.ContainsFunc(results, func(r *guardResult) bool { return r.Decision != DecisionPass }) {
				return nil, err
			}
			partial := aggregateResults(results)
			partial.ChunkTotal, partial.ChunksScanned, partial.Incomplete, partial.Hits = len(chunks), scanned, true, hits
			partial.LatencyMS = int(time.Since(started).Milliseconds())
			return partial, err
		}
		results = append(results, result)
		if result.Decision == DecisionPass {
			g.safeChunks.add(key, time.Now())
		} else {
			hits = append(hits, chunkHit{Index: index + 1, Roles: chunk.roles, Decision: result.Decision, Categories: result.Categories})
		}
		if result.Action == ActionBlock {
			break
		}
	}
	aggregated := aggregateResults(results)
	aggregated.ChunkTotal = len(chunks)
	aggregated.ChunksScanned = scanned
	aggregated.Hits = hits
	aggregated.LatencyMS = int(time.Since(started).Milliseconds())
	return aggregated, nil
}
