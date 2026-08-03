package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicStreamRequiresMessageStop(t *testing.T) {
	tests := []struct {
		name             string
		stream           string
		wantText         bool
		wantToolActivity bool
	}{
		{name: "empty", stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\"}}\n\n"},
		{name: "partial text", stream: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n", wantText: true},
		{name: "partial tool", stream: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"Read\",\"input\":{}}}\n\n", wantToolActivity: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(writer, test.stream)
			}))
			defer server.Close()

			err := NewAnthropicAdapter().Stream(context.Background(), minimalAnthropicStreamRequest(server.URL), func(ModelEvent) error { return nil })
			var incomplete *IncompleteStreamError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want IncompleteStreamError", err)
			}
			if incomplete.HasText != test.wantText || incomplete.HasToolActivity != test.wantToolActivity {
				t.Fatalf("incomplete = %+v", incomplete)
			}
		})
	}
}

func TestAnthropicRefusalIsExplicitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"stop_details\":{\"type\":\"safety\"}}\n\n")
		_, _ = fmt.Fprint(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	err := NewAnthropicAdapter().Stream(context.Background(), minimalAnthropicStreamRequest(server.URL), func(ModelEvent) error { return nil })
	var refusal *ProviderRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want ProviderRefusalError", err)
	}
	if !strings.Contains(refusal.StopDetails, "safety") {
		t.Fatalf("stop details = %q", refusal.StopDetails)
	}
}

func TestAnthropicRefusalWithoutMessageStopIsNotRecoveredAsDisconnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"safety\"}}}\n\n")
	}))
	defer server.Close()
	err := NewAnthropicAdapter().Stream(context.Background(), minimalAnthropicStreamRequest(server.URL), func(ModelEvent) error { return nil })
	var refusal *ProviderRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want ProviderRefusalError", err)
	}
}

func TestAnthropicStreamWithMessageStopCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		_, _ = fmt.Fprint(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	finished := false
	err := NewAnthropicAdapter().Stream(context.Background(), minimalAnthropicStreamRequest(server.URL), func(event ModelEvent) error {
		finished = finished || event.Kind == ModelEventKindTurnFinished
		return nil
	})
	if err != nil || !finished {
		t.Fatalf("err = %v, finished = %v", err, finished)
	}
}

func TestOpenAIResponsesRequiresTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	req := StreamRequest{
		RequestID: "request-1", ModelCallID: "call-1", ModelID: "adapter-1",
		Provider: "openai", BaseURL: server.URL, APIKey: "test-key", ProviderModelID: "gpt-test",
		OpenAIEndpoint: "/v1/responses", Messages: []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8, Stream: true, ProviderRequestMaxAttempts: 1,
	}
	err := NewOpenAIAdapter().Stream(context.Background(), req, func(ModelEvent) error { return nil })
	var incomplete *IncompleteStreamError
	if !errors.As(err, &incomplete) || !incomplete.HasText {
		t.Fatalf("error = %v, want text-bearing IncompleteStreamError", err)
	}
}

func TestOpenAIResponsesCompletedEventStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-test\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	req := StreamRequest{RequestID: "request-1", ModelCallID: "call-1", ModelID: "adapter-1", Provider: "openai", BaseURL: server.URL, APIKey: "test-key", ProviderModelID: "gpt-test", OpenAIEndpoint: "/v1/responses", Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: 8, Stream: true, ProviderRequestMaxAttempts: 1}
	if err := NewOpenAIAdapter().Stream(context.Background(), req, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("stream error = %v", err)
	}
}

func minimalAnthropicStreamRequest(baseURL string) StreamRequest {
	return StreamRequest{
		RequestID: "request-1", ModelCallID: "call-1", ModelID: "adapter-1",
		Provider: "anthropic", BaseURL: baseURL, APIKey: "test-key", ProviderModelID: "claude-test",
		Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: 8, Stream: true,
		ProviderRequestMaxAttempts: 1,
	}
}
