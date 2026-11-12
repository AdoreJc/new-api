package contentmoderation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// Extraction protocols.
const (
	ProtocolChat      = "openai_chat_completions"
	ProtocolClaude    = "anthropic_messages"
	ProtocolResponses = "openai_responses"
	ProtocolGemini    = "gemini"
	ProtocolImages    = "openai_images"
	// ProtocolCompletions covers the legacy prompt-based completions API.
	ProtocolCompletions = "openai_completions"
)

const (
	// maxInputRunes caps the text sent to the moderation engine.
	maxInputRunes = 12000
	// maxImages is the number of images sent along with the text.
	maxImages = 1
	// maxImageDataURLBytes skips inline images the engine would reject anyway.
	maxImageDataURLBytes = 12 << 20
	maxExcerptRunes      = 240
	// maxPromptFieldBytes bounds the multipart prompt field that is read.
	maxPromptFieldBytes = 256 << 10
)

// Input is the latest user message prepared for moderation.
type Input struct {
	Text   string
	Images []string
}

func (in Input) Empty() bool { return in.Text == "" && len(in.Images) == 0 }

// Hash identifies the input for sampling and the hash blocklist. Images are
// hashed separately so the digest does not depend on how they were encoded.
func (in Input) Hash() string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("text:"))
	_, _ = digest.Write([]byte(in.Text))
	for _, image := range in.Images {
		imageDigest := sha256.Sum256([]byte(image))
		_, _ = digest.Write([]byte("\nimage:"))
		_, _ = digest.Write([]byte(hex.EncodeToString(imageDigest[:])))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// collector gathers text parts and images. Text parts carrying a
// client-injected reminder block (<system-reminder>) are not the user's own
// words and are skipped for the engine; keepReminders keeps them, for the
// keyword check.
type collector struct {
	parts         []string
	images        []string
	keepReminders bool
}

// ExtractInput returns only the latest user message of a request body: its
// text (whitespace collapsed, capped at 12000 characters) and at most one
// image. A conversation whose last message is not from the user (a tool
// result or an assistant prefill) yields nothing. Text parts carrying a
// reminder block are left out.
func ExtractInput(protocol string, body []byte) Input {
	c := collectLatestUserMessage(protocol, body, false)
	text := joinParts(c.parts)
	if runes := []rune(text); len(runes) > maxInputRunes {
		text = string(runes[:maxInputRunes])
	}
	images := make([]string, 0, maxImages)
	for _, image := range c.images {
		if len(images) == maxImages {
			break
		}
		if len(image) <= maxImageDataURLBytes {
			images = append(images, image)
		}
	}
	return Input{Text: text, Images: images}
}

// ExtractKeywordText returns the text the keyword lists are matched against:
// the same latest user message as ExtractInput, but with text parts that
// carry a reminder block kept and without the length cap, so a keyword
// cannot hide next to a reminder.
func ExtractKeywordText(protocol string, body []byte) string {
	return joinParts(collectLatestUserMessage(protocol, body, true).parts)
}

func joinParts(parts []string) string {
	return strings.Join(strings.Fields(strings.Join(parts, "\n")), " ")
}

// MultipartPrompt reads the "prompt" field of a multipart/form-data body,
// which is how image edit requests send their prompt.
func MultipartPrompt(contentType string, body []byte) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			return ""
		}
		if part.FormName() == "prompt" && part.FileName() == "" {
			value, _ := io.ReadAll(io.LimitReader(part, maxPromptFieldBytes))
			return string(value)
		}
	}
}

func collectLatestUserMessage(protocol string, body []byte, keepReminders bool) collector {
	c := collector{keepReminders: keepReminders}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return c
	}
	switch protocol {
	case ProtocolClaude:
		c.lastClaudeUserMessage(gjson.GetBytes(body, "messages"))
	case ProtocolChat:
		c.lastChatUserMessage(gjson.GetBytes(body, "messages"))
	case ProtocolResponses:
		c.lastResponsesInput(gjson.GetBytes(body, "input"))
	case ProtocolGemini:
		c.lastGeminiContent(gjson.GetBytes(body, "contents"))
	case ProtocolImages:
		c.addText(gjson.GetBytes(body, "prompt").String())
	case ProtocolCompletions:
		prompt := gjson.GetBytes(body, "prompt")
		if prompt.IsArray() {
			if items := prompt.Array(); len(items) > 0 {
				prompt = items[len(items)-1]
			}
		}
		if prompt.Type == gjson.String {
			c.addText(prompt.String())
		}
	}
	return c
}

func (c *collector) addText(text string) {
	text = strings.TrimSpace(text)
	if text == "" || (!c.keepReminders && strings.Contains(text, "<system-reminder>")) {
		return
	}
	c.parts = append(c.parts, text)
}

func (c *collector) addImage(image string) {
	image = strings.TrimSpace(image)
	if strings.HasPrefix(image, "data:image/") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		c.images = append(c.images, image)
	}
}

func (c *collector) addImageData(mediaType, data string) {
	mediaType, data = strings.TrimSpace(mediaType), strings.TrimSpace(data)
	if mediaType != "" && data != "" {
		c.addImage("data:" + mediaType + ";base64," + data)
	}
}

// keepIfNonEmpty commits what was collected into candidate only when it holds
// some text or an image.
func (c *collector) keepIfNonEmpty(candidate *collector) {
	if len(candidate.parts) == 0 && len(candidate.images) == 0 {
		return
	}
	c.parts = append(c.parts, candidate.parts...)
	c.images = append(c.images, candidate.images...)
}

func role(value gjson.Result) string {
	return strings.ToLower(strings.TrimSpace(value.Get("role").String()))
}

func (c *collector) lastChatUserMessage(messages gjson.Result) {
	items := messages.Array()
	if len(items) == 0 || role(items[len(items)-1]) != "user" {
		return
	}
	candidate := collector{keepReminders: c.keepReminders}
	candidate.content(items[len(items)-1].Get("content"))
	c.keepIfNonEmpty(&candidate)
}

// lastClaudeUserMessage skips trailing system messages some clients append
// after the user turn, then requires the last remaining message to be the
// user's.
func (c *collector) lastClaudeUserMessage(messages gjson.Result) {
	items := messages.Array()
	last := len(items) - 1
	for last >= 0 && role(items[last]) == "system" {
		last--
	}
	if last < 0 || role(items[last]) != "user" {
		return
	}
	candidate := collector{keepReminders: c.keepReminders}
	candidate.content(items[last].Get("content"))
	c.keepIfNonEmpty(&candidate)
}

func (c *collector) lastResponsesInput(input gjson.Result) {
	switch {
	case input.Type == gjson.String:
		c.addText(input.String())
	case input.IsArray():
		items := input.Array()
		if len(items) > 0 {
			c.responsesUserItem(items[len(items)-1])
		}
	case input.IsObject():
		c.responsesUserItem(input)
	}
}

// responsesUserItem takes a message item from the user, or an untyped input
// item without a role; function call outputs and assistant items are skipped.
func (c *collector) responsesUserItem(item gjson.Result) {
	if itemRole := role(item); itemRole != "" && itemRole != "user" {
		return
	}
	itemType := strings.ToLower(item.Get("type").String())
	if itemType != "" && itemType != "message" && itemType != "input_text" && itemType != "input_image" {
		return
	}
	candidate := collector{keepReminders: c.keepReminders}
	candidate.content(item.Get("content"))
	if itemType == "input_text" || itemType == "input_image" {
		candidate.content(item)
	}
	c.keepIfNonEmpty(&candidate)
}

func (c *collector) lastGeminiContent(contents gjson.Result) {
	items := contents.Array()
	if len(items) == 0 {
		return
	}
	last := items[len(items)-1]
	if itemRole := role(last); itemRole != "" && itemRole != "user" {
		return
	}
	candidate := collector{keepReminders: c.keepReminders}
	last.Get("parts").ForEach(func(_, part gjson.Result) bool {
		candidate.addText(part.Get("text").String())
		candidate.addImageData(part.Get("inline_data.mime_type").String(), part.Get("inline_data.data").String())
		candidate.addImageData(part.Get("inlineData.mimeType").String(), part.Get("inlineData.data").String())
		if mime := part.Get("file_data.mime_type").String() + part.Get("fileData.mimeType").String(); strings.HasPrefix(mime, "image/") {
			candidate.addImage(part.Get("file_data.file_uri").String())
			candidate.addImage(part.Get("fileData.fileUri").String())
		}
		return true
	})
	c.keepIfNonEmpty(&candidate)
}

// content reads a message content value in any of the supported shapes: a
// string, an array of parts, or a single part object. Tool results, tool
// calls and documents are not the user's typed input and are skipped.
func (c *collector) content(value gjson.Result) {
	switch {
	case value.Type == gjson.String:
		c.addText(value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			c.content(item)
			return true
		})
	case value.IsObject():
		switch strings.ToLower(strings.TrimSpace(value.Get("type").String())) {
		case "", "text", "input_text":
			c.addText(value.Get("text").String())
		case "image_url":
			c.addImage(value.Get("image_url.url").String())
			if url := value.Get("image_url"); url.Type == gjson.String {
				c.addImage(url.String())
			}
		case "input_image":
			c.addImage(value.Get("image_url").String())
		case "image":
			c.addImageData(value.Get("source.media_type").String(), value.Get("source.data").String())
			if value.Get("source.type").String() == "url" {
				c.addImage(value.Get("source.url").String())
			}
		}
	}
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>，。；、]+`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|token|session|cookie|authorization|bearer|password|passwd|pwd|secret|client[_-]?secret|private[_-]?key)\s*[:=]\s*["']?[^"'\s,;，。；、]{6,}`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)\b(?:sk|sk-proj|sk-ant|sess|rk|pk|ak|api|key|token|secret)[_-][A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`),
	regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9+/_-]{48,}={0,2}`),
}

// Excerpt returns the first characters of the input with URLs, credentials,
// e-mail addresses and long tokens replaced, for display on the event page.
func Excerpt(text string) string {
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, "[redacted]")
	}
	if runes := []rune(text); len(runes) > maxExcerptRunes {
		return string(runes[:maxExcerptRunes])
	}
	return text
}
