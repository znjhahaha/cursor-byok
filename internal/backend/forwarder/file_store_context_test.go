package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func contextTestEntry(turnSeq int64, requestID string, text string) HistoryEntry {
	return newAssistantTextEntry(turnSeq, requestID, text, "", "")
}

// 追加路径必须落成 JSONL 行：第二次追加只写新增行，不重写整个文件。
func TestAppendEntriesWritesIncrementalJSONL(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())

	first, _, err := store.AppendEntries("conversation-1", []HistoryEntry{contextTestEntry(1, "request-1", "first")})
	if err != nil {
		t.Fatalf("first AppendEntries() error = %v", err)
	}
	path := store.contextPath("conversation-1")
	firstInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat context: %v", err)
	}

	if _, _, err := store.AppendEntries("conversation-1", []HistoryEntry{contextTestEntry(1, "request-1", "second")}); err != nil {
		t.Fatalf("second AppendEntries() error = %v", err)
	}
	secondInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat context: %v", err)
	}
	if secondInfo.ModTime().Equal(firstInfo.ModTime()) && secondInfo.Size() == firstInfo.Size() {
		t.Fatal("第二次追加没有写入任何内容")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read context: %v", err)
	}
	entries, err := parseConversationContextBody(body)
	if err != nil {
		t.Fatalf("parse jsonl context: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}

	conversation, err := store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(conversation.Entries) != 2 || first == nil || conversation.Entries[0].Seq != first.Entries[0].Seq {
		t.Fatalf("重读结果与写入不一致: %#v", conversation.Entries)
	}
}

// 旧版单 JSON 对象格式必须继续可读（存量会话兼容）。
func TestParseConversationContextLegacyFormat(t *testing.T) {
	legacy := conversationContextFile{
		SchemaVersion:  conversationSchemaVersion,
		ConversationID: "conversation-1",
		Version:        2,
		UpdatedAt:      time.Now().UTC(),
		Items: []HistoryEntry{
			contextTestEntry(1, "request-1", "legacy entry"),
			{Seq: 2, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: []byte(`{"text":"hi"}`)},
		},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy context: %v", err)
	}
	entries, err := parseConversationContextBody(body)
	if err != nil {
		t.Fatalf("parse legacy context: %v", err)
	}
	if len(entries) != 2 || entries[0].Kind != "assistant_text" {
		t.Fatalf("legacy entries = %#v", entries)
	}
}

// JSONL 结尾的半行是追加中途崩溃的残迹，读取时容忍并丢弃。
func TestParseConversationContextJSONLToleratesTornTail(t *testing.T) {
	full, err := encodeConversationContextJSONL("conversation-1", &ConversationFile{
		Entries: []HistoryEntry{contextTestEntry(1, "request-1", "entry-one")},
	})
	if err != nil {
		t.Fatalf("encode context: %v", err)
	}
	torn := append([]byte(nil), full...)
	torn = append(torn, []byte(`{"seq":2,"turn_seq":1,"kind":"assistant_text","payload":{"te`)...)

	entries, err := parseConversationContextBody(torn)
	if err != nil {
		t.Fatalf("parse torn context: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (torn tail dropped)", len(entries))
	}
}

// 中间行损坏（非法 JSON）必须硬失败：静默跳过会让历史出现不可解释的空洞。
func TestParseConversationContextJSONLRejectsCorruptMiddleLine(t *testing.T) {
	full, err := encodeConversationContextJSONL("conversation-1", &ConversationFile{
		Entries: []HistoryEntry{
			contextTestEntry(1, "request-1", "entry-one"),
			contextTestEntry(1, "request-1", "entry-two"),
		},
	})
	if err != nil {
		t.Fatalf("encode context: %v", err)
	}
	// 中间插入一段被截断的 JSON，其后还有后续行：这不是结尾残迹，必须报错。
	corrupt := append([]byte(nil), full...)
	corrupt = append(corrupt, []byte("{\"seq\":99,\"kind\":\"assistant_text\",\"payload\":{\"te")...)
	corrupt = append(corrupt, '\n')
	corrupt = append(corrupt, []byte("{\"seq\":100,\"kind\":\"assistant_text\"}\n")...)

	if _, err := parseConversationContextBody(corrupt); err == nil {
		t.Fatal("中间损坏行被静默跳过了")
	}
}

// 外部写入（其他进程/外部修改）让缓存失效后，追加必须回退到
// 全量读取 + 全量重写，磁盘语义保持正确。
func TestAppendEntriesFallsBackAfterExternalRewrite(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	if _, _, err := store.AppendEntries("conversation-1", []HistoryEntry{contextTestEntry(1, "request-1", "original")}); err != nil {
		t.Fatalf("first AppendEntries() error = %v", err)
	}

	// 外部进程用旧格式完整重写 context.json，只保留一条更短的历史。
	external := conversationContextFile{
		SchemaVersion:  conversationSchemaVersion,
		ConversationID: "conversation-1",
		Version:        5,
		UpdatedAt:      time.Now().UTC(),
		Items:          []HistoryEntry{{Seq: 5, TurnSeq: 2, RequestID: "request-2", Role: "assistant", Kind: "assistant_text", Payload: []byte(`{"text":"external"}`)}},
	}
	body, err := json.Marshal(external)
	if err != nil {
		t.Fatalf("marshal external context: %v", err)
	}
	if err := os.WriteFile(store.contextPath("conversation-1"), body, 0o644); err != nil {
		t.Fatalf("write external context: %v", err)
	}

	if _, _, err := store.AppendEntries("conversation-1", []HistoryEntry{contextTestEntry(2, "request-2", "after-external")}); err != nil {
		t.Fatalf("second AppendEntries() error = %v", err)
	}
	conversation, err := store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(conversation.Entries) != 2 {
		t.Fatalf("entries = %d, want external + new = 2: %#v", len(conversation.Entries), conversation.Entries)
	}
	if string(conversation.Entries[0].Payload) != `{"text":"external"}` {
		t.Fatalf("外部写入的历史丢失: %#v", conversation.Entries[0])
	}
}

// 搜索读取器必须同时支持两种格式。
func TestSearchReaderSupportsJSONLContext(t *testing.T) {
	root := t.TempDir()
	store := NewConversationFileStore(root)
	if _, _, err := store.AppendEntries("conversation-1", []HistoryEntry{contextTestEntry(1, "request-1", "searchable text")}); err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	entries, ok := readConversationEntriesForSearch(store, "conversation-1")
	if !ok || len(entries) != 1 {
		t.Fatalf("search entries = %#v ok=%t", entries, ok)
	}

	legacyPath := filepath.Join(root, "conversation-2", "context.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := conversationContextFile{Items: []HistoryEntry{contextTestEntry(1, "request-1", "legacy")}}
	body, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, body, 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	legacyEntries, ok := readConversationEntriesForSearch(store, "conversation-2")
	if !ok || len(legacyEntries) != 1 {
		t.Fatalf("legacy search entries = %#v ok=%t", legacyEntries, ok)
	}
}
