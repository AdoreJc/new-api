package codex

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetRequestURLAlphaSearch(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeCodex,
			ChannelBaseUrl: "https://chatgpt.com",
		},
		RelayMode: relayconstant.RelayModeAlphaSearch,
	}

	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://chatgpt.com/backend-api/codex/alpha/search", url)
}

// The Codex backend rejects these fields, so the adaptor clears them rather
// than forwarding what the client sent.
func TestConvertOpenAIResponsesRequestDropsPenalties(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex},
		RelayMode:   relayconstant.RelayModeResponses,
	}

	converted, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, dto.OpenAIResponsesRequest{
		Model:            "gpt-5-codex",
		Input:            json.RawMessage(`"hello"`),
		MaxOutputTokens:  lo.ToPtr(uint(128)),
		Temperature:      lo.ToPtr(1.0),
		FrequencyPenalty: json.RawMessage(`1.5`),
		PresencePenalty:  json.RawMessage(`1.5`),
	})
	require.NoError(t, err)

	request, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.Nil(t, request.MaxOutputTokens)
	assert.Nil(t, request.Temperature)
	assert.Nil(t, request.FrequencyPenalty)
	assert.Nil(t, request.PresencePenalty)
}

// The Codex backend accepts only streamed responses, so Responses requests
// always stream upstream whatever the client asked; compaction is a plain JSON
// call that takes neither stream nor store.
func TestCodexResponsesRequestStreamsUpstreamExceptCompact(t *testing.T) {
	tests := []struct {
		name       string
		relayMode  int
		wantStream *bool
		wantAccept string
	}{
		{name: "responses", relayMode: relayconstant.RelayModeResponses, wantStream: lo.ToPtr(true), wantAccept: "text/event-stream"},
		{name: "compact", relayMode: relayconstant.RelayModeResponsesCompact, wantAccept: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			adaptor := &Adaptor{}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType: constant.ChannelTypeCodex,
					ApiKey:      `{"access_token":"fake-access-token","account_id":"acct-test"}`,
				},
				RelayMode: tt.relayMode,
			}

			converted, err := adaptor.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
				Model:  "gpt-5-codex",
				Input:  json.RawMessage(`"hello"`),
				Stream: lo.ToPtr(false),
			})
			require.NoError(t, err)
			request, ok := converted.(dto.OpenAIResponsesRequest)
			require.True(t, ok)
			assert.Equal(t, tt.wantStream, request.Stream)

			header := http.Header{}
			require.NoError(t, adaptor.SetupRequestHeader(c, &header, info))
			assert.Equal(t, tt.wantAccept, header.Get("Accept"))
		})
	}
}

func convertCodexRequest(t *testing.T, relayMode int, request dto.OpenAIResponsesRequest, setting dto.ChannelSettings) map[string]any {
	t.Helper()
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex, ChannelSetting: setting},
		RelayMode:   relayMode,
	}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	require.NoError(t, err)
	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, common.Unmarshal(encoded, &body))
	return body
}

// Fields the ChatGPT backend rejects are dropped on both the responses and
// the compaction path; nothing is stored upstream, so previous_response_id is
// dropped too.
func TestConvertCodexRequestStripsUnsupportedFields(t *testing.T) {
	for _, relayMode := range []int{relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact} {
		body := convertCodexRequest(t, relayMode, dto.OpenAIResponsesRequest{
			Model:              "gpt-5.5",
			Input:              json.RawMessage(`[{"type":"message","role":"user","content":"hi"}]`),
			Temperature:        lo.ToPtr(0.2),
			TopP:               lo.ToPtr(0.9),
			MaxOutputTokens:    lo.ToPtr(uint(64)),
			PreviousResponseID: "resp_123",
			PromptCacheOptions: json.RawMessage(`{"mode":"x"}`),
			Prompt:             json.RawMessage(`{"id":"pmpt_1"}`),
			TopK:               json.RawMessage(`5`),
			EnableThinking:     json.RawMessage(`true`),
			Stream:             lo.ToPtr(false),
			Store:              json.RawMessage(`true`),
			Reasoning:          &dto.Reasoning{Effort: "high"},
		}, dto.ChannelSettings{})
		for _, field := range []string{"temperature", "top_p", "max_output_tokens", "previous_response_id", "prompt_cache_options", "prompt", "top_k", "enable_thinking"} {
			assert.NotContains(t, body, field, "relay mode %d", relayMode)
		}
		if relayMode == relayconstant.RelayModeResponsesCompact {
			assert.NotContains(t, body, "stream")
			assert.NotContains(t, body, "store")
			// The compaction endpoint is not asked for encrypted reasoning.
			assert.NotContains(t, body, "include")
		} else {
			assert.Equal(t, true, body["stream"])
			assert.Equal(t, false, body["store"])
			assert.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
		}
	}
}

// Native Responses input is rewritten into the form the Codex backend
// accepts under store=false.
func TestConvertCodexRequestNormalizesInput(t *testing.T) {
	tests := []struct {
		name             string
		request          dto.OpenAIResponsesRequest
		wantInput        string
		wantInstructions string
		wantInclude      []any
	}{
		{
			name:             "string input becomes a user message",
			request:          dto.OpenAIResponsesRequest{Input: json.RawMessage(`"hello"`), Instructions: json.RawMessage(`"be brief"`)},
			wantInput:        `[{"content":"hello","role":"user","type":"message"}]`,
			wantInstructions: "be brief",
		},
		{
			name: "system text moves to the instructions",
			request: dto.OpenAIResponsesRequest{
				Instructions: json.RawMessage(`"client rules"`),
				Input: json.RawMessage(`[{"role":"system","content":"sys one"},` +
					`{"type":"message","role":"system","content":[{"type":"input_text","text":"sys two"}]},` +
					`{"type":"message","role":"user","content":"hi"}]`),
			},
			wantInput:        `[{"content":"hi","role":"user","type":"message"}]`,
			wantInstructions: "sys one\n\nsys two\n\nclient rules",
		},
		{
			name: "system with an image stays as a developer message",
			request: dto.OpenAIResponsesRequest{
				Instructions: json.RawMessage(`"x"`),
				Input:        json.RawMessage(`[{"type":"message","role":"system","content":[{"type":"input_image","image_url":"data:,"}]}]`),
			},
			wantInput:        `[{"content":[{"type":"input_image","image_url":"data:,"}],"role":"developer","type":"message"}]`,
			wantInstructions: "x",
		},
		{
			name: "item ids and references are dropped, reasoning gets a summary",
			request: dto.OpenAIResponsesRequest{
				Instructions: json.RawMessage(`"x"`),
				Reasoning:    &dto.Reasoning{Effort: "high"},
				Include:      json.RawMessage(`["reasoning.encrypted_content"]`),
				Input: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"enc"},` +
					`{"type":"item_reference","id":"msg_1"},` +
					`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":"{}"},` +
					`{"type":"function_call_output","call_id":"call_1","output":"ok"}]`),
			},
			wantInput: `[{"encrypted_content":"enc","summary":[],"type":"reasoning"},` +
				`{"arguments":"{}","call_id":"call_1","name":"f","type":"function_call"},` +
				`{"call_id":"call_1","output":"ok","type":"function_call_output"}]`,
			wantInstructions: "x",
			wantInclude:      []any{"reasoning.encrypted_content"},
		},
		{
			name: "reasoning asks for the encrypted content",
			request: dto.OpenAIResponsesRequest{
				Instructions: json.RawMessage(`"x"`),
				Reasoning:    &dto.Reasoning{Effort: "low"},
				Include:      json.RawMessage(`["message.output_text.logprobs"]`),
				Input:        json.RawMessage(`[]`),
			},
			wantInput:        `[]`,
			wantInstructions: "x",
			wantInclude:      []any{"message.output_text.logprobs", "reasoning.encrypted_content"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.request.Model = "gpt-5.5"
			body := convertCodexRequest(t, relayconstant.RelayModeResponses, tt.request, dto.ChannelSettings{})
			input, err := common.Marshal(body["input"])
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantInput, string(input))
			assert.Equal(t, tt.wantInstructions, body["instructions"])
			if tt.wantInclude != nil {
				assert.Equal(t, tt.wantInclude, body["include"])
			}
		})
	}
}

// A 401 is answered by refreshing the credential and sending the request once
// more with the renewed token; a revoked token is not refreshed. A successful
// answer ends a usage-limit cooldown of the channel.
func TestCodexDoRequestRefreshesAfterUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:codex401?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	originalDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = originalDB })

	var tokenCalls int
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"refresh-2","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	originalTokenURL := service.CodexOAuthTokenURL
	service.CodexOAuthTokenURL = tokenServer.URL
	t.Cleanup(func() { service.CodexOAuthTokenURL = originalTokenURL })

	for _, tt := range []struct {
		name           string
		channelID      int
		unauthorized   string
		wantStatus     int
		wantTokenCalls int
		wantAuth       []string
		wantCooling    bool
	}{
		{name: "expired token", channelID: 9401, unauthorized: `{"error":{"code":"token_expired"}}`, wantStatus: http.StatusOK, wantTokenCalls: 1, wantAuth: []string{"Bearer old-access", "Bearer new-access"}},
		{name: "revoked token", channelID: 9402, unauthorized: `{"error":{"code":"token_revoked"}}`, wantStatus: http.StatusUnauthorized, wantAuth: []string{"Bearer old-access"}, wantCooling: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tokenCalls = 0
			var auths, bodies []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auths = append(auths, r.Header.Get("Authorization"))
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				if r.Header.Get("Authorization") != "Bearer new-access" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(tt.unauthorized))
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(upstream.Close)

			key := `{"access_token":"old-access","refresh_token":"refresh-1","account_id":"acct-test"}`
			model.CoolDownCodexChannel(tt.channelID, time.Now().Add(time.Hour))
			t.Cleanup(func() { model.ClearCodexChannelCooldown(tt.channelID) })
			require.NoError(t, db.Create(&model.Channel{Id: tt.channelID, Type: constant.ChannelTypeCodex, Name: "codex", Key: key, Status: common.ChannelStatusEnabled, BaseURL: lo.ToPtr(upstream.URL)}).Error)

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex, ChannelId: tt.channelID, ChannelBaseUrl: upstream.URL, ApiKey: key},
				RelayMode:   relayconstant.RelayModeResponses,
			}
			body, closer, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"gpt-5.5"}`))
			require.NoError(t, err)
			t.Cleanup(func() { _ = closer.Close() })

			resp, err := (&Adaptor{}).DoRequest(c, info, body)
			require.NoError(t, err)
			httpResp := resp.(*http.Response)
			service.CloseResponseBodyGracefully(httpResp)
			assert.Equal(t, tt.wantStatus, httpResp.StatusCode)
			assert.Equal(t, tt.wantTokenCalls, tokenCalls)
			assert.Equal(t, tt.wantAuth, auths)
			assert.Equal(t, tt.wantCooling, model.CodexChannelCoolingDown(tt.channelID, time.Now()))
			for _, sent := range bodies {
				assert.Equal(t, `{"model":"gpt-5.5"}`, sent)
			}
		})
	}
}
