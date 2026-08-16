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

// 撤回重写历史后快照必须重基：System/Tools 沿用旧值，会话分类按撤回后的
// entries 重新估算，ContextVersion 对齐重编号后的 Seq，否则 UI 显示 0 或单一分类。
func TestRebasePromptTokenSnapshotAfterRewind(t *testing.T) {
	service := &Service{}
	stream := &ActiveStream{RequestID: "request-1", Mode: agentv1.AgentMode_AGENT_MODE_AGENT}
	stream.PromptTokens = promptTokenSnapshot{
		ContextVersion:     40,
		EntryCount:         40,
		SystemTokens:       3000,
		ToolsTokens:        15000,
		SummaryTokens:      800,
		ConversationTokens: 30000,
	}
	stream.HasPromptTokens = true

	rewound := promptTokenTestConversation(t, "kept question", "kept answer")

	service.rebasePromptTokenSnapshotAfterRewind(stream, rewound)

	snapshot, ok := checkpointPromptTokenEstimate(stream, rewound)
	if !ok {
		t.Fatal("重基后的快照必须继续可用于展示")
	}
	if snapshot.SystemTokens != 3000 || snapshot.ToolsTokens != 15000 {
		t.Fatalf("system/tools 应沿用旧值，实际 system=%d tools=%d", snapshot.SystemTokens, snapshot.ToolsTokens)
	}
	if snapshot.ConversationTokens <= 0 {
		t.Fatalf("conversation 分类应反映截断后的 entries，实际 %d", snapshot.ConversationTokens)
	}
	if snapshot.ContextVersion != int64(len(rewound.Entries)) {
		t.Fatalf("ContextVersion = %d, want %d", snapshot.ContextVersion, len(rewound.Entries))
	}
	breakdown := snapshot.breakdown(uint32(snapshot.totalTokens()), 1000000)
	if breakdown == nil || len(breakdown.GetCategories()) < 2 {
		t.Fatalf("重基后必须保留真实分类，实际 %+v", breakdown)
	}
}

// 撤回把 Seq 从 1 重新编号：即使条目数没有变少，版本倒退也必须作废旧快照，
// 否则 estimateEntriesTokensAfter 统计不到任何新增，显示值冻结在撤回之前。
func TestCheckpointPromptTokenEstimateInvalidatedByVersionBackwards(t *testing.T) {
	stream := &ActiveStream{RequestID: "request-1", Mode: agentv1.AgentMode_AGENT_MODE_AGENT}
	stream.PromptTokens = promptTokenSnapshot{
		ContextVersion:     100,
		EntryCount:         2,
		SystemTokens:       10,
		ToolsTokens:        10,
		ConversationTokens: 10,
	}
	stream.HasPromptTokens = true

	// 条目数 4 >= 旧快照 EntryCount 2（条目数守卫不会触发），但 Seq 只有 1..4。
	conversation := promptTokenTestConversation(t, "a", "b", "c", "d")
	if _, ok := checkpointPromptTokenEstimate(stream, conversation); ok {
		t.Fatal("版本倒退（Seq 重新编号）后快照必须作废")
	}
}
