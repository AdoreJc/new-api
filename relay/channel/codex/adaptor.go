package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("codex channel: endpoint not supported")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/messages endpoint not supported")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("codex channel: endpoint not supported")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("codex channel: endpoint not supported")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/chat/completions endpoint not supported")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/rerank endpoint not supported")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/embeddings endpoint not supported")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	isCompact := info != nil && info.RelayMode == relayconstant.RelayModeResponsesCompact

	// The Codex backend rejects role "system" input items and string input;
	// system text moves to the instructions (see normalizeCodexInput).
	if err := normalizeCodexInput(&request); err != nil {
		return nil, err
	}

	if info != nil && info.ChannelSetting.SystemPrompt != "" {
		systemPrompt := info.ChannelSetting.SystemPrompt

		if len(request.Instructions) == 0 {
			if b, err := common.Marshal(systemPrompt); err == nil {
				request.Instructions = b
			} else {
				return nil, err
			}
		} else if info.ChannelSetting.SystemPromptOverride {
			var existing string
			if err := common.Unmarshal(request.Instructions, &existing); err == nil {
				existing = strings.TrimSpace(existing)
				if existing == "" {
					if b, err := common.Marshal(systemPrompt); err == nil {
						request.Instructions = b
					} else {
						return nil, err
					}
				} else {
					if b, err := common.Marshal(systemPrompt + "\n" + existing); err == nil {
						request.Instructions = b
					} else {
						return nil, err
					}
				}
			} else {
				if b, err := common.Marshal(systemPrompt); err == nil {
					request.Instructions = b
				} else {
					return nil, err
				}
			}
		}
	}
	// Codex backend requires the `instructions` field to be present.
	// Keep it consistent with Codex CLI behavior by defaulting to an empty string.
	if len(request.Instructions) == 0 {
		request.Instructions = json.RawMessage(`""`)
	}

	// The ChatGPT backend answers "Unsupported parameter" for these; chat
	// requests converted to Responses carry them (Playground sends top_p).
	// Compaction rejects them as well.
	request.MaxOutputTokens = nil
	request.Temperature = nil
	request.FrequencyPenalty = nil
	request.PresencePenalty = nil
	request.TopP = nil
	request.TopLogProbs = nil
	request.StreamOptions = nil
	request.Truncation = nil
	request.User = nil
	request.Metadata = nil
	request.SafetyIdentifier = nil
	request.PromptCacheRetention = nil
	request.PromptCacheOptions = nil
	request.ChatTemplateKwargs = nil
	request.Stop = nil
	request.Prompt = nil
	request.TopK = nil
	request.MinP = nil
	request.RepetitionPenalty = nil
	request.CacheSalt = nil
	request.Preset = nil
	request.EnableThinking = nil
	request.ThinkingBudget = nil
	// Nothing is stored upstream (store=false), so a previous response can
	// never be found.
	request.PreviousResponseID = ""

	if isCompact {
		// Compaction is a plain JSON call that takes neither field.
		request.Stream = nil
		request.Store = nil
		return request, nil
	}
	// The Codex backend rejects non-streaming requests ("Stream must be set to
	// true"); DoResponse aggregates the stream for non-streaming clients.
	request.Stream = lo.ToPtr(true)
	// codex: store must be false
	request.Store = json.RawMessage("false")
	return request, nil
}

// DoRequest keeps the usage-limit headers of the answer on the request so a
// usage limit of a subscription-bound account can be told apart from other
// errors.
//
// A 401 is retried once on the same channel after refreshing the credential;
// the usage windows of every answer are sampled for the channel, and the body
// of a 429 is kept so the channel can cool down until the limit resets.
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	resp, err := channel.DoApiRequest(a, c, info, requestBody)
	if err == nil && resp != nil && resp.StatusCode == http.StatusUnauthorized {
		resp, err = a.retryAfterUnauthorized(c, info, requestBody, resp)
	}
	var headers http.Header
	var limitBody []byte
	if err == nil && resp != nil {
		headers = resp.Header
		if !info.ChannelIsMultiKey {
			service.RecordCodexPassiveUsage(info.ChannelId, resp.Header)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			limitBody = peekCodexResponseBody(resp)
		}
	}
	service.RecordSubscriptionLimitHeaders(c, headers)
	service.RecordCodexUsageLimitBody(c, limitBody)
	return resp, err
}

// codexUnauthorizedPermanentCodes mark a 401 whose token was invalidated or
// revoked: a refresh cannot help and the account must be authorized again.
var codexUnauthorizedPermanentCodes = []string{"token_invalidated", "token_revoked"}

// retryAfterUnauthorized refreshes the channel credential after a 401 and
// sends the request once more with the renewed token. The original answer is
// returned when the token was revoked, the body cannot be replayed, the
// channel holds several keys, or the refresh fails.
func (a *Adaptor) retryAfterUnauthorized(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader, resp *http.Response) (*http.Response, error) {
	replayable, ok := requestBody.(common.ReplayableBody)
	if !ok || info.ChannelIsMultiKey {
		return resp, nil
	}
	body := strings.ToLower(string(peekCodexResponseBody(resp)))
	for _, code := range codexUnauthorizedPermanentCodes {
		if strings.Contains(body, code) {
			return resp, nil
		}
	}
	var rejected string
	if resp.Request != nil {
		rejected = strings.TrimSpace(strings.TrimPrefix(resp.Request.Header.Get("Authorization"), "Bearer "))
	}
	_, ch, err := service.RefreshCodexChannelCredential(c.Request.Context(), info.ChannelId, service.CodexCredentialRefreshOptions{ResetCaches: true, RejectedAccessToken: rejected})
	if err != nil {
		logger.LogWarn(c, common.LogText("codex channel #%d: refreshing the credential after a 401 failed: %v", info.ChannelId, err))
		return resp, nil
	}
	retryBody, err := replayable.NewReader()
	if err != nil {
		return resp, nil
	}
	service.CloseResponseBodyGracefully(resp)
	info.ApiKey = ch.Key
	logger.LogInfo(c, common.LogText("codex channel #%d: credential refreshed after a 401, sending the request again", info.ChannelId))
	return channel.DoApiRequest(a, c, info, retryBody)
}

// codexErrorBodyLimit bounds how much of an error answer is read to classify it.
const codexErrorBodyLimit = 64 << 10

// peekCodexResponseBody reads an error answer and puts it back for the
// regular error handling.
func peekCodexResponseBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, codexErrorBodyLimit))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case relayconstant.RelayModeAlphaSearch:
		// Alpha search responses are handled by relay.AlphaSearchHelper.
		return nil, types.NewError(errors.New("codex channel: alpha search response should be handled by AlphaSearchHelper"), types.ErrorCodeInvalidRequest)
	case relayconstant.RelayModeResponsesCompact:
		return openai.OaiResponsesCompactionHandler(c, resp)
	case relayconstant.RelayModeResponses:
		if info.IsStream {
			return openai.OaiResponsesStreamHandler(c, info, resp)
		}
		// The upstream always streams (see ConvertOpenAIResponsesRequest) and
		// may omit the event-stream Content-Type, so the body is aggregated
		// whatever the header says.
		return openai.OaiResponsesStreamToNonStreamHandler(c, info, resp)
	default:
		return nil, types.NewError(errors.New("codex channel: endpoint not supported"), types.ErrorCodeInvalidRequest)
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	var path string
	switch info.RelayMode {
	case relayconstant.RelayModeResponses:
		path = "/backend-api/codex/responses"
	case relayconstant.RelayModeResponsesCompact:
		path = "/backend-api/codex/responses/compact"
	case relayconstant.RelayModeAlphaSearch:
		path = "/backend-api/codex/alpha/search"
	default:
		return "", errors.New("codex channel: only /v1/responses, /v1/responses/compact and /v1/alpha/search are supported")
	}
	return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, path, info.ChannelType), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)

	key := strings.TrimSpace(info.ApiKey)
	if !strings.HasPrefix(key, "{") {
		return errors.New("codex channel: key must be a JSON object")
	}

	oauthKey, err := ParseOAuthKey(key)
	if err != nil {
		return err
	}
	// A renewed key whose database write failed replaces the cached key that
	// still carries the rotated-out refresh token.
	if stashed, ok := service.StashedOAuthChannelKey(info.ChannelId, oauthKey.RefreshToken); ok {
		if renewed, err := ParseOAuthKey(stashed); err == nil {
			oauthKey = renewed
		}
	}

	accessToken := strings.TrimSpace(oauthKey.AccessToken)
	accountID := strings.TrimSpace(oauthKey.AccountID)

	if accessToken == "" {
		return errors.New("codex channel: access_token is required")
	}
	if accountID == "" {
		return errors.New("codex channel: account_id is required")
	}

	req.Set("Authorization", "Bearer "+accessToken)
	req.Set("chatgpt-account-id", accountID)

	if req.Get("OpenAI-Beta") == "" {
		req.Set("OpenAI-Beta", "responses=experimental")
	}
	if req.Get("originator") == "" {
		req.Set("originator", "codex_cli_rs")
	}

	// chatgpt.com/backend-api/codex/responses is strict about Content-Type.
	// Clients may omit it or include parameters like `application/json; charset=utf-8`,
	// which can be rejected by the upstream. Force the exact media type.
	req.Set("Content-Type", "application/json")
	// Responses requests are always streamed upstream (see ConvertOpenAIResponsesRequest).
	if info.IsStream || info.RelayMode == relayconstant.RelayModeResponses {
		req.Set("Accept", "text/event-stream")
	} else if req.Get("Accept") == "" {
		req.Set("Accept", "application/json")
	}

	return nil
}
