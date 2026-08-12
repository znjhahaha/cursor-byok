package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	backgroundTaskParentRequestHeader  = "x-parent-request-id"
	backgroundTaskRootRequestHeader    = "x-root-parent-request-id"
	backgroundTaskParentToolCallHeader = "x-parent-agent-tool-call-id"
	backgroundTaskDirectMetaHeader     = "x-direct-meta-parent-child-subagent"
)

func readBackgroundTaskRequestMetadata(header http.Header) backgroundTaskRequestMetadata {
	if header == nil {
		return backgroundTaskRequestMetadata{}
	}
	return backgroundTaskRequestMetadata{
		ParentRequestID:     strings.TrimSpace(header.Get(backgroundTaskParentRequestHeader)),
		RootParentRequestID: strings.TrimSpace(header.Get(backgroundTaskRootRequestHeader)),
		ParentToolCallID:    strings.TrimSpace(header.Get(backgroundTaskParentToolCallHeader)),
		DirectMeta:          strings.EqualFold(strings.TrimSpace(header.Get(backgroundTaskDirectMetaHeader)), "true"),
	}
}

func (service *Service) observeBackgroundTaskRequest(requestID string, metadata backgroundTaskRequestMetadata) error {
	if service == nil || service.backgroundTasks == nil {
		return nil
	}
	task, found, err := service.backgroundTasks.ObserveChildRequest(requestID, metadata)
	if err != nil || !found {
		return err
	}
	service.debug.LogRuntime(context.Background(), requestID, task.ChildConversationID, "background_subagent_request_linked", map[string]any{
		"background_task_id":  task.ID,
		"parent_conversation": task.ParentConversationID,
		"parent_request_id":   task.ParentRequestID,
		"parent_tool_call_id": task.ParentToolCallID,
		"root_parent_request": task.RootParentRequestID,
		"direct_meta":         metadata.DirectMeta,
	})
	return nil
}

func (service *Service) bindBackgroundTaskConversation(intent InboundIntent) error {
	if service == nil || service.backgroundTasks == nil || strings.TrimSpace(intent.ConversationID) == "" {
		return nil
	}
	task, found, err := service.backgroundTasks.BindChildConversation(intent.RequestID, intent.ConversationID, intent.SubagentTypeName)
	if err != nil || !found {
		return err
	}
	rootConversationID := task.ParentConversationID
	if service.store != nil {
		if parent, loadErr := service.store.LoadConversation(task.ParentConversationID); loadErr != nil {
			return loadErr
		} else if parent != nil {
			rootConversationID = firstNonEmpty(parent.RootConversationID, parent.ConversationID, rootConversationID)
		}
		_, err = service.store.UpdateConversationMeta(intent.ConversationID, func(conversation *ConversationFile) error {
			conversation.ParentConversationID = task.ParentConversationID
			conversation.ParentToolCallID = task.ParentToolCallID
			conversation.RootConversationID = rootConversationID
			conversation.SubagentTypeName = firstNonEmpty(intent.SubagentTypeName, task.SubagentTypeName)
			return nil
		})
		if err != nil {
			return err
		}
	}
	service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "background_subagent_conversation_bound", map[string]any{
		"background_task_id":     task.ID,
		"parent_conversation_id": task.ParentConversationID,
		"parent_tool_call_id":    task.ParentToolCallID,
		"root_conversation_id":   rootConversationID,
	})
	return nil
}

func (service *Service) registerBackgroundSubagentTask(stream *ActiveStream, pending runtimecore.PendingExec, invocation runtimecore.ToolInvocation, message *agentv1.AgentServerMessage) error {
	if service == nil || service.backgroundTasks == nil || stream == nil || strings.TrimSpace(invocation.ToolName) != "Task" {
		return nil
	}
	subagentArgs := message.GetExecServerMessage().GetSubagentArgs()
	if subagentArgs == nil || !subagentArgs.GetRunInBackground() {
		return nil
	}
	var args struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		SubagentType string `json:"subagent_type"`
	}
	_ = json.Unmarshal(invocation.ArgsJSON, &args)
	stream.mu.Lock()
	rootParentRequestID := firstNonEmpty(stream.RequestMetadata.RootParentRequestID, stream.RequestID)
	stream.mu.Unlock()
	_, err := service.backgroundTasks.Register(BackgroundSubagentTask{
		ParentConversationID: strings.TrimSpace(stream.ConversationID),
		ParentRequestID:      strings.TrimSpace(stream.RequestID),
		RootParentRequestID:  rootParentRequestID,
		ParentModelCallID:    strings.TrimSpace(invocation.ModelCallID),
		ParentToolCallID:     strings.TrimSpace(pending.ToolCallID),
		SubagentTypeName:     firstNonEmpty(strings.TrimSpace(args.SubagentType), strings.TrimSpace(subagentArgs.GetSubagentType())),
		Description:          strings.TrimSpace(args.Description),
		Prompt:               strings.TrimSpace(args.Prompt),
		Status:               BackgroundTaskStatusAccepted,
	})
	return err
}

func (service *Service) observeBackgroundSubagentAck(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) error {
	if service == nil || service.backgroundTasks == nil || stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || message == nil {
		return nil
	}
	result := message.GetSubagentResult()
	if result == nil {
		return nil
	}
	success := result.GetSuccess()
	if success == nil || success.GetBackgroundReason() == agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_UNSPECIFIED {
		return nil
	}
	_, _, err := service.backgroundTasks.MarkRunning(stream.RequestID, pending.ToolCallID, success.GetAgentId())
	return err
}

func (service *Service) completeBackgroundSubagentSuccess(conversationID string, finalMessage string) {
	service.completeBackgroundSubagent(conversationID, BackgroundTaskStatusCompleted, finalMessage, "")
}

func (service *Service) completeBackgroundSubagentError(conversationID string, errorText string) {
	service.completeBackgroundSubagent(conversationID, BackgroundTaskStatusError, "", errorText)
}

func (service *Service) completeBackgroundSubagentCanceled(conversationID string, reason string) {
	service.completeBackgroundSubagent(conversationID, BackgroundTaskStatusCanceled, "", reason)
}

func (service *Service) completeBackgroundSubagent(conversationID string, status BackgroundTaskStatus, finalMessage string, errorText string) {
	if service == nil || service.backgroundTasks == nil || strings.TrimSpace(conversationID) == "" {
		return
	}
	task, changed, err := service.backgroundTasks.CompleteChild(conversationID, status, finalMessage, errorText)
	if err != nil {
		log.Printf("forwarder persist background subagent terminal failed conversation_id=%s err=%v", strings.TrimSpace(conversationID), err)
		return
	}
	if !changed {
		return
	}
	service.debug.LogRuntime(context.Background(), task.ChildRequestID, conversationID, "background_subagent_terminal", map[string]any{
		"background_task_id":     task.ID,
		"parent_conversation_id": task.ParentConversationID,
		"parent_tool_call_id":    task.ParentToolCallID,
		"status":                 task.Status,
	})
	service.notifyBackgroundTaskCompletion(task)
}

func (service *Service) notifyBackgroundTaskCompletion(task BackgroundSubagentTask) {
	if service == nil || service.broker == nil || strings.TrimSpace(task.ParentRequestID) == "" {
		return
	}
	parentStream, ok := service.broker.Get(task.ParentRequestID)
	if !ok || parentStream == nil || isTerminalIntentStream(parentStream) {
		return
	}
	if err := service.postStreamCommandAsync(parentStream, streamCommand{
		Kind:       streamCommandBackgroundTaskCompleted,
		Background: &backgroundTaskCompletionEvent{Task: task},
	}); err != nil && !errors.Is(err, errProviderLoopInterrupted) {
		log.Printf("forwarder post background completion failed parent_request_id=%s task_id=%s err=%v", strings.TrimSpace(task.ParentRequestID), strings.TrimSpace(task.ID), err)
	}
}

func (service *Service) handleCancelSubagentIntent(intent InboundIntent) error {
	if service == nil || service.backgroundTasks == nil {
		return nil
	}
	targetID := strings.TrimSpace(intent.CancelSubagentID)
	if targetID == "" {
		return nil
	}
	reason := firstNonEmpty(strings.TrimSpace(intent.CancelReason), "Background subagent was canceled by the user.")
	task, found, changed, err := service.backgroundTasks.CancelSubagent(targetID, reason)
	if err != nil || !found {
		return err
	}
	if task.Status != BackgroundTaskStatusCanceled {
		return nil
	}
	reason = firstNonEmpty(task.Error, reason)
	parentAwaiting := service.parentStreamAwaitsBackgroundTask(task)
	var reopenErr error
	if parentAwaiting {
		if reopened, _, err := service.backgroundTasks.ReopenCanceledCompletionForAwait(task.ID, task.ParentRequestID); err != nil {
			reopenErr = err
		} else if strings.TrimSpace(reopened.ID) != "" {
			task = reopened
		}
	}
	var cancelErr error
	if childStream, ok := service.broker.Get(task.ChildRequestID); ok && childStream != nil {
		if err := service.postStreamCommandWait(childStream, streamCommand{
			Kind: streamCommandCancel,
			Intent: InboundIntent{
				Kind:         "cancel",
				RequestID:    task.ChildRequestID,
				CancelReason: reason,
			},
		}); err != nil && !errors.Is(err, errProviderLoopInterrupted) {
			cancelErr = err
		}
	}
	persistErr := service.persistCanceledBackgroundTaskResult(task, reason)
	if parentAwaiting && reopenErr == nil {
		service.notifyBackgroundTaskCompletion(task)
	}
	service.debug.LogRuntime(context.Background(), task.ChildRequestID, task.ChildConversationID, "background_subagent_canceled", map[string]any{
		"background_task_id":     task.ID,
		"parent_conversation_id": task.ParentConversationID,
		"parent_tool_call_id":    task.ParentToolCallID,
		"subagent_id":            targetID,
		"state_changed":          changed,
	})
	return errors.Join(cancelErr, persistErr, reopenErr)
}

func (service *Service) parentStreamAwaitsBackgroundTask(task BackgroundSubagentTask) bool {
	if service == nil || service.broker == nil || strings.TrimSpace(task.ParentRequestID) == "" || strings.TrimSpace(task.ID) == "" {
		return false
	}
	parentStream, ok := service.broker.Get(task.ParentRequestID)
	if !ok || parentStream == nil {
		return false
	}
	parentStream.mu.Lock()
	defer parentStream.mu.Unlock()
	if isTerminalStreamStatus(parentStream.Status) {
		return false
	}
	for _, wait := range parentStream.BackgroundTaskWaits {
		if wait != nil && !wait.Completed && strings.TrimSpace(wait.LedgerTaskID) == strings.TrimSpace(task.ID) {
			return true
		}
	}
	return false
}

func (service *Service) persistCanceledBackgroundTaskResult(task BackgroundSubagentTask, reason string) error {
	if service == nil || service.store == nil || strings.TrimSpace(task.ParentConversationID) == "" {
		return nil
	}
	updated, err := service.store.mutateConversation(task.ParentConversationID, false, func(conversation *ConversationFile) error {
		for index := len(conversation.Entries) - 1; index >= 0; index-- {
			entry := conversation.Entries[index]
			if strings.TrimSpace(entry.Kind) != "tool_result" || strings.TrimSpace(entry.ToolCallID) != strings.TrimSpace(task.ParentToolCallID) {
				continue
			}
			var payload toolResultEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return err
			}
			toolCall := &agentv1.ToolCall{}
			if len(payload.ToolCall) > 0 {
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return err
				}
			}
			if toolCall.GetTaskToolCall() == nil {
				continue
			}
			toolCall.GetTaskToolCall().Result = &agentv1.TaskResult{
				Result: &agentv1.TaskResult_Error{
					Error: &agentv1.TaskError{Error: firstNonEmpty(strings.TrimSpace(reason), "Background subagent was canceled by the user.")},
				},
			}
			encoded, err := protojson.Marshal(toolCall)
			if err != nil {
				return err
			}
			payload.ResultText = "background subagent canceled"
			payload.ToolCall = encoded
			entry.Payload, err = json.Marshal(payload)
			if err != nil {
				return err
			}
			conversation.Entries[index] = entry
			return nil
		}
		return nil
	})
	if err != nil || updated == nil {
		return err
	}
	for _, requestID := range service.broker.OtherConversationRequestIDs(task.ParentConversationID, "") {
		parentStream, ok := service.broker.Get(requestID)
		if !ok || parentStream == nil {
			continue
		}
		if err := service.replaceCheckpointConversation(parentStream, updated); err != nil {
			return err
		}
		if isTerminalIntentStream(parentStream) {
			continue
		}
		if err := service.publishCheckpoint(requestID, task.ParentConversationID); err != nil {
			return err
		}
	}
	return nil
}

func backgroundTaskClientID(task BackgroundSubagentTask) string {
	return firstNonEmpty(task.ClientTaskID, task.SubagentID, task.ID)
}

func backgroundTaskCompletionFromLedger(task BackgroundSubagentTask) *agentv1.BackgroundTaskCompletion {
	status := agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS
	detail := strings.TrimSpace(task.FinalMessage)
	switch task.Status {
	case BackgroundTaskStatusCanceled:
		status = agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ABORTED
		detail = firstNonEmpty(task.Error, "Background subagent was canceled.")
	case BackgroundTaskStatusError:
		status = agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ERROR
		detail = firstNonEmpty(task.Error, "Background subagent failed.")
	default:
		if detail == "" {
			detail = "(The subagent returned no final message.)"
		}
	}
	taskID := backgroundTaskClientID(task)
	subagentID := firstNonEmpty(task.SubagentID, task.ChildConversationID, task.ID)
	threadID := firstNonEmpty(task.ChildConversationID, subagentID)
	return &agentv1.BackgroundTaskCompletion{
		TaskId:     taskID,
		Kind:       agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Status:     status,
		Title:      firstNonEmpty(task.Description, task.SubagentTypeName, "Background subagent"),
		Detail:     stringPtr(detail),
		ThreadId:   stringPtr(threadID),
		Reason:     agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
		SubagentId: stringPtr(subagentID),
		ToolCallId: stringPtr(task.ParentToolCallID),
	}
}

func (service *Service) deferSuccessfulTurnForBackgroundTasks(stream *ActiveStream, completion pendingTurnCompletion) (pendingTurnCompletion, bool, error) {
	if service == nil || service.backgroundTasks == nil || stream == nil {
		return completion, false, nil
	}
	stream.mu.Lock()
	mode := stream.Mode
	conversationID := strings.TrimSpace(stream.ConversationID)
	requestID := strings.TrimSpace(stream.RequestID)
	modelCallID := firstNonEmpty(strings.TrimSpace(completion.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID))
	stream.mu.Unlock()
	if mode != agentv1.AgentMode_AGENT_MODE_MULTITASK || conversationID == "" || requestID == "" {
		return completion, false, nil
	}

	tasks, err := service.backgroundTasks.AwaitableTasksForRequest(conversationID, requestID)
	if err != nil {
		return completion, false, err
	}
	now := time.Now().UTC()
	started := make([]*BackgroundTaskWaitState, 0, len(tasks))
	terminal := make([]BackgroundSubagentTask, 0, len(tasks))
	outstanding := false

	stream.mu.Lock()
	if stream.BackgroundTaskWaits == nil {
		stream.BackgroundTaskWaits = make(map[string]*BackgroundTaskWaitState)
	}
	mergedCompletion := mergePendingProviderCompletion(stream.BackgroundTaskPendingCompletion, completion)
	for _, task := range tasks {
		taskID := backgroundTaskClientID(task)
		wait := stream.BackgroundTaskWaits[taskID]
		if wait == nil {
			for _, candidate := range stream.BackgroundTaskWaits {
				if candidate != nil && strings.TrimSpace(candidate.LedgerTaskID) == strings.TrimSpace(task.ID) {
					wait = candidate
					break
				}
			}
		}
		if wait == nil {
			wait = &BackgroundTaskWaitState{
				TaskID:          taskID,
				LedgerTaskID:    strings.TrimSpace(task.ID),
				AwaitToolCallID: "background-await-" + strings.TrimSpace(task.ID),
				ModelCallID:     modelCallID,
				StartedAt:       now,
			}
			stream.BackgroundTaskWaits[taskID] = wait
			started = append(started, wait)
		}
		if wait.Completed {
			continue
		}
		outstanding = true
		if isTerminalBackgroundTaskStatus(task.Status) {
			terminal = append(terminal, task)
		}
	}
	if !outstanding {
		for _, wait := range stream.BackgroundTaskWaits {
			if wait != nil && !wait.Completed {
				outstanding = true
				break
			}
		}
	}
	if !outstanding && len(started) == 0 && len(terminal) == 0 {
		stream.BackgroundTaskPendingCompletion = nil
		stream.BackgroundTaskWaits = make(map[string]*BackgroundTaskWaitState)
		stream.UpdatedAt = now
		stream.mu.Unlock()
		return mergedCompletion, false, nil
	}
	stream.BackgroundTaskPendingCompletion = &mergedCompletion
	stream.Phase = TurnPhaseWaitingExternal
	stream.UpdatedAt = now
	stream.mu.Unlock()

	for _, wait := range started {
		if err := service.broker.Publish(requestID, StreamEvent{
			Message: buildToolCallStartedMessage(wait.AwaitToolCallID, wait.ModelCallID, buildAwaitTaskToolCall(wait.TaskID, nil)),
		}); err != nil {
			return mergedCompletion, true, err
		}
	}
	if len(started) > 0 {
		if err := service.publishCheckpoint(requestID, conversationID); err != nil {
			return mergedCompletion, true, err
		}
	}
	processedTerminal := false
	for _, task := range terminal {
		if err := service.handleBackgroundTaskCompletionEvent(stream, &backgroundTaskCompletionEvent{Task: task}); err != nil {
			return mergedCompletion, true, err
		}
		processedTerminal = true
	}
	return mergedCompletion, outstanding || processedTerminal, nil
}

func (service *Service) handleBackgroundTaskCompletionEvent(stream *ActiveStream, event *backgroundTaskCompletionEvent) error {
	if service == nil || service.backgroundTasks == nil || stream == nil || event == nil {
		return nil
	}
	task := event.Task
	if !isTerminalBackgroundTaskStatus(task.Status) {
		return nil
	}
	clientTaskID := backgroundTaskClientID(task)
	stream.mu.Lock()
	wait := stream.BackgroundTaskWaits[clientTaskID]
	if wait == nil {
		for _, candidate := range stream.BackgroundTaskWaits {
			if candidate != nil && strings.TrimSpace(candidate.LedgerTaskID) == strings.TrimSpace(task.ID) {
				wait = candidate
				break
			}
		}
	}
	if wait == nil || wait.Completed {
		stream.mu.Unlock()
		return nil
	}
	requestID := strings.TrimSpace(stream.RequestID)
	conversationID := strings.TrimSpace(stream.ConversationID)
	turnSeq := stream.TurnSeq
	awaitToolCallID := strings.TrimSpace(wait.AwaitToolCallID)
	awaitTaskID := strings.TrimSpace(wait.TaskID)
	modelCallID := firstNonEmpty(strings.TrimSpace(wait.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID))
	startedAt := wait.StartedAt
	stream.mu.Unlock()

	completion := backgroundTaskCompletionFromLedger(task)
	claimed, claimedCount, err := service.claimBackgroundCompletions(conversationID, []*agentv1.BackgroundTaskCompletion{completion}, requestID)
	if err != nil {
		return err
	}
	if claimedCount == 0 || len(claimed) == 0 {
		return nil
	}
	completion = claimed[0]
	completion.TaskId = awaitTaskID
	entry, ok, err := newBackgroundSubagentCompletionEntry(turnSeq, requestID, completion)
	if err != nil || !ok {
		return err
	}
	assigned, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{entry})
	if err != nil {
		return err
	}

	stream.mu.Lock()
	if current := stream.BackgroundTaskWaits[clientTaskID]; current != nil {
		current.Completed = true
	} else {
		wait.Completed = true
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	// canonical 结果已入历史，立即在 ledger 上闭合这一条，避免父流后续取消/失败时
	// 按 requestID 释放 claim 把它回滚成待派发。
	if err := service.backgroundTasks.ConfirmCompletionClaimForTask(strings.TrimSpace(task.ID), requestID); err != nil {
		return err
	}

	if len(assigned) > 0 {
		if err := service.broker.Publish(requestID, StreamEvent{
			Message: buildUserMessageAppendedMessage(newBackgroundSubagentCompletionUserMessage(completion)),
		}); err != nil {
			return err
		}
	}
	var awaitResult *agentv1.AwaitResult
	if strings.TrimSpace(awaitTaskID) == "" {
		awaitTaskID = clientTaskID
	}
	if task.Status == BackgroundTaskStatusCompleted {
		awaitResult = buildAwaitTaskCompleteResult(awaitTaskID, startedAt, len(strings.TrimSpace(completion.GetDetail())))
	} else {
		awaitResult = buildAwaitTaskErrorResult(firstNonEmpty(strings.TrimSpace(task.Error), strings.TrimSpace(completion.GetDetail())))
	}
	if err := service.publishToolCallCompleted(requestID, awaitToolCallID, modelCallID, buildAwaitTaskToolCall(awaitTaskID, awaitResult)); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(requestID, conversationID); err != nil {
		return err
	}
	return service.requestProviderAction(stream, providerActionResume)
}

func (service *Service) closeBackgroundTaskWaits(stream *ActiveStream, reason string) error {
	if service == nil || stream == nil {
		return nil
	}
	reason = firstNonEmpty(strings.TrimSpace(reason), "parent turn stopped")
	stream.mu.Lock()
	requestID := strings.TrimSpace(stream.RequestID)
	waits := make([]BackgroundTaskWaitState, 0, len(stream.BackgroundTaskWaits))
	for _, wait := range stream.BackgroundTaskWaits {
		if wait == nil || wait.Completed {
			continue
		}
		waits = append(waits, *wait)
	}
	stream.BackgroundTaskWaits = make(map[string]*BackgroundTaskWaitState)
	stream.BackgroundTaskPendingCompletion = nil
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	var closeErr error
	for _, wait := range waits {
		taskID := strings.TrimSpace(wait.TaskID)
		if err := service.publishToolCallCompleted(
			requestID,
			strings.TrimSpace(wait.AwaitToolCallID),
			strings.TrimSpace(wait.ModelCallID),
			buildAwaitTaskToolCall(taskID, buildAwaitTaskErrorResult(reason)),
		); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

func (service *Service) startBackgroundTaskRecovery() {
	if service == nil || service.backgroundTasks == nil {
		return
	}
	claims, err := service.backgroundTasks.InterruptedCompletionClaims()
	if err != nil {
		log.Printf("forwarder load interrupted background completion claims failed: %v", err)
		return
	}
	resolvedRequests := make(map[string]struct{}, len(claims))
	for _, task := range claims {
		requestID := strings.TrimSpace(task.CompletionContinuationID)
		if requestID == "" {
			continue
		}
		if _, resolved := resolvedRequests[requestID]; resolved {
			continue
		}
		resolvedRequests[requestID] = struct{}{}
		confirmed, confirmErr := service.backgroundCompletionConfirmationPersisted(task.ParentConversationID, requestID)
		if confirmErr != nil {
			log.Printf("forwarder inspect interrupted background completion claim failed request_id=%s err=%v", requestID, confirmErr)
			continue
		}
		if confirmed {
			if err := service.backgroundTasks.ConfirmCompletionClaim(requestID); err != nil {
				log.Printf("forwarder recover confirmed background completion claim failed request_id=%s err=%v", requestID, err)
			}
			continue
		}
		if err := service.backgroundTasks.ReleaseCompletionClaim(requestID); err != nil {
			log.Printf("forwarder release interrupted background completion claim failed request_id=%s err=%v", requestID, err)
		}
	}
}

func (service *Service) backgroundCompletionConfirmationPersisted(parentConversationID string, requestID string) (bool, error) {
	if service == nil || service.store == nil {
		return false, nil
	}
	conversation, err := service.store.LoadConversation(strings.TrimSpace(parentConversationID))
	if err != nil || conversation == nil {
		return false, err
	}
	requestID = strings.TrimSpace(requestID)
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.RequestID) != requestID || strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Type) == "background_completion_confirmed" {
			return true, nil
		}
	}
	return false, nil
}

func (service *Service) persistBackgroundCompletionConfirmation(stream *ActiveStream, conversationID string, requestID string) error {
	if service == nil || service.backgroundTasks == nil || stream == nil {
		return nil
	}
	hasClaim, err := service.backgroundTasks.HasCompletionClaim(requestID)
	if err != nil || !hasClaim {
		return err
	}
	entry := newMetadataEntry(stream.TurnSeq, requestID, "background_completion_confirmed", map[string]any{
		"continuation_request_id": strings.TrimSpace(requestID),
	})
	entry.IdempotencyKey = "background-completion-confirmed:" + strings.TrimSpace(requestID)
	_, err = service.appendConversationEntries(stream, conversationID, []HistoryEntry{entry})
	return err
}

func (service *Service) resolveBackgroundCompletionParentConversation(completions []*agentv1.BackgroundTaskCompletion) (string, error) {
	if service == nil || service.backgroundTasks == nil || len(completions) == 0 {
		return "", nil
	}
	return service.backgroundTasks.ParentConversationIDForCompletions(completions)
}

func (service *Service) claimBackgroundCompletions(parentConversationID string, completions []*agentv1.BackgroundTaskCompletion, requestID string) ([]*agentv1.BackgroundTaskCompletion, int, error) {
	if service == nil || service.backgroundTasks == nil || len(completions) == 0 {
		return completions, 0, nil
	}
	claimed, claimedTaskCount, err := service.backgroundTasks.ClaimCompletions(parentConversationID, completions, requestID)
	if err != nil {
		return nil, 0, err
	}
	service.debug.LogRuntime(context.Background(), requestID, parentConversationID, "background_completions_claimed", map[string]any{
		"client_completion_count":  len(completions),
		"claimed_completion_count": len(claimed),
		"claimed_ledger_count":     claimedTaskCount,
	})
	return claimed, claimedTaskCount, nil
}

func (service *Service) confirmBackgroundCompletionClaim(requestID string) {
	if service == nil || service.backgroundTasks == nil {
		return
	}
	if err := service.backgroundTasks.ConfirmCompletionClaim(requestID); err != nil {
		log.Printf("forwarder confirm background completion claim failed request_id=%s err=%v", requestID, err)
	}
}

func (service *Service) releaseBackgroundCompletionClaim(requestID string) {
	if service == nil || service.backgroundTasks == nil {
		return
	}
	if err := service.backgroundTasks.ReleaseCompletionClaim(requestID); err != nil {
		log.Printf("forwarder release background completion claim failed request_id=%s err=%v", requestID, err)
	}
}
