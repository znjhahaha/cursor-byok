package forwarder

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

type countingPromptCompiler struct {
	compileCalls int
}

func (compiler *countingPromptCompiler) Compile(_ *ConversationFile, _ agentv1.AgentMode, _ string, _ string) (CompiledConversation, error) {
	compiler.compileCalls++
	return CompiledConversation{}, nil
}

func (compiler *countingPromptCompiler) DerivePromptContexts(_ *ConversationFile, _ agentv1.AgentMode, _ string) ([]PromptContextMessage, error) {
	return nil, nil
}

func promptTokenTestConversation(t *testing.T, payloads ...string) *ConversationFile {
	t.Helper()
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          1,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
	}
	entries := make([]HistoryEntry, 0, len(payloads))
	for _, payload := range payloads {
		entries = append(entries, newAssistantTextEntry(1, "request-1", payload, "", ""))
	}
	appendEntriesInPlace(conversation, entries)
	return conversation
}

// checkpoint 只是给 UI 显示一个 token 数，却曾经为此完整编译一遍对话，
// 代价与真正调用模型等价，而且每个工具结果都要付一次。
func TestCheckpointTokenDetailsDoesNotCompilePrompt(t *testing.T) {
	compiler := &countingPromptCompiler{}
	service := &Service{compiler: compiler}
	stream := &ActiveStream{RequestID: "request-1", Mode: agentv1.AgentMode_AGENT_MODE_AGENT}
	conversation := promptTokenTestConversation(t, "hello", "world")
	state := &agentv1.ConversationStateStructure{}

	service.rewriteCheckpointTokenDetailsForClient(stream, conversation, state)

	if compiler.compileCalls != 0 {
		t.Fatalf("展示路径不应触发编译，实际调用 %d 次", compiler.compileCalls)
	}
	if state.TokenDetails == nil {
		t.Fatal("expected token details to be populated")
	}
	if state.TokenDetails.GetMaxTokens() == 0 {
		t.Fatal("expected max tokens to be populated")
	}
}

// 分类之和必须严格等于原先的总量算法，否则「一次遍历同时得到总量和分类」这个
// 去重前提就不成立，展示值会漂移。
func TestPromptTokenSnapshotTotalMatchesCompiledEstimate(t *testing.T) {
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{
			{Role: "system", Content: "system prompt body"},
			{Role: "user", Content: "user question"},
			{Role: "assistant", Content: "<conversation_summary>earlier turns</conversation_summary>"},
			{Role: "tool", Content: "tool output", ToolCallID: "call-1", Name: "Shell"},
		},
		Tools: []json.RawMessage{json.RawMessage(`{"name":"Shell"}`), json.RawMessage(`{"name":"Read"}`)},
	}

	snapshot := newPromptTokenSnapshot(compiled, 7, 4)

	if got, want := snapshot.totalTokens(), estimateCompiledPromptTokens(compiled); got != want {
		t.Fatalf("snapshot.totalTokens() = %d, estimateCompiledPromptTokens() = %d", got, want)
	}
	if snapshot.SystemTokens <= 0 || snapshot.SummaryTokens <= 0 || snapshot.ConversationTokens <= 0 || snapshot.ToolsTokens <= 0 {
		t.Fatalf("expected every category to be populated, got %+v", snapshot)
	}
}

func TestCheckpointPromptTokenEstimateTracksHistoryGrowth(t *testing.T) {
	service := &Service{}
	stream := &ActiveStream{RequestID: "request-1"}
	conversation := promptTokenTestConversation(t, "first entry")
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{{Role: "user", Content: "hello"}},
		Tools:    []json.RawMessage{json.RawMessage(`{"name":"Shell"}`)},
	}

	recorded := service.recordPromptTokenSnapshot(stream, conversation, compiled)
	if recorded.totalTokens() <= 0 {
		t.Fatalf("expected a non-empty snapshot, got %+v", recorded)
	}

	unchanged, hasSnapshot := checkpointPromptTokenEstimate(stream, conversation)
	if !hasSnapshot {
		t.Fatal("expected the snapshot to be usable right after recording")
	}
	if unchanged.totalTokens() != recorded.totalTokens() {
		t.Fatalf("没有新增 entry 时展示值应保持一致：%d != %d", unchanged.totalTokens(), recorded.totalTokens())
	}

	appendEntriesInPlace(conversation, []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "a considerably longer follow up payload", "", ""),
	})
	grown, hasSnapshot := checkpointPromptTokenEstimate(stream, conversation)
	if !hasSnapshot {
		t.Fatal("expected the snapshot to survive an append")
	}
	if grown.totalTokens() <= recorded.totalTokens() {
		t.Fatalf("新增 entry 后展示值应增长：%d <= %d", grown.totalTokens(), recorded.totalTokens())
	}

	// 压缩会让 entries 变少，快照不再代表当前历史，必须作废而不是继续叠加增量。
	conversation.Entries = nil
	if _, stillValid := checkpointPromptTokenEstimate(stream, conversation); stillValid {
		t.Fatal("entries 减少后快照应当作废")
	}
}
