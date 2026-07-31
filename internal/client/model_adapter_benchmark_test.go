package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	serverconfig "cursor/internal/backend/server/config"
)

func TestAnthropicBenchmarkAcceptsDataOnlySSEAndDoesNotForceThinking(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":4}}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32\"}}\n\n")
	}))
	defer server.Close()

	service := &ProxyService{}
	metrics, err := service.executeAnthropicStreamingTest(context.Background(), serverconfig.ModelAdapterConfig{
		ID:      "adapter-test",
		Type:    "anthropic",
		BaseURL: server.URL,
		APIKey:  "test-key",
		ModelID: "claude-test",
	})
	if err != nil {
		t.Fatalf("executeAnthropicStreamingTest() error = %v", err)
	}
	if metrics == nil || metrics.firstTextTokenAt.IsZero() || strings.TrimSpace(metrics.text.String()) == "" {
		t.Fatalf("metrics = %+v, want parsed text", metrics)
	}
	if _, exists := requestBody["thinking"]; exists {
		t.Fatalf("benchmark request unexpectedly forced thinking: %#v", requestBody["thinking"])
	}
}

func TestAnthropicBenchmarkStopsAfterEnoughStreamingText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":4}}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20\"}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	startedAt := time.Now()
	metrics, err := (&ProxyService{}).executeAnthropicStreamingTest(context.Background(), serverconfig.ModelAdapterConfig{
		ID:      "adapter-test",
		Type:    "anthropic",
		BaseURL: server.URL,
		APIKey:  "test-key",
		ModelID: "claude-test",
	})
	if err != nil {
		t.Fatalf("executeAnthropicStreamingTest() error = %v", err)
	}
	if !metrics.benchmarkComplete {
		t.Fatal("benchmarkComplete = false, want true")
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("benchmark waited for the upstream stream to close: %s", elapsed)
	}
}

func TestModelAdapterBenchmarkUsesSmallFixedOutputBudget(t *testing.T) {
	adapter := serverconfig.ModelAdapterConfig{
		MaxCompletionTokens: 65536,
		AnthropicMaxTokens:  65536,
	}
	if got := modelAdapterTestConfiguredOpenAIMaxTokens(adapter); got != 128 {
		t.Fatalf("OpenAI benchmark max tokens = %d, want 128", got)
	}
	if got := modelAdapterTestConfiguredAnthropicMaxTokens(adapter); got != 128 {
		t.Fatalf("Anthropic benchmark max tokens = %d, want 128", got)
	}
}

func TestModelAdapterBenchmarkProtectsBudgetAndThinkingFromExtraParams(t *testing.T) {
	enabled, raw := modelAdapterTestExtraParams(
		true,
		`{"max_tokens":65536,"thinking":{"type":"adaptive"},"temperature":0.2}`,
		"max_tokens",
		"thinking",
	)
	if !enabled {
		t.Fatal("non-sensitive extra parameter should remain enabled")
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := params["max_tokens"]; ok {
		t.Fatal("max_tokens override was not removed")
	}
	if _, ok := params["thinking"]; ok {
		t.Fatal("thinking override was not removed")
	}
	if params["temperature"] != 0.2 {
		t.Fatalf("temperature = %v, want 0.2", params["temperature"])
	}
}

func TestModelAdapterBenchmarkTreatsTextBeforeStreamFailureAsAvailable(t *testing.T) {
	startedAt := time.Now().Add(-5 * time.Second)
	metrics := &modelAdapterTestMetrics{
		firstTextTokenAt: startedAt.Add(time.Second),
		finishedAt:       startedAt.Add(5 * time.Second),
	}
	metrics.text.WriteString("1 2 3 4")
	result := buildSuccessfulModelAdapterTestResult(
		"adapter-1",
		"hash",
		startedAt,
		metrics,
		false,
		"已收到有效文本，但测速流未完整结束",
	)
	if result.Status != string(ModelAdapterTestStatusSuccess) || result.Availability != "available" {
		t.Fatalf("result = %+v", result)
	}
	if result.BenchmarkComplete {
		t.Fatal("BenchmarkComplete = true, want false")
	}
	if result.Warning == "" {
		t.Fatal("Warning is empty")
	}
}
