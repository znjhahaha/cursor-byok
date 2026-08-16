package forwarder

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func newRewindTestUserMessageEntry(t *testing.T, turnSeq int64, seq int64, requestID string, messageID string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{MessageId: messageID, Text: "msg-" + messageID})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		Seq:       seq,
		RequestID: requestID,
		Role:      "user",
		Kind:      "user_message",
		Payload:   payload,
	}
}

func newRewindTestAssistantEntry(turnSeq int64, seq int64, requestID string) HistoryEntry {
	return HistoryEntry{
		TurnSeq:   turnSeq,
		Seq:       seq,
		RequestID: requestID,
		Role:      "assistant",
		Kind:      "assistant_text",
		Payload:   []byte(`{"text":"answer"}`),
	}
}

func newRewindTestIntent(t *testing.T, messageID string, turns [][]byte) InboundIntent {
	t.Helper()
	return InboundIntent{
		Kind:      "run",
		RequestID: "request-new",
		UserMessage: &agentv1.UserMessage{
			MessageId: messageID,
			Text:      "edited message",
		},
		ClientMessage: &agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_ConversationAction{
				ConversationAction: &agentv1.ConversationAction{
					Action: &agentv1.ConversationAction_UserMessageAction{
						UserMessageAction: &agentv1.UserMessageAction{},
					},
				},
			},
		},
		ConversationState: &agentv1.ConversationStateStructure{Turns: turns},
	}
}

func newRewindTestTurnBlob(t *testing.T, requestID string) []byte {
	t.Helper()
	wrapper := &agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
				RequestId: proto.String(requestID),
			},
		},
	}
	raw, err := proto.Marshal(wrapper)
	if err != nil {
		t.Fatalf("marshal turn: %v", err)
	}
	return raw
}

// 撤回后重发新 message_id 时，按 ConversationState 回合 request_id 对齐推断撤回点。
func TestDecideRunRewindConversationStateAlignmentFallback(t *testing.T) {
	service := &Service{}
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Entries: []HistoryEntry{
			newRewindTestUserMessageEntry(t, 1, 1, "request-a", "msg-1"),
			newRewindTestAssistantEntry(1, 2, "request-a"),
			newRewindTestUserMessageEntry(t, 2, 3, "request-b", "msg-2"),
			newRewindTestAssistantEntry(2, 4, "request-b"),
			newRewindTestUserMessageEntry(t, 3, 5, "request-c", "msg-3"),
			newRewindTestAssistantEntry(3, 6, "request-c"),
		},
	}
	// 客户端撤回到第 2 回合后用新 message_id 重发：turns 只剩 2 项，
	// 最后一项 request_id 仍指向服务端第 2 回合。
	intent := newRewindTestIntent(t, "msg-3-edited", [][]byte{
		newRewindTestTurnBlob(t, "request-a"),
		newRewindTestTurnBlob(t, "request-b"),
	})

	decision := service.decideRunRewind(intent, conversation)
	if !decision.Apply {
		t.Fatalf("decision.Apply = false, skip_reason=%q", decision.SkipReason)
	}
	if decision.TargetTurnSeq != 3 {
		t.Fatalf("TargetTurnSeq = %d, want 3", decision.TargetTurnSeq)
	}
	if decision.Reason != "conversation_state_aligned" {
		t.Fatalf("Reason = %q, want conversation_state_aligned", decision.Reason)
	}
	if len(decision.PrefixEntries) != 4 {
		t.Fatalf("PrefixEntries = %d, want 4 (turns 1-2 only)", len(decision.PrefixEntries))
	}
}

// 正常新回合（客户端回合数 == 服务端尾部）不能触发对齐撤回。
func TestDecideRunRewindConversationStateAlignmentSkipsNewTurn(t *testing.T) {
	service := &Service{}
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Entries: []HistoryEntry{
			newRewindTestUserMessageEntry(t, 1, 1, "request-a", "msg-1"),
			newRewindTestAssistantEntry(1, 2, "request-a"),
			newRewindTestUserMessageEntry(t, 2, 3, "request-b", "msg-2"),
			newRewindTestAssistantEntry(2, 4, "request-b"),
		},
	}
	intent := newRewindTestIntent(t, "msg-3", [][]byte{
		newRewindTestTurnBlob(t, "request-a"),
		newRewindTestTurnBlob(t, "request-b"),
	})

	decision := service.decideRunRewind(intent, conversation)
	if decision.Apply {
		t.Fatalf("decision.Apply = true, want false for a normal new turn")
	}
	if decision.SkipReason != "message_id_not_found" {
		t.Fatalf("SkipReason = %q, want message_id_not_found", decision.SkipReason)
	}
}

// messageId 出现多份（重发痕迹）时选最早副本，避免旧回合上下文残留。
func TestSelectRunRewindMatchPrefersEarliestDuplicate(t *testing.T) {
	matches := []runRewindMatch{
		{Entry: HistoryEntry{TurnSeq: 6, Seq: 9, Kind: "user_message"}},
		{Entry: HistoryEntry{TurnSeq: 2, Seq: 3, Kind: "user_message"}},
	}
	selected, reason := selectRunRewindMatch(matches, 5, true)
	if selected.Entry.TurnSeq != 2 {
		t.Fatalf("selected TurnSeq = %d, want 2", selected.Entry.TurnSeq)
	}
	if reason != "earliest_duplicate_before_client_turn_count" {
		t.Fatalf("reason = %q, want earliest_duplicate_before_client_turn_count", reason)
	}
}

// 导入历史没有 TurnSeq 时按 Seq 位置截断，而不是放弃撤回。
func TestDecideRunRewindPositionalFallbackForZeroTurnSeq(t *testing.T) {
	service := &Service{}
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Entries: []HistoryEntry{
			newRewindTestUserMessageEntry(t, 0, 1, "request-a", "msg-1"),
			newRewindTestAssistantEntry(0, 2, "request-a"),
			newRewindTestUserMessageEntry(t, 0, 3, "request-b", "msg-2"),
			newRewindTestAssistantEntry(0, 4, "request-b"),
		},
	}
	// 撤回 msg-2：客户端回合里已看不到第 2 回合。
	intent := newRewindTestIntent(t, "msg-2", [][]byte{
		newRewindTestTurnBlob(t, "request-a"),
	})

	decision := service.decideRunRewind(intent, conversation)
	if !decision.Apply {
		t.Fatalf("decision.Apply = false, skip_reason=%q", decision.SkipReason)
	}
	if decision.TargetTurnSeq != 1 {
		t.Fatalf("TargetTurnSeq = %d, want 1", decision.TargetTurnSeq)
	}
	if len(decision.PrefixEntries) != 2 {
		t.Fatalf("PrefixEntries = %d, want 2", len(decision.PrefixEntries))
	}
}

// 终态或已被轮转取代的流不再允许恢复类回调写历史，防止撤回后回灌。
func TestStaleRecoveryAppendBlocksTerminalAndSupersededStreams(t *testing.T) {
	service := &Service{broker: NewStreamBroker()}
	stream, err := service.broker.OpenStream("request-1", "conversation-1", 1, "", "", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if service.staleRecoveryAppend(stream) {
		t.Fatal("active stream must not be stale")
	}

	if err := service.broker.Complete("request-1", "", ""); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if !service.staleRecoveryAppend(stream) {
		t.Fatal("terminal stream must be stale")
	}

	// 模拟同 request_id 轮转出新流：旧流对象从此被视为已取代。
	service.broker.mu.Lock()
	service.broker.streams["request-1"] = &ActiveStream{RequestID: "request-1", Status: StreamStatusCreated}
	service.broker.mu.Unlock()
	if !service.staleRecoveryAppend(stream) {
		t.Fatal("superseded stream must be stale")
	}
}
