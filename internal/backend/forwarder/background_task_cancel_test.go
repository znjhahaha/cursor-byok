package forwarder

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func TestBackgroundTaskStaysActiveAfterParentGenerationCompletes(t *testing.T) {
	service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
	parent, err := service.broker.OpenStream("parent-request", "parent-conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_MULTITASK, "parent")
	if err != nil {
		t.Fatalf("OpenStream(parent) error = %v", err)
	}
	parent.mu.Lock()
	parent.Status = StreamStatusStreaming
	parent.Phase = TurnPhaseProviderRunning
	parent.mu.Unlock()
	task := registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	if err := service.broker.Complete("parent-request", "", ""); err != nil {
		t.Fatalf("complete parent generation error = %v", err)
	}
	parent.mu.Lock()
	parentStatus := parent.Status
	parent.mu.Unlock()
	if parentStatus != StreamStatusCompleted {
		t.Fatalf("parent generation status = %q, want completed", parentStatus)
	}
	active, err := service.backgroundTasks.ActiveTasks("parent-conversation")
	if err != nil {
		t.Fatalf("ActiveTasks() error = %v", err)
	}
	if len(active) != 1 || active[0].ID != task.ID || active[0].Status != BackgroundTaskStatusRunning {
		t.Fatalf("active tasks = %#v, want one running child", active)
	}
	if pending, err := service.backgroundTasks.PendingCompletions("parent-conversation"); err != nil || len(pending) != 0 {
		t.Fatalf("pending completions = %#v, err=%v; active child must not be terminal", pending, err)
	}
}

func TestCancelSubagentActionWithoutTargetIsRejected(t *testing.T) {
	service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_CancelSubagentAction{
					CancelSubagentAction: &agentv1.CancelSubagentAction{},
				},
			},
		},
	}
	if _, err := service.decodeInboundIntent("cancel-request", message, "conversation_action"); err == nil {
		t.Fatal("empty cancel_subagent_action target was accepted")
	}
}

func TestTransportCancelDoesNotCancelBackgroundTaskBusinessState(t *testing.T) {
	service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
	task := registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	if _, _, err := service.backgroundTasks.MarkRunning("parent-request", "task-call", "agent-1"); err != nil {
		t.Fatalf("MarkRunning() error = %v", err)
	}
	stream, err := service.broker.OpenStream("child-request", "child-conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_MULTITASK, "child")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	canceled := false
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderActive = true
	stream.ProviderCancel = func() { canceled = true }
	stream.mu.Unlock()
	if err := service.handleCancelIntent(InboundIntent{Kind: "cancel", RequestID: "child-request", CancelReason: "[canceled] Superseded by newer request"}); err != nil {
		t.Fatalf("transport cancel error = %v", err)
	}
	if !canceled {
		t.Fatal("transport cancel did not invoke child provider cancellation")
	}
	document, err := readBackgroundTaskFileDocument(service.backgroundTasks.path)
	if err != nil {
		t.Fatalf("read task ledger error = %v", err)
	}
	if got := document.Tasks[task.ID].Status; got != BackgroundTaskStatusRunning {
		t.Fatalf("task status after transport cancel = %q, want running", got)
	}
}

func TestCancelSubagentActionCancelsOnlyMatchedChildAndSuppressesLateCompletion(t *testing.T) {
	service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
	seedTaskResultInParentHistory(t, service, "parent-conversation", "parent-request", "task-call")
	first := registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request-1", "child-conversation-1")
	if _, _, err := service.backgroundTasks.MarkRunning("parent-request", "task-call", "agent-1"); err != nil {
		t.Fatalf("MarkRunning(first) error = %v", err)
	}
	second := registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call-2", "child-request-2", "child-conversation-2")
	if _, _, err := service.backgroundTasks.MarkRunning("parent-request", "task-call-2", "agent-2"); err != nil {
		t.Fatalf("MarkRunning(second) error = %v", err)
	}
	firstStream := openCancelableChildStream(t, service, first.ChildRequestID)
	secondStream := openCancelableChildStream(t, service, second.ChildRequestID)

	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_CancelSubagentAction{
					CancelSubagentAction: &agentv1.CancelSubagentAction{SubagentId: "agent-1"},
				},
			},
		},
	}
	intent, err := service.decodeInboundIntent("cancel-request", message, "conversation_action")
	if err != nil {
		t.Fatalf("decode cancel subagent intent error = %v", err)
	}
	if intent.Kind != "cancel_subagent" || intent.CancelSubagentID != "agent-1" {
		t.Fatalf("decoded cancel intent = %#v", intent)
	}
	if err := service.dispatchInboundIntent(intent); err != nil {
		t.Fatalf("dispatch cancel subagent error = %v", err)
	}
	assertStreamCanceled(t, firstStream)
	assertStreamStillActive(t, secondStream)

	document, err := readBackgroundTaskFileDocument(service.backgroundTasks.path)
	if err != nil {
		t.Fatalf("read task ledger error = %v", err)
	}
	if got := document.Tasks[first.ID].Status; got != BackgroundTaskStatusCanceled {
		t.Fatalf("first task status = %q, want canceled", got)
	}
	if got := document.Tasks[second.ID].Status; got != BackgroundTaskStatusRunning {
		t.Fatalf("second task status = %q, want running", got)
	}
	if pending, err := service.backgroundTasks.PendingCompletions("parent-conversation"); err != nil || len(pending) != 0 {
		t.Fatalf("canceled task left pending completion: %#v err=%v", pending, err)
	}
	if _, changed, err := service.backgroundTasks.CompleteChild("child-conversation-1", BackgroundTaskStatusCompleted, "late success", ""); err != nil || changed {
		t.Fatalf("late success changed canceled task: changed=%v err=%v", changed, err)
	}
	if err := service.handleCancelSubagentIntent(InboundIntent{
		Kind:             "cancel_subagent",
		CancelSubagentID: "agent-1",
		CancelReason:     "different retry reason",
	}); err != nil {
		t.Fatalf("repeated cancel subagent error = %v", err)
	}

	conversation, err := service.store.LoadConversation("parent-conversation")
	if err != nil {
		t.Fatalf("load parent conversation error = %v", err)
	}
	if got := canceledTaskResultError(t, conversation, "task-call"); got != "Background subagent was canceled by the user." {
		t.Fatalf("canceled Task result = %q, want user cancellation reason", got)
	}
}

func openCancelableChildStream(t *testing.T, service *Service, requestID string) *ActiveStream {
	t.Helper()
	stream, err := service.broker.OpenStream(requestID, "conversation-"+requestID, 1, "model", "model", agentv1.AgentMode_AGENT_MODE_MULTITASK, "child")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderActive = true
	stream.ProviderCancel = func() {}
	stream.mu.Unlock()
	return stream
}

func assertStreamCanceled(t *testing.T, stream *ActiveStream) {
	t.Helper()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Status != StreamStatusCanceled || stream.Phase != TurnPhaseCanceled || stream.ProviderActive {
		t.Fatalf("stream = status:%s phase:%s provider_active:%v, want canceled", stream.Status, stream.Phase, stream.ProviderActive)
	}
}

func assertStreamStillActive(t *testing.T, stream *ActiveStream) {
	t.Helper()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Status == StreamStatusCanceled || stream.Phase == TurnPhaseCanceled {
		t.Fatalf("unmatched child was canceled: status=%s phase=%s", stream.Status, stream.Phase)
	}
}

func seedTaskResultInParentHistory(t *testing.T, service *Service, conversationID string, requestID string, toolCallID string) {
	t.Helper()
	if _, err := service.store.LoadConversation(conversationID); err != nil {
		t.Fatalf("load parent before Task history error = %v", err)
	}
	args := &agentv1.TaskArgs{Description: "inspect code", Prompt: "inspect", Mode: agentv1.TaskMode_TASK_MODE_PLAN}
	completed := &agentv1.ToolCall{Tool: &agentv1.ToolCall_TaskToolCall{TaskToolCall: &agentv1.TaskToolCall{
		Args: args,
		Result: &agentv1.TaskResult{Result: &agentv1.TaskResult_Success{Success: &agentv1.TaskSuccess{
			AgentId:      stringPtr("agent-1"),
			IsBackground: true,
		}}},
	}}}
	payload, err := protojson.Marshal(completed)
	if err != nil {
		t.Fatalf("marshal Task ToolCall error = %v", err)
	}
	entries := []HistoryEntry{
		newToolCallEntry(1, requestID, toolCallID, "Task", "", "", payload),
		newToolResultEntry(1, requestID, toolCallID, "Task", "{}", `{"success":{"is_background":true}}`, "", payload),
	}
	if _, _, err := service.store.AppendEntries(conversationID, entries); err != nil {
		t.Fatalf("append Task history error = %v", err)
	}
}

func canceledTaskResultError(t *testing.T, conversation *ConversationFile, toolCallID string) string {
	t.Helper()
	for _, entry := range conversation.Entries {
		if entry.Kind != "tool_result" || entry.ToolCallID != toolCallID {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode Task result payload error = %v", err)
		}
		toolCall := &agentv1.ToolCall{}
		if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
			t.Fatalf("decode Task ToolCall error = %v", err)
		}
		return toolCall.GetTaskToolCall().GetResult().GetError().GetError()
	}
	return ""
}

func TestBackgroundTaskCancelSubagentActionIsIdempotent(t *testing.T) {
	service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
	task := registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	if _, _, err := service.backgroundTasks.MarkRunning("parent-request", "task-call", "agent-1"); err != nil {
		t.Fatalf("MarkRunning() error = %v", err)
	}
	first, found, changed, err := service.backgroundTasks.CancelSubagent("agent-1", "stop")
	if err != nil || !found || !changed || first.Status != BackgroundTaskStatusCanceled {
		t.Fatalf("first cancel task=%#v found=%v changed=%v err=%v", first, found, changed, err)
	}
	second, found, changed, err := service.backgroundTasks.CancelSubagent("agent-1", "stop again")
	if err != nil || !found || changed || second.Status != BackgroundTaskStatusCanceled {
		t.Fatalf("second cancel task=%#v found=%v changed=%v err=%v", second, found, changed, err)
	}
	if task.ID != first.ID {
		t.Fatalf("canceled task ID=%q, want %q", first.ID, task.ID)
	}
}

func TestBackgroundTaskCancelSubagentActionResolvesDetachedOwnershipFallbacks(t *testing.T) {
	for _, targetKind := range []string{"child_conversation", "child_request", "task_id"} {
		t.Run(targetKind, func(t *testing.T) {
			service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
			task := registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
			targetID := map[string]string{
				"child_conversation": task.ChildConversationID,
				"child_request":      task.ChildRequestID,
				"task_id":            task.ID,
			}[targetKind]
			canceled, found, changed, err := service.backgroundTasks.CancelSubagent(targetID, "stop")
			if err != nil || !found || !changed || canceled.ID != task.ID || canceled.Status != BackgroundTaskStatusCanceled {
				t.Fatalf("CancelSubagent(%q) task=%#v found=%v changed=%v err=%v", targetID, canceled, found, changed, err)
			}
		})
	}
}

func TestBackgroundTaskCancelSubagentActionDoesNotCancelParentGeneration(t *testing.T) {
	service := newBackgroundCompletionTestService(t, &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)})
	registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	parent, err := service.broker.OpenStream("parent-request", "parent-conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_MULTITASK, "parent")
	if err != nil {
		t.Fatalf("OpenStream(parent) error = %v", err)
	}
	parent.mu.Lock()
	parent.Status = StreamStatusStreaming
	parent.Phase = TurnPhaseProviderRunning
	parent.ProviderActive = true
	parent.ProviderCancel = func() { t.Fatal("parent provider was canceled") }
	parent.mu.Unlock()
	if err := service.handleCancelSubagentIntent(InboundIntent{Kind: "cancel_subagent", CancelSubagentID: "unknown-agent"}); err != nil {
		t.Fatalf("unknown cancel error = %v", err)
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.Status == StreamStatusCanceled || parent.Phase == TurnPhaseCanceled {
		t.Fatal("unknown subagent cancellation canceled parent generation")
	}
}
