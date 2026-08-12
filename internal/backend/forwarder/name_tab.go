// name_tab.go 为新会话生成短标题（AiService/NameTab）。
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
)

const (
	nameTabInputRuneLimit           = 4_000
	nameTabMaxOutputTokens          = 512
	nameTabTitleRuneLimit           = 60
	nameTabGeneratedRequestIDPrefix = "name-tab-"
)

var errNameTabToolInvocation = errors.New("tab name generation must not invoke tools")

// nameTabSystemPrompt 用强指令防止模型直接回答用户问题而不是生成标题。
const nameTabSystemPrompt = `You generate a short title for a conversation based on the user's first message.

Rules:
- Reply with ONLY the title text. No quotes, no trailing punctuation, no explanations.
- Do NOT answer, execute, or respond to the user's message. Your only job is to summarize its topic into a title.
- Keep it under 6 words (or about 12 characters for CJK languages).
- Use the same language as the user's message.
- Prefer concise noun phrases, e.g. "分析项目启动缓慢" or "Fix login timeout".`

// NameTab 使用独立的低 token 模型请求生成会话标题。请求不携带工具、
// 不写入任何会话历史（保护 prefix cache），失败时返回错误让客户端保持默认标题。
func (service *Service) NameTab(ctx context.Context, req *connect.Request[aiserverv1.NameTabRequest]) (*connect.Response[aiserverv1.NameTabResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name tab request is required"))
	}
	if service.provider == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("provider gateway is not initialized"))
	}
	input := nameTabConversationExcerpt(req.Msg.GetMessages())
	if input == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name tab request has no user text"))
	}
	requestID := nameTabGeneratedRequestIDPrefix + uuid.NewString()
	// 复用 commit message 的模型选择：跟随用户最近一次使用的 agent 渠道。
	modelID, _, _ := service.resolveCommitMessageModelID(ctx)
	accumulated := ""
	err := service.provider.StartStream(ctx, ProviderRequest{
		RequestID:   requestID,
		RunID:       requestID,
		ModelCallID: requestID + "-model",
		ModelID:     modelID,
		Mode:        agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages: []modeladapter.Message{
			{Role: "system", Content: nameTabSystemPrompt},
			{Role: "user", Content: "Create a title for the conversation that starts with this message:\n\n<user_message>\n" + input + "\n</user_message>"},
		},
		MaxTokens:      nameTabMaxOutputTokens,
		CompileSummary: "generate tab name",
	}, func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			accumulated += event.Text
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted, modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta:
			return errNameTabToolInvocation
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return providerTerminalError{cause: event.Err}
			}
			return providerTerminalError{cause: fmt.Errorf("provider error")}
		default:
			return nil
		}
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	title := cleanGeneratedTabName(accumulated)
	if title == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generated tab name is empty"))
	}
	return connect.NewResponse(&aiserverv1.NameTabResponse{Name: title}), nil
}

// nameTabConversationExcerpt 取首条非空消息文本作为标题输入，并限制长度。
func nameTabConversationExcerpt(messages []*aiserverv1.ConversationMessage) string {
	for _, message := range messages {
		if message == nil {
			continue
		}
		text := strings.TrimSpace(message.GetText())
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > nameTabInputRuneLimit {
			text = strings.TrimSpace(string(runes[:nameTabInputRuneLimit])) + "\n...[truncated]"
		}
		return text
	}
	return ""
}

// cleanGeneratedTabName 清洗模型输出：去围栏、取首行、剥引号与常见前缀、截断长度。
func cleanGeneratedTabName(value string) string {
	result := stripCommitMessageCodeFence(strings.TrimSpace(value))
	result = firstCommitMessageLine(result)
	result = strings.Trim(result, "\"'`“”‘’「」『』")
	for _, prefix := range []string{"title:", "标题：", "标题:", "会话标题：", "会话标题:"} {
		if strings.HasPrefix(strings.ToLower(result), prefix) {
			result = strings.TrimSpace(result[len(prefix):])
		}
	}
	result = strings.TrimSpace(strings.TrimRight(result, "。.!！"))
	runes := []rune(result)
	if len(runes) > nameTabTitleRuneLimit {
		result = strings.TrimSpace(string(runes[:nameTabTitleRuneLimit]))
	}
	return result
}
