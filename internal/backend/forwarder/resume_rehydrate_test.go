package forwarder

import (
	"testing"
)

func newRehydrateTestService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		store:     NewConversationFileStore(t.TempDir()),
		projector: NewHistoryProjector(),
		broker:    NewStreamBroker(),
	}
}

// 启动/切 tab 时的空 resume 被降级后，必须回发 checkpoint，客户端才有内容渲染面板。
func TestIgnoredEmptyResumeRehydratesCheckpoint(t *testing.T) {
	service := newRehydrateTestService(t)
	if _, _, err := service.store.AppendEntries("conversation-1", []HistoryEntry{
		testCheckpointUserEntry(t),
		newAssistantTextEntry(1, "request-old", "previous answer", "", ""),
	}); err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}

	// RunSSE 先到达时 broker 里只有占位流，这正是启动场景。
	if _, _, err := service.broker.Subscribe("request-resume"); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	intent := InboundIntent{
		Kind:          "metadata",
		RequestID:     "request-resume",
		ConversationID: "conversation-1",
		IgnoredReason: ignoredReasonEmptyResume,
	}
	if err := service.handleMetadataIntent(intent); err != nil {
		t.Fatalf("handleMetadataIntent() error = %v", err)
	}
	// 中间 checkpoint 已改为异步发布（checkpoint worker），断言前先排空队列。
	service.flushCheckpointWork()

	events, err := service.broker.ReadFromCursor("request-resume", 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	checkpointCount := 0
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpointCount++
		}
	}
	if checkpointCount == 0 {
		t.Fatalf("降级的空 resume 必须回发 conversation_checkpoint_update，实际事件 %d 条", len(events))
	}

	stream, ok := service.broker.Get("request-resume")
	if !ok || stream == nil {
		t.Fatal("rehydrate 后流必须存在")
	}
	stream.mu.Lock()
	terminal := isTerminalStreamStatus(stream.Status)
	initialized := stream.CheckpointConversation != nil
	stream.mu.Unlock()
	if terminal {
		t.Fatal("rehydrate 不应终结流，客户端要继续接收后续回合")
	}
	if !initialized {
		t.Fatal("rehydrate 后流必须持有会话镜像")
	}
}

// 服务端没有历史（不存在的会话、或命名产生的 skeleton）时维持原行为：不发布任何内容。
func TestIgnoredEmptyResumeWithoutHistoryStaysSilent(t *testing.T) {
	service := newRehydrateTestService(t)
	if _, _, err := service.broker.Subscribe("request-resume"); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	intent := InboundIntent{
		Kind:          "metadata",
		RequestID:     "request-resume",
		ConversationID: "conversation-missing",
		IgnoredReason: ignoredReasonEmptyResume,
	}
	if err := service.handleMetadataIntent(intent); err != nil {
		t.Fatalf("handleMetadataIntent() error = %v", err)
	}

	events, err := service.broker.ReadFromCursor("request-resume", 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			t.Fatal("无历史时不应发布 checkpoint")
		}
	}
}
