package contentaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var (
	errInvalidJSON = errors.New("request body is not a JSON object")
	errNoText      = errors.New("request contains no auditable text")

	bearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+\-/]+=*`)
	apiKeyPattern = regexp.MustCompile(`(?i)\b(sk|rk|pk|api[_-]?key|token|secret|password)[-_:=\s]+[A-Za-z0-9._~+\-/]{8,}`)
	canaryPattern = regexp.MustCompile(`(?i)([A-Z]+_CANARY_)[A-Za-z0-9_-]+`)
	emailPattern  = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	phonePattern  = regexp.MustCompile(`(?:\+?\d[\d\s().-]{8,}\d)`)
)

// prioritySeparator marks the end of the latest user turn inside the scan
// text, so chunking never mixes it with older messages.
const prioritySeparator = "\x00CONTENT_AUDIT_PRIORITY_END\x00"

const previewSourceMaxRunes = 96

// auditedFormats lists the relay formats whose prompts are audited. Every other
// format is exempt; exemptFormatReasons documents why.
var auditedFormats = []types.RelayFormat{
	types.RelayFormatOpenAI,
	types.RelayFormatClaude,
	types.RelayFormatGemini,
	types.RelayFormatOpenAIResponses,
	types.RelayFormatOpenAIResponsesCompaction,
	types.RelayFormatOpenAIImage,
}

var exemptFormatReasons = map[types.RelayFormat]string{
	types.RelayFormatEmbedding:         "embedding input is vectorized, not generated from",
	types.RelayFormatRerank:            "rerank input is scored, not generated from",
	types.RelayFormatOpenAIAudio:       "audio payloads carry no prompt text",
	types.RelayFormatOpenAIRealtime:    "realtime sessions stream over WebSocket",
	types.RelayFormatOpenAIAlphaSearch: "search queries, not generation prompts",
	types.RelayFormatDeepSeekTools:     "search and fetch tool calls, not generation prompts",
	types.RelayFormatTask:              "asynchronous task submissions do not use the relay entry",
	types.RelayFormatMjProxy:           "Midjourney submissions do not use the relay entry",
}

// IsAuditedFormat reports whether requests of this relay format are audited.
func IsAuditedFormat(format types.RelayFormat) bool {
	return slices.Contains(auditedFormats, format)
}

// ExemptReason explains why a relay format is not audited. It returns "" for
// audited formats and for formats nobody classified yet.
func ExemptReason(format types.RelayFormat) string {
	return exemptFormatReasons[format]
}

// promptSnapshot is the in-memory view of one request. It is never persisted;
// only Hash, Preview and the counts reach the event table.
type promptSnapshot struct {
	ScanText     string
	Hash         string
	Preview      string
	PromptLength int
	MessageCount int
}

type promptSegment struct {
	text string
	role string
}

func (segment promptSegment) isUser() bool {
	return segment.role == "" || segment.role == "user"
}

func extractPromptSnapshot(format types.RelayFormat, path string, body []byte) (promptSnapshot, error) {
	var root map[string]any
	if err := common.Unmarshal(body, &root); err != nil || root == nil {
		return promptSnapshot{}, errInvalidJSON
	}
	segments := normalizeSegments(extractSegments(format, path, root))
	if len(segments) == 0 {
		return promptSnapshot{}, errNoText
	}
	ordered := latestUserFirst(segments)
	fullText := strings.Join(ordered, "\n\n")
	scanText := fullText
	if len(ordered) > 1 {
		scanText = ordered[0] + prioritySeparator + strings.Join(ordered[1:], "\n\n")
	}
	digest := sha256.Sum256([]byte(fullText))
	return promptSnapshot{
		ScanText:     scanText,
		Hash:         hex.EncodeToString(digest[:]),
		Preview:      buildPromptPreview(fullText),
		PromptLength: utf8.RuneCountInString(fullText),
		MessageCount: len(segments),
	}, nil
}

func extractSegments(format types.RelayFormat, path string, root map[string]any) []promptSegment {
	switch format {
	case types.RelayFormatClaude:
		return append(systemSegments(root["system"]), extractMessages(root["messages"])...)
	case types.RelayFormatGemini:
		return extractGeminiRoot(root)
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		return append(systemSegments(root["instructions"]), extractResponsesInput(root["input"])...)
	case types.RelayFormatOpenAIImage:
		return roleSegments(extractMediaPrompts(root), "user")
	case types.RelayFormatOpenAI:
		if segments := extractMessages(root["messages"]); len(segments) > 0 {
			return segments
		}
		// /v1/completions and /v1/moderations carry prompt or input fields.
		if prompt := promptStrings(root["prompt"]); len(prompt) > 0 {
			return roleSegments(prompt, "user")
		}
		return extractResponsesInput(root["input"])
	default:
		return nil
	}
}

// clientRoles are every role a client may populate. Text placed in assistant or
// tool turns is client-controlled too, so it is audited as well.
var clientRoles = []string{"user", "system", "developer", "assistant", "tool", "model"}

func extractMessages(value any) []promptSegment {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]promptSegment, 0, len(items))
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		if !slices.Contains(clientRoles, role) {
			continue
		}
		for _, text := range contentTexts(message["content"]) {
			result = append(result, promptSegment{text: text, role: role})
		}
	}
	return result
}

func systemSegments(value any) []promptSegment {
	if text, ok := value.(string); ok {
		return roleSegments([]string{text}, "system")
	}
	return roleSegments(contentTexts(value), "system")
}

func extractResponsesInput(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		return roleSegments([]string{typed}, "user")
	case []any:
		result := make([]promptSegment, 0, len(typed))
		for _, item := range typed {
			switch entry := item.(type) {
			case string:
				result = append(result, promptSegment{text: entry, role: "user"})
			case map[string]any:
				role := strings.ToLower(stringValue(entry["role"]))
				if role != "" && !slices.Contains(clientRoles, role) {
					continue
				}
				if content, exists := entry["content"]; exists {
					result = append(result, roleSegments(contentTexts(content), role)...)
				} else if text := stringValue(entry["text"]); text != "" {
					result = append(result, promptSegment{text: text, role: role})
				}
			}
		}
		return result
	case map[string]any:
		role := strings.ToLower(stringValue(typed["role"]))
		if role != "" && !slices.Contains(clientRoles, role) {
			return nil
		}
		return roleSegments(contentTexts(typed["content"]), role)
	}
	return nil
}

func extractGeminiRoot(root map[string]any) []promptSegment {
	result := geminiSystemInstruction(root["systemInstruction"])
	result = append(result, geminiSystemInstruction(root["system_instruction"])...)
	result = append(result, extractGeminiContents(root["contents"])...)
	if requests, ok := root["requests"].([]any); ok {
		for _, item := range requests {
			if request, ok := item.(map[string]any); ok {
				result = append(result, geminiSystemInstruction(request["systemInstruction"])...)
				result = append(result, extractGeminiContents(request["contents"])...)
			}
		}
	}
	return result
}

func extractGeminiContents(value any) []promptSegment {
	var contents []any
	switch typed := value.(type) {
	case []any:
		contents = typed
	case map[string]any:
		contents = []any{typed}
	default:
		return nil
	}
	result := make([]promptSegment, 0, len(contents))
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(content["role"]))
		if role != "" && !slices.Contains(clientRoles, role) {
			continue
		}
		result = append(result, roleSegments(geminiPartTexts(content["parts"]), role)...)
	}
	return result
}

func geminiSystemInstruction(value any) []promptSegment {
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
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if object, ok := part.(map[string]any); ok {
			if text := stringValue(object["text"]); text != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

// mediaPromptKeys are the image request fields holding prompt text.
var mediaPromptKeys = []string{"prompt", "negative_prompt", "instruction", "input"}

func extractMediaPrompts(root map[string]any) []string {
	result := make([]string, 0, 2)
	for _, key := range mediaPromptKeys {
		for _, text := range promptStrings(root[key]) {
			if !looksLikeMediaPayload(text) {
				result = append(result, text)
			}
		}
	}
	return result
}

func promptStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

func looksLikeMediaPayload(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	if len(trimmed) < 256 {
		return false
	}
	for _, r := range trimmed {
		alphaNumeric := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if !alphaNumeric && r != '+' && r != '/' && r != '=' {
			return false
		}
	}
	return true
}

// contentTexts returns the text parts of a message content value and skips
// images, audio, files and other non-text parts.
func contentTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, part := range typed {
			object, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch strings.ToLower(stringValue(object["type"])) {
			case "", "text", "input_text", "output_text":
			default:
				continue
			}
			if text := stringValue(object["text"]); text != "" {
				result = append(result, text)
			}
		}
		return result
	case map[string]any:
		if text := stringValue(typed["text"]); text != "" {
			return []string{text}
		}
	}
	return nil
}

func roleSegments(texts []string, role string) []promptSegment {
	result := make([]promptSegment, 0, len(texts))
	for _, text := range texts {
		result = append(result, promptSegment{text: text, role: role})
	}
	return result
}

func normalizeSegments(values []promptSegment) []promptSegment {
	result := make([]promptSegment, 0, len(values))
	for _, value := range values {
		value.text = strings.TrimSpace(strings.ReplaceAll(value.text, "\x00", ""))
		if value.text != "" {
			result = append(result, value)
		}
	}
	return result
}

// latestUserFirst moves the latest user segment to the front so it is scanned
// first, keeping the order of everything else.
func latestUserFirst(segments []promptSegment) []string {
	priority := len(segments) - 1
	for index := len(segments) - 1; index >= 0; index-- {
		if segments[index].isUser() {
			priority = index
			break
		}
	}
	result := make([]string, 0, len(segments))
	result = append(result, segments[priority].text)
	for index, segment := range segments {
		if index != priority {
			result = append(result, segment.text)
		}
	}
	return result
}

// containsCredentialPattern reports text that looks like a pasted key or bearer
// token. It is only counted.
func containsCredentialPattern(text string) bool {
	return bearerPattern.MatchString(text) || apiKeyPattern.MatchString(text)
}

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

// buildPromptPreview keeps only a short head of the redacted text: inputs under
// 32 characters are withheld entirely, longer ones keep at most a quarter and
// never more than 24 characters.
func buildPromptPreview(value string) string {
	redacted := strings.TrimSpace(redactPreview(value, previewSourceMaxRunes))
	if redacted == "" {
		return ""
	}
	runes := []rune(redacted)
	truncated := strings.HasSuffix(redacted, "…")
	if truncated {
		runes = runes[:len(runes)-1]
	}
	const minRunesForPartialPreview = 32
	if len(runes) < minRunesForPartialPreview {
		if truncated {
			return "***…"
		}
		return "***"
	}
	keep := min(len(runes)/4, 24)
	return string(runes[:keep]) + "***…"
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

// splitChunks cuts the scan text into chunks of at most limit runes. The latest
// user turn always starts its own chunk.
func splitChunks(scanText string, limit int) []string {
	chunks := make([]string, 0, 2)
	for segment := range strings.SplitSeq(scanText, prioritySeparator) {
		runes := []rune(segment)
		for start := 0; start < len(runes); start += limit {
			chunks = append(chunks, string(runes[start:min(start+limit, len(runes))]))
		}
	}
	return chunks
}
