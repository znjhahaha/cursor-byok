package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
)

func TestApplyProviderModelsHeadersUsesProfileAndCustomPrecedence(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	provider := serverconfig.ProviderConfig{
		Type:          "anthropic",
		APIKey:        "Bearer secret",
		ClientProfile: modeladapter.ClientProfileClaudeCode,
		UserAgent:     "provider-agent",
		HeadersJSON:   `{"User-Agent":"custom-agent","anthropic-beta":"custom-beta"}`,
	}
	if err := applyProviderModelsHeaders(request, provider); err != nil {
		t.Fatalf("apply provider model headers: %v", err)
	}
	if got := request.Header.Get("x-api-key"); got != "secret" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("User-Agent"); got != "custom-agent" {
		t.Fatalf("custom User-Agent precedence failed: %q", got)
	}
	if got := request.Header.Get("anthropic-beta"); got != "custom-beta" {
		t.Fatalf("custom beta precedence failed: %q", got)
	}
	if got := request.Header.Get("x-app"); got != "cli" {
		t.Fatalf("Claude Code profile missing from model list request: %q", got)
	}
}

func TestProviderModelsRequestHashIncludesClientProfile(t *testing.T) {
	generic := serverconfig.ProviderConfig{
		Type:          "anthropic",
		BaseURL:       "https://example.com",
		APIKey:        "secret",
		ClientProfile: modeladapter.ClientProfileGeneric,
	}
	claude := generic
	claude.ClientProfile = modeladapter.ClientProfileClaudeCode
	if buildProviderModelsRequestHash(generic) == buildProviderModelsRequestHash(claude) {
		t.Fatal("provider model cache hash ignored clientProfile")
	}
}
