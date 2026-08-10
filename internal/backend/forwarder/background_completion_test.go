package forwarder

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

type backgroundCompletionTestCompiler struct{}

func (backgroundCompletionTestCompiler) Compile(conversation *ConversationFile, _ agentv1.AgentMode, _ string, _ string) (CompiledConversation, error) {
	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		return CompiledConversation{}, err
	}
	return CompiledConversation{Messages: messages, StableMessageCount: len(messages)}, nil
}

func (backgroundCompletionTestCompiler) DerivePromptContexts(_ *ConversationFile, _ agentv1.AgentMode, _ string) ([]PromptContextMessage, error) {
	return nil, nil
}

type backgroundCompletionTestProvider struct {
	mu       sync.Mutex
	requests []ProviderRequest
	seen     chan ProviderRequest
}

func (provider *backgroundCompletionTestProvider) StartStream(_ context.Context, request ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	request.Messages = append([]modeladapter.Message(nil), request.Messages...)
	provider.mu.Lock()
	provider.requests = append(provider.requests, request)
	provider.mu.Unlock()
	provider.seen <- request
	if err := sink(modeladapter.ModelEvent{
		Kind:     modeladapter.ModelEventKindTextDelta,
		Provider: "test",
		Model:    request.ModelID,
		Text:     "continuation acknowledged",
	}); err != nil {
		return err
	}
	return sink(modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		Provider:     "test",
		Model:        request.ModelID,
		FinishReason: "stop",
	})
}

func (provider *backgroundCompletionTestProvider) requestCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.requests)
}

type backgroundCompletionToolProvider struct {
	seen chan ProviderRequest
}

func (provider *backgroundCompletionToolProvider) StartStream(_ context.Context, request ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	provider.seen <- request
	if err := sink(modeladapter.ModelEvent{
		Kind:     modeladapter.ModelEventKindToolLikeCompleted,
		Provider: "test",
		Model:    request.ModelID,
		ToolInvocation: &runtimecore.ToolInvocation{
			CallID:   "read-call",
			ToolName: "Read",
			ArgsJSON: []byte(`{"path":"D:\\cursor_me\\go.mod"}`),
		},
	}); err != nil {
		return err
	}
	return sink(modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		Provider:     "test",
		Model:        request.ModelID,
		FinishReason: "tool_calls",
	})
}

type backgroundCompletionRetryProvider struct {
	mu       sync.Mutex
	attempts int
	seen     chan ProviderRequest
}

func (provider *backgroundCompletionRetryProvider) StartStream(_ context.Context, request ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	provider.mu.Lock()
	provider.attempts++
	attempt := provider.attempts
	provider.mu.Unlock()
	provider.seen <- request
	if attempt == 1 {
		return sink(modeladapter.ModelEvent{
			Kind:     modeladapter.ModelEventKindProviderError,
			Provider: "test",
			Model:    request.ModelID,
			Err:      errors.New("injected provider failure"),
		})
	}
	if err := sink(modeladapter.ModelEvent{
		Kind:     modeladapter.ModelEventKindTextDelta,
		Provider: "test",
		Model:    request.ModelID,
		Text:     "retry completed",
	}); err != nil {
		return err
	}
	return sink(modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		Provider:     "test",
		Model:        request.ModelID,
		FinishReason: "stop",
	})
}

func newBackgroundCompletionTestService(t *testing.T, provider ProviderGateway) *Service {
	t.Helper()
	// 这里不用 t.TempDir()：continuation 收口要等客户端 checkpoint blob ack，
	// 测试没有客户端，后台写盘会和 t.TempDir 的强制清理相互竞争。
	historyRoot, err := os.MkdirTemp("", "background-completion-history")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(historyRoot)
	})
	return newBackgroundCompletionTestServiceAtRoot(historyRoot, provider)
}

func newBackgroundCompletionTestServiceAtRoot(historyRoot string, provider ProviderGateway) *Service {
	store := NewConversationFileStore(historyRoot)
	broker := NewStreamBroker()
	debug := newDebugRecorder(store.HistoryDir(), broker, nil)
	return &Service{
		store:           store,
		backgroundTasks: NewBackgroundTaskFileStore(historyRoot),
		projector:       NewHistoryProjector(),
		compiler:        backgroundCompletionTestCompiler{},
		provider:        provider,
		broker:          broker,
		debug:           debug,
		recorder:        newArtifactRecorder(store, broker, debug),
		execBridge:      execbridge.NewBridge(),
	}
}

func backgroundCompletionForTest(detail string) *agentv1.BackgroundTaskCompletion {
	return &agentv1.BackgroundTaskCompletion{
		TaskId:     "task-1",
		SubagentId: proto.String("subagent-1"),
		ToolCallId: proto.String("call-1"),
		ThreadId:   proto.String("thread-1"),
		Kind:       agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Status:     agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
		Reason:     agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
		Title:      "worker",
		Detail:     proto.String(detail),
	}
}

func backgroundCompletionActionMessage(completions ...*agentv1.BackgroundTaskCompletion) *agentv1.AgentClientMessage {
	return &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_BackgroundTaskCompletionAction{
					BackgroundTaskCompletionAction: &agentv1.BackgroundTaskCompletionAction{Completions: completions},
				},
			},
		},
	}
}

func runIntentWithBackgroundCompletion(t *testing.T, service *Service, requestID string, conversationID string, completion *agentv1.BackgroundTaskCompletion) error {
	t.Helper()
	return service.dispatchInboundIntent(InboundIntent{
		Kind:                      "run",
		RequestID:                 requestID,
		ConversationID:            conversationID,
		ModelID:                   "parent-model",
		ModelName:                 "parent-model",
		Mode:                      agentv1.AgentMode_AGENT_MODE_MULTITASK,
		BackgroundTaskCompletions: []*agentv1.BackgroundTaskCompletion{completion},
		StartsRun:                 true,
	})
}

// waitForBackgroundCompletionProviderIdle 等到 provider pass 收口。
// 这里不等 stream 终态：真实终态还要等客户端回 checkpoint blob ack。
func waitForBackgroundCompletionProviderIdle(t *testing.T, service *Service, requestID string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		stream, ok := service.broker.Get(requestID)
		if ok && stream != nil {
			stream.mu.Lock()
			idle := !stream.ProviderActive && stream.ProviderPassCount > 0
			stream.mu.Unlock()
			if idle {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("provider pass for %s did not finish", requestID)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForBackgroundCompletionTerminal(t *testing.T, service *Service, requestID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stream, ok := service.broker.Get(requestID)
		if ok && stream != nil {
			acknowledgeCheckpointBlobs(t, service, stream)
			stream.mu.Lock()
			completed := stream.Status == StreamStatusCompleted && stream.Phase == TurnPhaseCompleted
			stream.mu.Unlock()
			if completed {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background completion request %s did not complete", requestID)
}

func TestBackgroundCompletionInjectsDetailIntoContinuationProviderRequest(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 2)}
	service := newBackgroundCompletionTestService(t, provider)
	completion := backgroundCompletionForTest("worker final message")

	if err := runIntentWithBackgroundCompletion(t, service, "request-1", "conversation-1", completion); err != nil {
		t.Fatalf("first continuation error = %v", err)
	}
	select {
	case request := <-provider.seen:
		found := false
		for _, message := range request.Messages {
			if message.Role == "user" && strings.Contains(message.Content, "worker final message") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("provider messages = %#v, want completion detail", request.Messages)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider continuation was not started")
	}
	waitForBackgroundCompletionProviderIdle(t, service, "request-1")

	conversation, err := service.store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	completionEntries := backgroundCompletionUserMessages(conversation.Entries)
	if len(completionEntries) != 1 || !strings.Contains(completionEntries[0].GetText(), "worker final message") {
		t.Fatalf("completion history = %#v, want one native detail message", completionEntries)
	}
	if !completionEntries[0].GetIsSimulatedMsg() || completionEntries[0].GetSimulatedMsgReason() != agentv1.SimulatedMsgReason_SIMULATED_MSG_REASON_BACKGROUND_TASK_COMPLETION {
		t.Fatalf("completion user message metadata = %#v, want background completion reason", completionEntries[0])
	}
}

func TestBackgroundCompletionDuplicateActionDoesNotStartProviderAgain(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 4)}
	service := newBackgroundCompletionTestService(t, provider)
	completion := backgroundCompletionForTest("same result")

	if err := runIntentWithBackgroundCompletion(t, service, "request-1", "conversation-1", completion); err != nil {
		t.Fatalf("first continuation error = %v", err)
	}
	select {
	case <-provider.seen:
	case <-time.After(3 * time.Second):
		t.Fatal("first provider continuation was not started")
	}
	waitForBackgroundCompletionProviderIdle(t, service, "request-1")
	if err := runIntentWithBackgroundCompletion(t, service, "request-2", "conversation-1", completion); err != nil {
		t.Fatalf("duplicate continuation error = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := provider.requestCount(); got != 1 {
		t.Fatalf("provider request count = %d, want 1", got)
	}
	conversation, err := service.store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if got := len(backgroundCompletionUserMessages(conversation.Entries)); got != 1 {
		t.Fatalf("completion history count = %d, want 1", got)
	}
}

func TestBackgroundCompletionBatchPartiallyDeduplicates(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	first := backgroundCompletionForTest("first")
	second := backgroundCompletionForTest("second")
	second.TaskId = "task-2"
	second.SubagentId = proto.String("subagent-2")
	second.ToolCallId = proto.String("call-2")
	entries, err := buildRunEntries(InboundIntent{
		RequestID:                 "request-1",
		BackgroundTaskCompletions: []*agentv1.BackgroundTaskCompletion{first},
	}, agentv1.AgentMode_AGENT_MODE_MULTITASK, 1)
	if err != nil {
		t.Fatalf("build first entries error = %v", err)
	}
	if _, err := store.SaveConversationWithEntries("conversation-1", &ConversationFile{ConversationID: "conversation-1", Mode: "multitask"}, entries); err != nil {
		t.Fatalf("save first completion error = %v", err)
	}
	conversation, err := store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("load conversation error = %v", err)
	}
	fresh := filterNewBackgroundSubagentCompletions(conversation, []*agentv1.BackgroundTaskCompletion{first, second})
	if len(fresh) != 1 || fresh[0].GetTaskId() != "task-2" {
		t.Fatalf("fresh completions = %#v, want only second completion", fresh)
	}
}

// completion action 不带 mode/model，且父 stream 早已结束。
// 此时必须从父 conversation 恢复 Multitask 和模型，否则 continuation 会掉回 agent 模式。
func TestBackgroundCompletionRestoresParentModeAndModelWithoutActiveStream(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 2)}
	service := newBackgroundCompletionTestService(t, provider)
	parentEntries, err := buildRunEntries(InboundIntent{
		RequestID:      "request-0",
		ModelID:        "parent-model",
		ModelName:      "Parent Model",
		ThinkingEffort: "high",
	}, agentv1.AgentMode_AGENT_MODE_MULTITASK, 1)
	if err != nil {
		t.Fatalf("buildRunEntries() error = %v", err)
	}
	if _, err := service.store.SaveConversationWithEntries(
		"conversation-1",
		&ConversationFile{ConversationID: "conversation-1", RootConversationID: "conversation-1", Mode: "multitask"},
		parentEntries,
	); err != nil {
		t.Fatalf("seed parent conversation error = %v", err)
	}

	if err := service.dispatchInboundIntent(InboundIntent{
		Kind:                      "run",
		RequestID:                 "request-1",
		ConversationID:            "conversation-1",
		ModelID:                   "default",
		Mode:                      agentv1.AgentMode_AGENT_MODE_AGENT,
		BackgroundTaskCompletions: []*agentv1.BackgroundTaskCompletion{backgroundCompletionForTest("restored result")},
		StartsRun:                 true,
	}); err != nil {
		t.Fatalf("continuation dispatch error = %v", err)
	}

	select {
	case request := <-provider.seen:
		if request.ModelID != "parent-model" {
			t.Fatalf("continuation model_id = %q, want parent-model", request.ModelID)
		}
		if request.ThinkingEffort != "high" {
			t.Fatalf("continuation thinking_effort = %q, want high", request.ThinkingEffort)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("continuation provider request was not started")
	}
	stream, ok := service.broker.Get("request-1")
	if !ok || stream == nil {
		t.Fatal("continuation stream is missing")
	}
	stream.mu.Lock()
	mode := stream.Mode
	stream.mu.Unlock()
	if mode != agentv1.AgentMode_AGENT_MODE_MULTITASK {
		t.Fatalf("continuation stream mode = %s, want AGENT_MODE_MULTITASK", mode)
	}
}

func TestBackgroundCompletionActionSupportsBothClientSubscriptionOrders(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		subscribeFirst bool
	}{
		{name: "RunSSE before BidiAppend", subscribeFirst: true},
		{name: "BidiAppend before RunSSE"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
			service := newBackgroundCompletionTestService(t, provider)
			seedBackgroundTaskParent(t, service, "parent-conversation")
			registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
			completed, changed, err := service.backgroundTasks.CompleteChild("child-conversation", BackgroundTaskStatusCompleted, "ledger final message", "")
			if err != nil || !changed {
				t.Fatalf("CompleteChild() changed=%v err=%v", changed, err)
			}
			completion := backgroundTaskCompletionFromLedger(completed)
			completion.Detail = proto.String("untrusted client detail")
			requestID := "client-wakeup-request"
			if testCase.subscribeFirst {
				if _, _, err := service.broker.Subscribe(requestID); err != nil {
					t.Fatalf("subscribe before action error = %v", err)
				}
			}
			intent, err := service.decodeInboundIntent(requestID, backgroundCompletionActionMessage(completion), "conversation_action")
			if err != nil {
				t.Fatalf("decode completion action error = %v", err)
			}
			if intent.ConversationID != "parent-conversation" || !intent.StartsRun {
				t.Fatalf("decoded intent = %#v", intent)
			}
			if err := service.dispatchInboundIntent(intent); err != nil {
				t.Fatalf("dispatch completion action error = %v", err)
			}
			if !testCase.subscribeFirst {
				if _, _, err := service.broker.Subscribe(requestID); err != nil {
					t.Fatalf("subscribe after action error = %v", err)
				}
			}
			stream, ok := service.broker.Get(requestID)
			if !ok || stream == nil {
				t.Fatal("client wakeup stream is missing")
			}
			stream.mu.Lock()
			subscriberCount := len(stream.Subscribers)
			stream.mu.Unlock()
			if subscriberCount != 1 {
				t.Fatalf("subscriber count = %d, want 1", subscriberCount)
			}
			select {
			case request := <-provider.seen:
				joined := ""
				for _, message := range request.Messages {
					joined += "\n" + message.Content
				}
				if request.RequestID != requestID || !strings.Contains(joined, "ledger final message") || strings.Contains(joined, "untrusted client detail") {
					t.Fatalf("provider request=%#v messages=%q", request, joined)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("client wakeup provider request was not started")
			}
			waitForBackgroundCompletionProviderIdle(t, service, requestID)
			waitForBackgroundCompletionTerminal(t, service, requestID)
		})
	}
}

func TestBackgroundCompletionToolCallUsesClientBoundRequest(t *testing.T) {
	provider := &backgroundCompletionToolProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	seedBackgroundTaskParent(t, service, "parent-conversation")
	registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	completed, changed, err := service.backgroundTasks.CompleteChild("child-conversation", BackgroundTaskStatusCompleted, "tool follow-up result", "")
	if err != nil || !changed {
		t.Fatalf("CompleteChild() changed=%v err=%v", changed, err)
	}
	requestID := "client-tool-wakeup"
	if _, _, err := service.broker.Subscribe(requestID); err != nil {
		t.Fatalf("subscribe client wakeup error = %v", err)
	}
	intent, err := service.decodeInboundIntent(requestID, backgroundCompletionActionMessage(backgroundTaskCompletionFromLedger(completed)), "conversation_action")
	if err != nil {
		t.Fatalf("decode completion action error = %v", err)
	}
	if err := service.dispatchInboundIntent(intent); err != nil {
		t.Fatalf("dispatch completion action error = %v", err)
	}
	select {
	case request := <-provider.seen:
		if request.RequestID != requestID {
			t.Fatalf("tool provider request_id = %q, want %q", request.RequestID, requestID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool continuation provider request was not started")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stream, ok := service.broker.Get(requestID)
		if ok && stream != nil {
			stream.mu.Lock()
			var pending runtimecore.PendingExec
			for _, candidate := range stream.PendingExecs {
				pending = candidate
				break
			}
			phase := stream.Phase
			subscriberCount := len(stream.Subscribers)
			stream.mu.Unlock()
			if pending.ToolCallID == "read-call" && phase == TurnPhaseWaitingExternal {
				if pending.ExecKind != "read" || subscriberCount != 1 {
					t.Fatalf("pending=%#v phase=%s subscribers=%d", pending, phase, subscriberCount)
				}
				if err := service.handleCancelIntent(InboundIntent{Kind: "cancel", RequestID: requestID, CancelReason: "test complete"}); err != nil {
					t.Fatalf("cancel tool continuation error = %v", err)
				}
				pendingTasks, err := service.backgroundTasks.PendingCompletions("parent-conversation")
				if err != nil || len(pendingTasks) != 1 || pendingTasks[0].CompletionContinuationID != "" {
					t.Fatalf("released tool claim tasks=%#v err=%v", pendingTasks, err)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("client-bound Read tool call did not reach waiting_external")
}

func TestBackgroundCompletionFailureReleasesClaimForRetry(t *testing.T) {
	provider := &backgroundCompletionRetryProvider{seen: make(chan ProviderRequest, 2)}
	service := newBackgroundCompletionTestService(t, provider)
	seedBackgroundTaskParent(t, service, "parent-conversation")
	registerLinkedBackgroundTask(t, service, "parent-conversation", "parent-request", "task-call", "child-request", "child-conversation")
	completed, changed, err := service.backgroundTasks.CompleteChild("child-conversation", BackgroundTaskStatusCompleted, "retryable ledger result", "")
	if err != nil || !changed {
		t.Fatalf("CompleteChild() changed=%v err=%v", changed, err)
	}
	completion := backgroundTaskCompletionFromLedger(completed)
	if err := runIntentWithBackgroundCompletion(t, service, "failed-wakeup", "parent-conversation", completion); err != nil {
		t.Fatalf("dispatch failed wakeup error = %v", err)
	}
	select {
	case <-provider.seen:
	case <-time.After(3 * time.Second):
		t.Fatal("failed provider attempt was not started")
	}
	deadline := time.Now().Add(3 * time.Second)
	claimReleased := false
	for time.Now().Before(deadline) {
		stream, ok := service.broker.Get("failed-wakeup")
		failed := false
		if ok && stream != nil {
			stream.mu.Lock()
			failed = stream.Status == StreamStatusFailed && stream.Phase == TurnPhaseFailed
			stream.mu.Unlock()
		}
		pending, pendingErr := service.backgroundTasks.PendingCompletions("parent-conversation")
		if pendingErr != nil {
			t.Fatalf("PendingCompletions() error = %v", pendingErr)
		}
		if failed && len(pending) == 1 && pending[0].CompletionContinuationID == "" && pending[0].CompletionInjectedAt.IsZero() {
			claimReleased = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !claimReleased {
		pending, pendingErr := service.backgroundTasks.PendingCompletions("parent-conversation")
		t.Fatalf("claim was not released after failure tasks=%#v err=%v", pending, pendingErr)
	}

	if err := runIntentWithBackgroundCompletion(t, service, "retry-wakeup", "parent-conversation", completion); err != nil {
		t.Fatalf("dispatch retry wakeup error = %v", err)
	}
	select {
	case request := <-provider.seen:
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "retryable ledger result") {
			t.Fatalf("retry messages = %q", joined)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry provider attempt was not started")
	}
	waitForBackgroundCompletionProviderIdle(t, service, "retry-wakeup")
	waitForBackgroundCompletionTerminal(t, service, "retry-wakeup")
	waitForNoPendingBackgroundCompletions(t, service, "parent-conversation")
}

func TestBackgroundCompletionWireFieldsRoundTrip(t *testing.T) {
	original := backgroundCompletionForTest("wire detail")
	encoded, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("marshal completion error = %v", err)
	}
	decoded := &agentv1.BackgroundTaskCompletion{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("unmarshal completion error = %v", err)
	}
	if decoded.GetSubagentId() != "subagent-1" || decoded.GetToolCallId() != "call-1" {
		t.Fatalf("decoded wire fields subagent_id=%q tool_call_id=%q", decoded.GetSubagentId(), decoded.GetToolCallId())
	}
}
