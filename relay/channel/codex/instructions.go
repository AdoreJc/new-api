package codex

import (
	_ "embed"
	"strings"
)

// Codex CLI base instructions, sent when a request carries no instructions so
// the request looks like one from the Codex CLI. The texts are the Codex CLI
// prompts (openai/codex) as bundled by sub2api
// backend/internal/pkg/openai/instructions*.txt at commit 8e92c892
// (2026-11-05); the model mapping follows sub2api CodexBaseInstructionsForModel.
// Refresh both together when sub2api updates them.
var (
	//go:embed instructions/codex.txt
	codexInstructionsCodex string
	//go:embed instructions/gpt5_1.txt
	codexInstructionsGPT51 string
	//go:embed instructions/gpt5_2.txt
	codexInstructionsGPT52 string
	//go:embed instructions/gpt5_5.txt
	codexInstructionsGPT55 string
)

// codexBaseInstructions returns the Codex CLI base instructions for model:
// Codex models get the GPT-5-Codex prompt, the gpt-5.1 and gpt-5.2 families
// their own prompts, and every other model the latest one (gpt-5.5).
func codexBaseInstructions(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if _, after, ok := strings.Cut(name, "/"); ok {
		name = after
	}
	var text string
	switch {
	case strings.Contains(name, "codex"):
		text = codexInstructionsCodex
	case strings.HasPrefix(name, "gpt-5.2"):
		text = codexInstructionsGPT52
	case strings.HasPrefix(name, "gpt-5.1"):
		text = codexInstructionsGPT51
	default:
		text = codexInstructionsGPT55
	}
	// A Windows checkout may turn the embedded files into CRLF.
	return strings.ReplaceAll(text, "\r\n", "\n")
}
