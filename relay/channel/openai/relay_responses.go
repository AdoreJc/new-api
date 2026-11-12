package openai

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			sr.Error(err)
			return
		}
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
		if err := helper.ResponseChunkData(c, streamResponse, data); err != nil {
			sr.Stop(err)
			return
		}
		if (streamResponse.Type == "response.completed" || streamResponse.Type == "response.done") &&
			streamResponse.Response != nil && info.StreamStatus.ResponseOutcome() == string(relaycommon.ResponseOutcomeCompleted) {
			sr.Done()
		}
	})

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	return accumulator.Finish(), nil
}

// OaiResponsesStreamToNonStreamHandler answers a non-streaming Responses
// client from an upstream that only streams (Codex). Usage comes from the same
// accumulator as the streaming path. The client receives the terminal event's
// response; when that response carries no output, the items of the
// output_item.done events are filled in.
func OaiResponsesStreamToNonStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.RequireTerminal()
	accumulator := service.NewResponsesUsageAccumulator(info)
	var finalResponse []byte
	var outputItems [][]byte

	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	for finalResponse == nil && scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		if data == "" {
			continue
		}
		var event dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal buffered responses stream event: %s", err.Error()))
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		raw := []byte(data)
		accumulator.Observe(&event, raw)
		switch event.Type {
		case dto.ResponsesOutputTypeItemDone:
			if item := ojson.GetBytes(raw, "item"); item.IsObject() {
				outputItems = append(outputItems, []byte(item.Raw))
			}
		case "response.completed", "response.done", "response.incomplete":
			if response := ojson.GetBytes(raw, "response"); response.IsObject() {
				finalResponse = []byte(response.Raw)
			}
		case "response.failed", "response.error":
			if event.Response != nil {
				if oaiErr := event.Response.GetOpenAIError(); oaiErr != nil && oaiErr.Type != "" {
					return nil, types.WithOpenAIError(*oaiErr, http.StatusInternalServerError)
				}
			}
			return nil, types.NewOpenAIError(fmt.Errorf("responses stream error: %s", event.Type), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		case "error":
			return nil, ResponsesErrorEventError(&event, raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	if finalResponse == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("responses stream ended without a terminal event"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	if !ojson.GetBytes(finalResponse, "output.0").Exists() && len(outputItems) > 0 {
		output := append([]byte{'['}, bytes.Join(outputItems, []byte{','})...)
		output = append(output, ']')
		patched, err := ojson.SetRawBytes(finalResponse, "output", output)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		finalResponse = patched
	}

	resp.Header.Set("Content-Type", "application/json")
	service.IOCopyBytesGracefully(c, resp, finalResponse)
	return accumulator.Finish(), nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !ojson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := ojson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}

// ResponsesErrorEventError converts a bare `error` stream event, which the
// Codex backend sends instead of response.failed (for example when the
// account's usage limit is reached). The event carries code and message at the
// top level or inside an "error" object; the code also serves as the type so
// usage-limit codes stay recognisable, and those codes answer 429.
func ResponsesErrorEventError(event *dto.ResponsesStreamResponse, raw []byte) *types.NewAPIError {
	oaiErr := types.OpenAIError{Message: event.Message, Type: event.Code, Param: event.Param, Code: event.Code}
	if nested := ojson.GetBytes(raw, "error"); nested.IsObject() {
		code := nested.Get("code").String()
		if code == "" {
			code = nested.Get("type").String()
		}
		if oaiErr.Type == "" {
			oaiErr.Type = code
			oaiErr.Code = code
		}
		if oaiErr.Message == "" {
			oaiErr.Message = nested.Get("message").String()
		}
		if oaiErr.Param == "" {
			oaiErr.Param = nested.Get("param").String()
		}
	}
	if oaiErr.Message == "" {
		oaiErr.Message = "responses stream error"
	}
	status := http.StatusInternalServerError
	switch oaiErr.Type {
	case "usage_limit_reached", "rate_limit_exceeded", "rate_limit_error", "insufficient_quota":
		status = http.StatusTooManyRequests
	}
	return types.WithOpenAIError(oaiErr, status)
}
