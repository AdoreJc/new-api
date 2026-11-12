package promptaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// Protocols select the request body layout used for text extraction.
const (
	ProtocolChat      = "openai_chat"
	ProtocolClaude    = "anthropic_messages"
	ProtocolGemini    = "gemini"
	ProtocolResponses = "openai_responses"
	ProtocolImages    = "openai_images"
	// ProtocolGeneric tries chat, Responses, Gemini and prompt-like keys in turn
	// (legacy completions, moderations, edits).
	ProtocolGeneric = "generic"
)

const (
	previewMaxRunes    = 96
	fullPromptMaxRunes = 65536
	// prioritySeparator splits the latest user input from the rest of the
	// transcript so the first Guard chunk always starts with the newest turn.
	prioritySeparator = "\x00NEWAPI_PROMPT_AUDIT_PRIORITY_END\x00"
)

var (
	errNoPromptText  = errors.New("prompt audit request contains no text")
	errInvalidJSON   = errors.New("prompt audit request JSON is invalid")
	bearerPattern    = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+\-/]+=*`)
	apiKeyPattern    = regexp.MustCompile(`(?i)\b(sk|rk|pk|api[_-]?key|token|secret|password)[-_:=\s]+[A-Za-z0-9._~+\-/]{8,}`)
	canaryPattern    = regexp.MustCompile(`(?i)([A-Z]+_CANARY_)[A-Za-z0-9_-]+`)
	emailPattern     = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	phonePattern     = regexp.MustCompile(`(?:\+?\d[\d\s().-]{8,}\d)`)
	clientTextRoles  = []string{"user", "system", "developer", "assistant", "tool"}
	contentTextTypes = []string{"", "text", "input_text", "output_text"}
)

type promptSegment struct {
	text string
	role string
}

func (s promptSegment) isUser() bool { return s.role == "" || s.role == "user" }

// promptSnapshot is the extracted, client-controlled text of one request.
type promptSnapshot struct {
	ScanText     string
	FullPrompt   string
	Preview      string
	Hash         string
	Length       int
	MessageCount int
}

// extractPromptSnapshot reads every client-controlled text of the request
// (system, developer, user, assistant, tool) and puts the latest user input
// first. Images, audio and files are ignored.
func extractPromptSnapshot(protocol string, body []byte) (*promptSnapshot, error) {
	var document any
	if err := common.Unmarshal(body, &document); err != nil {
		return nil, errInvalidJSON
	}
	root, _ := document.(map[string]any)
	segments := latestUserFirst(protocolSegments(protocol, root))
	if len(segments) == 0 {
		return nil, errNoPromptText
	}
	fullText := strings.Join(segments, "\n\n")
	scanText := fullText
	if len(segments) > 1 {
		scanText = segments[0] + prioritySeparator + strings.Join(segments[1:], "\n\n")
	}
	digest := sha256.Sum256([]byte(fullText))
	return &promptSnapshot{
		ScanText:     scanText,
		FullPrompt:   buildFullPrompt(fullText),
		Preview:      buildPromptPreview(fullText),
		Hash:         hex.EncodeToString(digest[:]),
		Length:       utf8.RuneCountInString(fullText),
		MessageCount: len(segments),
	}, nil
}

func protocolSegments(protocol string, root map[string]any) []promptSegment {
	if root == nil {
		return nil
	}
	switch protocol {
	case ProtocolChat:
		return messageSegments(root["messages"])
	case ProtocolClaude:
		return append(instructionSegments(root["system"]), messageSegments(root["messages"])...)
	case ProtocolGemini:
		return geminiRootSegments(root)
	case ProtocolResponses:
		return append(instructionSegments(root["instructions"]), responsesInputSegments(root["input"])...)
	case ProtocolImages:
		return userSegments(mediaPrompts(root))
	}
	if segments := messageSegments(root["messages"]); len(segments) > 0 {
		return segments
	}
	if segments := append(instructionSegments(root["instructions"]), responsesInputSegments(root["input"])...); len(segments) > 0 {
		return segments
	}
	if segments := geminiRootSegments(root); len(segments) > 0 {
		return segments
	}
	return userSegments(mediaPrompts(root))
}

func messageSegments(value any) []promptSegment {
	items, _ := value.([]any)
	result := make([]promptSegment, 0, len(items))
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		if !slices.Contains(clientTextRoles, role) {
			continue
		}
		for _, text := range contentTexts(message["content"]) {
			result = append(result, promptSegment{text: text, role: role})
		}
	}
	return result
}

func instructionSegments(value any) []promptSegment {
	if text, ok := value.(string); ok {
		return roleSegments([]string{text}, "system")
	}
	return roleSegments(contentTexts(value), "system")
}

func responsesInputSegments(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		return userSegments([]string{typed})
	case map[string]any:
		return responsesItemSegments(typed)
	case []any:
		result := make([]promptSegment, 0, len(typed))
		for _, item := range typed {
			switch entry := item.(type) {
			case string:
				result = append(result, promptSegment{text: entry, role: "user"})
			case map[string]any:
				result = append(result, responsesItemSegments(entry)...)
			}
		}
		return result
	}
	return nil
}

// responsesItemSegments reads a message-like Responses input item. Items with
// a non-client role (for example tool call outputs typed by the server) are
// skipped; items without a role are treated as user input.
func responsesItemSegments(item map[string]any) []promptSegment {
	role := strings.ToLower(stringValue(item["role"]))
	if role != "" && !slices.Contains(clientTextRoles, role) {
		return nil
	}
	if content, exists := item["content"]; exists {
		return roleSegments(contentTexts(content), role)
	}
	return roleSegments([]string{stringValue(item["text"])}, role)
}

func geminiRootSegments(root map[string]any) []promptSegment {
	result := geminiSystemSegments(root["systemInstruction"])
	result = append(result, geminiSystemSegments(root["system_instruction"])...)
	result = append(result, geminiContentSegments(root["contents"])...)
	if requests, ok := root["requests"].([]any); ok {
		for _, item := range requests {
			if request, ok := item.(map[string]any); ok {
				result = append(result, geminiSystemSegments(request["systemInstruction"])...)
				result = append(result, geminiContentSegments(request["contents"])...)
			}
		}
	}
	if instances, ok := root["instances"].([]any); ok {
		for _, item := range instances {
			if instance, ok := item.(map[string]any); ok {
				result = append(result, userSegments([]string{stringValue(instance["prompt"])})...)
			}
		}
	}
	return result
}

func geminiContentSegments(value any) []promptSegment {
	contents, ok := value.([]any)
	if !ok {
		if single, isObject := value.(map[string]any); isObject {
			contents = []any{single}
		}
	}
	result := make([]promptSegment, 0, len(contents))
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(content["role"]))
		if role == "model" {
			role = "assistant"
		}
		if role != "" && !slices.Contains(clientTextRoles, role) {
			continue
		}
		result = append(result, roleSegments(geminiPartTexts(content["parts"]), role)...)
	}
	return result
}

func geminiSystemSegments(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		return roleSegments([]string{typed}, "system")
	case map[string]any:
		return roleSegments(geminiPartTexts(typed["parts"]), "system")
	}
	return nil
}

func geminiPartTexts(value any) []string {
	parts, _ := value.([]any)
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if object, ok := part.(map[string]any); ok {
			texts = append(texts, stringValue(object["text"]))
		}
	}
	return texts
}

// contentTexts reads a string content or the text parts of a content array,
// skipping image, audio and file parts.
func contentTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case map[string]any:
		return []string{stringValue(typed["text"])}
	case []any:
		texts := make([]string, 0, len(typed))
		for _, part := range typed {
			object, ok := part.(map[string]any)
			if !ok || !slices.Contains(contentTextTypes, strings.ToLower(stringValue(object["type"]))) {
				continue
			}
			texts = append(texts, stringValue(object["text"]))
		}
		return texts
	}
	return nil
}

var mediaPromptKeys = []string{"prompt", "inputprompt", "textprompt", "description", "query", "lyrics",
	"negativeprompt", "positiveprompt", "imageprompt", "input", "instruction"}

// mediaPrompts collects prompt-like string fields of image and legacy text
// requests, skipping data URLs, links and base64 payloads.
func mediaPrompts(root map[string]any) []string {
	result := make([]string, 0, 2)
	for _, key := range sortedKeys(root) {
		normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
		if !slices.Contains(mediaPromptKeys, normalized) {
			continue
		}
		values := []any{root[key]}
		if list, ok := root[key].([]any); ok {
			values = list
		}
		for _, value := range values {
			text, ok := value.(string)
			text = strings.TrimSpace(text)
			if !ok || text == "" || looksLikeMediaPayload(text) || slices.Contains(result, text) {
				continue
			}
			result = append(result, text)
		}
	}
	return result
}

func looksLikeMediaPayload(value string) bool {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	if len(value) < 256 {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '+' && r != '/' && r != '='
	})
}

// latestUserFirst drops empty texts and moves the most recent user text to the
// front, keeping the rest in request order.
func latestUserFirst(segments []promptSegment) []string {
	normalized := make([]promptSegment, 0, len(segments))
	for _, segment := range segments {
		segment.text = strings.TrimSpace(segment.text)
		if segment.text != "" {
			normalized = append(normalized, segment)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	priority := len(normalized) - 1
	for index := len(normalized) - 1; index >= 0; index-- {
		if normalized[index].isUser() {
			priority = index
			break
		}
	}
	result := make([]string, 0, len(normalized))
	result = append(result, normalized[priority].text)
	for index, segment := range normalized {
		if index != priority {
			result = append(result, segment.text)
		}
	}
	return result
}

func roleSegments(texts []string, role string) []promptSegment {
	result := make([]promptSegment, 0, len(texts))
	for _, text := range texts {
		result = append(result, promptSegment{text: text, role: role})
	}
	return result
}

func userSegments(texts []string) []promptSegment { return roleSegments(texts, "user") }

// redactPreview masks common credential, email and phone patterns.
func redactPreview(value string, maxRunes int) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer ***")
	value = apiKeyPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, ":= \t"); index >= 0 {
			return match[:index+1] + "***"
		}
		return "***"
	})
	value = canaryPattern.ReplaceAllString(value, "${1}***")
	value = emailPattern.ReplaceAllString(value, "***@***")
	value = phonePattern.ReplaceAllString(value, "***PHONE***")
	return trimRunes(value, maxRunes)
}

// buildPromptPreview keeps at most a quarter (and no more than 24 runes) of the
// redacted head; texts shorter than 32 runes are withheld completely so short
// unlabelled secrets do not leak.
func buildPromptPreview(value string) string {
	redacted := strings.TrimSpace(redactPreview(value, previewMaxRunes))
	if redacted == "" {
		return ""
	}
	truncated := strings.HasSuffix(redacted, "…")
	runes := []rune(strings.TrimSuffix(redacted, "…"))
	if len(runes) < 32 {
		if truncated {
			return "***…"
		}
		return "***"
	}
	keep := min(len(runes)/4, 24)
	return string(runes[:keep]) + "***…"
}

// buildFullPrompt keeps the unredacted text for root review, without NUL bytes
// (PostgreSQL TEXT rejects them) and capped in length.
func buildFullPrompt(value string) string {
	return trimRunes(strings.TrimSpace(strings.ReplaceAll(value, "\x00", "")), fullPromptMaxRunes)
}

func trimRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
