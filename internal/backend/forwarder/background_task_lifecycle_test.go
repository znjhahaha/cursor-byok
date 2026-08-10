package forwarder

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func seedBackgroundTaskParent(t *testing.T, service *Service, conversationID string) {
	t.Helper()
	entries, err := buildRunEntries(InboundIntent{
		RequestID:      "parent-request",
		ConversationID: conversationID,
		ModelID:        "parent-model",
		ModelName:      "Parent Model",
		ThinkingEffort: "high",
		UserMessage: &agentv1.UserMessage{
			MessageId: "parent-message",
			Text:      "delegate work",
		},
	}, agentv1.AgentMode_AGENT_MODE_MULTITASK, 1)
	if err != nil {
		t.Fatalf("buildRunEntries() error = %v", err)
	}
	_, err = service.store.SaveConversationWithEntries(conversationID, &ConversationFile{
		ConversationID:     conversationID,
		RootConversationID: conversationID,
		Mode:               "multitask",
	}, entries)
	if err != nil {
		t.Fatalf("seed parent conversation error = %v", err)
	}
}

func registerLinkedBackgroundTask(t *testing.T, service *Service, parentConversationID string, parentRequestID string, toolCallID string, childRequestID string, childConversationID string) BackgroundSubagentTask {
	t.Helper()
	_, err := service.backgroundTasks.Register(BackgroundSubagentTask{
		ParentConversationID: parentConversationID,
		ParentRequestID:      parentRequestID,
		RootParentRequestID:  parentRequestID,
		ParentToolCallID:     toolCallID,
		SubagentTypeName:     "explore",
		Description:          "inspect code",
		Status:               BackgroundTaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := service.observeBackgroundTaskRequest(childRequestID, backgroundTaskRequestMetadata{
		ParentRequestID:     parentRequestID,
		RootParentRequestID: parentRequestID,
		ParentToolCallID:    toolCallID,
		DirectMeta:          true,
	}); err != nil {
		t.Fatalf("observeBackgroundTaskRequest() error = %v", err)
	}
	if err := service.bindBackgroundTaskConversation(InboundIntent{
		RequestID:        childRequestID,
		ConversationID:   childConversationID,
		SubagentTypeName: "explore",
	}); err != nil {
		t.Fatalf("bindBackgroundTaskConversation() error = %v", err)
	}
	document, err := readBackgroundTaskFileDocument(service.backgroundTasks.path)
	if err != nil {
		t.Fatalf("read ledger error = %v", err)
	}
	task, ok := document.Tasks[backgroundTaskID(parentRequestID, toolCallID)]
	if !ok {
		t.Fatal("linked task is missing from ledger")
	}
	return task
}

func waitForNoPendingBackgroundCompletions(t *testing.T, service *Service, parentConversationID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := service.backgroundTasks.PendingCompletions(parentConversationID)
		if err != nil {
			t.Fatalf("PendingCompletions() error = %v", err)
		}
		if len(tasks) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background completion ledger was not marked as injected")
}

func TestBackgroundTaskRequestMetadataUsesOfficialDirectHeaders(t *testing.T) {
	header := http.Header{}
	header.Set(backgroundTaskParentRequestHeader, " parent-request ")
	header.Set(backgroundTaskRootRequestHeader, " root-request ")
	header.Set(backgroundTaskParentToolCallHeader, " task-call ")
	header.Set(backgroundTaskDirectMetaHeader, "TRUE")

	metadata := readBackgroundTaskRequestMetadata(header)
	if metadata.ParentRequestID != "parent-request" || metadata.RootParentRequestID != "root-request" || metadata.ParentToolCallID != "task-call" || !metadata.DirectMeta {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestNestedBackgroundTaskPreservesRootRequestChain(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	stream, err := service.broker.OpenStream(
		"child-request", "child-conversation", 1, "parent-model", "Parent Model",
		agentv1.AgentMode_AGENT_MODE_MULTITASK, "nested task",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	stream.mu.Lock()
	stream.RequestMetadata = backgroundTaskRequestMetadata{
		ParentRequestID:     "outer-parent-request",
		RootParentRequestID: "root-request",
		ParentToolCallID:    "outer-task-call",
		DirectMeta:          true,
	}
	stream.mu.Unlock()
	invocation := runtimecore.ToolInvocation{
		CallID:      "nested-task-call",
		ToolName:    "Task",
		ModelCallID: "nested-model-call",
		ArgsJSON:    []byte(`{"subagent_type":"explore","prompt":"inspect nested dependency","readonly":true}`),
	}
	message, pending, err := execbridge.NewBridge().OpenExec(execbridge.OpenExecContext{
		ConversationID:     "child-conversation",
		RootConversationID: "root-conversation",
		ModelID:            "parent-model",
		Mode:               agentv1.AgentMode_AGENT_MODE_MULTITASK,
	}, invocation)
	if err != nil {
		t.Fatalf("OpenExec() error = %v", err)
	}
	if err := service.registerBackgroundSubagentTask(stream, pending, invocation, message); err != nil {
		t.Fatalf("registerBackgroundSubagentTask() error = %v", err)
	}
	document, err := readBackgroundTaskFileDocument(service.backgroundTasks.path)
	if err != nil {
		t.Fatalf("read ledger error = %v", err)
	}
	task := document.Tasks[backgroundTaskID("child-request", "nested-task-call")]
	if task.RootParentRequestID != "root-request" {
		t.Fatalf("nested root parent request = %q, want root-request", task.RootParentRequestID)
	}
}

func TestBackgroundTaskDirectMetadataBindsAcrossRequestOrderAndRestart(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		restart bool
	}{
		{name: "BidiAppend then RunSSE"},
		{name: "RunSSE then BidiAppend after restart", restart: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
			service := newBackgroundCompletionTestServiceAtRoot(root, provider)
			seedBackgroundTaskParent(t, service, "parent-conversation")
			if _, err := service.backgroundTasks.Register(BackgroundSubagentTask{
				ParentConversationID: "parent-conversation",
				ParentRequestID:      "parent-request",
				ParentToolCallID:     "task-call",
				Status:               BackgroundTaskStatusAccepted,
			}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			metadata := backgroundTaskRequestMetadata{
				ParentRequestID:     "parent-request",
				RootParentRequestID: "root-request",
				ParentToolCallID:    "task-call",
				DirectMeta:          true,
			}
			if err := service.observeBackgroundTaskRequest("child-request", metadata); err != nil {
				t.Fatalf("first request observation error = %v", err)
			}
			if testCase.restart {
				service = newBackgroundCompletionTestServiceAtRoot(root, provider)
			}
			if err := service.bindBackgroundTaskConversation(InboundIntent{
				RequestID:        "child-request",
				ConversationID:   "child-conversation",
				SubagentTypeName: "explore",
			}); err != nil {
				t.Fatalf("BidiAppend binding error = %v", err)
			}
			if err := service.observeBackgroundTaskRequest("child-request", metadata); err != nil {
				t.Fatalf("second request observation error = %v", err)
			}
			document, err := readBackgroundTaskFileDocument(service.backgroundTasks.path)
			if err != nil {
				t.Fatalf("read ledger error = %v", err)
			}
			task := document.Tasks[backgroundTaskID("parent-request", "task-call")]
			if task.ChildRequestID != "child-request" || task.ChildConversationID != "child-conversation" || task.Status != BackgroundTaskStatusRunning {
				t.Fatalf("linked task = %#v", task)
			}
		})
	}
}

func TestBackgroundTaskCompletionRequiresClientBoundContinuation(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 2)}
	service := newBackgroundCompletionTestService(t, provider)
	seedBackgroundTaskParent(t, service, "parent-conversation")
	registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")

	service.completeBackgroundSubagentSuccess("child-conversation", "real provider final message")
	select {
	case request := <-provider.seen:
		t.Fatalf("child completion started an unsubscribed continuation: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}
	service.startBackgroundTaskRecovery()
	select {
	case request := <-provider.seen:
		t.Fatalf("startup recovery started an unsubscribed continuation: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}

	pending, err := service.backgroundTasks.PendingCompletions("parent-conversation")
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingCompletions() tasks=%#v err=%v", pending, err)
	}
	completion := backgroundTaskCompletionFromLedger(pending[0])
	completion.Detail = stringPtr("poison client detail")
	if err := service.dispatchInboundIntent(InboundIntent{
		Kind:                      "run",
		RequestID:                 "client-continuation",
		ConversationID:            "parent-conversation",
		ModelID:                   "default",
		Mode:                      agentv1.AgentMode_AGENT_MODE_AGENT,
		BackgroundTaskCompletions: []*agentv1.BackgroundTaskCompletion{completion},
		StartsRun:                 true,
	}); err != nil {
		t.Fatalf("client continuation error = %v", err)
	}
	select {
	case request := <-provider.seen:
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "real provider final message") || strings.Contains(joined, "poison client detail") {
			t.Fatalf("client-bound continuation messages = %q", joined)
		}
		if request.RequestID != "client-continuation" {
			t.Fatalf("provider request_id = %q, want client-continuation", request.RequestID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client-bound continuation was not started")
	}
	waitForBackgroundCompletionProviderIdle(t, service, "client-continuation")
	waitForBackgroundCompletionTerminal(t, service, "client-continuation")
	waitForNoPendingBackgroundCompletions(t, service, "parent-conversation")
}

func TestBackgroundTaskRecoveryReleasesClaimWithoutStartingProvider(t *testing.T) {
	root, err := os.MkdirTemp("", "background-task-recovery")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 2)}
	firstService := newBackgroundCompletionTestServiceAtRoot(root, provider)
	seedBackgroundTaskParent(t, firstService, "parent-conversation")
	registerLinkedBackgroundTask(t, firstService, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	completed, changed, err := firstService.backgroundTasks.CompleteChild("child-conversation", BackgroundTaskStatusCompleted, "persisted final message", "")
	if err != nil || !changed {
		t.Fatalf("CompleteChild() task=%#v changed=%v err=%v", completed, changed, err)
	}
	completion := backgroundTaskCompletionFromLedger(completed)
	claimed, claimedCount, err := firstService.backgroundTasks.ClaimCompletions(
		"parent-conversation",
		[]*agentv1.BackgroundTaskCompletion{completion},
		"crashed-continuation",
	)
	if err != nil || claimedCount != 1 || len(claimed) != 1 {
		t.Fatalf("ClaimCompletions() claimed=%#v count=%d err=%v", claimed, claimedCount, err)
	}
	entries, err := buildRunEntries(InboundIntent{
		RequestID:                 "crashed-continuation",
		ConversationID:            "parent-conversation",
		ModelID:                   "parent-model",
		BackgroundTaskCompletions: claimed,
	}, agentv1.AgentMode_AGENT_MODE_MULTITASK, 2)
	if err != nil {
		t.Fatalf("build recovery entries error = %v", err)
	}
	conversation, err := firstService.store.LoadConversation("parent-conversation")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if _, err := firstService.store.SaveConversationWithEntries("parent-conversation", conversation, entries); err != nil {
		t.Fatalf("persist pre-crash completion error = %v", err)
	}

	recoveredService := newBackgroundCompletionTestServiceAtRoot(root, provider)
	recoveredService.startBackgroundTaskRecovery()
	select {
	case request := <-provider.seen:
		t.Fatalf("recovery started a headless provider request: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}
	pending, err := recoveredService.backgroundTasks.PendingCompletions("parent-conversation")
	if err != nil || len(pending) != 1 {
		t.Fatalf("recovered pending tasks=%#v err=%v", pending, err)
	}
	if pending[0].CompletionContinuationID != "" || !pending[0].CompletionInjectedAt.IsZero() {
		t.Fatalf("recovered task retained interrupted claim: %#v", pending[0])
	}

	if err := recoveredService.dispatchInboundIntent(InboundIntent{
		Kind:                      "run",
		RequestID:                 "retry-continuation",
		ConversationID:            "parent-conversation",
		ModelID:                   "default",
		Mode:                      agentv1.AgentMode_AGENT_MODE_AGENT,
		BackgroundTaskCompletions: []*agentv1.BackgroundTaskCompletion{completion},
		StartsRun:                 true,
	}); err != nil {
		t.Fatalf("retry continuation error = %v", err)
	}
	select {
	case request := <-provider.seen:
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "persisted final message") {
			t.Fatalf("retry provider messages = %q", joined)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client retry did not start provider")
	}
	waitForBackgroundCompletionProviderIdle(t, recoveredService, "retry-continuation")
	waitForBackgroundCompletionTerminal(t, recoveredService, "retry-continuation")
	waitForNoPendingBackgroundCompletions(t, recoveredService, "parent-conversation")
	conversation, err = recoveredService.store.LoadConversation("parent-conversation")
	if err != nil {
		t.Fatalf("reload parent error = %v", err)
	}
	if got := len(backgroundCompletionUserMessages(conversation.Entries)); got != 1 {
		t.Fatalf("completion history count = %d, want 1", got)
	}
}

func TestBackgroundTaskRecoveryConfirmsPersistedSuccessfulContinuationWithoutProvider(t *testing.T) {
	root := t.TempDir()
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	firstService := newBackgroundCompletionTestServiceAtRoot(root, provider)
	seedBackgroundTaskParent(t, firstService, "parent-conversation")
	registerLinkedBackgroundTask(t, firstService, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	completed, changed, err := firstService.backgroundTasks.CompleteChild("child-conversation", BackgroundTaskStatusCompleted, "persisted successful result", "")
	if err != nil || !changed {
		t.Fatalf("CompleteChild() changed=%v err=%v", changed, err)
	}
	if _, claimedCount, err := firstService.backgroundTasks.ClaimCompletions(
		"parent-conversation",
		[]*agentv1.BackgroundTaskCompletion{backgroundTaskCompletionFromLedger(completed)},
		"completed-continuation",
	); err != nil || claimedCount != 1 {
		t.Fatalf("ClaimCompletions() count=%d err=%v", claimedCount, err)
	}
	conversation, err := firstService.store.LoadConversation("parent-conversation")
	if err != nil {
		t.Fatalf("load parent error = %v", err)
	}
	confirmation := newMetadataEntry(2, "completed-continuation", "background_completion_confirmed", map[string]any{
		"continuation_request_id": "completed-continuation",
	})
	confirmation.IdempotencyKey = "background-completion-confirmed:completed-continuation"
	if _, err := firstService.store.SaveConversationWithEntries("parent-conversation", conversation, []HistoryEntry{confirmation}); err != nil {
		t.Fatalf("persist completion confirmation error = %v", err)
	}

	recoveredService := newBackgroundCompletionTestServiceAtRoot(root, provider)
	recoveredService.startBackgroundTaskRecovery()
	select {
	case request := <-provider.seen:
		t.Fatalf("recovery started provider for confirmed continuation: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}
	pending, err := recoveredService.backgroundTasks.PendingCompletions("parent-conversation")
	if err != nil || len(pending) != 0 {
		t.Fatalf("confirmed recovery pending tasks=%#v err=%v", pending, err)
	}
	document, err := readBackgroundTaskFileDocument(recoveredService.backgroundTasks.path)
	if err != nil {
		t.Fatalf("read recovered ledger error = %v", err)
	}
	task := document.Tasks[completed.ID]
	if task.CompletionContinuationID != "completed-continuation" || task.CompletionInjectedAt.IsZero() {
		t.Fatalf("confirmed recovered task = %#v", task)
	}
}

func TestBackgroundTaskOutOfOrderTerminalBatchInjectsEachTaskOnce(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 4)}
	service := newBackgroundCompletionTestService(t, provider)
	seedBackgroundTaskParent(t, service, "parent-conversation")
	registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call-1", "child-request-1", "child-conversation-1")
	registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call-2", "child-request-2", "child-conversation-2")
	second, changed, err := service.backgroundTasks.CompleteChild("child-conversation-2", BackgroundTaskStatusCompleted, "second child finished first", "")
	if err != nil || !changed {
		t.Fatalf("complete second child changed=%v err=%v", changed, err)
	}
	time.Sleep(time.Millisecond)
	first, changed, err := service.backgroundTasks.CompleteChild("child-conversation-1", BackgroundTaskStatusCompleted, "first child finished second", "")
	if err != nil || !changed {
		t.Fatalf("complete first child changed=%v err=%v", changed, err)
	}

	service.startBackgroundTaskRecovery()
	select {
	case request := <-provider.seen:
		t.Fatalf("recovery started a headless batch continuation: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}
	firstCompletion := backgroundTaskCompletionFromLedger(first)
	firstCompletion.Detail = stringPtr("poison first detail")
	secondCompletion := backgroundTaskCompletionFromLedger(second)
	secondCompletion.Detail = stringPtr("poison second detail")
	completions := []*agentv1.BackgroundTaskCompletion{secondCompletion, firstCompletion}
	if err := service.dispatchInboundIntent(InboundIntent{
		Kind:                      "run",
		RequestID:                 "batch-continuation",
		ConversationID:            "parent-conversation",
		ModelID:                   "default",
		Mode:                      agentv1.AgentMode_AGENT_MODE_AGENT,
		BackgroundTaskCompletions: completions,
		StartsRun:                 true,
	}); err != nil {
		t.Fatalf("batch continuation error = %v", err)
	}
	select {
	case request := <-provider.seen:
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "second child finished first") || !strings.Contains(joined, "first child finished second") {
			t.Fatalf("batched provider messages = %q", joined)
		}
		if strings.Contains(joined, "poison first detail") || strings.Contains(joined, "poison second detail") {
			t.Fatalf("batch trusted client detail instead of ledger: %q", joined)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client-bound batch continuation was not started")
	}
	waitForBackgroundCompletionProviderIdle(t, service, "batch-continuation")
	waitForBackgroundCompletionTerminal(t, service, "batch-continuation")
	waitForNoPendingBackgroundCompletions(t, service, "parent-conversation")

	if err := service.dispatchInboundIntent(InboundIntent{
		Kind:                      "run",
		RequestID:                 "duplicate-batch-continuation",
		ConversationID:            "parent-conversation",
		ModelID:                   "default",
		Mode:                      agentv1.AgentMode_AGENT_MODE_AGENT,
		BackgroundTaskCompletions: completions,
		StartsRun:                 true,
	}); err != nil {
		t.Fatalf("duplicate batch continuation error = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := provider.requestCount(); got != 1 {
		t.Fatalf("provider request count = %d, want one idempotent continuation", got)
	}
	conversation, err := service.store.LoadConversation("parent-conversation")
	if err != nil {
		t.Fatalf("load parent error = %v", err)
	}
	if got := len(backgroundCompletionUserMessages(conversation.Entries)); got != 2 {
		t.Fatalf("completion history count = %d, want 2", got)
	}
}

func TestBackgroundTaskTerminalStatusMapping(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		status     BackgroundTaskStatus
		wantStatus agentv1.BackgroundTaskStatus
	}{
		{name: "completed", status: BackgroundTaskStatusCompleted, wantStatus: agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS},
		{name: "canceled", status: BackgroundTaskStatusCanceled, wantStatus: agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ABORTED},
		{name: "error", status: BackgroundTaskStatusError, wantStatus: agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ERROR},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			completion := backgroundTaskCompletionFromLedger(BackgroundSubagentTask{
				ID:               "task",
				ParentToolCallID: "call",
				Status:           testCase.status,
				FinalMessage:     "done",
				Error:            "failed",
			})
			if completion.GetStatus() != testCase.wantStatus {
				t.Fatalf("completion status = %s, want %s", completion.GetStatus(), testCase.wantStatus)
			}
		})
	}
}

func TestBackgroundTaskLedgerConcurrentUpdatesRemainAtomic(t *testing.T) {
	store := NewBackgroundTaskFileStore(t.TempDir())
	const taskCount = 24
	var group sync.WaitGroup
	errors := make(chan error, taskCount)
	for index := 0; index < taskCount; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, err := store.Register(BackgroundSubagentTask{
				ParentConversationID: "parent-conversation",
				ParentRequestID:      "parent-request",
				ParentToolCallID:     "task-call-" + string(rune('a'+index)),
				Status:               BackgroundTaskStatusAccepted,
			})
			errors <- err
		}(index)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Register() error = %v", err)
		}
	}
	document, err := readBackgroundTaskFileDocument(store.path)
	if err != nil {
		t.Fatalf("read concurrent ledger error = %v", err)
	}
	if len(document.Tasks) != taskCount {
		t.Fatalf("ledger task count = %d, want %d", len(document.Tasks), taskCount)
	}
}

func TestBackgroundTaskLedgerQuarantinesCorruptDocument(t *testing.T) {
	root := t.TempDir()
	store := NewBackgroundTaskFileStore(root)
	if err := os.WriteFile(store.path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt ledger: %v", err)
	}
	registered, err := store.Register(BackgroundSubagentTask{
		ParentConversationID: "parent-conversation",
		ParentRequestID:      "parent-request",
		ParentToolCallID:     "task-call",
		Status:               BackgroundTaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("Register() after corruption error = %v", err)
	}
	backups, err := filepath.Glob(store.path + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob corrupt backup error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("corrupt ledger backups = %v, want one", backups)
	}
	document, err := readBackgroundTaskFileDocument(store.path)
	if err != nil {
		t.Fatalf("read reset ledger error = %v", err)
	}
	if _, ok := document.Tasks[registered.ID]; !ok {
		t.Fatalf("registered task %q missing after ledger recovery", registered.ID)
	}
}

func TestBackgroundTaskDuplicateTerminalPreservesFirstResult(t *testing.T) {
	store := NewBackgroundTaskFileStore(t.TempDir())
	if _, err := store.Register(BackgroundSubagentTask{
		ParentConversationID: "parent-conversation",
		ParentRequestID:      "parent-request",
		ParentToolCallID:     "task-call",
		ChildRequestID:       "child-request",
		ChildConversationID:  "child-conversation",
		Status:               BackgroundTaskStatusRunning,
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	first, changed, err := store.CompleteChild("child-conversation", BackgroundTaskStatusCompleted, "first final message", "")
	if err != nil || !changed {
		t.Fatalf("first completion task=%#v changed=%v err=%v", first, changed, err)
	}
	second, changed, err := store.CompleteChild("child-conversation", BackgroundTaskStatusError, "", "late error")
	if err != nil || changed {
		t.Fatalf("duplicate completion task=%#v changed=%v err=%v", second, changed, err)
	}
	if second.Status != BackgroundTaskStatusCompleted || second.FinalMessage != "first final message" || second.Error != "" {
		t.Fatalf("duplicate terminal overwrote first result: %#v", second)
	}
}
