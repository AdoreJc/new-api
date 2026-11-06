// Package claude_oauth relays Anthropic Messages requests with a Claude
// subscription OAuth token instead of an API key. Request and response
// conversion is the Anthropic adaptor's; only authentication, the client
// identity headers and the Claude Code system prompt prefix differ.
package claude_oauth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	ChannelName = "claude_oauth"

	betaClaudeCode               = "claude-code-20250219"
	betaInterleavedThinking      = "interleaved-thinking-2025-05-14"
	betaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	tokenRefreshTimeout          = 15 * time.Second
)

type Adaptor struct {
	claude.Adaptor
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

// GetRequestURL always adds the beta=true query the Claude Code CLI sends: the
// Messages endpoint only accepts a subscription OAuth token on that path and
// answers "invalid x-api-key" without it.
func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	requestURL, err := a.Adaptor.GetRequestURL(info)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("beta", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// DoRequest must be redeclared here: the embedded Claude adaptor passes its
// own receiver to channel.DoApiRequest, which would use the API-key headers
// and URL instead of the overrides above.
//
// Every upstream answer, streamed or not and whatever its status, carries the
// account's rate-limit headers; they are sampled here so the usage dialog can
// show windows the usage API leaves out, and kept on the request so a usage
// limit of a subscription-bound account can be told apart from other errors.
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	resp, err := channel.DoApiRequest(a, c, info, requestBody)
	var headers http.Header
	if err == nil && resp != nil {
		headers = resp.Header
		if info != nil {
			service.RecordClaudePassiveUsage(info.ChannelId, resp.Header)
		}
	}
	service.RecordSubscriptionLimitHeaders(c, headers)
	return resp, err
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return withClaudeCodeSystemPrompt(a.Adaptor.ConvertClaudeRequest(c, info, request))
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return withClaudeCodeSystemPrompt(a.Adaptor.ConvertOpenAIRequest(c, info, request))
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return withClaudeCodeSystemPrompt(a.Adaptor.ConvertGeminiRequest(c, info, request))
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return withClaudeCodeSystemPrompt(a.Adaptor.ConvertOpenAIResponsesRequest(c, info, request))
}

func withClaudeCodeSystemPrompt(converted any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if request, ok := converted.(*dto.ClaudeRequest); ok {
		EnsureClaudeCodeSystemPrompt(request)
	}
	return converted, nil
}

// EnsureClaudeCodeSystemPrompt makes the system prompt start with the Claude
// Code identity block the subscription token is scoped to, keeping whatever
// the client sent after it.
func EnsureClaudeCodeSystemPrompt(request *dto.ClaudeRequest) {
	identity := service.ClaudeCodeSystemPrompt
	switch system := request.System.(type) {
	case nil:
		request.System = identity
	case string:
		trimmed := strings.TrimSpace(system)
		if trimmed == "" {
			request.System = identity
			return
		}
		if hasClaudeCodePrefix(trimmed) {
			return
		}
		request.System = []dto.ClaudeMediaMessage{textBlock(identity), textBlock(system)}
	default:
		blocks := request.ParseSystem()
		if len(blocks) == 0 {
			request.System = identity
			return
		}
		if blocks[0].Type == "text" && hasClaudeCodePrefix(strings.TrimSpace(blocks[0].GetText())) {
			return
		}
		request.System = append([]dto.ClaudeMediaMessage{textBlock(identity)}, blocks...)
	}
}

func hasClaudeCodePrefix(system string) bool {
	return strings.HasPrefix(system, "You are Claude Code, Anthropic's official CLI for Claude")
}

func textBlock(text string) dto.ClaudeMediaMessage {
	return dto.ClaudeMediaMessage{Type: "text", Text: &text}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)

	key, err := service.ParseClaudeOAuthKey(info.ApiKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), tokenRefreshTimeout)
	defer cancel()
	key, err = service.EnsureClaudeChannelAccessToken(ctx, info.ChannelId, key)
	if err != nil {
		return err
	}

	req.Del("x-api-key")
	req.Set("Authorization", "Bearer "+key.AccessToken)
	anthropicVersion := c.Request.Header.Get("anthropic-version")
	if anthropicVersion == "" {
		anthropicVersion = "2023-06-01"
	}
	req.Set("anthropic-version", anthropicVersion)
	claude.CommonClaudeHeadersOperation(c, req, info)
	req.Set("anthropic-beta", MergeClaudeOAuthBetas(req.Get("anthropic-beta")))

	userAgent := c.Request.Header.Get("User-Agent")
	if !strings.Contains(strings.ToLower(userAgent), "claude-cli") {
		userAgent = service.ClaudeCLIUserAgent()
	}
	req.Set("User-Agent", userAgent)
	req.Set("x-app", "cli")
	if req.Get("anthropic-dangerous-direct-browser-access") == "" {
		req.Set("anthropic-dangerous-direct-browser-access", "true")
	}
	return nil
}

// MergeClaudeOAuthBetas puts the betas a subscription token requires in front
// of whatever the client asked for, without duplicates. A client that sent no
// beta header gets the Claude Code CLI default set (sub2api DefaultBetaHeader).
func MergeClaudeOAuthBetas(clientBetas string) string {
	required := []string{betaClaudeCode, service.ClaudeOAuthBeta}
	merged := slices.Clone(required)
	seen := false
	for item := range strings.SplitSeq(clientBetas, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		seen = true
		if !slices.Contains(merged, item) {
			merged = append(merged, item)
		}
	}
	if !seen {
		merged = append(merged, betaInterleavedThinking, betaFineGrainedToolStreaming)
	}
	return strings.Join(merged, ",")
}
