package forwarder

import (
	"fmt"
	"path/filepath"
	"testing"

	"cursor/gen/agentv1"
)

// 长对话热路径的成本基线：每追加一条 entry 的耗时不应随历史长度线性增长，
// 否则整个回合就是 O(N^2)，表现为「越聊越卡」。
func BenchmarkAppendConversationEntriesByHistorySize(b *testing.B) {
	for _, historySize := range []int{50, 200, 800} {
		b.Run(fmt.Sprintf("history-%d", historySize), func(b *testing.B) {
			service, stream := benchmarkStreamWithHistory(b, historySize)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
					newAssistantTextEntry(stream.TurnSeq, stream.RequestID, "benchmark entry", "", ""),
				}); err != nil {
					b.Fatalf("appendConversationEntries() error = %v", err)
				}
			}
		})
	}
}

// syncSummaryCarryForward 在每个工具结果后都会跑一遍，且只更新可重建的 meta 字段。
// 它的代价需要和 appendConversationEntries 放在一起看，才能判断热路径的真实构成。
func BenchmarkSyncSummaryCarryForwardByHistorySize(b *testing.B) {
	for _, historySize := range []int{50, 200, 800} {
		b.Run(fmt.Sprintf("history-%d", historySize), func(b *testing.B) {
			service, stream := benchmarkStreamWithHistory(b, historySize)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, "model-call-1"); err != nil {
					b.Fatalf("syncSummaryCarryForward() error = %v", err)
				}
			}
		})
	}
}

// 隔离 fsync 的固定开销：durable 与非 durable 写同一份 payload，差值即每次 fsync 的代价。
// 这决定了「派生文件是否值得为持久性付费」这个判断是否成立。
func BenchmarkWriteJSONFileDurability(b *testing.B) {
	payload := map[string]any{"conversation_id": "conversation-1", "mode": "agent"}
	for _, variant := range []struct {
		name    string
		durable bool
	}{
		{name: "durable", durable: true},
		{name: "without-sync", durable: false},
	} {
		b.Run(variant.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "state.json")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := writeJSONFile(path, payload, variant.durable); err != nil {
					b.Fatalf("writeJSONFile() error = %v", err)
				}
			}
		})
	}
}

func benchmarkStreamWithHistory(b *testing.B, historySize int) (*Service, *ActiveStream) {
	b.Helper()
	broker := NewStreamBroker()
	service := &Service{
		store:     NewConversationFileStore(b.TempDir()),
		projector: NewHistoryProjector(),
		broker:    broker,
	}
	stream, err := broker.OpenStream(
		"request-1", "conversation-1", 1, "default", "default",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		b.Fatalf("OpenStream() error = %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          1,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
	}
	seed := make([]HistoryEntry, 0, historySize)
	for i := 0; i < historySize; i++ {
		seed = append(seed, newAssistantTextEntry(1, "request-1", "seeded history entry payload", "", ""))
	}
	appendEntriesInPlace(conversation, seed)
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		b.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	if _, _, err := service.store.AppendEntries("conversation-1", resetEntrySequences(seed)); err != nil {
		b.Fatalf("seed AppendEntries() error = %v", err)
	}
	return service, stream
}
