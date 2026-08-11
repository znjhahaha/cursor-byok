package modeladapter

import (
	"strings"
	"testing"
)

func TestNormalizeAnthropicProviderMessagesThinkingCarrier(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "let me check", ReasoningContent: "R1", ReasoningSignature: "S1", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic},
		{Role: "user", Content: "tool result 1"},
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{{
			ID:       "call-2",
			Type:     "function",
			Function: ToolCallFunctionShape{Name: "read", Arguments: `{}`},
		}}},
		{Role: "user", Content: "tool result 2"},
		{Role: "assistant", Content: "done", ReasoningContent: "R2", ReasoningSignature: "S2", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 6 {
		t.Fatalf("message count = %d, want 6", len(messages))
	}
	assertAnthropicThinkingBlock(t, messages[1], "R1", "S1")
	assertAnthropicThinkingBlock(t, messages[3], "R1", "S1")
	assertAnthropicThinkingBlock(t, messages[5], "R2", "S2")
	if !anthropicMessageHasBlockType(messages[3], "tool_use") {
		t.Fatal("carrier fallback assistant message is missing tool_use")
	}
}

func TestNormalizeAnthropicProviderMessagesThinkingCarrierFirstTurn(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "ok"},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	assertAnthropicThinkingBlock(t, messages[1], "", "")
}

func TestNormalizeAnthropicProviderMessagesThinkingCarrierRejectsForeignSignature(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{
			Role:                     "assistant",
			ReasoningContent:         "foreign reasoning",
			ReasoningSignature:       "openai-signature",
			ReasoningSignatureSource: ReasoningSignatureSourceOpenAIResponses,
		},
		{Role: "user", Content: "continue"},
		{Role: "assistant", Content: "done"},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4", len(messages))
	}
	if anthropicMessageHasBlockType(messages[1], "thinking") {
		t.Fatal("foreign reasoning signature was emitted as an Anthropic thinking block")
	}
	assertAnthropicThinkingBlock(t, messages[3], "", "")
}

func TestNormalizeAnthropicProviderMessagesThinkingCarrierDisabled(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "ok", ReasoningContent: "R1", ReasoningSignature: "S1", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic},
		{Role: "assistant", Content: "done"},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, false, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for index, message := range messages {
		if anthropicMessageHasBlockType(message, "thinking") {
			t.Fatalf("unexpected thinking block at messages[%d]", index)
		}
	}
}

func TestNormalizeAnthropicProviderMessagesThinkingCarrierKeepsToolUseMerge(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "let me check", ReasoningContent: "R1", ReasoningSignature: "S1", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic},
		{Role: "assistant", ReasoningContent: "R1", ReasoningSignature: "S1", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic, ToolCalls: []ToolCallDescriptor{{
			ID:       "call-2",
			Type:     "function",
			Function: ToolCallFunctionShape{Name: "read", Arguments: `{}`},
		}}},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want merged count 2", len(messages))
	}
	assertAnthropicThinkingBlock(t, messages[1], "R1", "S1")
	if !anthropicMessageHasBlockType(messages[1], "tool_use") {
		t.Fatal("merged assistant message is missing tool_use")
	}
}

func assertAnthropicThinkingBlock(t *testing.T, message anthropicMessage, wantThinking string, wantSignature string) {
	t.Helper()
	if len(message.Content) == 0 {
		t.Fatal("expected non-empty assistant content")
	}
	first := message.Content[0]
	if got := strings.TrimSpace(anthropicStringField(first, "type")); got != "thinking" {
		t.Fatalf("first block type = %q, want thinking", got)
	}
	if got := anthropicStringField(first, "thinking"); got != wantThinking {
		t.Fatalf("thinking = %q, want %q", got, wantThinking)
	}
	if got := anthropicStringField(first, "signature"); got != wantSignature {
		t.Fatalf("signature = %q, want %q", got, wantSignature)
	}
}

func anthropicMessageHasBlockType(message anthropicMessage, blockType string) bool {
	for _, block := range message.Content {
		if strings.TrimSpace(anthropicStringField(block, "type")) == blockType {
			return true
		}
	}
	return false
}
