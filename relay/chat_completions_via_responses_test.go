package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	openaichannel "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareResponsesRequestRetainsConvertedAdaptor(t *testing.T) {
	type capturedRequest struct {
		path string
		body []byte
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capturedRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-4o")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeAdvancedCustom)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "test-key")
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{
		AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{
			IncomingPath: "/v1/responses", UpstreamPath: "/v1/chat/completions", Converter: relayconvert.ConverterOpenAIResponsesToOpenAIChat,
		}}},
	})
	request := &dto.OpenAIResponsesRequest{Model: "gpt-4o", Input: []byte(`"hello"`)}
	info := relaycommon.GenRelayInfoResponses(c, request)
	adaptor, body, closer, apiErr := PrepareResponsesRequest(c, info, request)
	require.Nil(t, apiErr)
	defer closer.Close()
	response, err := adaptor.DoRequest(c, info, body)
	require.NoError(t, err)
	httpResponse, ok := response.(*http.Response)
	require.True(t, ok)
	usage, apiErr := adaptor.DoResponse(c, httpResponse, info)
	require.Nil(t, apiErr)
	require.IsType(t, &dto.Usage{}, usage)
	assert.Equal(t, 5, usage.(*dto.Usage).TotalTokens)

	upstream := <-captured
	assert.Equal(t, "/v1/chat/completions", upstream.path)
	var upstreamRequest dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(upstream.body, &upstreamRequest))
	require.Len(t, upstreamRequest.Messages, 1)
	assert.Equal(t, "hello", upstreamRequest.Messages[0].StringContent())
	assert.Equal(t, []relaytypes.RelayFormat{relaytypes.RelayFormatOpenAIResponses, relaytypes.RelayFormatOpenAI}, info.RequestConversionChain)
}

func TestIsResponsesEventStreamContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "plain", contentType: "text/event-stream", want: true},
		{name: "mixed case with charset", contentType: "Text/Event-Stream; charset=utf-8", want: true},
		{name: "json", contentType: "application/json", want: false},
		{name: "empty", contentType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isResponsesEventStreamContentType(tt.contentType))
		})
	}
}

func TestRecalcQuotaFromRatiosIgnoresInvalidMultipliers(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: hosttypes.PriceData{
			Quota: 100,
		},
	}
	info.PriceData.AddOtherRatio("duration", 2)

	quota, ok := recalcQuotaFromRatios(info, map[string]float64{
		"duration": 3,
		"zero":     0,
		"negative": -1,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
	})

	require.True(t, ok)
	assert.Equal(t, 150, quota)
	assert.True(t, info.PriceData.HasOtherRatio("duration"))
}

func TestRecalcQuotaFromRatiosRejectsAllInvalidAdjustedRatios(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: hosttypes.PriceData{
			Quota: 100,
		},
	}
	info.PriceData.AddOtherRatio("duration", 2)

	quota, ok := recalcQuotaFromRatios(info, map[string]float64{
		"zero":     0,
		"negative": -1,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
	})

	require.False(t, ok)
	assert.Equal(t, 0, quota)
	assert.True(t, info.PriceData.HasOtherRatio("duration"))
}

func TestTextRequestViaResponsesConvertsClaudeDirectly(t *testing.T) {
	type capturedRequest struct {
		path string
		body []byte
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capturedRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"object":"response",
			"status":"completed",
			"model":"gpt-5.6-sol",
			"output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	info := &relaycommon.RelayInfo{
		RelayMode:              relayconstant.RelayModeChatCompletions,
		RelayFormat:            relaytypes.RelayFormatClaude,
		OriginModelName:        "gpt-5.6-sol",
		RequestConversionChain: []relaytypes.RelayFormat{relaytypes.RelayFormatClaude},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    server.URL,
			ApiKey:            "test-key",
			UpstreamModelName: "gpt-5.6-sol",
		},
	}
	adaptor := &openaichannel.Adaptor{}
	adaptor.Init(info)
	request := &dto.ClaudeRequest{
		Model:    "gpt-5.6-sol",
		Thinking: &dto.Thinking{Type: "adaptive", Display: "summarized"},
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	}

	usage, apiErr := textRequestViaResponses(c, info, adaptor, request)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.Equal(t, []relaytypes.RelayFormat{relaytypes.RelayFormatClaude, relaytypes.RelayFormatOpenAIResponses}, info.RequestConversionChain)

	upstream := <-captured
	assert.Equal(t, "/v1/responses", upstream.path)
	var upstreamBody map[string]any
	require.NoError(t, common.Unmarshal(upstream.body, &upstreamBody))
	assert.NotContains(t, upstreamBody, "messages")
	reasoning, ok := upstreamBody["reasoning"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "high", reasoning["effort"])
	assert.Equal(t, "detailed", reasoning["summary"])

	var response dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Content, 1)
	assert.Equal(t, "ok", response.Content[0].GetText())
}

func TestApplySystemPromptIfNeededSkipsToolLoadingMessages(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","function":{"name":"get_current_time","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`)
	toolLoading := dto.Message{Role: "system", Tools: tools}
	user := dto.Message{Role: "user", Content: "What time is it in Beijing?"}

	tests := []struct {
		name         string
		messages     []dto.Message
		wantMessages []dto.Message
		wantOverride bool
	}{
		{
			name:     "tool loading message alone is not a system prompt",
			messages: []dto.Message{toolLoading, user},
			wantMessages: []dto.Message{
				{Role: "system", Content: "Answer in English."},
				toolLoading,
				user,
			},
		},
		{
			name:     "override targets the real system prompt only",
			messages: []dto.Message{toolLoading, {Role: "system", Content: "You are Kimi."}, user},
			wantMessages: []dto.Message{
				toolLoading,
				{Role: "system", Content: "Answer in English.\nYou are Kimi."},
				user,
			},
			wantOverride: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelSetting: dto.ChannelSettings{
						SystemPrompt:         "Answer in English.",
						SystemPromptOverride: true,
					},
				},
			}
			request := &dto.GeneralOpenAIRequest{
				Model:    "kimi-k3",
				Messages: append([]dto.Message(nil), tt.messages...),
			}

			applySystemPromptIfNeeded(c, info, request)

			require.Len(t, request.Messages, len(tt.wantMessages))
			for i, want := range tt.wantMessages {
				got := request.Messages[i]
				assert.Equal(t, want.Role, got.Role, "message %d role", i)
				assert.Equal(t, want.Content, got.Content, "message %d content", i)
				if len(want.Tools) > 0 {
					assert.JSONEq(t, string(want.Tools), string(got.Tools), "message %d tools", i)
				} else {
					assert.Empty(t, got.Tools, "message %d tools", i)
				}
			}
			_, overrideSet := common.GetContextKey(c, constant.ContextKeySystemPromptOverride)
			assert.Equal(t, tt.wantOverride, overrideSet)
		})
	}
}

const codexStreamOnlySSE = `event: response.created
data: {"type":"response.created","response":{"id":"resp_codex","object":"response","status":"in_progress","model":"gpt-5-codex","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"item_id":"msg_1","content_index":0,"delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"item_id":"msg_1","content_index":0,"delta":" world"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello world","annotations":[]}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_codex","object":"response","status":"completed","model":"gpt-5-codex","output":[],"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":3},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":18}}}

`

// newCodexStreamOnlyUpstream behaves like chatgpt.com/backend-api/codex: it
// rejects requests that do not stream and answers the others with a stream
// whose terminal event carries no output, as the Codex backend does. The
// stream goes out with contentType; an empty one sends no Content-Type at all,
// which the ChatGPT backend can do.
func newCodexStreamOnlyUpstream(t *testing.T, contentType string) (*httptest.Server, <-chan map[string]any) {
	t.Helper()
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := common.DecodeJson(r.Body, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- body
		if r.URL.Path != "/backend-api/codex/responses" || body["stream"] != true || r.Header.Get("Accept") != "text/event-stream" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"Stream must be set to true"}`))
			return
		}
		if contentType == "" {
			// A nil value stops net/http from sniffing a type for the body.
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write([]byte(codexStreamOnlySSE))
	}))
	t.Cleanup(server.Close)
	return server, received
}

func newCodexRelayContext(t *testing.T, path string, baseURL string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-5-codex")
	common.SetContextKey(c, constant.ContextKeyChannelId, 45)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeCodex)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, `{"access_token":"fake-access-token","account_id":"acct-test"}`)
	return c, recorder
}

func TestShouldChatCompletionsUseResponsesRoutesCodexWithoutPolicy(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	oldPolicy := settings.ChatCompletionsToResponsesPolicy
	settings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{}
	t.Cleanup(func() { settings.ChatCompletionsToResponsesPolicy = oldPolicy })

	assert.True(t, service.ShouldChatCompletionsUseResponses(45, constant.ChannelTypeCodex, "gpt-5-codex"))
	assert.False(t, service.ShouldChatCompletionsUseResponses(1, constant.ChannelTypeOpenAI, "gpt-5"))
}

// codexUpstreamContentTypes are the Content-Type values the Codex streams are
// answered with; the body is a stream whatever the header says.
var codexUpstreamContentTypes = []string{"text/event-stream", "text/event-stream; charset=utf-8", "application/json", ""}

func TestTextRequestViaResponsesCodexStreamsUpstream(t *testing.T) {
	for _, contentType := range codexUpstreamContentTypes {
		for _, clientStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("content type %q client stream %v", contentType, clientStream), func(t *testing.T) {
				server, received := newCodexStreamOnlyUpstream(t, contentType)
				c, recorder := newCodexRelayContext(t, "/v1/chat/completions", server.URL)
				request := &dto.GeneralOpenAIRequest{
					Model:    "gpt-5-codex",
					Messages: []dto.Message{{Role: "user", Content: "hello"}},
					Stream:   lo.ToPtr(clientStream),
					TopP:     lo.ToPtr(0.9),
				}
				info := relaycommon.GenRelayInfoOpenAI(c, request)
				info.InitChannelMeta(c)
				info.ShouldIncludeUsage = true
				adaptor := &codex.Adaptor{}
				adaptor.Init(info)

				usage, apiErr := textRequestViaResponses(c, info, adaptor, request)

				require.Nil(t, apiErr)
				upstream := <-received
				assert.Equal(t, true, upstream["stream"])
				// The request policy counts the attempt as successful only on a
				// normal end without stream errors.
				assert.True(t, info.StreamStatus.IsNormalEnd())
				assert.False(t, info.StreamStatus.HasErrors())
				assert.NotContains(t, upstream, "messages")
				assert.NotContains(t, upstream, "top_p", "the Codex backend rejects top_p")
				require.NotNil(t, usage)
				assert.Equal(t, 11, usage.PromptTokens)
				assert.Equal(t, 7, usage.CompletionTokens)
				assert.Equal(t, 3, usage.PromptTokensDetails.CachedTokens)

				if clientStream {
					assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
					assert.Contains(t, recorder.Body.String(), `"content":"Hello"`)
					assert.Contains(t, recorder.Body.String(), "data: [DONE]")
					return
				}
				assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
				var response dto.OpenAITextResponse
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				require.Len(t, response.Choices, 1)
				assert.Equal(t, "Hello world", response.Choices[0].Message.StringContent())
				assert.Equal(t, 18, response.Usage.TotalTokens)
			})
		}
	}
}

func TestCodexResponsesRequestAlwaysStreamsUpstream(t *testing.T) {
	for _, contentType := range codexUpstreamContentTypes {
		for _, clientStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("content type %q client stream %v", contentType, clientStream), func(t *testing.T) {
				server, received := newCodexStreamOnlyUpstream(t, contentType)
				c, recorder := newCodexRelayContext(t, "/v1/responses", server.URL)
				request := &dto.OpenAIResponsesRequest{Model: "gpt-5-codex", Input: []byte(`"hello"`), Stream: lo.ToPtr(clientStream)}
				info := relaycommon.GenRelayInfoResponses(c, request)
				adaptor, body, closer, apiErr := PrepareResponsesRequest(c, info, request)
				require.Nil(t, apiErr)
				defer closer.Close()

				response, err := adaptor.DoRequest(c, info, body)
				require.NoError(t, err)
				httpResponse, ok := response.(*http.Response)
				require.True(t, ok)
				require.Equal(t, http.StatusOK, httpResponse.StatusCode)
				usage, apiErr := adaptor.DoResponse(c, httpResponse, info)

				require.Nil(t, apiErr)
				upstream := <-received
				assert.Equal(t, true, upstream["stream"])
				// The request policy counts the attempt as successful only on a
				// normal end without stream errors.
				assert.True(t, info.StreamStatus.IsNormalEnd())
				assert.False(t, info.StreamStatus.HasErrors())
				require.IsType(t, &dto.Usage{}, usage)
				assert.Equal(t, 11, usage.(*dto.Usage).PromptTokens)
				assert.Equal(t, 7, usage.(*dto.Usage).CompletionTokens)
				assert.Equal(t, 3, usage.(*dto.Usage).PromptTokensDetails.CachedTokens)

				if clientStream {
					assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
					assert.Contains(t, recorder.Body.String(), `"type":"response.output_text.delta"`)
					assert.Contains(t, recorder.Body.String(), `"type":"response.completed"`)
					return
				}
				assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
				var aggregated dto.OpenAIResponsesResponse
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &aggregated))
				assert.Equal(t, "resp_codex", aggregated.ID)
				require.Len(t, aggregated.Output, 1)
				require.Len(t, aggregated.Output[0].Content, 1)
				assert.Equal(t, "Hello world", aggregated.Output[0].Content[0].Text)
				require.NotNil(t, aggregated.Usage)
				assert.Equal(t, 18, aggregated.Usage.TotalTokens)
			})
		}
	}
}

// The subscription fallback reads the status, error type and limit headers of
// a failed attempt; the bridge must keep all three for Codex channels.
func TestTextRequestViaResponsesCodexKeepsUsageLimitDetectable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Codex-Primary-Used-Percent", "100")
		w.Header().Set("X-Codex-Primary-Reset-After-Seconds", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`))
	}))
	defer server.Close()
	c, _ := newCodexRelayContext(t, "/v1/chat/completions", server.URL)
	request := &dto.GeneralOpenAIRequest{Model: "gpt-5-codex", Messages: []dto.Message{{Role: "user", Content: "hello"}}}
	info := relaycommon.GenRelayInfoOpenAI(c, request)
	info.InitChannelMeta(c)
	adaptor := &codex.Adaptor{}
	adaptor.Init(info)

	_, apiErr := textRequestViaResponses(c, info, adaptor, request)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.Equal(t, "usage_limit_reached", apiErr.ToOpenAIError().Type)
	headers, ok := common.GetContextKeyType[http.Header](c, constant.ContextKeyUpstreamRateLimitHeaders)
	require.True(t, ok)
	assert.Equal(t, "100", headers.Get("X-Codex-Primary-Used-Percent"))
	_, limited := service.DetectSubscriptionAccountLimit(apiErr, headers, nil, time.Now())
	assert.True(t, limited)
}
