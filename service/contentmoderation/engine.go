package contentmoderation

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
)

const (
	maxResponseBytes = 256 * 1024

	freezeRateLimited = time.Minute
	freezeAuthFailed  = 10 * time.Minute
	freezeHTTPError   = 10 * time.Second
)

var errNoUsableKey = errors.New("no usable moderation API key")

// engineError describes a failed moderation call without any response body,
// so neither the key nor upstream text can reach logs or the runtime view.
type engineError struct {
	HTTPStatus int
	Timeout    bool
	Reason     string
}

func (e *engineError) Error() string {
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("moderation API returned HTTP %d", e.HTTPStatus)
	}
	return "moderation API " + e.Reason
}

// freezeDuration is how long a key is skipped after an HTTP error: rate
// limits for a minute, rejected keys for ten minutes, other HTTP errors for
// ten seconds. A 400 is about the input, not the key, and network errors are
// not the key's fault either.
func freezeDuration(status int) time.Duration {
	switch status {
	case 0, http.StatusBadRequest:
		return 0
	case http.StatusUnauthorized, http.StatusForbidden:
		return freezeAuthFailed
	case http.StatusTooManyRequests, 529:
		return freezeRateLimited
	}
	return freezeHTTPError
}

// moderationResult is the engine's answer for one input.
type moderationResult struct {
	Scores map[string]float64
	Model  string
}

// evaluate compares scores with the thresholds. The highest scoring category
// is reported whether or not anything was flagged.
func evaluate(scores, thresholds map[string]float64) (flagged bool, highest string, highestScore float64) {
	for _, category := range content_moderation_setting.Categories {
		score := scores[category]
		if highest == "" || score > highestScore {
			highest, highestScore = category, score
		}
		if threshold, ok := thresholds[category]; ok && score >= threshold {
			flagged = true
		}
	}
	return flagged, highest, highestScore
}

// KeyStatus is the health of one configured key. It identifies the key only
// by KeyID.
type KeyStatus struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	FrozenUntil    int64  `json:"frozen_until,omitempty"`
	Successes      int64  `json:"successes"`
	Failures       int64  `json:"failures"`
	LastHTTPStatus int    `json:"last_http_status"`
	LastError      string `json:"last_error,omitempty"`
	LastLatencyMS  int    `json:"last_latency_ms"`
	LastCheckedAt  int64  `json:"last_checked_at,omitempty"`
}

type keyHealth struct {
	frozenUntil    time.Time
	successes      int64
	failures       int64
	lastHTTPStatus int
	lastError      string
	lastLatencyMS  int
	lastCheckedAt  time.Time
}

type cachedClient struct {
	key    string
	client *http.Client
}

// engine calls an OpenAI-compatible /v1/moderations endpoint, rotating
// through the configured keys and skipping frozen ones.
type engine struct {
	mu     sync.Mutex
	health map[string]*keyHealth
	client cachedClient
	cursor atomic.Uint64
	now    func() time.Time
}

func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		// Only an administrator-configured address is called; never route it
		// through an environment proxy.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   content_moderation_setting.MaxWorkerCount,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// A redirect is answered as an error status; it is never followed, so
		// the Authorization header only goes to the configured address.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// httpClient returns the shared client, replacing it (and closing its idle
// connections) when the address or timeout changes.
func (e *engine) httpClient(config *content_moderation_setting.Config) *http.Client {
	key := fmt.Sprintf("%s|%d", config.BaseURL, config.TimeoutMS)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client.client != nil && e.client.key == key {
		return e.client.client
	}
	if e.client.client != nil {
		e.client.client.CloseIdleConnections()
	}
	e.client = cachedClient{key: key, client: newHTTPClient(time.Duration(config.TimeoutMS) * time.Millisecond)}
	return e.client.client
}

// requestInput is the "input" field: plain text, or text and image parts.
func requestInput(input Input, withImages bool) any {
	if !withImages || len(input.Images) == 0 {
		return input.Text
	}
	parts := make([]map[string]any, 0, len(input.Images)+1)
	if input.Text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": input.Text})
	}
	for _, image := range input.Images {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}})
	}
	return parts
}

// callOnce sends one moderation request with one key.
func callOnce(ctx context.Context, client *http.Client, config *content_moderation_setting.Config, apiKey string, input any) (*moderationResult, error) {
	payload, err := common.Marshal(map[string]any{"model": config.Model, "input": input})
	if err != nil {
		return nil, &engineError{Reason: "request could not be encoded"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.BaseURL+"/v1/moderations", bytes.NewReader(payload))
	if err != nil {
		return nil, &engineError{Reason: "request could not be created"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := client.Do(request)
	if err != nil {
		var netErr net.Error
		timeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
		if timeout {
			return nil, &engineError{Timeout: true, Reason: "timed out"}
		}
		return nil, &engineError{Reason: "is unreachable"}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return nil, &engineError{HTTPStatus: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &engineError{Reason: "response could not be read"}
	}
	if len(body) > maxResponseBytes {
		return nil, &engineError{Reason: "response is too large"}
	}
	var envelope struct {
		Model   string `json:"model"`
		Results []struct {
			CategoryScores map[string]float64 `json:"category_scores"`
		} `json:"results"`
	}
	if err := common.Unmarshal(body, &envelope); err != nil || len(envelope.Results) == 0 || envelope.Results[0].CategoryScores == nil {
		return nil, &engineError{Reason: "response is not a moderation result"}
	}
	return &moderationResult{Scores: envelope.Results[0].CategoryScores, Model: envelope.Model}, nil
}

// nextKey picks the next key in rotation that is not frozen.
func (e *engine) nextKey(keys []string) (string, bool) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for range keys {
		key := keys[int(e.cursor.Add(1)-1)%len(keys)]
		if state := e.health[content_moderation_setting.KeyID(key)]; state == nil || !state.frozenUntil.After(now) {
			return key, true
		}
	}
	return "", false
}

func (e *engine) record(key string, latency time.Duration, err error) {
	id := content_moderation_setting.KeyID(key)
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.health == nil {
		e.health = map[string]*keyHealth{}
	}
	state := e.health[id]
	if state == nil {
		state = &keyHealth{}
		e.health[id] = state
	}
	state.lastCheckedAt, state.lastLatencyMS = now, int(latency.Milliseconds())
	if err == nil {
		state.successes++
		state.lastHTTPStatus, state.lastError, state.frozenUntil = http.StatusOK, "", time.Time{}
		return
	}
	state.failures++
	state.lastError = err.Error()
	state.lastHTTPStatus = 0
	var engineErr *engineError
	if errors.As(err, &engineErr) {
		state.lastHTTPStatus = engineErr.HTTPStatus
		if freeze := freezeDuration(engineErr.HTTPStatus); freeze > 0 {
			state.frozenUntil = now.Add(freeze)
		}
	}
}

// moderate tries up to RetryCount+1 calls, each with the next usable key,
// and stops early on a 400 (the input itself was rejected).
func (e *engine) moderate(ctx context.Context, snapshot *content_moderation_setting.Snapshot, input Input) (*moderationResult, int, error) {
	config := &snapshot.Config
	client := e.httpClient(config)
	payload := requestInput(input, config.SupportsImages())
	attempts := 0
	var lastErr error = errNoUsableKey
	for attempt := range config.RetryCount + 1 {
		key, ok := e.nextKey(snapshot.Keys)
		if !ok {
			break
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, attempts, lastErr
			case <-time.After(time.Duration(100*attempt) * time.Millisecond):
			}
		}
		attempts++
		started := e.now()
		result, err := callOnce(ctx, client, config, key, payload)
		e.record(key, e.now().Sub(started), err)
		if err == nil {
			return result, attempts, nil
		}
		lastErr = err
		var engineErr *engineError
		if errors.As(err, &engineErr) && engineErr.HTTPStatus == http.StatusBadRequest {
			break
		}
	}
	return nil, attempts, lastErr
}

// keyStatuses reports the health of the given keys in their configured order.
func (e *engine) keyStatuses(keys []string) []KeyStatus {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	statuses := make([]KeyStatus, 0, len(keys))
	for _, key := range keys {
		id := content_moderation_setting.KeyID(key)
		status := KeyStatus{ID: id, Status: "untested"}
		if state := e.health[id]; state != nil {
			status.Successes, status.Failures = state.successes, state.failures
			status.LastHTTPStatus, status.LastError, status.LastLatencyMS = state.lastHTTPStatus, state.lastError, state.lastLatencyMS
			status.LastCheckedAt = state.lastCheckedAt.Unix()
			status.Status = "healthy"
			if state.lastError != "" {
				status.Status = "failing"
			}
			if state.frozenUntil.After(now) {
				status.Status, status.FrozenUntil = "frozen", state.frozenUntil.Unix()
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// forget drops the health of keys that are no longer configured.
func (e *engine) forget(keys []string) {
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, content_moderation_setting.KeyID(key))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range e.health {
		if !slices.Contains(ids, id) {
			delete(e.health, id)
		}
	}
}
