package forwarder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// mode 是投影的必经字段：一旦无法解析，ProjectCheckpointProjection 会整体报错，
// 磁盘上仍在的 entries 全部无法送达前端，表现为「整个会话凭空消失」。
// 这里锁定两件事：写入路径不会产出不可解析的 mode；读取路径能把脏 mode 拉回可用值。

func TestConversationModeAliasNeverBreaksProjection(t *testing.T) {
	entries := func(t *testing.T) []HistoryEntry {
		t.Helper()
		return []HistoryEntry{
			testCheckpointUserEntry(t),
			newAssistantTextEntry(1, "request-1", "hi", "", ""),
		}
	}

	assertProjectable := func(t *testing.T, store *ConversationFileStore, conversationID string) {
		t.Helper()
		loaded, err := store.LoadConversation(conversationID)
		if err != nil {
			t.Fatalf("LoadConversation() error = %v", err)
		}
		if loaded.Mode != defaultConversationModeAlias {
			t.Fatalf("loaded mode = %q, want %q", loaded.Mode, defaultConversationModeAlias)
		}
		projection, err := NewHistoryProjector().ProjectCheckpointProjection(loaded)
		if err != nil {
			t.Fatalf("ProjectCheckpointProjection() error = %v, want history to survive (%d entries on disk)", err, len(loaded.Entries))
		}
		if len(projection.State.GetTurns()) == 0 {
			t.Fatalf("projected turns = 0, want history to survive")
		}
	}

	// 会话首次落盘时若没有任何 mode 输入，兜底构造必须自带 agent，
	// 否则 state.json 会写出空 mode，之后每次读取都投影失败。
	t.Run("write paths always persist a parsable mode", func(t *testing.T) {
		cases := []struct {
			name  string
			write func(t *testing.T, store *ConversationFileStore, entries []HistoryEntry)
		}{
			{
				name: "ReplaceEntries on fresh conversation",
				write: func(t *testing.T, store *ConversationFileStore, entries []HistoryEntry) {
					if _, err := store.ReplaceEntries("conversation-1", entries, nil); err != nil {
						t.Fatalf("ReplaceEntries() error = %v", err)
					}
				},
			},
			{
				name: "SaveConversationWithEntries with nil source",
				write: func(t *testing.T, store *ConversationFileStore, entries []HistoryEntry) {
					if _, err := store.SaveConversationWithEntries("conversation-1", nil, entries); err != nil {
						t.Fatalf("SaveConversationWithEntries() error = %v", err)
					}
				},
			},
			{
				name: "SaveConversationWithEntries with source missing mode",
				write: func(t *testing.T, store *ConversationFileStore, entries []HistoryEntry) {
					source := &ConversationFile{ConversationID: "conversation-1", RootConversationID: "conversation-1"}
					if _, err := store.SaveConversationWithEntries("conversation-1", source, entries); err != nil {
						t.Fatalf("SaveConversationWithEntries() error = %v", err)
					}
				},
			},
			{
				name: "AppendEntries on fresh conversation",
				write: func(t *testing.T, store *ConversationFileStore, entries []HistoryEntry) {
					if _, _, err := store.AppendEntries("conversation-1", entries); err != nil {
						t.Fatalf("AppendEntries() error = %v", err)
					}
				},
			},
		}

		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				root := t.TempDir()
				store := NewConversationFileStore(root)
				testCase.write(t, store, entries(t))

				body, err := os.ReadFile(filepath.Join(root, "conversation-1", "state.json"))
				if err != nil {
					t.Fatalf("read state.json: %v", err)
				}
				var raw map[string]any
				if err := json.Unmarshal(body, &raw); err != nil {
					t.Fatalf("decode state.json: %v", err)
				}
				if mode, _ := raw["mode"].(string); mode != defaultConversationModeAlias {
					t.Fatalf("persisted mode = %q, want %q", mode, defaultConversationModeAlias)
				}
				assertProjectable(t, store, "conversation-1")
			})
		}
	})

	// 历史遗留或被外部改坏的 state.json 仍要能打开：读取归一化把不可解析的 mode
	// 拉回 agent，而不是让整条会话不可见。
	t.Run("load normalizes corrupted mode on disk", func(t *testing.T) {
		for _, corrupted := range []any{"", "   ", "AGENT_MODE_AGENT", "sub-agent", nil, 42} {
			t.Run(fmt.Sprintf("%v", corrupted), func(t *testing.T) {
				root := t.TempDir()
				store := NewConversationFileStore(root)
				seed := &ConversationFile{
					ConversationID:        "conversation-1",
					RootConversationID:    "conversation-1",
					Mode:                  "agent",
					TokenDetailsMaxTokens: projectedConversationMaxTokens,
				}
				if _, err := store.SaveConversationWithEntries("conversation-1", seed, entries(t)); err != nil {
					t.Fatalf("SaveConversationWithEntries() error = %v", err)
				}

				statePath := filepath.Join(root, "conversation-1", "state.json")
				body, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatalf("read state.json: %v", err)
				}
				var raw map[string]any
				if err := json.Unmarshal(body, &raw); err != nil {
					t.Fatalf("decode state.json: %v", err)
				}
				if corrupted == nil {
					delete(raw, "mode")
				} else {
					raw["mode"] = corrupted
				}
				patched, err := json.Marshal(raw)
				if err != nil {
					t.Fatalf("encode state.json: %v", err)
				}
				if err := os.WriteFile(statePath, patched, 0o644); err != nil {
					t.Fatalf("write state.json: %v", err)
				}

				assertProjectable(t, store, "conversation-1")
			})
		}
	})
}

func TestNormalizeConversationModeAlias(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "agent", want: "agent"},
		{raw: "ask", want: "ask"},
		{raw: "plan", want: "plan"},
		{raw: "debug", want: "debug"},
		{raw: "multitask", want: "multitask"},
		{raw: "  PLAN  ", want: "plan"},
		{raw: "", want: defaultConversationModeAlias},
		{raw: "   ", want: defaultConversationModeAlias},
		{raw: "unknown-mode", want: defaultConversationModeAlias},
		{raw: "AGENT_MODE_AGENT", want: defaultConversationModeAlias},
	}
	for _, testCase := range cases {
		if got := normalizeConversationModeAlias(testCase.raw); got != testCase.want {
			t.Errorf("normalizeConversationModeAlias(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}