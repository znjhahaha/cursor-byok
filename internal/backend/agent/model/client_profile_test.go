package modeladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

type capturedProviderRequest struct {
	Header http.Header
	Path   string
	Query  string
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
			Query:  request.URL.RawQuery,
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
		applyAnthropicProfileAuthHeaders(request, "Bearer secret", ClientProfileClaudeCode)
		applyClaudeCodeRequestHeaders(request, "conversation-1", true, true)
		if got := request.Header.Get("User-Agent"); got != ClaudeCodeUserAgent {
			t.Fatalf("Claude Code User-Agent = %q", got)
		}
		wantBeta := strings.Join(claudeCodeBetas(true, true), ",")
		if got := request.Header.Get("anthropic-beta"); got != wantBeta {
			t.Fatalf("Claude Code anthropic-beta = %q", got)
		}
		if got := request.Header.Get("anthropic-dangerous-direct-browser-access"); got != "true" {
			t.Fatalf("direct-browser header = %q", got)
		}
		if got := request.Header.Get("Accept-Encoding"); got != "identity" {
			t.Fatalf("Accept-Encoding = %q", got)
		}
		if got := request.Header.Get("x-app"); got != "cli" {
			t.Fatalf("x-app = %q", got)
		}
		if got := request.Header.Get("x-stainless-package-version"); got != "0.94.0" {
			t.Fatalf("x-stainless-package-version = %q", got)
		}
		if got := request.Header.Get("x-stainless-runtime-version"); got != "v26.3.0" {
			t.Fatalf("x-stainless-runtime-version = %q", got)
		}
		if got := request.Header.Get("x-stainless-timeout"); got != "600" {
			t.Fatalf("x-stainless-timeout = %q", got)
		}
		if got := request.Header.Get("x-api-key"); got != "" {
			t.Fatalf("Claude Code unexpectedly sent x-api-key = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := request.Header.Get("X-Claude-Code-Session-Id"); !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(got) {
			t.Fatalf("X-Claude-Code-Session-Id = %q", got)
		}
		if got := request.Header.Get("x-client-request-id"); got != "" {
			t.Fatalf("unexpected x-client-request-id = %q", got)
		}
		if first, second := claudeCodeSessionID("conversation-1"), claudeCodeSessionID("conversation-1"); first != second {
			t.Fatalf("session ID is not stable: %q != %q", first, second)
		}
		noCapabilities := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
		applyClaudeCodeRequestHeaders(noCapabilities, "conversation-1", false, false)
		if got := noCapabilities.Header.Get("anthropic-beta"); got != strings.Join(claudeCodeBetas(false, false), ",") {
			t.Fatalf("disabled capabilities anthropic-beta = %q", got)
		}

		err := ApplyCustomHeaders(request, true, `{
			"User-Agent": "custom-agent",
			"anthropic-beta": "custom-beta",
			"x-api-key": "custom-key",
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
		if got := request.Header.Get("x-api-key"); got != "custom-key" {
			t.Fatalf("custom x-api-key precedence failed: %q", got)
		}
	})

	t.Run("codex", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
		applyCodexRequestHeaders(request, "session-source", "thread-source")
		if got := request.Header.Get("Originator"); got != CodexClientOriginator {
			t.Fatalf("Originator = %q", got)
		}
		if got := request.Header.Get("Version"); got != CodexClientVersion {
			t.Fatalf("Version = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != codexUserAgent(runtime.GOOS, runtime.GOARCH) {
			t.Fatalf("Codex User-Agent = %q", got)
		}
		uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		if got := request.Header.Get("session-id"); !uuidPattern.MatchString(got) {
			t.Fatalf("Codex session-id = %q", got)
		}
		if got := request.Header.Get("thread-id"); !uuidPattern.MatchString(got) {
			t.Fatalf("Codex thread-id = %q", got)
		}
		if got, want := request.Header.Get("session-id"), deterministicCodexUUID("session", "session-source"); got != want {
			t.Fatalf("Codex session-id = %q, want %q", got, want)
		}
		if err := ApplyCustomHeaders(request, true, `{"User-Agent":"custom-codex","Version":"custom-version","session-id":"custom-session","thread-id":"custom-thread"}`); err != nil {
			t.Fatalf("apply Codex custom headers: %v", err)
		}
		if request.Header.Get("User-Agent") != "custom-codex" || request.Header.Get("Version") != "custom-version" ||
			request.Header.Get("session-id") != "custom-session" || request.Header.Get("thread-id") != "custom-thread" {
			t.Fatalf("Codex custom header precedence failed: %#v", request.Header)
		}
	})
}

func TestClaudeCodeWireDiagnosticIsSanitized(t *testing.T) {
	req := StreamRequest{
		ClientProfile:             ClientProfileClaudeCode,
		Anthropic1MContextEnabled: true,
		APIKey:                    "must-not-appear",
	}
	diagnostic := buildWireDiagnostic(req, "anthropic", "claude-opus-5", map[string]any{
		"stream":   true,
		"model":    "claude-opus-5",
		"thinking": map[string]any{"type": "adaptive"},
	})
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatalf("marshal diagnostic: %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, req.APIKey) || strings.Contains(text, `"messages"`) {
		t.Fatalf("wire diagnostic leaked credentials or request content: %s", text)
	}
	if !strings.Contains(text, ClaudeCodeVersion) || !strings.Contains(text, AnthropicExtendedContextBeta) {
		t.Fatalf("wire diagnostic lacks profile metadata: %s", text)
	}
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
		CustomHeadersJSON:           `{"x-app":"custom-cli","x-api-key":"custom-key"}`,
		AnthropicExtraParamsEnabled: false,
		ConversationID:              "conversation-1",
	}
	if err := adapter.Stream(context.Background(), base, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error")
	}
	normal := <-captures
	if normal.Path != "/v1/messages" {
		t.Fatalf("Anthropic path = %q", normal.Path)
	}
	if normal.Query != "beta=true" {
		t.Fatalf("Claude Code query = %q, want beta=true", normal.Query)
	}
	if got := normal.Body["model"]; got != "claude-opus-4-1" {
		t.Fatalf("normal wire model = %#v", got)
	}
	if got := normal.Header.Get("x-app"); got != "custom-cli" {
		t.Fatalf("custom header did not override profile: %q", got)
	}
	if got := normal.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", got)
	}
	if got := normal.Header.Get("anthropic-beta"); got != strings.Join(claudeCodeBetas(true, true), ",") {
		t.Fatalf("1M anthropic-beta = %q", got)
	}
	outputConfig, _ := normal.Body["output_config"].(map[string]any)
	if got := outputConfig["effort"]; got != "high" {
		t.Fatalf("Claude Code wire effort = %#v, want high", got)
	}
	metadata, _ := normal.Body["metadata"].(map[string]any)
	var metadataUser struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(metadata["user_id"])), &metadataUser); err != nil {
		t.Fatalf("decode Claude Code metadata user_id: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(metadataUser.DeviceID) {
		t.Fatalf("Claude Code metadata device_id = %q", metadataUser.DeviceID)
	}
	if metadataUser.AccountUUID != "" {
		t.Fatalf("Claude Code metadata account_uuid = %q, want empty", metadataUser.AccountUUID)
	}
	if metadataUser.SessionID != normal.Header.Get("X-Claude-Code-Session-Id") {
		t.Fatalf("metadata session_id = %q, header = %q", metadataUser.SessionID, normal.Header.Get("X-Claude-Code-Session-Id"))
	}
	if got := normal.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Claude Code Authorization = %q", got)
	}
	if got := normal.Header.Get("x-api-key"); got != "custom-key" {
		t.Fatalf("custom x-api-key override = %q", got)
	}
	if got := normal.Header.Get("X-Claude-Code-Session-Id"); got != claudeCodeSessionID("conversation-1") {
		t.Fatalf("session ID = %q", got)
	}
	encodedSystem, _ := json.Marshal(normal.Body["system"])
	if strings.Contains(string(encodedSystem), "x-anthropic-billing-header") || strings.Contains(string(encodedSystem), "cc_version=") {
		t.Fatalf("legacy billing system block leaked into request: %s", encodedSystem)
	}
	system, _ := normal.Body["system"].([]any)
	if len(system) == 0 {
		t.Fatal("Claude Code system identity is missing")
	}
	firstSystem, _ := system[0].(map[string]any)
	if got := strings.TrimSpace(fmt.Sprint(firstSystem["text"])); got != claudeCodeSystemIdentity {
		t.Fatalf("first Claude Code system block = %q", got)
	}
	thinking, _ := normal.Body["thinking"].(map[string]any)
	if got := thinking["display"]; got != "omitted" {
		t.Fatalf("Claude Code thinking display = %#v, want omitted", got)
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
	if got := overridden.Body["model"]; got != "claude-opus-4-1" {
		t.Fatalf("override wire model retained capability suffix: %#v", got)
	}
	if got := overridden.Header.Get("User-Agent"); got != normal.Header.Get("User-Agent") {
		t.Fatalf("override path profile differs: %q != %q", got, normal.Header.Get("User-Agent"))
	}
	if got := overridden.Header.Get("anthropic-beta"); got != normal.Header.Get("anthropic-beta") {
		t.Fatalf("override path 1M beta differs: %q != %q", got, normal.Header.Get("anthropic-beta"))
	}
	overrideSystem, _ := overridden.Body["system"].([]any)
	if len(overrideSystem) == 0 {
		t.Fatal("override path lacks Claude Code system identity")
	}
	overrideFirst, _ := overrideSystem[0].(map[string]any)
	if got := strings.TrimSpace(fmt.Sprint(overrideFirst["text"])); got != claudeCodeSystemIdentity {
		t.Fatalf("override first system block = %q", got)
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
		{name: "claude-code", profile: ClientProfileClaudeCode, want: "claude-opus-4-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := anthropicWireModelID("claude-opus-4-1", test.profile, true); got != test.want {
				t.Fatalf("wire model = %q, want %q", got, test.want)
			}
			if got := anthropicWireModelID("claude-opus-4-1[1m]", test.profile, false); got != "claude-opus-4-1" {
				t.Fatalf("legacy capability suffix leaked to wire for profile %q: %q", test.profile, got)
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

func TestClaudeCodeRemovesUnsignedThinkingHistory(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "unsigned", "signature": ""},
					map[string]any{"type": "thinking", "thinking": "signed", "signature": "valid-signature"},
					map[string]any{"type": "text", "text": "answer"},
				},
			},
			map[string]any{"role": "user", "content": "continue"},
		},
	}
	removeUnsignedAnthropicThinkingBlocks(body, ClientProfileClaudeCode)
	messages := body["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content block count = %d, want 2", len(content))
	}
	first := content[0].(map[string]any)
	if first["thinking"] != "signed" || first["signature"] != "valid-signature" {
		t.Fatalf("signed thinking block changed: %#v", first)
	}

	genericBody := cloneRequestBodyOverride(body)
	genericMessages := genericBody["messages"].([]any)
	genericContent := genericMessages[0].(map[string]any)["content"].([]any)
	genericContent = append([]any{map[string]any{"type": "thinking", "thinking": "unsigned"}}, genericContent...)
	genericMessages[0].(map[string]any)["content"] = genericContent
	removeUnsignedAnthropicThinkingBlocks(genericBody, ClientProfileGeneric)
	if got := len(genericMessages[0].(map[string]any)["content"].([]any)); got != 3 {
		t.Fatalf("generic profile history was changed, content blocks = %d", got)
	}

	customMetadata := map[string]any{
		"metadata": map[string]any{"user_id": `{"device_id":"custom","account_uuid":"","session_id":"custom"}`},
	}
	applyClaudeCodeMetadata(customMetadata, ClientProfileClaudeCode, "conversation")
	if got := customMetadata["metadata"].(map[string]any)["user_id"]; got != `{"device_id":"custom","account_uuid":"","session_id":"custom"}` {
		t.Fatalf("explicit Claude Code metadata was overwritten: %v", got)
	}
}

func TestOpenAIRequestProfileIsLimitedToResponses(t *testing.T) {
	captures := make(chan capturedProviderRequest, 3)
	server := newProviderCaptureServer(t, captures)
	defer server.Close()

	adapter := NewOpenAIAdapter()
	adapter.client = server.Client()
	base := StreamRequest{
		BaseURL:         server.URL,
		APIKey:          "secret",
		ProviderModelID: "gpt-5",
		ClientProfile:   ClientProfileCodex,
		ConversationID:  "conversation-codex",
		RequestID:       "request-codex",
		RunID:           "run-codex",
		MaxTokens:       16,
		RequestBodyOverride: map[string]any{
			"model":  "gpt-5",
			"stream": true,
			"input":  []any{},
		},
	}

	normalResponses := base
	normalResponses.OpenAIEndpoint = "/v1/responses"
	normalResponses.RequestBodyOverride = nil
	normalResponses.Messages = []Message{{Role: "user", Content: "ping"}}
	if err := adapter.Stream(context.Background(), normalResponses, func(ModelEvent) error { return nil }); err == nil {
		t.Fatal("expected capture server status error for normal Responses request")
	}
	normalResponsesCapture := <-captures

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
	if got, want := responsesCapture.Header.Get("session-id"), deterministicCodexUUID("session", base.ConversationID); got != want {
		t.Fatalf("Responses session-id = %q, want %q", got, want)
	}
	if got, want := responsesCapture.Header.Get("thread-id"), deterministicCodexUUID("thread", base.ConversationID); got != want {
		t.Fatalf("Responses thread-id = %q, want %q", got, want)
	}
	if got := responsesCapture.Header.Get("Version"); got != CodexClientVersion {
		t.Fatalf("Responses Version = %q", got)
	}
	if got := responsesCapture.Header.Get("User-Agent"); got != codexUserAgent(runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("Responses User-Agent = %q", got)
	}
	for _, headerName := range []string{"Originator", "Version", "User-Agent", "session-id", "thread-id", "Authorization"} {
		if normal, override := normalResponsesCapture.Header.Get(headerName), responsesCapture.Header.Get(headerName); normal != override {
			t.Fatalf("normal and RequestBodyOverride Responses %s differ: %q != %q", headerName, normal, override)
		}
	}
	if got := normalResponsesCapture.Body["model"]; got != "gpt-5" {
		t.Fatalf("normal Responses model = %v", got)
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
	if got := chatCapture.Header.Get("session-id"); got != "" {
		t.Fatalf("Chat Completions unexpectedly sent session-id: %q", got)
	}
	if got := chatCapture.Header.Get("thread-id"); got != "" {
		t.Fatalf("Chat Completions unexpectedly sent thread-id: %q", got)
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

func TestClientProfileSurvivesTransientProviderRetries(t *testing.T) {
	var headers []http.Header
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		headers = append(headers, request.Header.Clone())
		status := http.StatusServiceUnavailable
		if len(headers) == 3 {
			status = http.StatusTeapot
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Retry-After": []string{"0"}},
			Body:       io.NopCloser(strings.NewReader("captured")),
			Request:    request,
		}, nil
	})}
	build := func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/messages", nil)
		if err != nil {
			return nil, err
		}
		applyAnthropicProfileAuthHeaders(request, "secret", ClientProfileClaudeCode)
		applyClaudeCodeRequestHeaders(request, "conversation-retry", true, true)
		return request, nil
	}
	response, err := DoProviderRequestWithRetry(context.Background(), client, "anthropic", "", "", build)
	if err != nil {
		t.Fatalf("provider request: %v", err)
	}
	defer response.Body.Close()
	if len(headers) != 3 {
		t.Fatalf("provider request attempts = %d, want 3", len(headers))
	}
	for index := 1; index < len(headers); index++ {
		if headers[index].Get("User-Agent") != headers[0].Get("User-Agent") ||
			headers[index].Get("anthropic-beta") != headers[0].Get("anthropic-beta") ||
			headers[index].Get("X-Claude-Code-Session-Id") != headers[0].Get("X-Claude-Code-Session-Id") {
			t.Fatalf("retry %d changed Claude Code profile headers", index+1)
		}
	}
	if got := ProviderRetryAttemptSummary(response); got != "attempts=3 statuses=503,503,418" {
		t.Fatalf("retry summary = %q", got)
	}
}

func TestCodexProfileSurvivesTransientProviderRetries(t *testing.T) {
	var headers []http.Header
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		headers = append(headers, request.Header.Clone())
		status := http.StatusServiceUnavailable
		if len(headers) == 3 {
			status = http.StatusTeapot
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Retry-After": []string{"0"}},
			Body:       io.NopCloser(strings.NewReader("captured")),
			Request:    request,
		}, nil
	})}
	req := StreamRequest{
		ClientProfile:  ClientProfileCodex,
		OpenAIEndpoint: "/v1/responses",
		ConversationID: "conversation-codex-retry",
		RequestID:      "request-codex-retry",
	}
	build := func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/responses", nil)
		if err != nil {
			return nil, err
		}
		applyOpenAIRequestProfileHeaders(request, req)
		return request, nil
	}
	response, err := DoProviderRequestWithRetry(context.Background(), client, "openai", "", "", build)
	if err != nil {
		t.Fatalf("provider request: %v", err)
	}
	defer response.Body.Close()
	if len(headers) != 3 {
		t.Fatalf("provider request attempts = %d, want 3", len(headers))
	}
	for index := 1; index < len(headers); index++ {
		for _, name := range []string{"Originator", "Version", "User-Agent", "session-id", "thread-id"} {
			if headers[index].Get(name) != headers[0].Get(name) {
				t.Fatalf("retry %d changed Codex header %s", index+1, name)
			}
		}
	}
	if got := ProviderRetryAttemptSummary(response); got != "attempts=3 statuses=503,503,418" {
		t.Fatalf("retry summary = %q", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
