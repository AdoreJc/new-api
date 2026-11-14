package promptaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"
)

// Segment kinds of request structure statistics. They refine the role of an
// extracted text by its position in the conversation.
const (
	kindSystem            = "system"
	kindDeveloper         = "developer"
	kindLatestUser        = "latest_user"
	kindHistoryUser       = "history_user"
	kindHistoryAssistant  = "history_assistant"
	kindTrailingAssistant = "trailing_assistant"
	kindAgentContext      = "agent_context"
	kindTool              = "tool"
)

// Client classes group requests by the kind of client that sent them. The
// User-Agent itself is never stored.
const (
	ClientClaudeCode = "claude_code"
	ClientCodex      = "codex"
	ClientGeminiCLI  = "gemini_cli"
	ClientSDK        = "sdk"
	ClientBrowser    = "browser"
	ClientOther      = "other"
)

// ClientClasses and StructureFormats list the values of the two dimensions
// request structure statistics are grouped by.
var (
	ClientClasses    = []string{ClientClaudeCode, ClientCodex, ClientGeminiCLI, ClientSDK, ClientBrowser, ClientOther}
	StructureFormats = []string{ProtocolChat, ProtocolClaude, ProtocolGemini, ProtocolResponses, ProtocolImages, ProtocolGeneric}
)

const (
	// noSystemFingerprint is the fingerprint of requests without system or
	// developer text.
	noSystemFingerprint = "none"
	// fingerprintPrefixRunes is how much of the normalized system prompt the
	// prefix hash covers, so templates that differ only further down can
	// still be grouped.
	fingerprintPrefixRunes = 512
	// encodedRunMinLength is the shortest unbroken run of base64 or hex
	// characters counted as an encoded payload.
	encodedRunMinLength = 256
	// manyAssistantTurns is the assistant turn count from which a request is
	// counted as a long replayed conversation.
	manyAssistantTurns = 10
)

var (
	// templateControlTokens are chat template control tokens of common model
	// families. They have no place in client text.
	templateControlTokens = []string{
		"<|im_start|>", "<|im_end|>", "<|start_header_id|>", "<|end_header_id|>", "<|eot_id|>",
		"<|begin_of_text|>", "<|endoftext|>", "<|system|>", "<|user|>", "<|assistant|>",
		"<start_of_turn>", "<end_of_turn>", "[INST]", "[/INST]", "<<SYS>>",
	}
	// roleLabels start a line that reads like another turn of the conversation.
	roleLabels = []string{"system", "assistant", "user", "human", "developer", "系统", "助手", "用户"}
	// fingerprintVolatileTags wrap text agents rewrite on every request.
	fingerprintVolatileTags = append([]string{"env"}, agentContextTags...)
)

// structureReport is everything request structure statistics keep of one
// request: flags, counts, a length and two hashes. It holds no prompt text.
type structureReport struct {
	HasSystem           bool
	HasDeveloper        bool
	HasHistoryUser      bool
	HasHistoryAssistant bool
	// TrailingAssistant means the conversation ends with an assistant message
	// that has text (a prefill).
	TrailingAssistant bool
	HasAgentContext   bool
	HasTool           bool
	// AssistantTurns and UserTurns count messages with text of that kind.
	AssistantTurns int
	UserTurns      int
	// MidSystem means a system or developer message follows a message of
	// another role.
	MidSystem      bool
	ControlTokens  bool
	RoleMarkers    bool
	InvisibleChars int
	EncodedRuns    int
	// SystemLength is the length in characters of all system and developer text.
	SystemLength int
	Fingerprint  string
	PrefixHash   string
}

// segmentKinds gives the kind of every normalized text. turns is the number
// of conversation messages, so texts of the last message can be told apart.
func segmentKinds(normalized []promptSegment, turns int) []string {
	inLatest := latestUserTurn(normalized)
	kinds := make([]string, len(normalized))
	for index, segment := range normalized {
		switch segment.role {
		case "system":
			kinds[index] = kindSystem
		case "developer":
			kinds[index] = kindDeveloper
		case roleAgentContext:
			kinds[index] = kindAgentContext
		case "tool":
			kinds[index] = kindTool
		case "assistant":
			kinds[index] = kindHistoryAssistant
			if turns > 0 && segment.turn == turns {
				kinds[index] = kindTrailingAssistant
			}
		default:
			kinds[index] = kindHistoryUser
			if inLatest[index] {
				kinds[index] = kindLatestUser
			}
		}
	}
	return kinds
}

// analyzeStructure computes the structure report of a request from its
// extracted texts. It returns false when the request has no text.
func analyzeStructure(segments []promptSegment, turns int) (structureReport, bool) {
	normalized := normalizeSegments(segments)
	if len(normalized) == 0 {
		return structureReport{}, false
	}
	var report structureReport
	var systemText strings.Builder
	lastAssistantTurn, lastUserTurn := -1, -1
	conversationStarted := false
	for index, kind := range segmentKinds(normalized, turns) {
		segment := normalized[index]
		switch kind {
		case kindSystem, kindDeveloper:
			report.HasSystem = report.HasSystem || kind == kindSystem
			report.HasDeveloper = report.HasDeveloper || kind == kindDeveloper
			report.MidSystem = report.MidSystem || (segment.turn > 0 && conversationStarted)
			report.SystemLength += utf8.RuneCountInString(segment.text)
			if systemText.Len() > 0 {
				systemText.WriteString(segmentSeparator)
			}
			systemText.WriteString(segment.text)
		case kindAgentContext:
			report.HasAgentContext = true
		case kindTool:
			report.HasTool = true
		case kindHistoryAssistant, kindTrailingAssistant:
			report.HasHistoryAssistant = report.HasHistoryAssistant || kind == kindHistoryAssistant
			report.TrailingAssistant = report.TrailingAssistant || kind == kindTrailingAssistant
			if segment.turn != lastAssistantTurn {
				report.AssistantTurns++
				lastAssistantTurn = segment.turn
			}
		default:
			report.HasHistoryUser = report.HasHistoryUser || kind == kindHistoryUser
			if segment.turn != lastUserTurn {
				report.UserTurns++
				lastUserTurn = segment.turn
			}
		}
		if kind != kindSystem && kind != kindDeveloper && segment.turn > 0 {
			conversationStarted = true
		}
		if kind == kindTool {
			// Tool output is not written by the client and routinely holds
			// encoded data and transcripts.
			continue
		}
		invisible, encoded := countHiddenText(segment.text)
		report.InvisibleChars += invisible
		report.EncodedRuns += encoded
		report.ControlTokens = report.ControlTokens || slices.ContainsFunc(templateControlTokens, func(token string) bool {
			return strings.Contains(segment.text, token)
		})
		if kind == kindLatestUser || kind == kindHistoryUser || kind == kindHistoryAssistant || kind == kindTrailingAssistant {
			report.RoleMarkers = report.RoleMarkers || hasRoleMarker(segment.text)
		}
	}
	report.Fingerprint, report.PrefixHash = noSystemFingerprint, noSystemFingerprint
	if report.HasSystem || report.HasDeveloper {
		report.Fingerprint, report.PrefixHash = systemFingerprint(systemText.String())
	}
	return report, true
}

// countHiddenText counts invisible characters (zero-width and Unicode tag
// characters) and long unbroken runs of base64 or hex characters.
func countHiddenText(text string) (invisible int, encodedRuns int) {
	run := 0
	for _, r := range text {
		if r < utf8.RuneSelf && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			r == '+' || r == '/' || r == '=' || r == '-' || r == '_') {
			run++
			continue
		}
		if run >= encodedRunMinLength {
			encodedRuns++
		}
		run = 0
		// The zero-width joiner is left out: emoji sequences use it.
		if r == 0x200B || r == 0x200C || r == 0xFEFF || (r >= 0x2060 && r <= 0x2064) || (r >= 0xE0000 && r <= 0xE007F) {
			invisible++
		}
	}
	if run >= encodedRunMinLength {
		encodedRuns++
	}
	return invisible, encodedRuns
}

// hasRoleMarker reports whether a line of the text starts with a role label
// followed by a colon, as a transcript of another turn would. Code and
// configuration with such keys match too, so the counter overstates.
func hasRoleMarker(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimLeft(line, " \t")
		for _, label := range roleLabels {
			if len(line) <= len(label) || !strings.EqualFold(line[:len(label)], label) {
				continue
			}
			rest := strings.TrimLeft(line[len(label):], " \t")
			if strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "：") {
				return true
			}
		}
	}
	return false
}

// systemFingerprint hashes the normalized system prompt, and its leading
// part on its own.
func systemFingerprint(text string) (fingerprint string, prefixHash string) {
	normalized := normalizeSystemPrompt(text)
	digest := sha256.Sum256([]byte(normalized))
	prefix, runes := normalized, 0
	for offset := range normalized {
		if runes == fingerprintPrefixRunes {
			prefix = normalized[:offset]
			break
		}
		runes++
	}
	prefixDigest := sha256.Sum256([]byte(prefix))
	return hex.EncodeToString(digest[:]), hex.EncodeToString(prefixDigest[:])
}

// normalizeSystemPrompt removes what agent templates change on every request,
// so the same template gives the same text: blocks agents rewrite per request
// (environment, reminders), file paths, identifiers and numbers such as
// dates and versions. Whitespace is collapsed. The text of a removed block
// has no part in the fingerprint.
func normalizeSystemPrompt(text string) string {
	text = withoutVolatileBlocks(text)
	var builder strings.Builder
	builder.Grow(len(text))
	for token := range strings.FieldsSeq(text) {
		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(stableToken(token))
	}
	return builder.String()
}

// withoutVolatileBlocks replaces every block wrapped in one of
// fingerprintVolatileTags with a space, in one pass over the text. A tag
// whose closing tag is missing once is not searched for again, so the work
// stays linear in the length of the text.
func withoutVolatileBlocks(text string) string {
	if !strings.Contains(text, "<") {
		return text
	}
	unclosed := make([]bool, len(fingerprintVolatileTags))
	var builder strings.Builder
	builder.Grow(len(text))
	for {
		start := strings.IndexByte(text, '<')
		if start < 0 {
			break
		}
		builder.WriteString(text[:start])
		text = text[start:]
		removed := false
		for index, tag := range fingerprintVolatileTags {
			body, isOpen := strings.CutPrefix(text, "<"+tag+">")
			if !isOpen || unclosed[index] {
				continue
			}
			_, after, closed := strings.Cut(body, "</"+tag+">")
			if !closed {
				unclosed[index] = true
				continue
			}
			builder.WriteByte(' ')
			text, removed = after, true
			break
		}
		if !removed {
			builder.WriteByte('<')
			text = text[1:]
		}
	}
	builder.WriteString(text)
	return builder.String()
}

// stableToken replaces a whitespace-separated token that names a file path or
// an identifier with a placeholder, and every run of digits with one zero.
func stableToken(token string) string {
	bare := strings.Trim(token, "\"'`()[]<>,.;:")
	if strings.HasPrefix(bare, "~/") || (len(bare) > 1 && bare[0] == '/') ||
		(len(bare) > 2 && bare[1] == ':' && (bare[2] == '\\' || bare[2] == '/')) {
		return "<path>"
	}
	hasDigit := strings.ContainsAny(bare, "0123456789")
	if len(bare) >= 8 && hasDigit && !strings.ContainsFunc(bare, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') && r != '-'
	}) {
		return "<id>"
	}
	if !hasDigit {
		return token
	}
	var builder strings.Builder
	inDigits := false
	for _, r := range token {
		if r >= '0' && r <= '9' {
			if !inDigits {
				builder.WriteByte('0')
			}
			inDigits = true
			continue
		}
		inDigits = false
		builder.WriteRune(r)
	}
	return builder.String()
}

// ClientClassOf groups a User-Agent into one of ClientClasses.
func ClientClassOf(userAgent string) string {
	agent := strings.ToLower(strings.TrimSpace(userAgent))
	switch {
	case strings.HasPrefix(agent, "claude-cli") || strings.Contains(agent, "claude-code"):
		return ClientClaudeCode
	case strings.HasPrefix(agent, "codex"):
		return ClientCodex
	case strings.HasPrefix(agent, "geminicli") || strings.HasPrefix(agent, "gemini-cli"):
		return ClientGeminiCLI
	case strings.HasPrefix(agent, "mozilla/"):
		return ClientBrowser
	case strings.Contains(agent, "openai/") || strings.Contains(agent, "anthropic/") || strings.Contains(agent, "anthropic-sdk") ||
		strings.Contains(agent, "google-genai") || strings.Contains(agent, "openai-"):
		return ClientSDK
	}
	return ClientOther
}
