package forwarder

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

// 超时后迟到的成功 ack 必须仍能解析出 blob key 并登记进会话级注册表：
// 旧实现超时会清空 pending 映射，迟到 ack 命中 unknown request 被丢弃，
// 注册表永远填不上，下个 checkpoint 全量重推 → 30MB 级风暴死循环。
func TestLateAcknowledgementAfterTimeoutRegistersBlob(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	stream.mu.Lock()
	requestIDs := make([]uint32, 0, len(stream.PendingCheckpointBlobWrites))
	for requestID := range stream.PendingCheckpointBlobWrites {
		requestIDs = append(requestIDs, requestID)
	}
	stream.mu.Unlock()
	if len(requestIDs) != len(projection.Blobs) {
		t.Fatalf("pending blob writes = %d, want %d", len(requestIDs), len(projection.Blobs))
	}

	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("handleCheckpointBlobTimeout() error = %v", err)
	}
	for _, requestID := range requestIDs {
		if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
			Id: requestID,
			Message: &agentv1.KvClientMessage_SetBlobResult{
				SetBlobResult: &agentv1.SetBlobResult{},
			},
		}); err != nil {
			t.Fatalf("late ACK %d error = %v", requestID, err)
		}
	}
	for _, blob := range projection.Blobs {
		if !service.conversationBlobAcked("conversation-1", string(blob.ID)) {
			t.Fatalf("迟到 ack 后 blob %x 未登记进会话注册表", blob.ID)
		}
	}

	eventsBefore := readCheckpointTestEvents(t, service, stream)
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("second queueCheckpointProjection() error = %v", err)
	}
	events := readCheckpointTestEvents(t, service, stream)
	checkpoints := 0
	for _, event := range events[len(eventsBefore):] {
		if event.Message.GetKvServerMessage().GetSetBlobArgs() != nil {
			t.Fatal("登记后的同流 checkpoint 不应再重发 blob")
		}
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("第二个 checkpoint 事件数 = %d, want 1", checkpoints)
	}
}

// 超时（ack 未到）后，同流下一个 checkpoint 不得重发已发送过的 blob：
// send-once per stream 是杀风暴的关键约束。
func TestCheckpointBlobsAreSentOncePerStreamEvenWithoutAcknowledgement(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	eventsFirst := readCheckpointTestEvents(t, service, stream)
	blobEvents := 0
	for _, event := range eventsFirst {
		if event.Message.GetKvServerMessage().GetSetBlobArgs() != nil {
			blobEvents++
		}
	}
	if blobEvents != len(projection.Blobs) {
		t.Fatalf("首轮 blob 事件 = %d, want %d", blobEvents, len(projection.Blobs))
	}

	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("handleCheckpointBlobTimeout() error = %v", err)
	}
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("second queueCheckpointProjection() error = %v", err)
	}
	events := readCheckpointTestEvents(t, service, stream)
	checkpoints := 0
	for _, event := range events[len(eventsFirst):] {
		if event.Message.GetKvServerMessage().GetSetBlobArgs() != nil {
			t.Fatal("超时后同流 checkpoint 不应重发已发送的 blob")
		}
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("超时后 checkpoint 事件数 = %d, want 1", checkpoints)
	}
}

// 已完结回合的 blob 编码缓存必须与全新投影器的全量投影逐字节一致，
// 包括 State、RootPromptMessagesJson 与 blob 列表的内容和顺序。
func TestCheckpointTurnCacheMatchesFreshProjection(t *testing.T) {
	projector := NewHistoryProjector()
	conversation := &ConversationFile{
		ConversationID:     "conversation-cache",
		RootConversationID: "conversation-cache",
		Mode:               "agent",
	}
	appendTestConversationTurn := func(requestID string, extraText string) {
		toolCallID := "call-" + requestID
		started := checkpointTestToolCallPayload(t, &agentv1.ToolCall{
			ToolCallId: &toolCallID,
			Tool: &agentv1.ToolCall_ReadToolCall{
				ReadToolCall: &agentv1.ReadToolCall{
					Args: &agentv1.ReadToolArgs{Path: "/tmp/" + requestID + ".txt"},
				},
			},
		})
		completed := checkpointTestToolCallPayload(t, &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_ReadToolCall{
				ReadToolCall: &agentv1.ReadToolCall{
					Result: &agentv1.ReadToolResult{
						Result: &agentv1.ReadToolResult_Success{
							Success: &agentv1.ReadToolSuccess{
								Path:   "/tmp/" + requestID + ".txt",
								Output: &agentv1.ReadToolSuccess_Content{Content: "contents " + requestID},
							},
						},
					},
				},
			},
		})
		userPayload, err := protojson.Marshal(&agentv1.UserMessage{Text: "prompt " + requestID, MessageId: requestID})
		if err != nil {
			t.Fatalf("marshal user message: %v", err)
		}
		entries := []HistoryEntry{
			{TurnSeq: conversation.NextTurnSeq, RequestID: requestID, Role: "user", Kind: "user_message", Payload: userPayload},
			newAssistantTextEntry(conversation.NextTurnSeq, requestID, "thinking about "+requestID, "", ""),
			newToolCallEntry(conversation.NextTurnSeq, requestID, toolCallID, "Read", "", "", started),
			newToolResultEntry(conversation.NextTurnSeq, requestID, toolCallID, "Read", `{"path":"/tmp/x"}`, "contents "+requestID, "", completed),
			newAssistantTextEntry(conversation.NextTurnSeq, requestID, "after "+requestID, "", ""),
		}
		if extraText != "" {
			entries = append(entries, newAssistantTextEntry(conversation.NextTurnSeq, requestID, extraText, "", ""))
		}
		appendEntriesInPlace(conversation, entries)
	}

	appendTestConversationTurn("request-1", "")
	projectAndCompareWithFresh := func(stage string) {
		t.Helper()
		cached, err := projector.ProjectCheckpointProjection(conversation)
		if err != nil {
			t.Fatalf("%s: cached projection error = %v", stage, err)
		}
		fresh, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
		if err != nil {
			t.Fatalf("%s: fresh projection error = %v", stage, err)
		}
		assertCheckpointProjectionsEqual(t, stage, cached, fresh)
	}
	projectAndCompareWithFresh("single turn")

	appendTestConversationTurn("request-2", "")
	projectAndCompareWithFresh("two turns")

	// 追加到“最后一个回合”之后再补一条，让前一回合走缓存命中路径。
	conversation.Entries = append(conversation.Entries, newAssistantTextEntry(conversation.NextTurnSeq-1, "request-2", "late follow-up", "", ""))
	projectAndCompareWithFresh("append into last turn")

	appendTestConversationTurn("request-3", "final answer")
	projectAndCompareWithFresh("three turns")
}

func assertCheckpointProjectionsEqual(t *testing.T, stage string, cached *CheckpointProjection, fresh *CheckpointProjection) {
	t.Helper()
	if !proto.Equal(cached.State, fresh.State) {
		t.Fatalf("%s: cached State 与全量投影不一致", stage)
	}
	cachedReplay := cached.State.GetRootPromptMessagesJson()
	freshReplay := fresh.State.GetRootPromptMessagesJson()
	if len(cachedReplay) != len(freshReplay) {
		t.Fatalf("%s: RootPromptMessagesJson 条数 %d != %d", stage, len(cachedReplay), len(freshReplay))
	}
	for index := range cachedReplay {
		if !bytes.Equal(cachedReplay[index], freshReplay[index]) {
			t.Fatalf("%s: RootPromptMessagesJson[%d] 不一致", stage, index)
		}
	}
	if len(cached.Blobs) != len(fresh.Blobs) {
		t.Fatalf("%s: blob 数 %d != %d", stage, len(cached.Blobs), len(fresh.Blobs))
	}
	for index := range cached.Blobs {
		if !bytes.Equal(cached.Blobs[index].ID, fresh.Blobs[index].ID) {
			t.Fatalf("%s: blob[%d] ID 不一致（顺序或内容漂移）", stage, index)
		}
		if !bytes.Equal(cached.Blobs[index].Data, fresh.Blobs[index].Data) {
			t.Fatalf("%s: blob[%d] Data 不一致", stage, index)
		}
	}
}

// checkpoint worker：终态发布阻塞等待并保持 checkpoint → turn_ended → End 顺序；
// 异步中间发布最终都会被处理（flush 后可见）。
func TestCheckpointWorkerKeepsTerminalOrdering(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		t.Fatalf("publishCheckpoint() error = %v", err)
	}
	service.flushCheckpointWork()

	completion := pendingTurnCompletion{
		RequestID: stream.RequestID,
		Usage:     turnUsageSnapshot{InputTokens: 5, OutputTokens: 3},
	}
	if err := service.publishCheckpointWithCompletion(stream.RequestID, stream.ConversationID, &completion); err != nil {
		t.Fatalf("terminal publishCheckpointWithCompletion() error = %v", err)
	}
	service.flushCheckpointWork()

	events := readCheckpointTestEvents(t, service, stream)
	checkpointIndex, turnEndedIndex, endIndex := -1, -1, -1
	for index, event := range events {
		switch {
		case event.Message.GetConversationCheckpointUpdate() != nil:
			checkpointIndex = index
		case event.Message.GetInteractionUpdate().GetTurnEnded() != nil:
			turnEndedIndex = index
		case event.End:
			endIndex = index
		}
	}
	if checkpointIndex < 0 || turnEndedIndex <= checkpointIndex || endIndex <= turnEndedIndex {
		t.Fatalf("terminal order checkpoint=%d turn_ended=%d end=%d", checkpointIndex, turnEndedIndex, endIndex)
	}
}
