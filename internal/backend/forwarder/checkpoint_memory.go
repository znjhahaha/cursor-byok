package forwarder

import (
	"fmt"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) replaceCheckpointConversation(stream *ActiveStream, conversation *ConversationFile) error {
	if stream == nil {
		return fmt.Errorf("active stream is required")
	}
	if conversation == nil {
		return fmt.Errorf("checkpoint conversation is required")
	}
	stream.mu.Lock()
	stream.CheckpointConversation = cloneConversationFile(conversation)
	stream.mu.Unlock()
	return nil
}

func checkpointConversationInitialized(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.CheckpointConversation != nil
}

func (service *Service) appendCheckpointEntries(stream *ActiveStream, entries []HistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if stream == nil {
		return fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return fmt.Errorf("checkpoint conversation is not initialized")
	}
	appendEntriesInPlace(stream.CheckpointConversation, entries)
	return nil
}

func (service *Service) snapshotCheckpointConversation(stream *ActiveStream) (*ConversationFile, []runtimecore.PendingExec, []runtimecore.PendingInteraction, error) {
	if stream == nil {
		return nil, nil, nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return nil, nil, nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	conversation := cloneConversationFile(stream.CheckpointConversation)
	pendingExecs := make([]runtimecore.PendingExec, 0, len(stream.PendingExecs))
	for _, pending := range stream.PendingExecs {
		pendingExecs = append(pendingExecs, pending)
	}
	pendingInteractions := make([]runtimecore.PendingInteraction, 0, len(stream.PendingInteractions))
	for _, pending := range stream.PendingInteractions {
		pendingInteractions = append(pendingInteractions, pending)
	}
	return conversation, pendingExecs, pendingInteractions, nil
}

// appendConversationEntries 落盘并同步内存权威副本。
// 磁盘 IO 刻意放在 stream.mu 之外：长历史下一次全量重写要 10ms 以上，
// 持锁落盘会连带卡住 exec 输出、心跳与 timer 重排，对外表现就是终端事件迟到或丢失。
// 落盘顺序由 persistMu 负责，内存可见性仍由 mu 负责。
func (service *Service) appendConversationEntries(stream *ActiveStream, conversationID string, entries []HistoryEntry) ([]HistoryEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	if service.store == nil {
		return service.appendCheckpointEntriesInMemory(stream, entries)
	}

	stream.persistMu.Lock()
	defer stream.persistMu.Unlock()

	if !checkpointConversationInitialized(stream) {
		return nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	// 持久化路径会返回权威快照，这里不需要先克隆一份内存态：
	// 克隆结果在 persisted 非空时会被整体丢弃，而这份深拷贝是 O(历史长度)。
	persisted, assigned, err := service.store.AppendEntries(conversationID, resetEntrySequences(entries))
	if err != nil {
		return nil, err
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	if persisted != nil {
		stream.CheckpointConversation = persisted
	} else {
		appendEntriesInPlace(stream.CheckpointConversation, assigned)
	}
	stream.UpdatedAt = time.Now().UTC()
	return assigned, nil
}

func (service *Service) appendCheckpointEntriesInMemory(stream *ActiveStream, entries []HistoryEntry) ([]HistoryEntry, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	assigned := appendEntriesInPlace(stream.CheckpointConversation, entries)
	stream.UpdatedAt = time.Now().UTC()
	return assigned, nil
}

func resetEntrySequences(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	reset := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		next := entry
		next.Seq = 0
		reset = append(reset, next)
	}
	return reset
}

func firstWorkspacePath(paths []string) string {
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func (service *Service) updateConversationMetaAndCheckpoint(stream *ActiveStream, conversationID string, update func(*ConversationFile) error) (*ConversationFile, error) {
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.CheckpointConversation == nil {
		return nil, fmt.Errorf("checkpoint conversation is not initialized")
	}
	conversation := cloneConversationFile(stream.CheckpointConversation)
	if err := update(conversation); err != nil {
		return nil, err
	}
	if err := service.syncConversationRecord(conversationID, conversation); err != nil {
		return nil, err
	}
	stream.CheckpointConversation = conversation
	stream.UpdatedAt = time.Now().UTC()
	return cloneConversationFile(conversation), nil
}
