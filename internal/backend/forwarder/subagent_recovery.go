package forwarder

import (
	"log"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

// subagentInactivityTimeout 是子代理执行桥在完全没有存活信号时的等待上限。
// 任何心跳或控制消息都会顺延这个 deadline，所以持续汇报的长任务不会被误杀；
// 只有客户端既不回结果也不回 stream close 的失联场景才会触发兜底。
const subagentInactivityTimeout = 10 * time.Minute

const subagentRecoveryReasonInactivity = "subagent_inactivity_deadline_exceeded"

func initializePendingSubagentForTracking(pending runtimecore.PendingExec) runtimecore.PendingExec {
	now := time.Now().UTC()
	if pending.OpenedAt.IsZero() {
		pending.OpenedAt = now
	}
	if pending.LastLivenessAt.IsZero() {
		pending.LastLivenessAt = pending.OpenedAt
	}
	if pending.InactivityDeadline.IsZero() {
		pending.InactivityDeadline = pending.LastLivenessAt.Add(subagentInactivityTimeout)
	}
	return pending
}

// scheduleSubagentInactivityRecovery 为子代理执行桥登记失联兜底定时器。
func (service *Service) scheduleSubagentInactivityRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	deadline := pending.InactivityDeadline
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(subagentInactivityTimeout)
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerSubagentInactivity, pending.ExecID),
		time.Until(deadline),
		streamTimerSubagentInactivity,
		pending.ExecID,
		pending.MessageID,
		subagentRecoveryReasonInactivity,
	)
}

// markSubagentLiveness 用一次上行事件顺延失联 deadline。
func markSubagentLiveness(stream *ActiveStream, pending runtimecore.PendingExec) runtimecore.PendingExec {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ExecID) == "" {
		return pending
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok {
		return pending
	}
	now := time.Now().UTC()
	current.LastLivenessAt = now
	current.InactivityDeadline = now.Add(subagentInactivityTimeout)
	stream.PendingExecs[pending.ExecID] = current
	stream.UpdatedAt = now
	return current
}

// recoverSubagentAfterInactivityIfNeeded 只在 deadline 真的已过期时才收口。
// 心跳顺延过 deadline 的情况下，这里会重新登记定时器而不是合成结果。
func (service *Service) recoverSubagentAfterInactivityIfNeeded(stream *ActiveStream, execID string, messageID uint32) error {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return nil
	}
	current, status, found := snapshotPendingExecWithStatus(stream, execID)
	if !found || current.MessageID != messageID || strings.TrimSpace(current.ExecKind) != "subagent" || isTerminalStreamStatus(status) {
		return nil
	}
	if !current.InactivityDeadline.IsZero() && time.Now().UTC().Before(current.InactivityDeadline) {
		service.scheduleSubagentInactivityRecovery(stream.RequestID, current)
		return nil
	}
	return service.recoverSubagentAfterInactivity(stream, current)
}

func (service *Service) recoverSubagentAfterInactivity(stream *ActiveStream, pending runtimecore.PendingExec) error {
	if stream == nil {
		return nil
	}
	pending.InactivityRecoveryScheduled = true
	markExecCompleted(stream, pending)
	log.Printf(
		"forwarder synthetic subagent recovery request_id=%s tool_call_id=%s message_id=%d exec_id=%s last_liveness_at=%s",
		strings.TrimSpace(stream.RequestID),
		strings.TrimSpace(pending.ToolCallID),
		pending.MessageID,
		strings.TrimSpace(pending.ExecID),
		pending.LastLivenessAt.Format(time.RFC3339Nano),
	)
	if err := service.appendToolResult(
		stream,
		pending.ToolCallID,
		deriveToolNameFromPendingExec(pending),
		pending.ArgsJSON,
		buildSyntheticSubagentResultPayload(pending),
		pending.ReasoningContent,
		nil,
	); err != nil {
		return err
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "subagent_stream_stalled", map[string]any{
			"tool_call_id":          pending.ToolCallID,
			"message_id":            pending.MessageID,
			"exec_id":               pending.ExecID,
			"exec_kind":             pending.ExecKind,
			"reason":                subagentRecoveryReasonInactivity,
			"recent_stream_state":   pending.StreamState,
			"opened_at":             pending.OpenedAt,
			"last_liveness_at":      pending.LastLivenessAt,
			"inactivity_deadline":   pending.InactivityDeadline,
			"inactivity_timeout_ms": subagentInactivityTimeout.Milliseconds(),
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func buildSyntheticSubagentResultPayload(pending runtimecore.PendingExec) string {
	return strings.Join([]string{
		"<subagent-incomplete>",
		"Missing terminal subagent result (expected success or error).",
		"The subagent stopped reporting liveness before returning a final message.",
		"It may still be running in the Cursor app client; treat its work as unknown rather than failed.",
		"</subagent-incomplete>",
	}, "\n")
}
