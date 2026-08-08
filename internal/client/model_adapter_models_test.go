package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

// Anthropic 的模型列表是分页接口，只读第一页会静默丢模型。
func TestFetchProviderModelsFollowsAnthropicPagination(t *testing.T) {
	var receivedCursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCursors = append(receivedCursors, r.URL.Query().Get("after_id"))
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "":
			fmt.Fprint(w, `{"data":[{"id":"claude-a"},{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`)
		case "claude-b":
			fmt.Fprint(w, `{"data":[{"id":"claude-c"}],"has_more":false,"last_id":"claude-c"}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("after_id"))
		}
	}))
	defer server.Close()

	provider := serverconfig.ProviderConfig{
		Type:    "anthropic",
		BaseURL: server.URL,
		APIKey:  "secret",
	}
	models, err := fetchProviderModels(server.URL+"/v1/models", provider)
	if err != nil {
		t.Fatalf("fetchProviderModels returned error: %v", err)
	}
	if len(receivedCursors) != 2 {
		t.Fatalf("requested %d pages, want 2", len(receivedCursors))
	}

	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if strings.Join(ids, ",") != "claude-a,claude-b,claude-c" {
		t.Fatalf("models = %v, want all three pages merged", ids)
	}
}

// OpenAI 兼容站点一次返回全量，不应被额外翻页放大请求次数。
func TestFetchProviderModelsDoesNotPaginateOpenAI(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("limit") != "" {
			t.Errorf("unexpected pagination query on openai: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`)
	}))
	defer server.Close()

	provider := serverconfig.ProviderConfig{
		Type:    "openai",
		BaseURL: server.URL,
		APIKey:  "secret",
	}
	models, err := fetchProviderModels(server.URL+"/v1/models", provider)
	if err != nil {
		t.Fatalf("fetchProviderModels returned error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requested %d times, want 1", requests)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2", len(models))
	}
}

// 分页中途失败时保留已取回的部分，而不是让整次探测归零。
func TestFetchProviderModelsKeepsPartialResultOnLaterPageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after_id") == "" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"claude-a"}],"has_more":true,"last_id":"claude-a"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := serverconfig.ProviderConfig{
		Type:    "anthropic",
		BaseURL: server.URL,
		APIKey:  "secret",
	}
	models, err := fetchProviderModels(server.URL+"/v1/models", provider)
	if err != nil {
		t.Fatalf("fetchProviderModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-a" {
		t.Fatalf("models = %v, want the first page preserved", models)
	}
}

// 首页就失败必须真失败，不能伪装成空列表成功。
func TestFetchProviderModelsFailsWhenFirstPageFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := serverconfig.ProviderConfig{
		Type:    "anthropic",
		BaseURL: server.URL,
		APIKey:  "secret",
	}
	if _, err := fetchProviderModels(server.URL+"/v1/models", provider); err == nil {
		t.Fatal("fetchProviderModels returned nil error for failing first page")
	}
}

func TestAppendProviderModelsCursorPreservesExistingQuery(t *testing.T) {
	got := appendProviderModelsCursor("https://api.example.com/v1/models?beta=true", "abc")
	if !strings.Contains(got, "beta=true") {
		t.Fatalf("cursor URL %q dropped the existing query", got)
	}
	if !strings.Contains(got, "after_id=abc") || !strings.Contains(got, "limit=1000") {
		t.Fatalf("cursor URL %q missing pagination params", got)
	}
}

func TestNextProviderModelsCursorStopsWithoutHasMore(t *testing.T) {
	body, err := json.Marshal(map[string]any{"has_more": false, "last_id": "tail"})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if cursor := nextProviderModelsCursor(body); cursor != "" {
		t.Fatalf("cursor = %q, want empty when has_more is false", cursor)
	}

	more, err := json.Marshal(map[string]any{"has_more": true, "last_id": "tail"})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if cursor := nextProviderModelsCursor(more); cursor != "tail" {
		t.Fatalf("cursor = %q, want tail", cursor)
	}
}

// 编辑器里未绑定中转站时，用表单值临时组装匿名站点；参数缺失要给出可读错误。
func TestResolveModelAdapterProviderValidatesStandaloneInput(t *testing.T) {
	service := &ProxyService{}

	if _, err := service.resolveModelAdapterProvider(ModelAdapterModelsRequest{Type: "gemini"}); err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if _, err := service.resolveModelAdapterProvider(ModelAdapterModelsRequest{Type: "openai"}); err == nil {
		t.Fatal("expected error for empty baseURL")
	}
	if _, err := service.resolveModelAdapterProvider(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: "https://api.example.com",
	}); err == nil {
		t.Fatal("expected error for empty apiKey")
	}

	provider, err := service.resolveModelAdapterProvider(ModelAdapterModelsRequest{
		Type:                 "openai",
		BaseURL:              "https://api.example.com",
		APIKey:               "secret",
		ClientProfile:        "codex",
		CustomHeadersEnabled: true,
		CustomHeadersJSON:    `{"X-Test":"1"}`,
	})
	if err != nil {
		t.Fatalf("resolveModelAdapterProvider returned error: %v", err)
	}
	if provider.ClientProfile != "codex" || provider.HeadersJSON != `{"X-Test":"1"}` {
		t.Fatalf("provider = %+v, want client profile and headers carried over", provider)
	}
}

// 自定义头开关关闭时，即便 JSON 还留在表单里也不应带上。
func TestResolveModelAdapterProviderIgnoresDisabledCustomHeaders(t *testing.T) {
	service := &ProxyService{}
	provider, err := service.resolveModelAdapterProvider(ModelAdapterModelsRequest{
		Type:                 "anthropic",
		BaseURL:              "https://api.example.com",
		APIKey:               "secret",
		CustomHeadersEnabled: false,
		CustomHeadersJSON:    `{"X-Test":"1"}`,
	})
	if err != nil {
		t.Fatalf("resolveModelAdapterProvider returned error: %v", err)
	}
	if provider.HeadersJSON != "" {
		t.Fatalf("headersJSON = %q, want empty when the toggle is off", provider.HeadersJSON)
	}
}