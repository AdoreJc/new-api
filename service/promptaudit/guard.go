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
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	LatencyMS         int
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
// answer. Safe allows; Controversial warns, except jailbreak, PII and
// self-harm which are critical; Unsafe is critical when an enabled or unknown
// category matched (or none was named), otherwise a high-risk warning.
func parseQwen3Guard(content string, enabledScanners []string) (*guardResult, error) {
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
		if slices.Contains(enabledScanners, category) {
			result.MatchedScanners = append(result.MatchedScanners, category)
		}
	}
	switch safety {
	case "controversial":
		result.Decision, result.RiskLevel, result.Action = DecisionFlag, RiskMedium, ActionWarn
		if slices.ContainsFunc(result.MatchedScanners, func(category string) bool {
			return category == "jailbreak" || category == "pii" || category == "suicide_and_self_harm"
		}) {
			result.Decision, result.RiskLevel, result.Action = DecisionCritical, RiskCritical, ActionBlock
		}
	case "unsafe":
		result.Decision, result.RiskLevel, result.Action = DecisionFlag, RiskHigh, ActionWarn
		if len(result.MatchedScanners) > 0 || len(result.UnknownCategories) > 0 || len(result.Categories) == 0 {
			result.Decision, result.RiskLevel, result.Action = DecisionCritical, RiskCritical, ActionBlock
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
	categories, matched, unknown := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
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
	}
	aggregated.Categories = orderedCategories(categories)
	aggregated.MatchedScanners = orderedCategories(matched)
	aggregated.UnknownCategories = orderedCategories(unknown)
	return aggregated
}

// splitScanText cuts the text at the priority separator, then into chunks of
// at most limit runes.
func splitScanText(value string, limit int) []string {
	chunks := make([]string, 0, 2)
	for segment := range strings.SplitSeq(value, prioritySeparator) {
		runes := []rune(segment)
		for start := 0; start < len(runes); start += limit {
			chunks = append(chunks, string(runes[start:min(start+limit, len(runes))]))
		}
	}
	return chunks
}

// guardClient calls OpenAI-compatible chat completion endpoints serving
// Qwen3Guard. HTTP clients are cached per endpoint id, address and timeout.
type guardClient struct {
	clients sync.Map
}

func (g *guardClient) httpClient(endpoint prompt_audit_setting.Endpoint) *http.Client {
	key := fmt.Sprintf("%s|%s|%d", endpoint.ID, endpoint.BaseURL, endpoint.TimeoutMS)
	if cached, ok := g.clients.Load(key); ok {
		return cached.(*http.Client)
	}
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
	client, _ := g.clients.LoadOrStore(key, &http.Client{Transport: transport, Timeout: timeout})
	return client.(*http.Client)
}

// scan sends one chunk to one endpoint.
func (g *guardClient) scan(ctx context.Context, endpoint prompt_audit_setting.Endpoint, token, chunk string, scanners []string) (*guardResult, error) {
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
	response, err := g.httpClient(endpoint).Do(request)
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
	result, err := parseQwen3Guard(envelope.Choices[0].Message.Content, scanners)
	if err != nil {
		return nil, err
	}
	result.GuardEndpointID = endpoint.ID
	result.ScannerVersion = endpoint.Model
	return result, nil
}

// scanChunk tries the enabled endpoints in order, moving on only after a
// retryable error.
func (g *guardClient) scanChunk(ctx context.Context, snapshot *prompt_audit_setting.Snapshot, endpoints []prompt_audit_setting.Endpoint, chunk string, metrics *Metrics) (*guardResult, error) {
	var lastErr error
	for index, endpoint := range endpoints {
		result, err := g.scan(ctx, endpoint, snapshot.Tokens[endpoint.ID], chunk, snapshot.Config.Scanners)
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
	return nil, lastErr
}

// evaluate scans every chunk of the prompt and aggregates the verdicts. It
// stops early at the first chunk that would block.
func (g *guardClient) evaluate(ctx context.Context, snapshot *prompt_audit_setting.Snapshot, scanText string, metrics *Metrics) (*guardResult, error) {
	endpoints := snapshot.Config.EnabledEndpoints()
	if len(endpoints) == 0 {
		return nil, &guardError{Code: errorCodeUnavailable}
	}
	inputLimit := endpoints[0].InputLimit
	for _, endpoint := range endpoints[1:] {
		inputLimit = min(inputLimit, endpoint.InputLimit)
	}
	started := time.Now()
	chunks := splitScanText(scanText, inputLimit)
	results := make([]*guardResult, 0, len(chunks))
	for _, chunk := range chunks {
		result, err := g.scanChunk(ctx, snapshot, endpoints, chunk, metrics)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		if result.Action == ActionBlock {
			break
		}
	}
	aggregated := aggregateResults(results)
	aggregated.ChunkTotal = len(chunks)
	aggregated.LatencyMS = int(time.Since(started).Milliseconds())
	return aggregated, nil
}
