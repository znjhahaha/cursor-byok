package forwarder

import (
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func newTrackedShellPendingExec(argsJSON string) runtimecore.PendingExec {
	return initializePendingExecForTracking(runtimecore.PendingExec{
		MessageID:   42,
		ExecID:      "exec-shell-1",
		ToolCallID:  "tool-call-1",
		ModelCallID: "model-call-1",
		ExecKind:    "shell",
		StreamState: "started",
		ArgsJSON:    []byte(argsJSON),
	})
}

func registerShellPendingExec(stream *ActiveStream, pending runtimecore.PendingExec) {
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()
}

// 前台预算必须是「空闲窗口」而不是从 OpenedAt 一次算死的固定预算，
// 否则长对话下 exit 事件排队变慢就会稳定撞上恒定的 31.5s 上限。
func TestShellForegroundDeadlineFollowsLastActivity(t *testing.T) {
	pending := newTrackedShellPendingExec(`{"command":"git status"}`)
	openedDeadline := pending.ShellForegroundDeadline

	activityAt := pending.OpenedAt.Add(20 * time.Second)
	refreshed := refreshShellForegroundRecoveryDeadline(pending, activityAt)

	if !refreshed.ShellForegroundDeadline.After(openedDeadline) {
		t.Fatalf("refreshed deadline = %s, want later than %s", refreshed.ShellForegroundDeadline, openedDeadline)
	}
	wantDeadline := activityAt.Add(30*time.Second + shellTerminalRecoveryGrace)
	if !refreshed.ShellForegroundDeadline.Equal(wantDeadline) {
		t.Fatalf("refreshed deadline = %s, want %s", refreshed.ShellForegroundDeadline, wantDeadline)
	}
	if !refreshed.LastShellActivityAt.Equal(activityAt) {
		t.Fatalf("last activity = %s, want %s", refreshed.LastShellActivityAt, activityAt)
	}
}

// 空闲窗口不能无限顺延：持续刷屏的命令仍要在绝对上限处被回收。
func TestShellForegroundDeadlineRespectsAbsoluteLimit(t *testing.T) {
	pending := newTrackedShellPendingExec(`{"command":"tail -f log"}`)
	activityAt := pending.OpenedAt.Add(shellAbsoluteRecoveryLimit - time.Second)

	refreshed := refreshShellForegroundRecoveryDeadline(pending, activityAt)

	wantDeadline := pending.OpenedAt.Add(shellAbsoluteRecoveryLimit)
	if !refreshed.ShellForegroundDeadline.Equal(wantDeadline) {
		t.Fatalf("clamped deadline = %s, want %s", refreshed.ShellForegroundDeadline, wantDeadline)
	}
}

// 用户显式要求长前台等待时，10 分钟的兜底上限不能反过来截断用户意图。
func TestShellForegroundDeadlineHonorsExplicitLongBlockUntil(t *testing.T) {
	pending := newTrackedShellPendingExec(`{"command":"sleep 1800","block_until_ms":1800000}`)

	wantDeadline := pending.OpenedAt.Add(30*time.Minute + shellTerminalRecoveryGrace)
	if !pending.ShellForegroundDeadline.Equal(wantDeadline) {
		t.Fatalf("explicit deadline = %s, want %s", pending.ShellForegroundDeadline, wantDeadline)
	}
	if !pending.ShellForegroundDeadline.After(pending.OpenedAt.Add(shellAbsoluteRecoveryLimit)) {
		t.Fatalf("explicit deadline = %s, want beyond the default absolute limit", pending.ShellForegroundDeadline)
	}
}

// stdout 抵达即证明命令仍活着，必须顺延窗口，避免正常长命令被合成收口。
func TestShellStdoutExtendsForegroundDeadline(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	service.execBridge = execbridge.NewBridge()
	pending := newTrackedShellPendingExec(`{"command":"go test ./..."}`)
	pending.OpenedAt = time.Now().UTC().Add(-25 * time.Second)
	pending.LastShellActivityAt = pending.OpenedAt
	pending.ShellForegroundDeadline = shellForegroundRecoveryDeadline(pending)
	registerShellPendingExec(stream, pending)
	previousDeadline := pending.ShellForegroundDeadline

	updated := service.applyExecProgress(stream, pending, &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{
			ShellStream: &agentv1.ShellStream{
				Event: &agentv1.ShellStream_Stdout{
					Stdout: &agentv1.ShellStreamStdout{Data: "still running\n"},
				},
			},
		},
	})

	if !updated.ShellForegroundDeadline.After(previousDeadline) {
		t.Fatalf("deadline after stdout = %s, want later than %s", updated.ShellForegroundDeadline, previousDeadline)
	}
}

// 心跳没有输出但同样证明对端存活，也必须顺延窗口。
func TestShellHeartbeatExtendsForegroundDeadline(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newTrackedShellPendingExec(`{"command":"npm install"}`)
	pending.OpenedAt = time.Now().UTC().Add(-25 * time.Second)
	pending.LastShellActivityAt = pending.OpenedAt
	pending.ShellForegroundDeadline = shellForegroundRecoveryDeadline(pending)
	registerShellPendingExec(stream, pending)
	previousDeadline := pending.ShellForegroundDeadline

	updated := service.applyExecControlProgress(stream, pending, &agentv1.ExecClientControlMessage{
		Message: &agentv1.ExecClientControlMessage_Heartbeat{
			Heartbeat: &agentv1.ExecClientHeartbeat{},
		},
	})

	if !updated.ShellForegroundDeadline.After(previousDeadline) {
		t.Fatalf("deadline after heartbeat = %s, want later than %s", updated.ShellForegroundDeadline, previousDeadline)
	}
	if !updated.LastShellHeartbeatAt.After(pending.LastShellActivityAt) {
		t.Fatalf("heartbeat stamp = %s, want refreshed", updated.LastShellHeartbeatAt)
	}
}

// 窗口被顺延后，先前排定的旧 timer 回调不得抢跑并提前收口。
func TestStaleForegroundTimerDoesNotRecoverExtendedShell(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newTrackedShellPendingExec(`{"command":"go build ./..."}`)
	pending.LastShellActivityAt = time.Now().UTC()
	pending.ShellForegroundDeadline = time.Now().UTC().Add(time.Hour)
	registerShellPendingExec(stream, pending)

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonForegroundDeadline); err != nil {
		t.Fatalf("recoverShellWithoutTerminalIfNeeded() error = %v", err)
	}

	if got := pendingExecCountForTest(stream); got != 1 {
		t.Fatalf("pending execs = %d, want the shell to stay pending", got)
	}
}

// 真正静默到 deadline 之后必须收口，否则回合永远卡在 WaitingExternal。
func TestExpiredForegroundDeadlineSynthesizesShellResult(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newTrackedShellPendingExec(`{"command":"cd repo"}`)
	pending.LastShellActivityAt = time.Now().UTC().Add(-time.Minute)
	pending.ShellForegroundDeadline = time.Now().UTC().Add(-time.Second)
	registerShellPendingExec(stream, pending)

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonForegroundDeadline); err != nil {
		t.Fatalf("recoverShellWithoutTerminalIfNeeded() error = %v", err)
	}

	if got := pendingExecCountForTest(stream); got != 0 {
		t.Fatalf("pending execs after recovery = %d, want 0", got)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	foundResult := false
	foundMetadata := false
	for _, entry := range conversation.Entries {
		switch strings.TrimSpace(entry.Kind) {
		case "tool_result":
			if strings.Contains(string(entry.Payload), "shell-incomplete") {
				foundResult = true
			}
		case "metadata":
			if strings.Contains(string(entry.Payload), "shell_stream_stalled") {
				foundMetadata = true
			}
		}
	}
	if !foundResult || !foundMetadata {
		t.Fatalf("synthetic result = %v, stalled metadata = %v, want both", foundResult, foundMetadata)
	}
}

// 恢复收口发布的完成事件必须携带结构化 ToolCall。
// 传 nil 会让客户端拿不到工具身份，把这次调用渲染成 Skipped。
func TestShellRecoveryPublishesStructuredCompletedToolCall(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newTrackedShellPendingExec(`{"command":"git status"}`)
	pending.StdoutBuffer = "partial output\n"
	pending.LastShellActivityAt = time.Now().UTC().Add(-time.Minute)
	pending.ShellForegroundDeadline = time.Now().UTC().Add(-time.Second)
	registerShellPendingExec(stream, pending)

	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonForegroundDeadline); err != nil {
		t.Fatalf("recoverShellWithoutTerminalIfNeeded() error = %v", err)
	}

	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	var completed *agentv1.ToolCall
	completedCount := 0
	abortCount := 0
	for _, event := range events {
		if abort := event.Message.GetExecServerControlMessage().GetAbort(); abort != nil {
			abortCount++
			if abort.GetId() != pending.MessageID {
				t.Fatalf("abort id = %d, want %d", abort.GetId(), pending.MessageID)
			}
		}
		update := event.Message.GetInteractionUpdate().GetToolCallCompleted()
		if update == nil || update.GetCallId() != pending.ToolCallID {
			continue
		}
		completedCount++
		completed = update.GetToolCall()
	}
	if abortCount != 1 {
		t.Fatalf("exec abort events = %d, want exactly 1", abortCount)
	}
	if completedCount != 1 {
		t.Fatalf("tool_call_completed events = %d, want exactly 1", completedCount)
	}
	if completed == nil {
		t.Fatal("completed tool call = nil, want a structured shell tool call")
	}
	shellCall := completed.GetShellToolCall()
	if shellCall == nil {
		t.Fatalf("completed tool call = %#v, want the shell variant", completed)
	}
	failure := shellCall.GetResult().GetFailure()
	if failure == nil {
		t.Fatalf("shell result = %#v, want the failure variant", shellCall.GetResult())
	}
	if failure.GetExitCode() == 0 {
		t.Fatalf("shell exit code = %d, want non-zero", failure.GetExitCode())
	}
	if !failure.GetAborted() {
		t.Fatal("synthetic shell failure must be marked aborted after the timeout abort request")
	}
	if failure.GetAbortReason() != agentv1.ShellAbortReason_SHELL_ABORT_REASON_TIMEOUT {
		t.Fatalf("shell abort reason = %s, want TIMEOUT", failure.GetAbortReason())
	}
	if !strings.Contains(failure.GetStdout(), "partial output") {
		t.Fatalf("shell stdout = %q, want the captured output preserved", failure.GetStdout())
	}
}

// transport 已经关闭时无法再向该执行桥可靠投递 abort；恢复结果只能陈述未知状态，
// 不能伪装成客户端已经执行了超时中止。
func TestTransportCloseRecoveryDoesNotClaimAbort(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newTrackedShellPendingExec(`{"command":"long-running-job"}`)
	pending.StreamState = "transport_closed"
	registerShellPendingExec(stream, pending)

	if err := service.recoverShellWithoutTerminal(stream, pending, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("recoverShellWithoutTerminal() error = %v", err)
	}

	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	var failure *agentv1.ShellFailure
	for _, event := range events {
		if abort := event.Message.GetExecServerControlMessage().GetAbort(); abort != nil {
			t.Fatalf("transport-close recovery emitted unexpected abort id=%d", abort.GetId())
		}
		completed := event.Message.GetInteractionUpdate().GetToolCallCompleted()
		if completed == nil || completed.GetCallId() != pending.ToolCallID {
			continue
		}
		failure = completed.GetToolCall().GetShellToolCall().GetResult().GetFailure()
	}
	if failure == nil {
		t.Fatal("transport-close recovery did not publish a structured shell failure")
	}
	if failure.GetAborted() {
		t.Fatal("transport-close recovery must not claim that the client aborted the process")
	}
}
