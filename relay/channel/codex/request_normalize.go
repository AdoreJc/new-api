package codex

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// normalizeCodexInput rewrites a native Responses request into the form the
// ChatGPT Codex backend (store=false, no previous_response_id) accepts, after
// sub2api openai_codex_transform.go:
//   - a string input becomes one user message;
//   - text-only role "system" messages move to the front of the instructions;
//     a system message with other content stays as a "developer" message;
//   - item ids are dropped (an rs_* id replayed under store=false is answered
//     404) and item_reference items are dropped, since nothing is stored to
//     refer to;
//   - a reasoning item without a summary gets an empty one (400 otherwise);
//   - when reasoning is requested, include asks for reasoning.encrypted_content
//     so the next turn can replay it.
func normalizeCodexInput(request *dto.OpenAIResponsesRequest) error {
	if err := ensureCodexReasoningInclude(request); err != nil {
		return err
	}
	input := bytes.TrimSpace(request.Input)
	if len(input) == 0 {
		return nil
	}
	if input[0] == '"' {
		var text string
		if err := common.Unmarshal(input, &text); err != nil {
			return err
		}
		wrapped, err := common.Marshal([]map[string]any{{"type": "message", "role": "user", "content": text}})
		if err != nil {
			return err
		}
		request.Input = wrapped
		return nil
	}
	if input[0] != '[' {
		return nil
	}

	var items []json.RawMessage
	if err := common.Unmarshal(input, &items); err != nil {
		return err
	}
	var systemTexts []string
	normalized := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var item map[string]json.RawMessage
		if err := common.Unmarshal(raw, &item); err != nil || item == nil {
			normalized = append(normalized, raw)
			continue
		}
		itemType := codexRawString(item["type"])
		if itemType == "item_reference" {
			continue
		}
		if codexRawString(item["role"]) == "system" && (itemType == "" || itemType == "message") {
			if text, textOnly := codexContentText(item["content"]); textOnly {
				if text != "" {
					systemTexts = append(systemTexts, text)
				}
				continue
			}
			item["role"] = json.RawMessage(`"developer"`)
		}
		delete(item, "id")
		if itemType == "reasoning" {
			if summary, ok := item["summary"]; !ok || string(bytes.TrimSpace(summary)) == "null" {
				item["summary"] = json.RawMessage(`[]`)
			}
		}
		encoded, err := common.Marshal(item)
		if err != nil {
			return err
		}
		normalized = append(normalized, encoded)
	}
	encoded, err := common.Marshal(normalized)
	if err != nil {
		return err
	}
	request.Input = encoded

	if len(systemTexts) == 0 {
		return nil
	}
	instructions := strings.Join(systemTexts, "\n\n")
	var existing string
	if len(request.Instructions) > 0 && common.Unmarshal(request.Instructions, &existing) == nil && strings.TrimSpace(existing) != "" {
		instructions += "\n\n" + existing
	}
	request.Instructions, err = common.Marshal(instructions)
	return err
}

// codexContentText joins the text of a message content (a string or an array
// of text parts) and reports whether the content held text only.
func codexContentText(content json.RawMessage) (string, bool) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || string(content) == "null" {
		return "", true
	}
	if content[0] == '"' {
		var text string
		if common.Unmarshal(content, &text) != nil {
			return "", false
		}
		return text, true
	}
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if common.Unmarshal(content, &parts) != nil {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
		default:
			return "", false
		}
		if part.Text != nil && *part.Text != "" {
			texts = append(texts, *part.Text)
		}
	}
	return strings.Join(texts, "\n"), true
}

func codexRawString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || common.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// codexReasoningEncryptedContent is the include value that returns the
// encrypted reasoning a store=false conversation replays on the next turn.
const codexReasoningEncryptedContent = "reasoning.encrypted_content"

func ensureCodexReasoningInclude(request *dto.OpenAIResponsesRequest) error {
	if request.Reasoning == nil {
		return nil
	}
	var include []string
	if len(bytes.TrimSpace(request.Include)) > 0 && string(bytes.TrimSpace(request.Include)) != "null" {
		if err := common.Unmarshal(request.Include, &include); err != nil {
			// An include of another shape is left for the upstream to judge.
			return nil
		}
	}
	if slices.Contains(include, codexReasoningEncryptedContent) {
		return nil
	}
	encoded, err := common.Marshal(append(include, codexReasoningEncryptedContent))
	if err != nil {
		return err
	}
	request.Include = encoded
	return nil
}

// codexInstructionsEmpty reports a missing, null or blank string instructions.
func codexInstructionsEmpty(instructions json.RawMessage) bool {
	trimmed := bytes.TrimSpace(instructions)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return true
	}
	var text string
	return common.Unmarshal(trimmed, &text) == nil && strings.TrimSpace(text) == ""
}

// removeResponsesExperimentalBeta drops the responses=experimental beta,
// which the Codex CLI does not send to the ChatGPT backend; other beta values
// stay.
func removeResponsesExperimentalBeta(header http.Header) {
	values := header.Values("OpenAI-Beta")
	if len(values) == 0 {
		return
	}
	var kept []string
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" && !strings.EqualFold(part, "responses=experimental") {
				kept = append(kept, part)
			}
		}
	}
	header.Del("OpenAI-Beta")
	if len(kept) > 0 {
		header.Set("OpenAI-Beta", strings.Join(kept, ","))
	}
}
