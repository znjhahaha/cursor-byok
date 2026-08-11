package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cursor/gen/agentv1"
)

const (
	backgroundTaskFileName          = "background_tasks.json"
	backgroundTaskFileSchemaVersion = 1
)

type BackgroundTaskStatus string

const (
	BackgroundTaskStatusAccepted  BackgroundTaskStatus = "accepted"
	BackgroundTaskStatusRunning   BackgroundTaskStatus = "running"
	BackgroundTaskStatusCompleted BackgroundTaskStatus = "completed"
	BackgroundTaskStatusCanceled  BackgroundTaskStatus = "canceled"
	BackgroundTaskStatusError     BackgroundTaskStatus = "error"
)

type BackgroundSubagentTask struct {
	ID                       string               `json:"id"`
	ParentConversationID     string               `json:"parent_conversation_id"`
	ParentRequestID          string               `json:"parent_request_id"`
	RootParentRequestID      string               `json:"root_parent_request_id,omitempty"`
	ParentModelCallID        string               `json:"parent_model_call_id,omitempty"`
	ParentToolCallID         string               `json:"parent_tool_call_id"`
	ChildRequestID           string               `json:"child_request_id,omitempty"`
	ChildConversationID      string               `json:"child_conversation_id,omitempty"`
	SubagentID               string               `json:"subagent_id,omitempty"`
	SubagentTypeName         string               `json:"subagent_type_name,omitempty"`
	Description              string               `json:"description,omitempty"`
	Prompt                   string               `json:"prompt,omitempty"`
	Status                   BackgroundTaskStatus `json:"status"`
	FinalMessage             string               `json:"final_message,omitempty"`
	Error                    string               `json:"error,omitempty"`
	CompletionContinuationID string               `json:"completion_continuation_id,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
	UpdatedAt                time.Time            `json:"updated_at"`
	CompletedAt              time.Time            `json:"completed_at,omitempty"`
	CompletionInjectedAt     time.Time            `json:"completion_injected_at,omitempty"`
}

type backgroundTaskRequestMetadata struct {
	ParentRequestID     string
	RootParentRequestID string
	ParentToolCallID    string
	DirectMeta          bool
}

type backgroundTaskFileDocument struct {
	SchemaVersion int                               `json:"schema_version"`
	UpdatedAt     time.Time                         `json:"updated_at"`
	Tasks         map[string]BackgroundSubagentTask `json:"tasks"`
}

type backgroundTaskLedgerCorruptionError struct {
	cause error
}

func (err backgroundTaskLedgerCorruptionError) Error() string {
	return err.cause.Error()
}

func (err backgroundTaskLedgerCorruptionError) Unwrap() error {
	return err.cause
}

type BackgroundTaskFileStore struct {
	path string
}

func NewBackgroundTaskFileStore(historyRoot string) *BackgroundTaskFileStore {
	return &BackgroundTaskFileStore{path: filepath.Join(strings.TrimSpace(historyRoot), backgroundTaskFileName)}
}

func backgroundTaskID(parentRequestID string, parentToolCallID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(parentRequestID) + "\x00" + strings.TrimSpace(parentToolCallID)))
	return "background-subagent-" + hex.EncodeToString(digest[:12])
}

func (store *BackgroundTaskFileStore) Register(task BackgroundSubagentTask) (BackgroundSubagentTask, error) {
	task.ParentConversationID = strings.TrimSpace(task.ParentConversationID)
	task.ParentRequestID = strings.TrimSpace(task.ParentRequestID)
	task.ParentToolCallID = strings.TrimSpace(task.ParentToolCallID)
	if task.ParentConversationID == "" || task.ParentRequestID == "" || task.ParentToolCallID == "" {
		return BackgroundSubagentTask{}, fmt.Errorf("background task parent conversation, request, and tool call are required")
	}
	if task.ID == "" {
		task.ID = backgroundTaskID(task.ParentRequestID, task.ParentToolCallID)
	}
	return store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		now := time.Now().UTC()
		if existing, ok := document.Tasks[task.ID]; ok {
			task = mergeBackgroundSubagentTask(existing, task)
		}
		if task.CreatedAt.IsZero() {
			task.CreatedAt = now
		}
		if task.Status == "" {
			task.Status = BackgroundTaskStatusAccepted
		}
		task.UpdatedAt = now
		document.Tasks[task.ID] = task
		return task, nil
	})
}

func (store *BackgroundTaskFileStore) ObserveChildRequest(requestID string, metadata backgroundTaskRequestMetadata) (BackgroundSubagentTask, bool, error) {
	requestID = strings.TrimSpace(requestID)
	metadata.ParentRequestID = strings.TrimSpace(metadata.ParentRequestID)
	metadata.RootParentRequestID = strings.TrimSpace(metadata.RootParentRequestID)
	metadata.ParentToolCallID = strings.TrimSpace(metadata.ParentToolCallID)
	if requestID == "" || metadata.ParentRequestID == "" || metadata.ParentToolCallID == "" {
		return BackgroundSubagentTask{}, false, nil
	}
	var found bool
	task, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		for id, candidate := range document.Tasks {
			if candidate.ParentRequestID != metadata.ParentRequestID || candidate.ParentToolCallID != metadata.ParentToolCallID {
				continue
			}
			candidate.ChildRequestID = requestID
			candidate.RootParentRequestID = firstNonEmpty(metadata.RootParentRequestID, candidate.RootParentRequestID)
			if !isTerminalBackgroundTaskStatus(candidate.Status) {
				candidate.Status = BackgroundTaskStatusRunning
			}
			candidate.UpdatedAt = time.Now().UTC()
			document.Tasks[id] = candidate
			found = true
			return candidate, nil
		}
		return BackgroundSubagentTask{}, nil
	})
	return task, found, err
}

func (store *BackgroundTaskFileStore) BindChildConversation(requestID string, conversationID string, subagentTypeName string) (BackgroundSubagentTask, bool, error) {
	requestID = strings.TrimSpace(requestID)
	conversationID = strings.TrimSpace(conversationID)
	if requestID == "" || conversationID == "" {
		return BackgroundSubagentTask{}, false, nil
	}
	var found bool
	task, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		for id, candidate := range document.Tasks {
			if candidate.ChildRequestID != requestID {
				continue
			}
			candidate.ChildConversationID = conversationID
			candidate.SubagentTypeName = firstNonEmpty(strings.TrimSpace(subagentTypeName), candidate.SubagentTypeName)
			if !isTerminalBackgroundTaskStatus(candidate.Status) {
				candidate.Status = BackgroundTaskStatusRunning
			}
			candidate.UpdatedAt = time.Now().UTC()
			document.Tasks[id] = candidate
			found = true
			return candidate, nil
		}
		return BackgroundSubagentTask{}, nil
	})
	return task, found, err
}

func (store *BackgroundTaskFileStore) MarkRunning(parentRequestID string, parentToolCallID string, subagentID string) (BackgroundSubagentTask, bool, error) {
	id := backgroundTaskID(parentRequestID, parentToolCallID)
	var found bool
	task, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		candidate, ok := document.Tasks[id]
		if !ok {
			return BackgroundSubagentTask{}, nil
		}
		found = true
		candidate.SubagentID = firstNonEmpty(strings.TrimSpace(subagentID), candidate.SubagentID)
		if !isTerminalBackgroundTaskStatus(candidate.Status) {
			candidate.Status = BackgroundTaskStatusRunning
		}
		candidate.UpdatedAt = time.Now().UTC()
		document.Tasks[id] = candidate
		return candidate, nil
	})
	return task, found, err
}

func (store *BackgroundTaskFileStore) CancelSubagent(subagentID string, reason string) (BackgroundSubagentTask, bool, bool, error) {
	subagentID = strings.TrimSpace(subagentID)
	if subagentID == "" {
		return BackgroundSubagentTask{}, false, false, nil
	}
	var found bool
	var changed bool
	task, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		bestID := ""
		bestScore := 0
		var candidate BackgroundSubagentTask
		for id, item := range document.Tasks {
			score := backgroundTaskCancellationMatchScore(item, subagentID)
			if score == 0 || score < bestScore || (score == bestScore && bestID != "" && id >= bestID) {
				continue
			}
			bestID = id
			bestScore = score
			candidate = item
		}
		if bestID == "" {
			return BackgroundSubagentTask{}, nil
		}
		found = true
		if isTerminalBackgroundTaskStatus(candidate.Status) {
			return candidate, nil
		}
		now := time.Now().UTC()
		candidate.Status = BackgroundTaskStatusCanceled
		candidate.FinalMessage = ""
		candidate.Error = firstNonEmpty(strings.TrimSpace(reason), "Background subagent was canceled by the user.")
		candidate.CompletedAt = now
		candidate.CompletionContinuationID = ""
		candidate.CompletionInjectedAt = now
		candidate.UpdatedAt = now
		document.Tasks[bestID] = candidate
		changed = true
		return candidate, nil
	})
	return task, found, changed, err
}

func (store *BackgroundTaskFileStore) ActiveTasks(parentConversationID string) ([]BackgroundSubagentTask, error) {
	parentConversationID = strings.TrimSpace(parentConversationID)
	if store == nil || strings.TrimSpace(store.path) == "" {
		return nil, nil
	}
	document, err := store.load()
	if err != nil {
		return nil, err
	}
	tasks := make([]BackgroundSubagentTask, 0)
	for _, task := range document.Tasks {
		if parentConversationID != "" && strings.TrimSpace(task.ParentConversationID) != parentConversationID {
			continue
		}
		if task.Status == BackgroundTaskStatusAccepted || task.Status == BackgroundTaskStatusRunning {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks, nil
}

func backgroundTaskCancellationMatchScore(task BackgroundSubagentTask, subagentID string) int {
	subagentID = strings.TrimSpace(subagentID)
	if subagentID == "" {
		return 0
	}
	switch {
	case strings.TrimSpace(task.SubagentID) == subagentID:
		return 500
	case strings.TrimSpace(task.ChildConversationID) == subagentID:
		return 400
	case strings.TrimSpace(task.ChildRequestID) == subagentID:
		return 300
	case strings.TrimSpace(task.ID) == subagentID:
		return 200
	default:
		return 0
	}
}

func (store *BackgroundTaskFileStore) CompleteChild(conversationID string, status BackgroundTaskStatus, finalMessage string, errorText string) (BackgroundSubagentTask, bool, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || !isTerminalBackgroundTaskStatus(status) {
		return BackgroundSubagentTask{}, false, nil
	}
	var changed bool
	task, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		for id, candidate := range document.Tasks {
			if candidate.ChildConversationID != conversationID {
				continue
			}
			if isTerminalBackgroundTaskStatus(candidate.Status) {
				return candidate, nil
			}
			now := time.Now().UTC()
			candidate.Status = status
			candidate.FinalMessage = strings.TrimSpace(finalMessage)
			candidate.Error = strings.TrimSpace(errorText)
			candidate.CompletedAt = now
			candidate.UpdatedAt = now
			document.Tasks[id] = candidate
			changed = true
			return candidate, nil
		}
		return BackgroundSubagentTask{}, nil
	})
	return task, changed, err
}

func (store *BackgroundTaskFileStore) PendingCompletions(parentConversationID string) ([]BackgroundSubagentTask, error) {
	parentConversationID = strings.TrimSpace(parentConversationID)
	if store == nil || strings.TrimSpace(store.path) == "" || parentConversationID == "" {
		return nil, nil
	}
	document, err := store.load()
	if err != nil {
		return nil, err
	}
	tasks := make([]BackgroundSubagentTask, 0)
	for _, task := range document.Tasks {
		if task.ParentConversationID == parentConversationID && isTerminalBackgroundTaskStatus(task.Status) && task.CompletionInjectedAt.IsZero() {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CompletedAt.Equal(tasks[j].CompletedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CompletedAt.Before(tasks[j].CompletedAt)
	})
	return tasks, nil
}

func (store *BackgroundTaskFileStore) ClaimCompletions(parentConversationID string, completions []*agentv1.BackgroundTaskCompletion, continuationRequestID string) ([]*agentv1.BackgroundTaskCompletion, int, error) {
	parentConversationID = strings.TrimSpace(parentConversationID)
	continuationRequestID = strings.TrimSpace(continuationRequestID)
	if len(completions) == 0 || parentConversationID == "" || continuationRequestID == "" {
		return nil, 0, nil
	}
	claimedCompletions := make([]*agentv1.BackgroundTaskCompletion, 0, len(completions))
	claimedTaskCount := 0
	claimedTaskIDs := make(map[string]struct{}, len(completions))
	_, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		now := time.Now().UTC()
		for _, completion := range completions {
			taskID, task, matched := findBestBackgroundTaskMatch(document.Tasks, parentConversationID, completion)
			if !matched {
				claimedCompletions = append(claimedCompletions, completion)
				continue
			}
			if _, duplicate := claimedTaskIDs[taskID]; duplicate {
				continue
			}
			claimedTaskIDs[taskID] = struct{}{}
			if !isTerminalBackgroundTaskStatus(task.Status) || !task.CompletionInjectedAt.IsZero() {
				continue
			}
			if claimID := strings.TrimSpace(task.CompletionContinuationID); claimID != "" && claimID != continuationRequestID {
				continue
			}
			task.CompletionContinuationID = continuationRequestID
			task.UpdatedAt = now
			document.Tasks[taskID] = task
			claimedCompletions = append(claimedCompletions, backgroundTaskCompletionFromLedger(task))
			claimedTaskCount++
		}
		return BackgroundSubagentTask{}, nil
	})
	return claimedCompletions, claimedTaskCount, err
}

func (store *BackgroundTaskFileStore) ParentConversationIDForCompletions(completions []*agentv1.BackgroundTaskCompletion) (string, error) {
	if store == nil || len(completions) == 0 {
		return "", nil
	}
	document, err := store.load()
	if err != nil {
		return "", err
	}
	parentConversationID := ""
	for _, completion := range completions {
		_, task, matched := findBestBackgroundTaskMatch(document.Tasks, "", completion)
		if !matched {
			continue
		}
		candidate := strings.TrimSpace(task.ParentConversationID)
		if candidate == "" {
			continue
		}
		if parentConversationID != "" && parentConversationID != candidate {
			return "", fmt.Errorf("background completions resolve to multiple parent conversations")
		}
		parentConversationID = candidate
	}
	return parentConversationID, nil
}

func (store *BackgroundTaskFileStore) ConfirmCompletionClaim(continuationRequestID string) error {
	continuationRequestID = strings.TrimSpace(continuationRequestID)
	if continuationRequestID == "" {
		return nil
	}
	_, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		now := time.Now().UTC()
		for id, task := range document.Tasks {
			if strings.TrimSpace(task.CompletionContinuationID) != continuationRequestID || !task.CompletionInjectedAt.IsZero() {
				continue
			}
			task.CompletionInjectedAt = now
			task.UpdatedAt = now
			document.Tasks[id] = task
		}
		return BackgroundSubagentTask{}, nil
	})
	return err
}

func (store *BackgroundTaskFileStore) ReleaseCompletionClaim(continuationRequestID string) error {
	continuationRequestID = strings.TrimSpace(continuationRequestID)
	if continuationRequestID == "" {
		return nil
	}
	_, err := store.mutate(func(document *backgroundTaskFileDocument) (BackgroundSubagentTask, error) {
		now := time.Now().UTC()
		for id, task := range document.Tasks {
			if strings.TrimSpace(task.CompletionContinuationID) != continuationRequestID || !task.CompletionInjectedAt.IsZero() {
				continue
			}
			task.CompletionContinuationID = ""
			task.UpdatedAt = now
			document.Tasks[id] = task
		}
		return BackgroundSubagentTask{}, nil
	})
	return err
}

func (store *BackgroundTaskFileStore) HasCompletionClaim(continuationRequestID string) (bool, error) {
	continuationRequestID = strings.TrimSpace(continuationRequestID)
	if store == nil || continuationRequestID == "" {
		return false, nil
	}
	document, err := store.load()
	if err != nil {
		return false, err
	}
	for _, task := range document.Tasks {
		if strings.TrimSpace(task.CompletionContinuationID) == continuationRequestID && task.CompletionInjectedAt.IsZero() {
			return true, nil
		}
	}
	return false, nil
}

func (store *BackgroundTaskFileStore) InterruptedCompletionClaims() ([]BackgroundSubagentTask, error) {
	if store == nil {
		return nil, nil
	}
	document, err := store.load()
	if err != nil {
		return nil, err
	}
	tasks := make([]BackgroundSubagentTask, 0)
	for _, task := range document.Tasks {
		if strings.TrimSpace(task.CompletionContinuationID) == "" || !task.CompletionInjectedAt.IsZero() {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].ParentConversationID == tasks[j].ParentConversationID {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].ParentConversationID < tasks[j].ParentConversationID
	})
	return tasks, nil
}

func findBestBackgroundTaskMatch(tasks map[string]BackgroundSubagentTask, parentConversationID string, completion *agentv1.BackgroundTaskCompletion) (string, BackgroundSubagentTask, bool) {
	parentConversationID = strings.TrimSpace(parentConversationID)
	bestID := ""
	bestScore := 0
	var bestTask BackgroundSubagentTask
	for id, task := range tasks {
		if parentConversationID != "" && strings.TrimSpace(task.ParentConversationID) != parentConversationID {
			continue
		}
		score := backgroundTaskMatchScore(task, completion)
		if score == 0 || score < bestScore || (score == bestScore && bestID != "" && id >= bestID) {
			continue
		}
		bestID = id
		bestTask = task
		bestScore = score
	}
	return bestID, bestTask, bestScore > 0
}

func backgroundTaskMatchScore(task BackgroundSubagentTask, completion *agentv1.BackgroundTaskCompletion) int {
	if completion == nil {
		return 0
	}
	score := 0
	taskToolCallID := strings.TrimSpace(task.ParentToolCallID)
	if toolCallID := strings.TrimSpace(completion.GetToolCallId()); toolCallID != "" && toolCallID == taskToolCallID {
		score += 1000
	}
	taskSubagentID := strings.TrimSpace(task.SubagentID)
	taskChildConversationID := strings.TrimSpace(task.ChildConversationID)
	taskID := strings.TrimSpace(task.ID)
	if subagentID := strings.TrimSpace(completion.GetSubagentId()); subagentID != "" {
		switch {
		case taskSubagentID != "" && subagentID == taskSubagentID:
			score += 500
		case taskChildConversationID != "" && subagentID == taskChildConversationID:
			score += 450
		case taskID != "" && subagentID == taskID:
			score += 400
		}
	}
	if completionTaskID := strings.TrimSpace(completion.GetTaskId()); completionTaskID != "" {
		switch {
		case taskSubagentID != "" && completionTaskID == taskSubagentID:
			score += 350
		case taskID != "" && completionTaskID == taskID:
			score += 300
		case taskChildConversationID != "" && completionTaskID == taskChildConversationID:
			score += 250
		}
	}
	if threadID := strings.TrimSpace(completion.GetThreadId()); threadID != "" && threadID == taskChildConversationID && taskChildConversationID != "" {
		score += 200
	}
	return score
}

func isTerminalBackgroundTaskStatus(status BackgroundTaskStatus) bool {
	switch status {
	case BackgroundTaskStatusCompleted, BackgroundTaskStatusCanceled, BackgroundTaskStatusError:
		return true
	default:
		return false
	}
}

func mergeBackgroundSubagentTask(existing BackgroundSubagentTask, incoming BackgroundSubagentTask) BackgroundSubagentTask {
	incoming.ID = firstNonEmpty(incoming.ID, existing.ID)
	incoming.ParentConversationID = firstNonEmpty(incoming.ParentConversationID, existing.ParentConversationID)
	incoming.ParentRequestID = firstNonEmpty(incoming.ParentRequestID, existing.ParentRequestID)
	incoming.RootParentRequestID = firstNonEmpty(incoming.RootParentRequestID, existing.RootParentRequestID)
	incoming.ParentModelCallID = firstNonEmpty(incoming.ParentModelCallID, existing.ParentModelCallID)
	incoming.ParentToolCallID = firstNonEmpty(incoming.ParentToolCallID, existing.ParentToolCallID)
	incoming.ChildRequestID = firstNonEmpty(incoming.ChildRequestID, existing.ChildRequestID)
	incoming.ChildConversationID = firstNonEmpty(incoming.ChildConversationID, existing.ChildConversationID)
	incoming.SubagentID = firstNonEmpty(incoming.SubagentID, existing.SubagentID)
	incoming.SubagentTypeName = firstNonEmpty(incoming.SubagentTypeName, existing.SubagentTypeName)
	incoming.Description = firstNonEmpty(incoming.Description, existing.Description)
	incoming.Prompt = firstNonEmpty(incoming.Prompt, existing.Prompt)
	incoming.CreatedAt = existing.CreatedAt
	if isTerminalBackgroundTaskStatus(existing.Status) {
		incoming.Status = existing.Status
		incoming.FinalMessage = existing.FinalMessage
		incoming.Error = existing.Error
		incoming.CompletedAt = existing.CompletedAt
		incoming.CompletionContinuationID = existing.CompletionContinuationID
		incoming.CompletionInjectedAt = existing.CompletionInjectedAt
	}
	return incoming
}

func (store *BackgroundTaskFileStore) load() (backgroundTaskFileDocument, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return backgroundTaskFileDocument{SchemaVersion: backgroundTaskFileSchemaVersion, Tasks: make(map[string]BackgroundSubagentTask)}, nil
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return backgroundTaskFileDocument{}, fmt.Errorf("create background task directory: %w", err)
	}
	release, err := acquireConversationLock(store.path + ".lock")
	if err != nil {
		return backgroundTaskFileDocument{}, err
	}
	defer release()
	return store.readOrRecoverLocked()
}

func (store *BackgroundTaskFileStore) readOrRecoverLocked() (backgroundTaskFileDocument, error) {
	document, err := readBackgroundTaskFileDocument(store.path)
	if err == nil {
		return document, nil
	}
	var corruption backgroundTaskLedgerCorruptionError
	if !errors.As(err, &corruption) {
		return backgroundTaskFileDocument{}, err
	}
	quarantinePath := fmt.Sprintf("%s.corrupt-%s", store.path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if renameErr := os.Rename(store.path, quarantinePath); renameErr != nil && !errors.Is(renameErr, os.ErrNotExist) {
		return backgroundTaskFileDocument{}, fmt.Errorf("quarantine corrupt background task ledger: %w", renameErr)
	}
	document = backgroundTaskFileDocument{
		SchemaVersion: backgroundTaskFileSchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Tasks:         make(map[string]BackgroundSubagentTask),
	}
	if writeErr := writeJSONFileAtomic(store.path, document); writeErr != nil {
		return backgroundTaskFileDocument{}, fmt.Errorf("reset corrupt background task ledger: %w", writeErr)
	}
	return document, nil
}

func (store *BackgroundTaskFileStore) mutate(update func(*backgroundTaskFileDocument) (BackgroundSubagentTask, error)) (BackgroundSubagentTask, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return BackgroundSubagentTask{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return BackgroundSubagentTask{}, fmt.Errorf("create background task directory: %w", err)
	}
	release, err := acquireConversationLock(store.path + ".lock")
	if err != nil {
		return BackgroundSubagentTask{}, err
	}
	defer release()
	document, err := store.readOrRecoverLocked()
	if err != nil {
		return BackgroundSubagentTask{}, err
	}
	result, err := update(&document)
	if err != nil {
		return BackgroundSubagentTask{}, err
	}
	document.SchemaVersion = backgroundTaskFileSchemaVersion
	document.UpdatedAt = time.Now().UTC()
	if err := writeJSONFileAtomic(store.path, document); err != nil {
		return BackgroundSubagentTask{}, err
	}
	return result, nil
}

func readBackgroundTaskFileDocument(path string) (backgroundTaskFileDocument, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return backgroundTaskFileDocument{SchemaVersion: backgroundTaskFileSchemaVersion, Tasks: make(map[string]BackgroundSubagentTask)}, nil
		}
		return backgroundTaskFileDocument{}, fmt.Errorf("read background task ledger: %w", err)
	}
	var document backgroundTaskFileDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return backgroundTaskFileDocument{}, backgroundTaskLedgerCorruptionError{
			cause: fmt.Errorf("decode background task ledger: %w", err),
		}
	}
	if document.SchemaVersion > backgroundTaskFileSchemaVersion {
		return backgroundTaskFileDocument{}, fmt.Errorf("background task ledger schema %d is newer than supported schema %d", document.SchemaVersion, backgroundTaskFileSchemaVersion)
	}
	if document.Tasks == nil {
		document.Tasks = make(map[string]BackgroundSubagentTask)
	}
	return document, nil
}
