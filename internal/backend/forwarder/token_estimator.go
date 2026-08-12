package forwarder

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

const (
	estimatedTokensPerMessageOverhead  = int64(8)
	estimatedTokensPerToolCallOverhead = int64(6)
	estimatedTokensPerImagePart        = int64(1024)
)

func estimateCompiledPromptTokens(compiled CompiledConversation) int64 {
	return estimateModelMessagesTokens(compiled.Messages) + estimateToolDescriptorsTokens(compiled.Tools)
}

func estimateModelMessagesTokens(messages []modeladapter.Message) int64 {
	total := int64(0)
	for _, item := range messages {
		total += estimateModelMessageTokens(item)
	}
	return total
}

func estimateModelMessageTokens(item modeladapter.Message) int64 {
	total := estimatedTokensPerMessageOverhead
	total += estimateTextTokens(item.Role)
	total += estimateTextTokens(item.Content)
	total += estimateModelContentPartsTokens(item.Content, item.ContentParts)
	total += estimateTextTokens(item.ReasoningContent)
	total += estimateTextTokens(item.ReasoningSignature)
	total += estimateTextTokens(item.ToolCallID)
	total += estimateTextTokens(item.Name)
	for _, toolCall := range item.ToolCalls {
		total += estimatedTokensPerToolCallOverhead
		total += estimateTextTokens(toolCall.ID)
		total += estimateTextTokens(toolCall.Type)
		total += estimateTextTokens(toolCall.Function.Name)
		total += estimateTextTokens(toolCall.Function.Arguments)
	}
	return total
}

func estimateToolDescriptorsTokens(tools []json.RawMessage) int64 {
	total := int64(0)
	for _, item := range tools {
		total += estimateTextTokens(string(item))
	}
	return total
}

func estimateContextItemTokens(item *aiserverv1.ContextItem) int64 {
	if item == nil {
		return 0
	}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(item)
	if err != nil {
		return estimateTextTokens(item.String())
	}
	return estimateTextTokens(string(body))
}

func estimateTextTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimated := int64((runeCount + 3) / 4)
	estimated += int64(strings.Count(trimmed, "\n"))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func estimateModelContentPartsTokens(content string, parts []modeladapter.ContentPart) int64 {
	if len(parts) == 0 {
		return 0
	}
	total := int64(0)
	countText := strings.TrimSpace(content) == ""
	for _, part := range parts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "", "text":
			if countText {
				total += estimateTextTokens(part.Text)
			}
		case "image":
			total += estimatedTokensPerImagePart
			if part.Image != nil {
				total += estimateTextTokens(part.Image.MIMEType)
				total += estimateTextTokens(part.Image.Path)
			}
		}
	}
	return total
}

func estimatePromptContentPartsTokens(content string, parts []promptengine.ContentPart) int64 {
	if len(parts) == 0 {
		return 0
	}
	total := int64(0)
	countText := strings.TrimSpace(content) == ""
	for _, part := range parts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "", "text":
			if countText {
				total += estimateTextTokens(part.Text)
			}
		case "image":
			total += estimatedTokensPerImagePart
			if part.Image != nil {
				total += estimateTextTokens(part.Image.MIMEType)
				total += estimateTextTokens(part.Image.Path)
			}
		}
	}
	return total
}

// promptTokenSnapshot 记录一次真实编译得到的 prompt 规模。
// 生产者只有 driveProvider —— 那里的 compiled 本来就要发给模型，取它的规模是零额外成本；
// 消费者是 checkpoint 展示路径，它不再为了显示一个数字而重新编译整个对话。
//
// 四个分类之和恒等于 estimateCompiledPromptTokens 的结果，
// 因此总量与分类只需一次遍历，而不是原先各扫一遍全部 messages。
type promptTokenSnapshot struct {
	ContextVersion     int64
	EntryCount         int
	SystemTokens       int64
	ToolsTokens        int64
	SummaryTokens      int64
	ConversationTokens int64
}

func newPromptTokenSnapshot(compiled CompiledConversation, contextVersion int64, entryCount int) promptTokenSnapshot {
	systemTokens, summaryTokens, conversationTokens := estimateMessageBreakdownTokens(compiled.Messages)
	return promptTokenSnapshot{
		ContextVersion:     contextVersion,
		EntryCount:         entryCount,
		SystemTokens:       systemTokens,
		ToolsTokens:        estimateToolDescriptorsTokens(compiled.Tools),
		SummaryTokens:      summaryTokens,
		ConversationTokens: conversationTokens,
	}
}

func (snapshot promptTokenSnapshot) totalTokens() int64 {
	return snapshot.SystemTokens + snapshot.ToolsTokens + snapshot.SummaryTokens + snapshot.ConversationTokens
}

// withExtraConversationTokens 把快照之后新增 entries 的估算叠加到会话分类上，
// 让展示值保持单调增长，而不是等到下一次真实编译才跳变。
func (snapshot promptTokenSnapshot) withExtraConversationTokens(extra int64) promptTokenSnapshot {
	if extra > 0 {
		snapshot.ConversationTokens += extra
	}
	return snapshot
}

func (snapshot promptTokenSnapshot) breakdown(usedTokens uint32, maxTokens uint32) *agentv1.PromptTokenBreakdownSnapshot {
	if maxTokens == 0 {
		return nil
	}
	categories := make([]*agentv1.PromptTokenBreakdownCategory, 0, 4)
	categories = appendPromptTokenBreakdownCategory(categories, "system_prompt", "System Prompt", snapshot.SystemTokens)
	categories = appendPromptTokenBreakdownCategory(categories, "tools", "Tools", snapshot.ToolsTokens)
	categories = appendPromptTokenBreakdownCategory(categories, "summarized_conversation", "Summarized Conversation", snapshot.SummaryTokens)
	categories = appendPromptTokenBreakdownCategory(categories, "conversation", "Conversation", snapshot.ConversationTokens)
	return buildPromptTokenBreakdownSnapshot(categories, usedTokens, maxTokens)
}

// fallbackPromptTokenBreakdown 用于还没有任何真实编译快照的时刻（回合刚开始）：
// 只能给出一个笼统的会话分类，但代价是常量级。
func fallbackPromptTokenBreakdown(usedTokens uint32, maxTokens uint32) *agentv1.PromptTokenBreakdownSnapshot {
	if maxTokens == 0 {
		return nil
	}
	categories := make([]*agentv1.PromptTokenBreakdownCategory, 0, 1)
	if usedTokens > 0 {
		categories = appendPromptTokenBreakdownCategory(categories, "conversation", "Conversation", int64(usedTokens))
	}
	return buildPromptTokenBreakdownSnapshot(categories, usedTokens, maxTokens)
}

func buildPromptTokenBreakdownSnapshot(categories []*agentv1.PromptTokenBreakdownCategory, usedTokens uint32, maxTokens uint32) *agentv1.PromptTokenBreakdownSnapshot {
	categoryTotal := int64(0)
	for _, category := range categories {
		categoryTotal += int64(category.GetEstimatedTokens())
	}
	totalUsedTokens := usedTokens
	if categoryTotal > int64(totalUsedTokens) {
		totalUsedTokens = clampInt64ToUint32(categoryTotal)
	}
	return &agentv1.PromptTokenBreakdownSnapshot{
		TotalUsedTokens: totalUsedTokens,
		MaxTokens:       maxTokens,
		Categories:      categories,
	}
}

func estimateMessageBreakdownTokens(messages []modeladapter.Message) (int64, int64, int64) {
	systemTokens := int64(0)
	summaryTokens := int64(0)
	conversationTokens := int64(0)
	for _, message := range messages {
		tokens := estimateModelMessageTokens(message)
		switch {
		case strings.TrimSpace(message.Role) == "system":
			systemTokens += tokens
		case isConversationSummaryMessage(message):
			summaryTokens += tokens
		default:
			conversationTokens += tokens
		}
	}
	return systemTokens, summaryTokens, conversationTokens
}

func isConversationSummaryMessage(message modeladapter.Message) bool {
	return strings.Contains(message.Content, "<conversation_summary>") || strings.Contains(message.Content, "</conversation_summary>")
}

func appendPromptTokenBreakdownCategory(categories []*agentv1.PromptTokenBreakdownCategory, id string, label string, estimatedTokens int64) []*agentv1.PromptTokenBreakdownCategory {
	if estimatedTokens <= 0 {
		return categories
	}
	return append(categories, &agentv1.PromptTokenBreakdownCategory{
		Id:              id,
		Label:           label,
		EstimatedTokens: clampInt64ToUint32(estimatedTokens),
	})
}

func clampInt64ToInt32(value int64) int32 {
	if value <= 0 {
		return 0
	}
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	return int32(value)
}
