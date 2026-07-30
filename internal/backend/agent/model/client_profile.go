package modeladapter

import (
	"net/http"
	"runtime"
	"strings"
)

const (
	ClientProfileGeneric    = "generic"
	ClientProfileClaudeCode = "claude-code"
	ClientProfileCodex      = "codex"

	ClaudeCodeUserAgent            = "claude-cli/2.1.158 (external, sdk-cli)"
	ClaudeCodeAnthropicBeta        = "claude-code-20250219,interleaved-thinking-2025-05-14,effort-2025-11-24,redact-thinking-2026-02-12"
	AnthropicExtendedContextBeta   = "context-1m-2025-08-07"
	ClaudeCodeStainlessPackage     = "0.55.1"
	ClaudeCodeRuntimeVersion       = "v20.19.4"
	CodexClientVersion             = "0.101.0"
	CodexClientOriginator          = "codex_cli_rs"
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
	current := strings.TrimSpace(httpReq.Header.Get("anthropic-beta"))
	for _, beta := range strings.Split(current, ",") {
		if strings.EqualFold(strings.TrimSpace(beta), AnthropicExtendedContextBeta) {
			return
		}
	}
	if current == "" {
		httpReq.Header.Set("anthropic-beta", AnthropicExtendedContextBeta)
		return
	}
	httpReq.Header.Set("anthropic-beta", current+","+AnthropicExtendedContextBeta)
}

func openAIClientProfile(value string, endpoint string) string {
	if NormalizeClientProfile(value) == ClientProfileCodex &&
		strings.EqualFold(strings.TrimSpace(endpoint), "/v1/responses") {
		return ClientProfileCodex
	}
	return ClientProfileGeneric
}

func applyClaudeCodeHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Accept", "application/json")
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
}

func applyCodexHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Originator", CodexClientOriginator)
	httpReq.Header.Set("Version", CodexClientVersion)
	httpReq.Header.Set("User-Agent", "codex_cli_rs/"+CodexClientVersion+" ("+codexPlatform(runtime.GOOS)+"; "+runtime.GOARCH+")")
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
	if !extendedContext || NormalizeClientProfile(profile) != ClientProfileClaudeCode {
		return model
	}
	if strings.HasSuffix(strings.ToLower(model), anthropicExtendedContextSuffix) {
		return model
	}
	return model + anthropicExtendedContextSuffix
}

func hasAnthropicExtendedContextSuffix(modelID string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(modelID)), anthropicExtendedContextSuffix)
}
