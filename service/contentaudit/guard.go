package contentaudit

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
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/content_audit_setting"
)

const (
	maxGuardResponseBytes = 256 * 1024
	// maxGuardRetries is how many extra rounds over the endpoint list a chunk
	// gets after retryable failures.
	maxGuardRetries = 2
	// maxChunksPerRequest bounds Guard calls per request; the latest user turn
	// is always in the first chunk.
	maxChunksPerRequest = 16
)

const (
	guardErrUnavailable     = "guard_unavailable"
	guardErrInvalidResponse = "guard_invalid_response"
	guardErrNoEndpoint      = "guard_no_endpoint"
)

var categoryAliases = map[string]string{
	"violent": "violent", "violence": "violent",
	"non violent illegal acts":      "non_violent_illegal_acts",
	"sexual content or sexual acts": "sexual_content_or_sexual_acts", "sexual": "sexual_content_or_sexual_acts",
	"pii": "pii", "personal identifying information": "pii", "personal identifiable information": "pii",
	"suicide self harm": "suicide_and_self_harm", "suicide and self harm": "suicide_and_self_harm",
	"unethical acts": "unethical_acts", "unethical": "unethical_acts",
	"politically sensitive topics": "politically_sensitive_topics", "political": "politically_sensitive_topics",
	"copyright violation": "copyright_violation", "copyright": "copyright_violation",
	"jailbreak": "jailbreak", "prompt injection": "jailbreak",
}

// guardError classifies a failed Guard call. Retryable covers network errors,
// timeouts, 429 and 5xx.
type guardError struct {
	Code       string
	HTTPStatus int
	Retryable  bool
	Cause      error
}

func (e *guardError) Error() string {
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.HTTPStatus)
	}
	return e.Code
}

func (e *guardError) Unwrap() error { return e.Cause }

func errorCode(err error) string {
	var guardErr *guardError
	if errors.As(err, &guardErr) {
		return guardErr.Error()
	}
	return guardErrUnavailable
}

// guardResult is the verdict for one chunk or, after aggregation, a request.
type guardResult struct {
	Safety            string
	Decision          string // "", warn or block
	RiskLevel         string
	Score             float64
	Categories        []string
	MatchedScanners   []string
	UnknownCategories []string
	EndpointID        string
	ScannerVersion    string
	ChunkTotal        int
}

func decisionSeverity(decision string) int {
	switch decision {
	case model.ContentAuditDecisionBlock:
		return 2
	case model.ContentAuditDecisionWarn:
		return 1
	default:
		return 0
	}
}

func normalizeCategory(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("_", " ", "&", " and ", "/", " ", "-", " ", "–", " ", "—", " ").Replace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	if canonical, ok := categoryAliases[normalized]; ok {
		return canonical
	}
	return strings.ReplaceAll(normalized, " ", "_")
}

// parseQwen3Guard maps the "Safety:" and "Categories:" lines of a Qwen3Guard
// reply to a decision. Safe allows; Controversial warns, except for categories
// that are always treated as block; Unsafe blocks when an enabled, unknown or
// unnamed category is reported and warns otherwise.
func parseQwen3Guard(content string, enabledScanners []string) (*guardResult, error) {
	var safety, categoryLine string
	for line := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "safety:"):
			if safety != "" {
				return nil, &guardError{Code: guardErrInvalidResponse}
			}
			safety = strings.TrimSpace(line[len("safety:"):])
		case strings.HasPrefix(lower, "categories:"):
			if categoryLine != "" {
				return nil, &guardError{Code: guardErrInvalidResponse}
			}
			categoryLine = strings.TrimSpace(line[len("categories:"):])
		}
	}
	switch strings.ToLower(safety) {
	case "safe":
		safety = "Safe"
	case "controversial":
		safety = "Controversial"
	case "unsafe":
		safety = "Unsafe"
	default:
		return nil, &guardError{Code: guardErrInvalidResponse}
	}
	if categoryLine == "" {
		return nil, &guardError{Code: guardErrInvalidResponse}
	}
	known := map[string]struct{}{}
	unknown := map[string]struct{}{}
	for raw := range strings.SplitSeq(categoryLine, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.EqualFold(raw, "none") || strings.EqualFold(raw, "n/a") {
			continue
		}
		category := normalizeCategory(raw)
		if slices.Contains(content_audit_setting.ScannerIDs, category) {
			known[category] = struct{}{}
			continue
		}
		digest := sha256.Sum256([]byte(category))
		unknown[fmt.Sprintf("unknown:%x", digest[:8])] = struct{}{}
	}
	result := &guardResult{
		Safety:            safety,
		RiskLevel:         "low",
		Categories:        orderedCategories(known),
		UnknownCategories: sortedKeys(unknown),
	}
	for _, category := range result.Categories {
		if slices.Contains(enabledScanners, category) {
			result.MatchedScanners = append(result.MatchedScanners, category)
		}
	}
	switch safety {
	case "Controversial":
		result.Score, result.Decision, result.RiskLevel = 0.5, model.ContentAuditDecisionWarn, "medium"
		for _, category := range result.MatchedScanners {
			if category == "jailbreak" || category == "pii" || category == "suicide_and_self_harm" {
				result.Decision, result.RiskLevel = model.ContentAuditDecisionBlock, "critical"
			}
		}
	case "Unsafe":
		result.Score = 1
		if len(result.MatchedScanners) > 0 || len(result.UnknownCategories) > 0 || len(result.Categories) == 0 {
			result.Decision, result.RiskLevel = model.ContentAuditDecisionBlock, "critical"
		} else {
			result.Decision, result.RiskLevel = model.ContentAuditDecisionWarn, "high"
		}
	}
	return result, nil
}

// aggregateResults keeps the most severe chunk verdict and the union of all
// reported categories.
func aggregateResults(results []*guardResult) *guardResult {
	aggregated := &guardResult{Safety: "Safe", RiskLevel: "low", ChunkTotal: len(results)}
	categories := map[string]struct{}{}
	matched := map[string]struct{}{}
	unknown := map[string]struct{}{}
	for _, result := range results {
		if aggregated.EndpointID == "" || decisionSeverity(result.Decision) > decisionSeverity(aggregated.Decision) ||
			result.Score > aggregated.Score && result.Decision == aggregated.Decision {
			aggregated.Safety = result.Safety
			aggregated.Decision = result.Decision
			aggregated.RiskLevel = result.RiskLevel
			aggregated.Score = result.Score
			aggregated.EndpointID = result.EndpointID
			aggregated.ScannerVersion = result.ScannerVersion
		}
		for _, category := range result.Categories {
			categories[category] = struct{}{}
		}
		for _, category := range result.MatchedScanners {
			matched[category] = struct{}{}
		}
		for _, category := range result.UnknownCategories {
			unknown[category] = struct{}{}
		}
	}
	aggregated.Categories = orderedCategories(categories)
	aggregated.MatchedScanners = orderedCategories(matched)
	aggregated.UnknownCategories = sortedKeys(unknown)
	return aggregated
}

func orderedCategories(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, id := range content_audit_setting.ScannerIDs {
		if _, ok := values[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

// guardEndpoint is an endpoint resolved together with its token and timeout.
type guardEndpoint struct {
	content_audit_setting.Endpoint
	Token   string
	Timeout time.Duration
}

// guardClient talks to OpenAI-compatible Guard endpoints. Endpoints are
// configured by the root administrator only, so private and loopback
// addresses are allowed; the transport never uses environment proxies,
// requires TLS 1.2+, refuses redirects and caps the response size.
type guardClient struct {
	clients sync.Map
}

func (g *guardClient) httpClient(endpoint guardEndpoint) *http.Client {
	key := fmt.Sprintf("%s|%s|%d", endpoint.ID, endpoint.BaseURL, endpoint.Timeout)
	if cached, ok := g.clients.Load(key); ok {
		return cached.(*http.Client)
	}
	dialer := &net.Dialer{Timeout: min(endpoint.Timeout, 3*time.Second), KeepAlive: 30 * time.Second}
	client := &http.Client{
		Timeout: endpoint.Timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   content_audit_setting.MaxWorkerCount,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: endpoint.Timeout,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	actual, _ := g.clients.LoadOrStore(key, client)
	return actual.(*http.Client)
}

func (g *guardClient) do(ctx context.Context, endpoint guardEndpoint, method, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := common.Marshal(payload)
		if err != nil {
			return nil, &guardError{Code: guardErrInvalidResponse, Cause: err}
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.BaseURL+path, body)
	if err != nil {
		return nil, &guardError{Code: guardErrUnavailable, Cause: err}
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if endpoint.Token != "" {
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	}
	response, err := g.httpClient(endpoint).Do(request)
	if err != nil {
		return nil, &guardError{Code: guardErrUnavailable, Retryable: true, Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGuardResponseBytes))
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return nil, &guardError{Code: guardErrUnavailable, HTTPStatus: response.StatusCode, Retryable: retryable}
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxGuardResponseBytes+1))
	if err != nil {
		return nil, &guardError{Code: guardErrUnavailable, Retryable: true, Cause: err}
	}
	if len(responseBody) > maxGuardResponseBytes {
		return nil, &guardError{Code: guardErrInvalidResponse}
	}
	return responseBody, nil
}

// scan classifies one chunk with one endpoint.
func (g *guardClient) scan(ctx context.Context, endpoint guardEndpoint, chunk string, enabledScanners []string) (*guardResult, error) {
	payload := map[string]any{
		"model":       endpoint.Model,
		"messages":    []map[string]string{{"role": "user", "content": chunk}},
		"temperature": 0,
		"max_tokens":  64,
		"seed":        42,
	}
	responseBody, err := g.do(ctx, endpoint, http.MethodPost, "/v1/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := common.Unmarshal(responseBody, &envelope); err != nil || len(envelope.Choices) == 0 {
		return nil, &guardError{Code: guardErrInvalidResponse}
	}
	content := strings.Join(contentTexts(envelope.Choices[0].Message.Content), "\n")
	result, err := parseQwen3Guard(content, enabledScanners)
	if err != nil {
		return nil, err
	}
	result.EndpointID = endpoint.ID
	result.ScannerVersion = endpoint.Model
	return result, nil
}

// guardStats receives retry and failover counts.
type guardStats interface {
	addRetry()
	addFailover()
}

// scanChunk walks the endpoints in order, failing over on retryable errors.
// After a full round of retryable failures it backs off and retries, at most
// maxGuardRetries times.
func (g *guardClient) scanChunk(ctx context.Context, endpoints []guardEndpoint, chunk string, enabledScanners []string, stats guardStats) (*guardResult, error) {
	var lastErr error = &guardError{Code: guardErrNoEndpoint}
	for attempt := range maxGuardRetries + 1 {
		if attempt > 0 {
			stats.addRetry()
			select {
			case <-ctx.Done():
				return nil, &guardError{Code: guardErrUnavailable, Cause: ctx.Err()}
			case <-time.After(time.Duration(100<<attempt) * time.Millisecond):
			}
		}
		for index, endpoint := range endpoints {
			if index > 0 {
				stats.addFailover()
			}
			result, err := g.scan(ctx, endpoint, chunk, enabledScanners)
			if err == nil {
				return result, nil
			}
			lastErr = err
			var guardErr *guardError
			if !errors.As(err, &guardErr) || !guardErr.Retryable {
				return nil, err
			}
		}
	}
	return nil, lastErr
}

// resolveEndpoints returns the enabled endpoints of a snapshot in order.
func resolveEndpoints(snapshot *content_audit_setting.Snapshot) []guardEndpoint {
	timeout := time.Duration(snapshot.Config.TimeoutMS) * time.Millisecond
	endpoints := make([]guardEndpoint, 0, len(snapshot.Config.Endpoints))
	for _, endpoint := range snapshot.Config.Endpoints {
		if endpoint.Enabled {
			endpoints = append(endpoints, guardEndpoint{Endpoint: endpoint, Token: snapshot.Tokens[endpoint.ID], Timeout: timeout})
		}
	}
	return endpoints
}
