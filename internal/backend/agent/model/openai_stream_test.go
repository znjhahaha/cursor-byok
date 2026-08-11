package modeladapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatCompletionsIgnoresBlankFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":"first"},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":" second"},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Ls","arguments":""}}]},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"path\":"}}]},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"\"/tmp\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, 8)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "deepseek-v4-flash",
		OpenAIEndpoint:  "/v1/chat/completions",
		Messages:        []Message{{Role: "user", Content: "list files"}},
		MaxTokens:       128,
		RequestKnobs:    map[string]any{},
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingDelta, 2)
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)

	toolEvent := firstOpenAIEventForTest(events, ModelEventKindToolLikeCompleted)
	if toolEvent == nil || toolEvent.ToolInvocation == nil {
		t.Fatalf("completed tool invocation missing: %#v", toolEvent)
	}
	if toolEvent.ToolInvocation.ToolName != "Ls" {
		t.Fatalf("tool name = %q, want Ls", toolEvent.ToolInvocation.ToolName)
	}
	if got := string(toolEvent.ToolInvocation.ArgsJSON); got != `{"path":"/tmp"}` {
		t.Fatalf("tool args = %q, want %q", got, `{"path":"/tmp"}`)
	}

	finished := firstOpenAIEventForTest(events, ModelEventKindTurnFinished)
	if finished == nil || finished.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", finished.FinishReason)
	}
	if finished.InputTokens != 12 || finished.OutputTokens != 7 {
		t.Fatalf("usage = input:%d output:%d, want input:12 output:7", finished.InputTokens, finished.OutputTokens)
	}
}

func assertOpenAIEventKindCount(t *testing.T, events []ModelEvent, kind ModelEventKind, want int) {
	t.Helper()
	got := 0
	for _, event := range events {
		if event.Kind == kind {
			got++
		}
	}
	if got != want {
		t.Fatalf("event kind %s count = %d, want %d; events=%#v", kind, got, want, events)
	}
}

func firstOpenAIEventForTest(events []ModelEvent, kind ModelEventKind) *ModelEvent {
	for index := range events {
		if events[index].Kind == kind {
			return &events[index]
		}
	}
	return nil
}

func TestOpenAICloseOnlyThinkTagRouting(t *testing.T) {
	tests := []struct {
		name          string
		modelID       string
		chunks        []string
		wantReasoning string
		wantText      string
		wantTools     int
	}{
		{
			name:          "single chunk",
			modelID:       "cp_dsv4_flash_0731_pro5000",
			chunks:        []string{openAIChatChunk(`reasoning text</think>final answer`, "", "stop")},
			wantReasoning: "reasoning text",
			wantText:      "final answer",
		},
		{
			name:    "multiple chunks and split close tag",
			modelID: "cp_dsv4_flash_0731_pro5000",
			chunks: []string{
				openAIChatChunk("first ", "", ""),
				openAIChatChunk("second</thi", "", ""),
				openAIChatChunk("nk>final", "", "stop"),
			},
			wantReasoning: "first second",
			wantText:      "final",
		},
		{
			name:    "tool call does not flush split close tag",
			modelID: "cp_dsv4_flash_0731_pro5000",
			chunks: []string{
				openAIChatChunk("reasoning</thi", "", ""),
				`{"model":"cp_dsv4_flash_0731_pro5000","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Ls","arguments":"{\"path\":\"/tmp\"}"}}]},"finish_reason":""}]}`,
				openAIChatChunk("nk>answer", "", "tool_calls"),
			},
			wantReasoning: "reasoning",
			wantText:      "answer",
			wantTools:     1,
		},
		{
			name:          "missing close stays reasoning",
			modelID:       "cp_dsv4_flash_0731_pro5000",
			chunks:        []string{openAIChatChunk("reasoning only", "", "stop")},
			wantReasoning: "reasoning only",
		},
		{
			name:     "ordinary model stays text",
			modelID:  "ordinary-openai-model",
			chunks:   []string{openAIChatChunk("reasoning text</think>final answer", "", "stop")},
			wantText: "reasoning text</think>final answer",
		},
		{
			name:          "normal think tags still route",
			modelID:       "ordinary-openai-model",
			chunks:        []string{openAIChatChunk("before<think>reasoning</think>after", "", "stop")},
			wantReasoning: "reasoning",
			wantText:      "beforeafter",
		},
		{
			name:          "explicit reasoning remains supported",
			modelID:       "ordinary-openai-model",
			chunks:        []string{openAIChatChunk("answer", "explicit reasoning", "stop")},
			wantReasoning: "explicit reasoning",
			wantText:      "answer",
		},
		{
			name:          "explicit reasoning wins when first",
			modelID:       "cp_dsv4_flash_0731_pro5000",
			chunks:        []string{openAIChatChunk("duplicate reasoning</think>answer", "explicit reasoning", "stop")},
			wantReasoning: "explicit reasoning",
			wantText:      "answer",
		},
		{
			name:    "implicit reasoning wins when first",
			modelID: "cp_dsv4_flash_0731_pro5000",
			chunks: []string{
				openAIChatChunk("implicit ", "", ""),
				openAIChatChunk("duplicate", "explicit reasoning", ""),
				openAIChatChunk("</think>answer", "", "stop"),
			},
			wantReasoning: "implicit duplicate",
			wantText:      "answer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := runOpenAIChatStreamForTest(t, test.modelID, test.chunks)
			if got := openAIEventText(events, ModelEventKindThinkingDelta); got != test.wantReasoning {
				t.Fatalf("reasoning = %q, want %q; events=%#v", got, test.wantReasoning, events)
			}
			if got := openAIEventText(events, ModelEventKindTextDelta); got != test.wantText {
				t.Fatalf("text = %q, want %q; events=%#v", got, test.wantText, events)
			}
			assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, test.wantTools)
			wantThinkingCompleted := 0
			if test.wantReasoning != "" {
				wantThinkingCompleted = 1
			}
			assertOpenAIEventKindCount(t, events, ModelEventKindThinkingCompleted, wantThinkingCompleted)
		})
	}
}

func openAIChatChunk(content string, reasoning string, finishReason string) string {
	finish := "null"
	if finishReason != "" {
		finish = fmt.Sprintf("%q", finishReason)
	}
	return fmt.Sprintf(
		`{"model":"test-model","choices":[{"delta":{"content":%q,"reasoning_content":%q},"finish_reason":%s}]}`,
		content,
		reasoning,
		finish,
	)
}

func runOpenAIChatStreamForTest(t *testing.T, modelID string, chunks []string) []ModelEvent {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, len(chunks)+3)
	if err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-close-only",
		RunID:           "run-close-only",
		ModelCallID:     "model-call-close-only",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: modelID,
		OpenAIEndpoint:  "/v1/chat/completions",
		Messages:        []Message{{Role: "user", Content: "test"}},
		MaxTokens:       128,
		RequestKnobs:    map[string]any{},
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	return events
}

func openAIEventText(events []ModelEvent, kind ModelEventKind) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Kind == kind {
			builder.WriteString(event.Text)
		}
	}
	return builder.String()
}
