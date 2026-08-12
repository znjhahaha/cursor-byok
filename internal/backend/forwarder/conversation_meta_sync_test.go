package forwarder

import (
	"os"
	"testing"
)

// state.json 是 context.json 的派生投影，meta 同步只应重写 state.json。
// 一旦它连带把整条历史原样重写一遍，长对话里每个工具结果都要付一次全量写盘，
// 这正是「越聊越卡」的来源，因此这里把「不碰 context.json」钉成回归。
func TestSyncConversationRecordWritesMetaWithoutRewritingContext(t *testing.T) {
	const conversationID = "conversation-meta-sync"
	service := &Service{
		store:     NewConversationFileStore(t.TempDir()),
		projector: NewHistoryProjector(),
		broker:    NewStreamBroker(),
	}
	persisted, _, err := service.store.AppendEntries(conversationID, []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		newAssistantTextEntry(1, "request-1", "second answer", "", ""),
	})
	if err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	contextPath := service.store.contextPath(conversationID)
	contextBefore, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read context.json error = %v", err)
	}

	snapshot := cloneConversationFile(persisted)
	snapshot.SubagentTypeName = "explore"
	snapshot.TokenDetailsUsedTokens = 4242
	snapshot.AutoCompactionPending = true
	// 归零 schema version：派生逻辑只允许作用在写盘副本上，
	// 如果它回写到调用方快照，这里会被改成当前版本号。
	snapshot.SchemaVersion = 0
	snapshotEntryCount := len(snapshot.Entries)

	if err := service.syncConversationRecord(conversationID, snapshot); err != nil {
		t.Fatalf("syncConversationRecord() error = %v", err)
	}

	contextAfter, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read context.json error = %v", err)
	}
	if string(contextAfter) != string(contextBefore) {
		t.Fatalf("syncConversationRecord() 重写了 context.json")
	}
	if snapshot.SchemaVersion != 0 || len(snapshot.Entries) != snapshotEntryCount {
		t.Fatalf("syncConversationRecord() 修改了调用方快照: schema=%d entries=%d", snapshot.SchemaVersion, len(snapshot.Entries))
	}

	loaded, err := service.store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadConversation() returned nil conversation")
	}
	if loaded.SubagentTypeName != "explore" {
		t.Fatalf("subagent type = %q, want %q", loaded.SubagentTypeName, "explore")
	}
	if loaded.TokenDetailsUsedTokens != 4242 {
		t.Fatalf("used tokens = %d, want 4242", loaded.TokenDetailsUsedTokens)
	}
	if !loaded.AutoCompactionPending {
		t.Fatal("auto compaction pending = false, want true")
	}
	if loaded.SchemaVersion != conversationSchemaVersion {
		t.Fatalf("schema version = %d, want %d", loaded.SchemaVersion, conversationSchemaVersion)
	}
	if len(loaded.Entries) != snapshotEntryCount {
		t.Fatalf("entries = %d, want %d", len(loaded.Entries), snapshotEntryCount)
	}
}

// meta 同步过去靠 CreateConversation 兜底建记录。改成只写 state.json 后，
// 尚未落盘的会话必须仍能被后续追加正常接续，否则第一轮就会丢历史。
func TestSyncConversationRecordCreatesStateForUnknownConversation(t *testing.T) {
	const conversationID = "conversation-meta-sync-fresh"
	service := &Service{
		store:     NewConversationFileStore(t.TempDir()),
		projector: NewHistoryProjector(),
		broker:    NewStreamBroker(),
	}
	if err := service.syncConversationRecord(conversationID, &ConversationFile{
		ConversationID:     conversationID,
		RootConversationID: conversationID,
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}); err != nil {
		t.Fatalf("syncConversationRecord() error = %v", err)
	}
	if exists, err := fileExists(service.store.statePath(conversationID)); err != nil || !exists {
		t.Fatalf("state.json exists = %v, err = %v", exists, err)
	}
	if exists, err := fileExists(service.store.contextPath(conversationID)); err != nil || exists {
		t.Fatalf("context.json exists = %v, err = %v; meta 同步不应创建 context.json", exists, err)
	}

	appended, _, err := service.store.AppendEntries(conversationID, []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "answer after meta only state", "", ""),
	})
	if err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	if appended == nil || len(appended.Entries) != 1 {
		t.Fatalf("entries after append = %v, want 1", appended)
	}
	if appended.Mode != "agent" {
		t.Fatalf("mode = %q, want %q", appended.Mode, "agent")
	}
}

// state.json 是可重建的派生投影，所以它不再 fsync —— 代价是崩溃后它可能缺失或写坏。
// 这时 context.json 仍是权威历史，会话必须能被完整重建，而不是变成一个空会话。
func TestLoadConversationRebuildsMetaFromContextEntries(t *testing.T) {
	tests := []struct {
		name       string
		damageMeta func(t *testing.T, path string)
	}{
		{
			name: "meta missing",
			damageMeta: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove state.json error = %v", err)
				}
			},
		},
		{
			name: "meta truncated",
			damageMeta: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"conversation_id":`), 0o600); err != nil {
					t.Fatalf("write broken state.json error = %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const conversationID = "conversation-meta-rebuild"
			store := NewConversationFileStore(t.TempDir())
			persisted, _, err := store.AppendEntries(conversationID, []HistoryEntry{
				newAssistantTextEntry(3, "request-3", "first answer", "", ""),
				newAssistantTextEntry(3, "request-3", "second answer", "", ""),
			})
			if err != nil {
				t.Fatalf("AppendEntries() error = %v", err)
			}
			test.damageMeta(t, store.statePath(conversationID))

			loaded, err := store.LoadConversation(conversationID)
			if err != nil {
				t.Fatalf("LoadConversation() error = %v", err)
			}
			if loaded == nil {
				t.Fatal("meta 损坏被当成会话不存在，历史会被下一次写入覆盖")
			}
			if len(loaded.Entries) != len(persisted.Entries) {
				t.Fatalf("entries = %d, want %d", len(loaded.Entries), len(persisted.Entries))
			}
			// 序号只能从 entries 反推，否则续写会从 1 开始并覆盖既有历史。
			if loaded.NextEntrySeq != persisted.NextEntrySeq || loaded.NextTurnSeq != persisted.NextTurnSeq {
				t.Fatalf(
					"next seq = (entry %d, turn %d), want (entry %d, turn %d)",
					loaded.NextEntrySeq, loaded.NextTurnSeq,
					persisted.NextEntrySeq, persisted.NextTurnSeq,
				)
			}
			// mode 是投影必经字段，缺失时必须回落而不是留空。
			if loaded.Mode != defaultConversationModeAlias {
				t.Fatalf("mode = %q, want %q", loaded.Mode, defaultConversationModeAlias)
			}
			if loaded.ConversationID != conversationID || loaded.RootConversationID != conversationID {
				t.Fatalf("ids = (%q, %q), want %q", loaded.ConversationID, loaded.RootConversationID, conversationID)
			}

			appended, _, err := store.AppendEntries(conversationID, []HistoryEntry{
				newAssistantTextEntry(4, "request-4", "answer after rebuild", "", ""),
			})
			if err != nil {
				t.Fatalf("AppendEntries() after rebuild error = %v", err)
			}
			if len(appended.Entries) != len(persisted.Entries)+1 {
				t.Fatalf("重建后续写丢历史：entries = %d, want %d", len(appended.Entries), len(persisted.Entries)+1)
			}
		})
	}
}

// context.json 才是权威历史。它损坏时必须硬失败：
// 若当成空历史继续，后续写入就会用空内容盖掉本来还能抢救的文件。
func TestLoadConversationFailsOnCorruptContext(t *testing.T) {
	const conversationID = "conversation-context-corrupt"
	store := NewConversationFileStore(t.TempDir())
	if _, _, err := store.AppendEntries(conversationID, []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "answer", "", ""),
	}); err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	if err := os.WriteFile(store.contextPath(conversationID), []byte(`{"items":`), 0o600); err != nil {
		t.Fatalf("write broken context.json error = %v", err)
	}
	if _, err := store.LoadConversation(conversationID); err == nil {
		t.Fatal("context.json 损坏时 LoadConversation 必须报错，不能静默返回空历史")
	}
}
