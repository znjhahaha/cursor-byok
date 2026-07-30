package modeladapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type capturedProviderRequest struct {
	Header http.Header
	Path   string
	Body   map[string]any
}

func newProviderCaptureServer(t *testing.T, captures chan<- capturedProviderRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		body := map[string]any{}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
		}
		captures <- capturedProviderRequest{
			Header: request.Header.Clone(),
			Path:   request.URL.Path,
			Body:   body,
		}
		http.Error(writer, "captured", http.StatusTeapot)
	}))
}

func TestClientProfileHeadersAndCustomHeaderPrecedence(t *testing.T) {
	t.Run("generic", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
		ApplyClientProfileHeaders(request, ClientProfileGeneric)
		if got := request.Header.Get("User-Agent"); got != "" {
			t.Fatalf("generic User-Agent = %q, want empty", got)
		}
		if got := request.Header.Get("anthropic-beta"); got != "" {
			t.Fatalf("generic anthropic-beta = %q, want empty", got)
		}
	})

	t.Run("claude-code", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
		ApplyAnthropicCompatibleAuthHeaders(request, "Bearer secret")
		ApplyClientProfileHeaders(request, ClientProfileClaudeCode)
		if got := request.Header.Get("User-Agent"); got != ClaudeCodeUserAgent {
			t.Fatalf("Claude Code User-Agent = %q", got)
		}
		if got := request.Header.Get("anthropic-beta"); got != ClaudeCodeAnthropicBeta {
			t.Fatalf("Claude Code anthropic-beta = %q", got)
		}
		if got := request.Header.Get("anthropic-dangerous-direct-browser-access"); got != "true" {
			t.Fatalf("direct-browser header = %q", got)
		}
		if got := request.Header.Get("x-app"); got != "cli" {
			t.Fatalf("x-app = %q", got)
		}
		if got := request.Header.Get("x-stainless-runtime-version"); got != ClaudeCodeRuntimeVersion {
			t.Fatalf("x-stainless-runtime-version = %q", got)
		}
		if got := request.Header.Get("x-api-key"); got != "secret" {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}

		err := ApplyCustomHeaders(request, true, `{
			"User-Agent": "custom-agent",
			"anthropic-beta": "custom-beta",
			"Version": "custom-version"
		}`)
		if err != nil {
			t.Fatalf("apply custom headers: %v", err)
		}
		if got := request.Header.Get("User-Agent"); got != "custom-agent" {
			t.Fatalf("custom User-Agent precedence failed: %q", got)
		}
		if got := request.Header.Get("anthropic-beta"); got != "custom-beta" {
			t.Fatalf("custom beta precedence failed: %q", got)
		}
	})

	t.Run("codex", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
		ApplyClientProfileHeaders(request, ClientProfileCodex)
		if got := request.Header.Get("Originator"); got != CodexClientOriginator {
			t.Fatalf("Originator = %q", got)
		}
		if got := request.Header.Get("Version"); got != CodexClientVersion {
			t.Fatalf("Version = %q", got)
		}
		if got := request.Header.Get("User-Agent"); !strings.HasPrefix(got, "codex_cli_rs/"+CodexClientVersion+" ") {
			t.Fatalf("Codex User-Agent = %q", got)
		}
	})
}

func TestAnthropicRequestProfilesAndOneMillionWireModel(t *testing.T) {
	captures := make(chan capturedProviderRequest, 3)
	server := newProviderCaptureServer(t, captures)
	defer server.Close()

	adapter := NewAnthropicAdapter()
	adapter.client = server.Client()
	base := StreamRequest{
		BaseURL:                     server.URL,
		APIKey:                      "secret",
		ProviderModelID:             "claude-opus-4-1",
		ClientProfile:               ClientProfileClaudeCode,
		Anthropic1MContextEnabled:   true,
		MaxTokens:                   16,
		AnthropicThinkingEffort:     "xhigh",
		Messages:                    []Message{{Role: "user", Content: "hello"}},
		ProviderStreamIdleTimeout:   0,
		CustomHeadersEnabled:        true,
		CustomHeadersJSON:           `{"x-app":"custom-cli"}`,
		AnthropicExtraParamsEnabled: false,
	}
	if err := adapter.Stream(context.Background(), base, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error")
	}
	normal := <-captures
	if normal.Path != "/v1/messages" {
		t.Fatalf("Anthropic path = %q", normal.Path)
	}
	if got := normal.Body["model"]; got != "claude-opus-4-1[1m]" {
		t.Fatalf("normal wire model = %#v", got)
	}
	if got := normal.Header.Get("x-app"); got != "custom-cli" {
		t.Fatalf("custom header did not override profile: %q", got)
	}
	if got := normal.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", got)
	}
	if got := normal.Header.Get("anthropic-beta"); got != ClaudeCodeAnthropicBeta+","+AnthropicExtendedContextBeta {
		t.Fatalf("1M anthropic-beta = %q", got)
	}

	override := base
	override.RequestBodyOverride = map[string]any{
		"model":      "claude-opus-4-1[1m]",
		"messages":   []any{},
		"stream":     true,
		"max_tokens": 16,
	}
	if err := adapter.Stream(context.Background(), override, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error for override")
	}
	overridden := <-captures
	if got := overridden.Body["model"]; got != "claude-opus-4-1[1m]" {
		t.Fatalf("override wire model duplicated suffix: %#v", got)
	}
	if got := overridden.Header.Get("User-Agent"); got != normal.Header.Get("User-Agent") {
		t.Fatalf("override path profile differs: %q != %q", got, normal.Header.Get("User-Agent"))
	}
	if got := overridden.Header.Get("anthropic-beta"); got != normal.Header.Get("anthropic-beta") {
		t.Fatalf("override path 1M beta differs: %q != %q", got, normal.Header.Get("anthropic-beta"))
	}

	disabled := base
	disabled.Anthropic1MContextEnabled = false
	disabled.RequestBodyOverride = nil
	if err := adapter.Stream(context.Background(), disabled, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error with 1M disabled")
	}
	if got := (<-captures).Body["model"]; got != "claude-opus-4-1" {
		t.Fatalf("disabled wire model = %#v", got)
	}
}

func TestAnthropicWireModelRequiresClaudeCodeProfile(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
		want    string
	}{
		{name: "generic", profile: ClientProfileGeneric, want: "claude-opus-4-1"},
		{name: "codex", profile: ClientProfileCodex, want: "claude-opus-4-1"},
		{name: "claude-code", profile: ClientProfileClaudeCode, want: "claude-opus-4-1[1m]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := anthropicWireModelID("claude-opus-4-1", test.profile, true); got != test.want {
				t.Fatalf("wire model = %q, want %q", got, test.want)
			}
			request := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
			ApplyClientProfileHeaders(request, test.profile)
			applyAnthropicExtendedContextHeader(request, test.profile, true)
			hasExtendedBeta := strings.Contains(request.Header.Get("anthropic-beta"), AnthropicExtendedContextBeta)
			if hasExtendedBeta != (test.profile == ClientProfileClaudeCode) {
				t.Fatalf("extended beta presence = %t for profile %q", hasExtendedBeta, test.profile)
			}
		})
	}
}

func TestOpenAIRequestProfileIsLimitedToResponses(t *testing.T) {
	captures := make(chan capturedProviderRequest, 2)
	server := newProviderCaptureServer(t, captures)
	defer server.Close()

	adapter := NewOpenAIAdapter()
	adapter.client = server.Client()
	base := StreamRequest{
		BaseURL:         server.URL,
		APIKey:          "secret",
		ProviderModelID: "gpt-5",
		ClientProfile:   ClientProfileCodex,
		MaxTokens:       16,
		RequestBodyOverride: map[string]any{
			"model":  "gpt-5",
			"stream": true,
			"input":  []any{},
		},
	}

	responses := base
	responses.OpenAIEndpoint = "/v1/responses"
	if err := adapter.Stream(context.Background(), responses, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error for Responses")
	}
	responsesCapture := <-captures
	if got := responsesCapture.Header.Get("Originator"); got != CodexClientOriginator {
		t.Fatalf("Responses Originator = %q", got)
	}
	if got := responsesCapture.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Responses Authorization = %q", got)
	}

	chat := base
	chat.OpenAIEndpoint = "/v1/chat/completions"
	chat.RequestBodyOverride = map[string]any{
		"model":    "gpt-5",
		"stream":   true,
		"messages": []any{},
	}
	if err := adapter.Stream(context.Background(), chat, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error for Chat Completions")
	}
	chatCapture := <-captures
	if got := chatCapture.Header.Get("Originator"); got != "" {
		t.Fatalf("Chat Completions unexpectedly sent Codex Originator: %q", got)
	}
	if got := chatCapture.Header.Get("User-Agent"); got != "" {
		t.Fatalf("Chat Completions unexpectedly sent client User-Agent: %q", got)
	}
}

func TestClientProfileSurvivesRebuiltProviderRequest(t *testing.T) {
	var headers []http.Header
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		headers = append(headers, request.Header.Clone())
		return &http.Response{
			StatusCode: http.StatusTeapot,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("captured")),
			Request:    request,
		}, nil
	})}
	build := func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/messages", nil)
		if err != nil {
			return nil, err
		}
		ApplyClientProfileHeaders(request, ClientProfileClaudeCode)
		return request, nil
	}
	for range 2 {
		response, err := DoProviderRequestWithRetry(context.Background(), client, "anthropic", "", "", build)
		if err != nil {
			t.Fatalf("provider request: %v", err)
		}
		_ = response.Body.Close()
	}
	if len(headers) != 2 || headers[0].Get("User-Agent") != headers[1].Get("User-Agent") {
		t.Fatalf("rebuilt request profile changed: %#v", headers)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
