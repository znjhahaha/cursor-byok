package modeladapter

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

const (
	ClientProfileGeneric    = "generic"
	ClientProfileClaudeCode = "claude-code"
	ClientProfileCodex      = "codex"

	ClaudeCodeVersion              = "2.1.220"
	ClaudeCodeUserAgent            = "claude-cli/" + ClaudeCodeVersion + " (external, sdk-cli)"
	ClaudeCodeAnthropicBeta        = "claude-code-20250219"
	ClaudeCodeInterleavedBeta      = "interleaved-thinking-2025-05-14"
	ClaudeCodeThinkingTokenBeta    = "thinking-token-count-2026-05-13"
	ClaudeCodeContextManageBeta    = "context-management-2025-06-27"
	ClaudeCodePromptCacheScopeBeta = "prompt-caching-scope-2026-01-05"
	ClaudeCodeMidSystemBeta        = "mid-conversation-system-2026-04-07"
	ClaudeCodeEffortBeta           = "effort-2025-11-24"
	ClaudeCodeFallbackCreditBeta   = "fallback-credit-2026-06-01"
	AnthropicExtendedContextBeta   = "context-1m-2025-08-07"
	ClaudeCodeStainlessPackage     = "0.94.0"
	ClaudeCodeRuntimeVersion       = "v26.3.0"
	ClaudeCodeStainlessTimeout     = "600"
	CodexClientVersion             = "0.146.0-alpha.9.2"
	CodexClientOriginator          = "codex_cli_rs"
	CodexClientTerminal            = "unknown"
	CodexWindowsVersion            = "10.0.26200"
	CodexMacOSVersion              = "15.5.0"
	CodexLinuxVersion              = "6.8.0"
	anthropicExtendedContextSuffix = "[1m]"
	AnthropicExtendedContextTokens = 1_000_000
)

// NormalizeClientProfile returns the stable request fingerprint profile name.
func NormalizeClientProfile(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ClientProfileClaudeCode:
		return ClientProfileClaudeCode
	case ClientProfileCodex:
		return ClientProfileCodex
	default:
		return ClientProfileGeneric
	}
}

// ApplyClientProfileHeaders adds client fingerprint headers before user headers
// are applied. Custom headers therefore retain final precedence.
func ApplyClientProfileHeaders(httpReq *http.Request, profile string) {
	if httpReq == nil {
		return
	}
	switch NormalizeClientProfile(profile) {
	case ClientProfileClaudeCode:
		applyClaudeCodeHeaders(httpReq)
	case ClientProfileCodex:
		applyCodexHeaders(httpReq)
	default:
		// Suppress Go's implicit transport UA so generic really is generic.
		httpReq.Header.Set("User-Agent", "")
	}
}

func anthropicClientProfile(value string) string {
	if NormalizeClientProfile(value) == ClientProfileClaudeCode {
		return ClientProfileClaudeCode
	}
	return ClientProfileGeneric
}

func applyAnthropicExtendedContextHeader(httpReq *http.Request, profile string, enabled bool) {
	if httpReq == nil || !enabled || anthropicClientProfile(profile) != ClientProfileClaudeCode {
		return
	}
	appendAnthropicBeta(httpReq, AnthropicExtendedContextBeta)
}

func openAIClientProfile(value string, endpoint string) string {
	if NormalizeClientProfile(value) == ClientProfileCodex &&
		strings.EqualFold(strings.TrimSpace(endpoint), "/v1/responses") {
		return ClientProfileCodex
	}
	return ClientProfileGeneric
}

func applyOpenAIRequestProfileHeaders(httpReq *http.Request, req StreamRequest) {
	profile := openAIClientProfile(req.ClientProfile, req.OpenAIEndpoint)
	if profile != ClientProfileCodex {
		ApplyClientProfileHeaders(httpReq, profile)
		return
	}
	sessionSource := firstNonEmptyString(req.ConversationID, req.RunID, req.RequestID, req.ModelCallID)
	threadSource := firstNonEmptyString(req.ConversationID, req.RequestID, req.RunID, req.ModelCallID)
	applyCodexRequestHeaders(httpReq, sessionSource, threadSource)
}

func applyClaudeCodeHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Set("User-Agent", ClaudeCodeUserAgent)
	httpReq.Header.Set("anthropic-beta", ClaudeCodeAnthropicBeta)
	httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	httpReq.Header.Set("x-app", "cli")
	httpReq.Header.Set("x-stainless-arch", stainlessArch(runtime.GOARCH))
	httpReq.Header.Set("x-stainless-helper-method", "stream")
	httpReq.Header.Set("x-stainless-lang", "js")
	httpReq.Header.Set("x-stainless-os", stainlessOS(runtime.GOOS))
	httpReq.Header.Set("x-stainless-package-version", ClaudeCodeStainlessPackage)
	httpReq.Header.Set("x-stainless-retry-count", "0")
	httpReq.Header.Set("x-stainless-runtime", "node")
	httpReq.Header.Set("x-stainless-runtime-version", ClaudeCodeRuntimeVersion)
	httpReq.Header.Set("x-stainless-timeout", ClaudeCodeStainlessTimeout)
}

func applyClaudeCodeRequestHeaders(httpReq *http.Request, sessionSource string, thinkingEnabled bool, extendedContext bool) {
	applyClaudeCodeHeaders(httpReq)
	httpReq.Header.Set("X-Claude-Code-Session-Id", claudeCodeSessionID(sessionSource))
	for _, beta := range claudeCodeBetas(thinkingEnabled, extendedContext) {
		appendAnthropicBeta(httpReq, beta)
	}
}

func claudeCodeBetas(thinkingEnabled bool, extendedContext bool) []string {
	betas := []string{ClaudeCodeAnthropicBeta}
	if extendedContext {
		betas = append(betas, AnthropicExtendedContextBeta)
	}
	betas = append(betas,
		ClaudeCodeInterleavedBeta,
		ClaudeCodeThinkingTokenBeta,
		ClaudeCodeContextManageBeta,
		ClaudeCodePromptCacheScopeBeta,
		ClaudeCodeMidSystemBeta,
		ClaudeCodeEffortBeta,
	)
	if thinkingEnabled {
		betas = append(betas, ClaudeCodeFallbackCreditBeta)
	}
	return betas
}

func appendAnthropicBeta(httpReq *http.Request, value string) {
	if httpReq == nil || strings.TrimSpace(value) == "" {
		return
	}
	current := strings.TrimSpace(httpReq.Header.Get("anthropic-beta"))
	for _, beta := range strings.Split(current, ",") {
		if strings.EqualFold(strings.TrimSpace(beta), value) {
			return
		}
	}
	if current == "" {
		httpReq.Header.Set("anthropic-beta", value)
		return
	}
	httpReq.Header.Set("anthropic-beta", current+","+value)
}

func claudeCodeSessionID(source string) string {
	return deterministicClaudeCodeUUID("session", source)
}

func claudeCodeDeviceID(source string) string {
	sum := sha256.Sum256([]byte("cursor-byok/claude-code-device/" + strings.TrimSpace(source)))
	return fmt.Sprintf("%x", sum[:])
}

func deterministicClaudeCodeUUID(kind string, source string) string {
	sum := sha256.Sum256([]byte("cursor-byok/claude-code-" + strings.TrimSpace(kind) + "/" + strings.TrimSpace(source)))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func applyCodexHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Originator", CodexClientOriginator)
	// Version is retained for relay compatibility. The official client carries
	// the same value in User-Agent, while some Codex-only relays still inspect it.
	httpReq.Header.Set("Version", CodexClientVersion)
	httpReq.Header.Set("User-Agent", codexUserAgent(runtime.GOOS, runtime.GOARCH))
}

func applyCodexRequestHeaders(httpReq *http.Request, sessionSource string, threadSource string) {
	if httpReq == nil {
		return
	}
	applyCodexHeaders(httpReq)
	httpReq.Header.Set("session-id", deterministicCodexUUID("session", sessionSource))
	httpReq.Header.Set("thread-id", deterministicCodexUUID("thread", threadSource))
}

func deterministicCodexUUID(kind string, source string) string {
	sum := sha256.Sum256([]byte("cursor-byok/codex-" + strings.TrimSpace(kind) + "/" + strings.TrimSpace(source)))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func codexUserAgent(goos string, goarch string) string {
	return CodexClientOriginator + "/" + CodexClientVersion + " (" + codexPlatform(goos) + " " + codexPlatformVersion(goos) + "; " + codexArchitecture(goarch) + ") " + CodexClientTerminal
}

func codexPlatformVersion(value string) string {
	switch value {
	case "darwin":
		return CodexMacOSVersion
	case "windows":
		return CodexWindowsVersion
	case "linux":
		return CodexLinuxVersion
	default:
		return "0.0.0"
	}
}

func codexArchitecture(value string) string {
	switch value {
	case "amd64":
		return "x86_64"
	default:
		return value
	}
}

func stainlessOS(value string) string {
	switch value {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return value
	}
}

func stainlessArch(value string) string {
	switch value {
	case "amd64":
		return "x64"
	default:
		return value
	}
}

func codexPlatform(value string) string {
	switch value {
	case "darwin":
		return "Mac OS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return value
	}
}

func anthropicWireModelID(modelID string, profile string, extendedContext bool) string {
	model := strings.TrimSpace(modelID)
	for strings.HasSuffix(strings.ToLower(model), anthropicExtendedContextSuffix) {
		model = strings.TrimSpace(model[:len(model)-len(anthropicExtendedContextSuffix)])
	}
	return model
}

func hasAnthropicExtendedContextSuffix(modelID string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(modelID)), anthropicExtendedContextSuffix)
}
