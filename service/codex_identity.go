package service

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
)

// Outbound client identity of Codex (ChatGPT OAuth) channels. The ChatGPT
// backend tiers requests by the client they claim to be, and a User-Agent that
// does not pair with originator (or an old version) is answered with errors
// such as an in-stream server_is_overloaded. Every request to chatgpt.com and
// auth.openai.com therefore carries one consistent identity: User-Agent,
// originator and version all name the same Codex CLI release.
const (
	// codexDefaultClientVersion is the announced Codex CLI version when
	// CODEX_CLIENT_VERSION is unset. The upstream answers 404 to a version
	// below 0.144.0. Same value as sub2api (codexCLIVersion, 2026-11-05).
	codexDefaultClientVersion = "0.146.0"
	// CodexOriginator is the originator header of the Codex CLI.
	CodexOriginator         = "codex_cli_rs"
	codexUserAgentPlatform  = " (Ubuntu 22.4.0; x86_64) xterm-256color"
	codexClientVersionLimit = 64
)

var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){2}$`)

// codexClientVersion is resolved once so every request of a process announces
// the same client.
var codexClientVersion = sync.OnceValue(func() string {
	return resolveCodexClientVersion(os.Getenv("CODEX_CLIENT_VERSION"))
})

// CodexClientVersion is the Codex CLI version announced upstream.
func CodexClientVersion() string {
	return codexClientVersion()
}

// CodexUserAgent is the User-Agent of the announced Codex CLI release.
func CodexUserAgent() string {
	return CodexOriginator + "/" + CodexClientVersion() + codexUserAgentPlatform
}

// SetCodexIdentityHeaders replaces whatever identity the client sent with the
// gateway's Codex CLI identity.
func SetCodexIdentityHeaders(h http.Header) {
	h.Set("User-Agent", CodexUserAgent())
	h.Set("originator", CodexOriginator)
	h.Set("version", CodexClientVersion())
}

// resolveCodexClientVersion accepts an override only when it is a plain x.y.z
// release not older than the built-in version.
func resolveCodexClientVersion(env string) string {
	version := strings.TrimSpace(env)
	if version == "" {
		return codexDefaultClientVersion
	}
	if len(version) > codexClientVersionLimit || !codexClientVersionPattern.MatchString(version) ||
		slices.Compare(codexVersionParts(version), codexVersionParts(codexDefaultClientVersion)) < 0 {
		logger.LogWarn(context.Background(), common.LogText(
			"ignoring CODEX_CLIENT_VERSION=%q: it must be a plain x.y.z release not older than %s; using %s",
			version, codexDefaultClientVersion, codexDefaultClientVersion))
		return codexDefaultClientVersion
	}
	return version
}

func codexVersionParts(version string) []int {
	var parts []int
	for field := range strings.SplitSeq(version, ".") {
		n, _ := strconv.Atoi(field)
		parts = append(parts, n)
	}
	return parts
}
