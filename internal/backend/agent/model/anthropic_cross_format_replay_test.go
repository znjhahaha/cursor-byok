package modeladapter

import (
	"strings"
	"testing"
)

// 跨格式回放回归测试。
//
// 背景：会话历史以 provider 中立的 []Message 持久化，但其中的推理字段
// （ReasoningContent / ReasoningSignature / ReasoningSignatureSource）带有 provider 语义。
// 会话中途切换模型时，Anthropic 适配器会读到 OpenAI Responses 产出的 signature，反之亦然。
// 历史 bug：不可回放的 thinking block 被静默丢弃，导致「只有推理、没有正文」的 assistant
// 轮次整条消失，历史看起来被清空并触发显示异常。
//
// 这里锁定的不变量：跨格式归一化不得减少可见的会话轮次。

// crossFormatMessage 用可读的方式描述归一化结果，便于表驱动断言。
type crossFormatMessage struct {
	role   string
	blocks []string
}

func TestCrossFormatReplayPreservesHistory(t *testing.T) {
	// 同一段对话，分别以三种「历史出处」构造：
	//   anthropic        —— 上一轮由 Anthropic 模型产出
	//   openai_responses —— 上一轮由 OpenAI Responses 模型产出
	//   none             —— 无 signature（Chat Completions 形态的 reasoning_content）
	history := func(signatureSource string) []Message {
		signature := ""
		switch signatureSource {
		case ReasoningSignatureSourceAnthropic:
			signature = "anthropic-sig"
		case ReasoningSignatureSourceOpenAIResponses:
			signature = "openai-sig"
		}
		return []Message{
			{Role: "user", Content: "问题一"},
			{
				Role:                     "assistant",
				ReasoningContent:         "只有推理没有正文",
				ReasoningSignature:       signature,
				ReasoningSignatureSource: signatureSource,
			},
			{Role: "user", Content: "问题二"},
		}
	}

	tests := []struct {
		name            string
		signatureSource string
		thinkingEnabled bool
		want            []crossFormatMessage
	}{
		{
			// 同格式对照组：signature 可回放，thinking block 原样保留。
			name:            "anthropic_history_to_anthropic_keeps_thinking_block",
			signatureSource: ReasoningSignatureSourceAnthropic,
			thinkingEnabled: true,
			want: []crossFormatMessage{
				{role: "user", blocks: []string{"text"}},
				{role: "assistant", blocks: []string{"thinking"}},
				{role: "user", blocks: []string{"text"}},
			},
		},
		{
			// 核心回归：OpenAI Responses 的 signature 对 Anthropic 无效，必须丢弃 signature，
			// 但推理正文降级成 text block 保留，消息条数与角色交替不变。
			name:            "openai_responses_history_to_anthropic_degrades_to_text",
			signatureSource: ReasoningSignatureSourceOpenAIResponses,
			thinkingEnabled: true,
			want: []crossFormatMessage{
				{role: "user", blocks: []string{"text"}},
				{role: "assistant", blocks: []string{"text"}},
				{role: "user", blocks: []string{"text"}},
			},
		},
		{
			// 无 signature 的 reasoning_content 同样不可作为 thinking 回放，走降级路径。
			name:            "unsigned_history_to_anthropic_degrades_to_text",
			signatureSource: "",
			thinkingEnabled: true,
			want: []crossFormatMessage{
				{role: "user", blocks: []string{"text"}},
				{role: "assistant", blocks: []string{"text"}},
				{role: "user", blocks: []string{"text"}},
			},
		},
		{
			// thinking 关闭时同样不能丢历史。
			name:            "anthropic_history_to_anthropic_thinking_disabled_degrades_to_text",
			signatureSource: ReasoningSignatureSourceAnthropic,
			thinkingEnabled: false,
			want: []crossFormatMessage{
				{role: "user", blocks: []string{"text"}},
				{role: "assistant", blocks: []string{"text"}},
				{role: "user", blocks: []string{"text"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, messages, err := normalizeAnthropicProviderMessages(history(test.signatureSource), test.thinkingEnabled, false)
			if err != nil {
				t.Fatalf("normalize anthropic messages: %v", err)
			}
			assertCrossFormatMessages(t, messages, test.want)
		})
	}
}

// TestCrossFormatReplayPreservesToolRoundTrip 覆盖带工具调用的完整一轮：
// assistant(推理 + tool_call) -> tool(结果) -> assistant(正文)。
// 跨格式时 signature 不可用，但 tool_use / tool_result 的配对关系必须保持完整，
// 否则 Anthropic 会因为 tool_use 缺失对应结果而报错。
func TestCrossFormatReplayPreservesToolRoundTrip(t *testing.T) {
	sources := []struct {
		name            string
		signatureSource string
		signature       string
	}{
		{name: "anthropic_history", signatureSource: ReasoningSignatureSourceAnthropic, signature: "anthropic-sig"},
		{name: "openai_responses_history", signatureSource: ReasoningSignatureSourceOpenAIResponses, signature: "openai-sig"},
	}

	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			input := []Message{
				{Role: "user", Content: "查一下天气"},
				{
					Role:                     "assistant",
					ReasoningContent:         "需要调用工具",
					ReasoningSignature:       source.signature,
					ReasoningSignatureSource: source.signatureSource,
					ToolCalls: []ToolCallDescriptor{{
						ID:       "call_1",
						Type:     "function",
						Function: ToolCallFunctionShape{Name: "get_weather", Arguments: `{"city":"beijing"}`},
					}},
				},
				{Role: "tool", ToolCallID: "call_1", Content: "晴"},
				{Role: "assistant", Content: "北京今天晴。"},
			}

			_, messages, err := normalizeAnthropicProviderMessages(input, true, false)
			if err != nil {
				t.Fatalf("normalize anthropic messages: %v", err)
			}

			roles := make([]string, 0, len(messages))
			for _, message := range messages {
				roles = append(roles, message.Role)
			}
			wantRoles := []string{"user", "assistant", "user", "assistant"}
			if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
				t.Fatalf("role sequence = %v, want %v", roles, wantRoles)
			}

			toolUseID := findAnthropicBlockField(messages, "tool_use", "id")
			toolResultID := findAnthropicBlockField(messages, "tool_result", "tool_use_id")
			if toolUseID == "" {
				t.Fatalf("tool_use block missing in %v", messages)
			}
			if toolUseID != toolResultID {
				t.Fatalf("tool_use id %q does not match tool_result tool_use_id %q", toolUseID, toolResultID)
			}
		})
	}
}

// TestCrossFormatReplayIntoOpenAIKeepsMessageCount 是反方向的对称约束：
// Anthropic 历史切换到 OpenAI Chat Completions 时，消息条数必须原样保留。
func TestCrossFormatReplayIntoOpenAIKeepsMessageCount(t *testing.T) {
	tests := []struct {
		name            string
		signatureSource string
		signature       string
	}{
		{name: "anthropic_history", signatureSource: ReasoningSignatureSourceAnthropic, signature: "anthropic-sig"},
		{name: "openai_responses_history", signatureSource: ReasoningSignatureSourceOpenAIResponses, signature: "openai-sig"},
		{name: "unsigned_history", signatureSource: "", signature: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []Message{
				{Role: "user", Content: "问题一"},
				{
					Role:                     "assistant",
					ReasoningContent:         "只有推理没有正文",
					ReasoningSignature:       test.signature,
					ReasoningSignatureSource: test.signatureSource,
				},
				{Role: "user", Content: "问题二"},
			}

			items, err := normalizeOpenAIProviderMessages(input, true)
			if err != nil {
				t.Fatalf("normalize openai messages: %v", err)
			}
			if len(items) != len(input) {
				t.Fatalf("message count = %d, want %d (history lost on format switch)", len(items), len(input))
			}
			if got, _ := items[1]["reasoning_content"].(string); got != "只有推理没有正文" {
				t.Fatalf("assistant reasoning_content = %q, want reasoning preserved", got)
			}
		})
	}
}

func assertCrossFormatMessages(t *testing.T, got []anthropicMessage, want []crossFormatMessage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d; got %v", len(got), len(want), describeAnthropicMessages(got))
	}
	for index, wantMessage := range want {
		gotMessage := got[index]
		if gotMessage.Role != wantMessage.role {
			t.Fatalf("message[%d].role = %q, want %q", index, gotMessage.Role, wantMessage.role)
		}
		gotBlocks := anthropicBlockTypes(gotMessage)
		if strings.Join(gotBlocks, ",") != strings.Join(wantMessage.blocks, ",") {
			t.Fatalf("message[%d].blocks = %v, want %v", index, gotBlocks, wantMessage.blocks)
		}
	}
}

func anthropicBlockTypes(message anthropicMessage) []string {
	types := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		types = append(types, strings.TrimSpace(anthropicStringField(block, "type")))
	}
	return types
}

func describeAnthropicMessages(messages []anthropicMessage) []string {
	described := make([]string, 0, len(messages))
	for _, message := range messages {
		described = append(described, message.Role+"["+strings.Join(anthropicBlockTypes(message), "+")+"]")
	}
	return described
}

func findAnthropicBlockField(messages []anthropicMessage, blockType string, field string) string {
	for _, message := range messages {
		for _, block := range message.Content {
			if strings.TrimSpace(anthropicStringField(block, "type")) != blockType {
				continue
			}
			if value := strings.TrimSpace(anthropicStringField(block, field)); value != "" {
				return value
			}
		}
	}
	return ""
}