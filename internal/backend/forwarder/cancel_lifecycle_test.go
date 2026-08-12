package forwarder

import (
	"context"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// 停止在长会话里「点了没反应」，根因是取消能力原先绑在单个 provider pass 上，
// 而两个 pass 之间正好夹着随历史长度增长的快照、编译与落盘。
// 这一组用例锁定修复后的性质：中断按回合存在，落在空窗里也不会丢。
func TestStreamInterruptCoversPassBoundaries(t *testing.T) {
	stream := &ActiveStream{RequestID: "request-1"}

	armStreamInterrupt(stream)
	firstPass, cancelFirst := context.WithCancel(streamInterruptContext(stream))
	defer cancelFirst()
	if firstPass.Err() != nil {
		t.Fatal("刚 arm 的中断闩不应让 pass context 立即失效")
	}

	interruptStream(stream)
	if firstPass.Err() == nil {
		t.Fatal("中断应立即取消正在运行的 pass")
	}

	// 这一条是空窗修复的本体：中断到达时下一个 pass 还没创建 context，
	// 它必须一诞生就是已取消状态，否则请求照样会发给模型。
	nextPass, cancelNext := context.WithCancel(streamInterruptContext(stream))
	defer cancelNext()
	if nextPass.Err() == nil {
		t.Fatal("中断之后派生的 pass context 必须一诞生就是已取消状态")
	}

	interruptStream(stream)

	// 同一个 stream 承载新回合时闩必须重新可用，否则新回合刚启动就被判为已取消。
	armStreamInterrupt(stream)
	revived, cancelRevived := context.WithCancel(streamInterruptContext(stream))
	defer cancelRevived()
	if revived.Err() != nil {
		t.Fatal("新回合 arm 后 pass context 不应继承上一回合的取消状态")
	}
}

// driveProvider 在 actor 里独占执行，cancel 命令此刻还排在队列里，
// Status 与 Phase 都尚未变化，因此这里只能靠中断闩判断用户已经按下停止。
func TestDriveProviderSkipsPassAfterInterrupt(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	stream, err := service.broker.OpenStream(
		"request-1", "conversation-1", 1, "model", "model",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	armStreamInterrupt(stream)
	interruptStream(stream)

	if err := service.driveProvider(stream); err != nil {
		t.Fatalf("driveProvider() error = %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatalf("中断后仍向模型发出了 %d 次请求", provider.requestCount())
	}
	stream.mu.Lock()
	providerActive := stream.ProviderActive
	stream.mu.Unlock()
	if providerActive {
		t.Fatal("被跳过的 pass 不应把流标记为 provider 运行中")
	}
}

// 取消的第一步必须是掐断执行体，而不是先写历史。
// 原实现先落盘中断输出与取消条目、最后才取消 provider，停止延迟因此正比于历史长度。
func TestCancelIntentInterruptsProviderBeforeWritingHistory(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	conversation, err := service.store.CreateConversation(
		"conversation-1", agentv1.AgentMode_AGENT_MODE_AGENT, "", "", "conversation-1",
	)
	if err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	stream, err := service.broker.OpenStream(
		"request-1", "conversation-1", 1, "model", "model",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	entriesWhenInterrupted := -1
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderActive = true
	stream.ProviderAccumulatedText = "部分回答"
	stream.CheckpointConversation = conversation
	stream.ProviderCancel = func() {
		stream.mu.Lock()
		entriesWhenInterrupted = len(stream.CheckpointConversation.Entries)
		stream.mu.Unlock()
	}
	stream.mu.Unlock()
	armStreamInterrupt(stream)

	if err := service.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    "request-1",
		CancelReason: "user aborted",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}

	stream.mu.Lock()
	entriesAfterCancel := len(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if entriesWhenInterrupted < 0 {
		t.Fatal("取消没有中断正在运行的 provider pass")
	}
	if entriesWhenInterrupted >= entriesAfterCancel {
		t.Fatalf("中断发生在落盘之后：中断时 %d 条，收口后 %d 条", entriesWhenInterrupted, entriesAfterCancel)
	}
}

// 停止后客户端仍可能重发停止，或者流早已被清理。
// 这些路径必须静默成功，否则界面会在停止之后再弹一次失败提示。
func TestCancelIsIdempotentAcrossRepeatsAndMissingStreams(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)

	if err := service.handleCancelIntent(InboundIntent{Kind: "cancel", RequestID: "never-existed"}); err != nil {
		t.Fatalf("取消一个已经不存在的请求应当无操作，实际 error = %v", err)
	}

	stream, err := service.broker.OpenStream(
		"request-1", "conversation-1", 1, "model", "model",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.mu.Unlock()
	armStreamInterrupt(stream)
	pass, cancelPass := context.WithCancel(streamInterruptContext(stream))
	defer cancelPass()

	for attempt := 1; attempt <= 3; attempt++ {
		if err := service.handleCancelIntent(InboundIntent{Kind: "cancel", RequestID: "request-1"}); err != nil {
			t.Fatalf("第 %d 次取消 error = %v", attempt, err)
		}
	}
	if pass.Err() == nil {
		t.Fatal("取消未中断本回合的 pass context")
	}
	if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: "request-1"}); err != nil {
		t.Fatalf("对已收口的流再次派发取消 error = %v", err)
	}
}

// 主回合取消只应终止仍在等待结果的前台执行桥。
// 已收到 backgrounded 的 Shell 已脱离 PendingExecs，必须继续由 shell_id 生命周期管理。
func TestCancelRetiresForegroundShellWithoutKillingBackgroundShell(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	stream, err := service.broker.OpenStream(
		"request-1", "conversation-1", 1, "model", "model",
		agentv1.AgentMode_AGENT_MODE_AGENT, "run commands",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	foreground := runtimecore.PendingExec{
		MessageID:   42,
		ExecID:      "exec-shell-foreground",
		ToolCallID:  "tool-shell-foreground",
		ExecKind:    "shell",
		StreamState: "started",
		ArgsJSON:    []byte(`{"command":"go test ./..."}`),
	}
	background := &BackgroundShellState{
		ShellID:           "9",
		OriginalMessageID: 77,
		OriginalExecID:    "exec-shell-background",
		Status:            backgroundShellStatusBackgrounded,
	}
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseWaitingExternal
	stream.PendingExecs[foreground.ExecID] = foreground
	stream.BackgroundShells[background.ShellID] = background
	stream.BackgroundShellsByMessageID[background.OriginalMessageID] = background.ShellID
	stream.BackgroundShellsByExecID[background.OriginalExecID] = background.ShellID
	stream.mu.Unlock()
	armStreamInterrupt(stream)

	if err := service.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    stream.RequestID,
		CancelReason: "user aborted",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}

	stream.mu.Lock()
	pendingCount := len(stream.PendingExecs)
	keptBackground := stream.BackgroundShells[background.ShellID]
	_, tombstoned := stream.RecentCompletedExecs[foreground.MessageID]
	stream.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending execs after cancel = %d, want 0", pendingCount)
	}
	if !tombstoned {
		t.Fatal("foreground shell message id was not tombstoned")
	}
	if keptBackground != background || keptBackground.Status != backgroundShellStatusBackgrounded {
		t.Fatalf("background shell after cancel = %#v, want original background state", keptBackground)
	}

	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	abortCount := 0
	for _, event := range events {
		abort := event.Message.GetExecServerControlMessage().GetAbort()
		if abort == nil {
			continue
		}
		abortCount++
		if abort.GetId() != foreground.MessageID {
			t.Fatalf("abort id = %d, want foreground message id %d", abort.GetId(), foreground.MessageID)
		}
	}
	if abortCount != 1 {
		t.Fatalf("abort events = %d, want exactly one foreground abort", abortCount)
	}

	// 客户端可能在收到 abort 后仍补发一个 exit；tombstone 必须让它静默失效。
	if err := service.handleExecResult(InboundIntent{
		Kind:      "exec_result",
		RequestID: stream.RequestID,
		ExecClientMessage: &agentv1.ExecClientMessage{
			Id:     foreground.MessageID,
			ExecId: foreground.ExecID,
			Message: &agentv1.ExecClientMessage_ShellStream{
				ShellStream: &agentv1.ShellStream{
					Event: &agentv1.ShellStream_Exit{Exit: &agentv1.ShellStreamExit{Code: 1, Aborted: true}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("late shell exit after cancel error = %v", err)
	}
}
