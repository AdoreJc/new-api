package promptaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"
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
	segmentSeparator   = "\n\n"
	// roleAgentContext marks user-role text that is wholly a wrapper coding
	// agents inject (Claude Code reminders, Codex environment and AGENTS.md
	// instructions). It is scanned only with the "all" scope.
	roleAgentContext = "agent_context"
)

var (
	errNoPromptText = errors.New("prompt audit request contains no text")
	// errAgentContextOnly means the scan scope left nothing to scan because the
	// only user text was agent wrappers.
	errAgentContextOnly = errors.New("prompt audit request has only agent context in scope")
	errInvalidJSON      = errors.New("prompt audit request JSON is invalid")
	bearerPattern       = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+\-/]+=*`)
	apiKeyPattern       = regexp.MustCompile(`(?i)\b(sk|rk|pk|api[_-]?key|token|secret|password)[-_:=\s]+[A-Za-z0-9._~+\-/]{8,}`)
	canaryPattern       = regexp.MustCompile(`(?i)([A-Z]+_CANARY_)[A-Za-z0-9_-]+`)
	emailPattern        = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	phonePattern        = regexp.MustCompile(`(?:\+?\d[\d\s().-]{8,}\d)`)
	hostPortPattern     = regexp.MustCompile(`(?i)\b(?:\d{1,3}(?:\.\d{1,3}){3}|localhost)(?::\d{1,5})?\b`)
	clientTextRoles     = []string{"user", "system", "developer", "assistant", "tool"}
	contentTextTypes    = []string{"", "text", "input_text", "output_text"}
	// agentContextTags wrap context that coding agents add to user messages.
	agentContextTags = []string{"system-reminder", "environment_context", "user_instructions"}
)

type promptSegment struct {
	text string
	role string
	// turn is the 1-based position of the conversation message the text came
	// from; 0 for text outside the conversation (top-level instructions,
	// media prompts).
	turn int
}

func (s promptSegment) isUser() bool { return s.role == "" || s.role == "user" }

func (s promptSegment) displayRole() string {
	if s.role == "" {
		return "user"
	}
	return s.role
}

// roleSpan is the rune range of one segment inside a scanPart.
type roleSpan struct {
	start, end int
	role       string
}

// scanPart is text sent to the Guard together with the role of each piece,
// so a finding can name the roles of the chunk that triggered it.
type scanPart struct {
	text  string
	spans []roleSpan
}

func joinSegments(segments []promptSegment) scanPart {
	var builder strings.Builder
	spans := make([]roleSpan, 0, len(segments))
	offset := 0
	for index, segment := range segments {
		if index > 0 {
			builder.WriteString(segmentSeparator)
			offset += utf8.RuneCountInString(segmentSeparator)
		}
		builder.WriteString(segment.text)
		length := utf8.RuneCountInString(segment.text)
		spans = append(spans, roleSpan{start: offset, end: offset + length, role: segment.displayRole()})
		offset += length
	}
	return scanPart{text: builder.String(), spans: spans}
}

// promptSnapshot is the extracted, client-controlled text of one request.
// Latest and Rest are what the scan scope sends to the Guard; FullPrompt,
// Preview and Hash always cover every extracted text.
type promptSnapshot struct {
	Latest       scanPart
	Rest         scanPart
	FullPrompt   string
	Preview      string
	Hash         string
	Length       int
	MessageCount int
}

// extractPromptSnapshot reads every client-controlled text of the request
// (system, developer, user, assistant, tool) with the latest user turn first.
// The scope narrows what is scanned: latest_user keeps only the latest user
// turn, user keeps every user text, all keeps everything. Images, audio and
// files are ignored.
func extractPromptSnapshot(protocol string, body []byte, scope string) (*promptSnapshot, error) {
	segments, _, err := requestSegments(protocol, body)
	if err != nil {
		return nil, err
	}
	return scopedSnapshot(segments, scope)
}

// requestSegments parses the request and returns its client-controlled texts
// in request order, with the number of conversation messages.
func requestSegments(protocol string, body []byte) ([]promptSegment, int, error) {
	var document any
	if err := common.Unmarshal(body, &document); err != nil {
		return nil, 0, errInvalidJSON
	}
	root, _ := document.(map[string]any)
	segments, turns := protocolSegments(protocol, root)
	return segments, turns, nil
}

func scopedSnapshot(segments []promptSegment, scope string) (*promptSnapshot, error) {
	latest, rest := splitLatestUserTurn(segments)
	if len(latest) == 0 {
		return nil, errNoPromptText
	}
	ordered := append(slices.Clone(latest), rest...)
	texts := make([]string, 0, len(ordered))
	for _, segment := range ordered {
		texts = append(texts, segment.text)
	}
	fullText := strings.Join(texts, segmentSeparator)
	digest := sha256.Sum256([]byte(fullText))
	snapshot := &promptSnapshot{
		FullPrompt:   buildFullPrompt(fullText),
		Preview:      buildPromptPreview(fullText),
		Hash:         hex.EncodeToString(digest[:]),
		Length:       utf8.RuneCountInString(fullText),
		MessageCount: len(ordered),
	}
	switch scope {
	case prompt_audit_setting.ScanScopeAll:
		snapshot.Latest, snapshot.Rest = joinSegments(latest), joinSegments(rest)
	case prompt_audit_setting.ScanScopeUser:
		snapshot.Latest, snapshot.Rest = joinSegments(userOnly(latest)), joinSegments(userOnly(rest))
	default:
		snapshot.Latest = joinSegments(userOnly(latest))
	}
	if snapshot.Latest.text == "" && snapshot.Rest.text == "" {
		if slices.ContainsFunc(ordered, func(segment promptSegment) bool { return segment.role == roleAgentContext }) {
			return nil, errAgentContextOnly
		}
		return nil, errNoPromptText
	}
	return snapshot, nil
}

func userOnly(segments []promptSegment) []promptSegment {
	result := make([]promptSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.isUser() {
			result = append(result, segment)
		}
	}
	return result
}

// protocolSegments returns the texts of the request and the number of
// messages in its conversation, which is the turn of the last message.
func protocolSegments(protocol string, root map[string]any) ([]promptSegment, int) {
	if root == nil {
		return nil, 0
	}
	switch protocol {
	case ProtocolChat:
		return messageSegments(root["messages"]), conversationTurns(root["messages"])
	case ProtocolClaude:
		return append(instructionSegments(root["system"]), messageSegments(root["messages"])...), conversationTurns(root["messages"])
	case ProtocolGemini:
		return geminiRootSegments(root), conversationTurns(root["contents"])
	case ProtocolResponses:
		return append(instructionSegments(root["instructions"]), responsesInputSegments(root["input"])...), conversationTurns(root["input"])
	case ProtocolImages:
		return userSegments(mediaPrompts(root)), 0
	}
	if segments := messageSegments(root["messages"]); len(segments) > 0 {
		return segments, conversationTurns(root["messages"])
	}
	if segments := append(instructionSegments(root["instructions"]), responsesInputSegments(root["input"])...); len(segments) > 0 {
		return segments, conversationTurns(root["input"])
	}
	if segments := geminiRootSegments(root); len(segments) > 0 {
		return segments, conversationTurns(root["contents"])
	}
	return userSegments(mediaPrompts(root)), 0
}

// conversationTurns counts the messages of a conversation value: the items
// of a list, or one for a single message or bare text.
func conversationTurns(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case []any:
		return len(typed)
	}
	return 1
}

func messageSegments(value any) []promptSegment {
	items, _ := value.([]any)
	result := make([]promptSegment, 0, len(items))
	for index, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		if !slices.Contains(clientTextRoles, role) {
			continue
		}
		for _, text := range contentTexts(message["content"]) {
			result = append(result, promptSegment{text: text, role: role, turn: index + 1})
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
		return []promptSegment{{text: typed, role: "user", turn: 1}}
	case map[string]any:
		return inTurn(responsesItemSegments(typed), 1)
	case []any:
		result := make([]promptSegment, 0, len(typed))
		for index, item := range typed {
			switch entry := item.(type) {
			case string:
				result = append(result, promptSegment{text: entry, role: "user", turn: index + 1})
			case map[string]any:
				result = append(result, inTurn(responsesItemSegments(entry), index+1)...)
			}
		}
		return result
	}
	return nil
}

// inTurn sets the conversation position of one message's texts.
func inTurn(segments []promptSegment, turn int) []promptSegment {
	for index := range segments {
		segments[index].turn = turn
	}
	return segments
}

// responsesItemSegments reads a message-like Responses input item. Items with
// a non-client role are skipped. Items without a role are user input when
// they are messages or bare text; other typed items (tool calls and their
// outputs, reasoning) are agent traffic and count as tool text.
func responsesItemSegments(item map[string]any) []promptSegment {
	role := strings.ToLower(stringValue(item["role"]))
	if role != "" && !slices.Contains(clientTextRoles, role) {
		return nil
	}
	itemType := strings.ToLower(stringValue(item["type"]))
	if role == "" && itemType != "message" && !slices.Contains(contentTextTypes, itemType) {
		role = "tool"
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
				// A batch holds several conversations; their texts get no turn.
				result = append(result, inTurn(geminiContentSegments(request["contents"]), 0)...)
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
	for index, item := range contents {
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
		result = append(result, inTurn(roleSegments(geminiPartTexts(content["parts"]), role), index+1)...)
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

// splitLatestUserTurn drops empty texts, marks agent wrappers inside user
// messages, and separates the latest user turn (the last user text and the
// user texts right before it, stepping over agent wrappers) from the rest,
// which keeps request order. Without any user text the last text leads.
func splitLatestUserTurn(segments []promptSegment) (latest []promptSegment, rest []promptSegment) {
	normalized := normalizeSegments(segments)
	if len(normalized) == 0 {
		return nil, nil
	}
	inLatest := latestUserTurn(normalized)
	for index, segment := range normalized {
		if inLatest[index] {
			latest = append(latest, segment)
		} else {
			rest = append(rest, segment)
		}
	}
	return latest, rest
}

// normalizeSegments trims texts, drops empty ones and marks user texts that
// are wholly agent wrappers.
func normalizeSegments(segments []promptSegment) []promptSegment {
	normalized := make([]promptSegment, 0, len(segments))
	for _, segment := range segments {
		segment.text = strings.TrimSpace(segment.text)
		if segment.text == "" {
			continue
		}
		if segment.isUser() && isAgentContext(segment.text) {
			segment.role = roleAgentContext
		}
		normalized = append(normalized, segment)
	}
	return normalized
}

// latestUserTurn marks the normalized texts that form the latest user turn;
// without any user text it marks the last text.
func latestUserTurn(normalized []promptSegment) []bool {
	inLatest := make([]bool, len(normalized))
	found := false
	for index := len(normalized) - 1; index >= 0; index-- {
		if normalized[index].isUser() {
			inLatest[index], found = true, true
			continue
		}
		if found && normalized[index].role != roleAgentContext {
			break
		}
	}
	if !found && len(normalized) > 0 {
		inLatest[len(normalized)-1] = true
	}
	return inLatest
}

// isAgentContext reports whether a user text is only agent wrappers: one or
// more blocks of a known tag, or Codex AGENTS.md instructions. Each block ends
// at its first closing tag, so user text between two blocks is not hidden.
func isAgentContext(text string) bool {
	if header, body, ok := strings.Cut(text, "\n"); ok && strings.HasPrefix(header, "# AGENTS.md instructions for ") &&
		onlyWrappedIn(body, "INSTRUCTIONS") {
		return true
	}
	return slices.ContainsFunc(agentContextTags, func(tag string) bool { return onlyWrappedIn(text, tag) })
}

func onlyWrappedIn(text, tag string) bool {
	openTag, closeTag := "<"+tag+">", "</"+tag+">"
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for text != "" {
		body, ok := strings.CutPrefix(text, openTag)
		if !ok {
			return false
		}
		_, after, found := strings.Cut(body, closeTag)
		if !found {
			return false
		}
		text = strings.TrimSpace(after)
	}
	return true
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
	value = maskPhoneNumbers(value)
	return trimRunes(value, maxRunes)
}

// maskPhoneNumbers masks phone-like digit runs but leaves IPv4 addresses,
// their ports and localhost ports alone, so proxy addresses are not mistaken
// for phone numbers.
func maskPhoneNumbers(value string) string {
	var builder strings.Builder
	previous := 0
	for _, span := range hostPortPattern.FindAllStringIndex(value, -1) {
		if !isNetworkAddress(value, span[0], span[1]) {
			continue
		}
		builder.WriteString(phonePattern.ReplaceAllString(value[previous:span[0]], "***PHONE***"))
		builder.WriteString(value[span[0]:span[1]])
		previous = span[1]
	}
	builder.WriteString(phonePattern.ReplaceAllString(value[previous:], "***PHONE***"))
	return builder.String()
}

// isNetworkAddress checks a hostPortPattern match: a localhost port, or four
// dotted octets of 0-255 without leading zeros that are not part of a longer
// dotted digit run. Phone numbers written in dotted groups fail the check.
func isNetworkAddress(value string, start, end int) bool {
	host, _, _ := strings.Cut(value[start:end], ":")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	if start >= 2 && value[start-1] == '.' && isDigit(value[start-2]) {
		return false
	}
	if end+1 < len(value) && value[end] == '.' && isDigit(value[end+1]) {
		return false
	}
	for octet := range strings.SplitSeq(host, ".") {
		number, err := strconv.Atoi(octet)
		if err != nil || number > 255 || (len(octet) > 1 && octet[0] == '0') {
			return false
		}
	}
	return true
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
