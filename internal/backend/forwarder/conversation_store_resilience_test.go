package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// state.json 只保存可从 context.json 反推的派生元数据（写入时 Entries 被置空），
// context.json 才是对话内容的真相来源。历史上 state.json 一旦缺失或损坏，
// readConversationLocked 会直接返回错误/空，导致 context.json 中完好的历史
// 整体无法送达前端——表现就是「改动都还在，但对话从界面上消失了」。
// 崩溃或强杀（中断对话后重启）最容易留下半截 state.json，因此这里逐一锁定。

func TestConversationSurvivesUnusableStateFile(t *testing.T) {
	seedConversation := func(t *testing.T, store *ConversationFileStore) {
		t.Helper()
		seed := &ConversationFile{
			ConversationID:        "conversation-1",
			RootConversationID:    "conversation-1",
			Mode:                  "plan",
			TokenDetailsMaxTokens: projectedConversationMaxTokens,
		}
		entries := []HistoryEntry{
			testCheckpointUserEntry(t),
			newAssistantTextEntry(1, "request-1", "hi", "", ""),
		}
		if _, err := store.SaveConversationWithEntries("conversation-1", seed, entries); err != nil {
			t.Fatalf("SaveConversationWithEntries() error = %v", err)
		}
	}

	damage := map[string]func(t *testing.T, statePath string){
		"truncated mid-write": func(t *testing.T, statePath string) {
			body, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatalf("read state.json: %v", err)
			}
			if err := os.WriteFile(statePath, body[:len(body)/2], 0o644); err != nil {
				t.Fatalf("write state.json: %v", err)
			}
		},
		"zero length after crash": func(t *testing.T, statePath string) {
			if err := os.WriteFile(statePath, nil, 0o644); err != nil {
				t.Fatalf("write state.json: %v", err)
			}
		},
		"removed": func(t *testing.T, statePath string) {
			if err := os.Remove(statePath); err != nil {
				t.Fatalf("remove state.json: %v", err)
			}
		},
		"wrong field type": func(t *testing.T, statePath string) {
			if err := os.WriteFile(statePath, []byte(`{"mode":42}`), 0o644); err != nil {
				t.Fatalf("write state.json: %v", err)
			}
		},
	}

	for name, breakState := range damage {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := NewConversationFileStore(root)
			seedConversation(t, store)
			breakState(t, filepath.Join(root, "conversation-1", "state.json"))

			loaded, err := store.LoadConversation("conversation-1")
			if err != nil {
				t.Fatalf("LoadConversation() error = %v, want history to be recovered from context.json", err)
			}
			if loaded == nil {
				t.Fatalf("LoadConversation() = nil, want conversation rebuilt from context.json")
			}
			if len(loaded.Entries) != 2 {
				t.Fatalf("recovered entries = %d, want 2", len(loaded.Entries))
			}
			projection, err := NewHistoryProjector().ProjectCheckpointProjection(loaded)
			if err != nil {
				t.Fatalf("ProjectCheckpointProjection() error = %v", err)
			}
			if len(projection.State.GetTurns()) == 0 {
				t.Fatalf("projected turns = 0, want history to reach the UI")
			}
		})
	}
}

// 会话真的不存在时不能被上面的重建逻辑伪造出来。
func TestLoadConversationReportsMissingConversation(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	loaded, err := store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if loaded != nil {
		t.Fatalf("LoadConversation() = %#v, want nil for a conversation that was never created", loaded)
	}
}

// 临时文件必须在 rename 前落盘，否则崩溃后 rename 可能已生效而内容仍是空的，
// 这正是「中断后重启对话消失」的源头。
func TestWriteJSONFileAtomicPersistsContentBeforeRename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	payload := map[string]any{"conversation_id": "conversation-1", "mode": "agent"}
	if err := writeJSONFileAtomic(path, payload); err != nil {
		t.Fatalf("writeJSONFileAtomic() error = %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode written file: %v", err)
	}
	if decoded["mode"] != "agent" {
		t.Fatalf("decoded mode = %v, want agent", decoded["mode"])
	}

	leftovers, err := filepath.Glob(filepath.Join(root, "*.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind = %v, want none", leftovers)
	}
}