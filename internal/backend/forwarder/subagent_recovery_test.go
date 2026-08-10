package forwarder

import (
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func newStalledSubagentPendingExec(deadline time.Time) runtimecore.PendingExec {
	return runtimecore.PendingExec{
		MessageID:          42,
		ExecID:             "exec-subagent-1",
		ToolCallID:         "task-call-1",
		ModelCallID:        "model-call-1",
		ExecKind:           "subagent",
		StreamState:        "opened",
		ArgsJSON:           []byte(`{"subagent_type":"explore","prompt":"inspect"}`),
		OpenedAt:           deadline.Add(-subagentInactivityTimeout),
		LastLivenessAt:     deadline.Add(-subagentInactivityTimeout),
		InactivityDeadline: deadline,
	}
}

func registerSubagentPendingExec(stream *ActiveStream, pending runtimecore.PendingExec) {
	stream.mu.Lock()
	stream.PendingExecs[pending.ExecID] = pending
	stream.mu.Unlock()
}

func pendingExecCountForTest(stream *ActiveStream) int {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return len(stream.PendingExecs)
}

func TestInitializePendingExecTracksSubagentInactivityDeadline(t *testing.T) {
	tracked := initializePendingExecForTracking(runtimecore.PendingExec{
		ExecID:   "exec-subagent-1",
		ExecKind: "subagent",
	})
	if tracked.OpenedAt.IsZero() || tracked.LastLivenessAt.IsZero() {
		t.Fatalf("tracked liveness stamps = %#v, want populated", tracked)
	}
	wantDeadline := tracked.LastLivenessAt.Add(subagentInactivityTimeout)
	if !tracked.InactivityDeadline.Equal(wantDeadline) {
		t.Fatalf("inactivity deadline = %s, want %s", tracked.InactivityDeadline, wantDeadline)
	}
}

// 失联的 subagent 必须被合成收口，否则 PendingExecs 永不清空，
// 父 Agent 卡在 WaitingExternal，界面表现为无响应黑屏。
func TestExpiredSubagentInactivityDeadlineSynthesizesResultAndReleasesPending(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newStalledSubagentPendingExec(time.Now().UTC().Add(-time.Second))
	registerSubagentPendingExec(stream, pending)

	if err := service.recoverSubagentAfterInactivityIfNeeded(stream, pending.ExecID, pending.MessageID); err != nil {
		t.Fatalf("recoverSubagentAfterInactivityIfNeeded() error = %v", err)
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
			if strings.Contains(string(entry.Payload), "subagent-incomplete") {
				foundResult = true
			}
		case "metadata":
			if strings.Contains(string(entry.Payload), "subagent_stream_stalled") {
				foundMetadata = true
			}
		}
	}
	if !foundResult || !foundMetadata {
		t.Fatalf("synthetic result = %v, stalled metadata = %v, want both", foundResult, foundMetadata)
	}
}

// 负向验证：deadline 还没到时不能提前合成结果，否则会杀掉正常运行的子代理。
func TestUnexpiredSubagentInactivityDeadlineKeepsPendingExec(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newStalledSubagentPendingExec(time.Now().UTC().Add(time.Hour))
	registerSubagentPendingExec(stream, pending)

	if err := service.recoverSubagentAfterInactivityIfNeeded(stream, pending.ExecID, pending.MessageID); err != nil {
		t.Fatalf("recoverSubagentAfterInactivityIfNeeded() error = %v", err)
	}

	if got := pendingExecCountForTest(stream); got != 1 {
		t.Fatalf("pending execs = %d, want the subagent to stay pending", got)
	}
}

// 持续心跳的长任务不得被误杀：心跳必须顺延 deadline。
func TestSubagentHeartbeatExtendsInactivityDeadline(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	expiring := time.Now().UTC().Add(time.Millisecond)
	pending := newStalledSubagentPendingExec(expiring)
	registerSubagentPendingExec(stream, pending)

	updated := service.applyExecControlProgress(stream, pending, &agentv1.ExecClientControlMessage{
		Message: &agentv1.ExecClientControlMessage_Heartbeat{
			Heartbeat: &agentv1.ExecClientHeartbeat{},
		},
	})
	if !updated.InactivityDeadline.After(expiring) {
		t.Fatalf("heartbeat deadline = %s, want later than %s", updated.InactivityDeadline, expiring)
	}

	time.Sleep(5 * time.Millisecond)
	if err := service.recoverSubagentAfterInactivityIfNeeded(stream, pending.ExecID, pending.MessageID); err != nil {
		t.Fatalf("recoverSubagentAfterInactivityIfNeeded() error = %v", err)
	}
	if got := pendingExecCountForTest(stream); got != 1 {
		t.Fatalf("pending execs after heartbeat = %d, want 1", got)
	}
}

// 兜底只针对 subagent：shell 有自己的前台窗口语义，不能被这条路径收口。
func TestSubagentInactivityRecoveryIgnoresShellPendingExec(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	pending := newStalledSubagentPendingExec(time.Now().UTC().Add(-time.Second))
	pending.ExecKind = "shell"
	registerSubagentPendingExec(stream, pending)

	if err := service.recoverSubagentAfterInactivityIfNeeded(stream, pending.ExecID, pending.MessageID); err != nil {
		t.Fatalf("recoverSubagentAfterInactivityIfNeeded() error = %v", err)
	}
	if got := pendingExecCountForTest(stream); got != 1 {
		t.Fatalf("shell pending execs = %d, want untouched", got)
	}
}
