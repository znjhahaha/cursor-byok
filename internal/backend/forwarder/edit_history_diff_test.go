package forwarder

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func TestCompactEditHistoryResultKeepsBoundedDiffWithoutFullContents(t *testing.T) {
	diffString := strings.Repeat("汉字-diff\n", 6000)
	source := buildSuccessfulEditResult(
		"C:/workspace/example.go",
		strings.Repeat("before\n", 1000),
		strings.Repeat("after\n", 1000),
		diffString,
		17,
		9,
		"edit applied",
	)

	tests := []struct {
		name    string
		limit   int
		compact func(string, *agentv1.EditResult) *agentv1.EditResult
	}{
		{name: "PatchEdit", limit: patchEditHistoryDiffLimitBytes, compact: compactPatchEditHistoryEditResult},
		{name: "Write", limit: writeHistoryDiffLimitBytes, compact: compactWriteHistoryEditResult},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compacted := test.compact("C:/workspace/fallback.go", source)
			success := compacted.GetSuccess()
			if success == nil {
				t.Fatal("compact result has no success payload")
			}
			if got := success.GetPath(); got != "C:/workspace/example.go" {
				t.Fatalf("path = %q, want source path", got)
			}
			if success.BeforeFullFileContent != nil || success.GetAfterFullFileContent() != "" {
				t.Fatalf("full contents leaked into durable result: before=%v after_bytes=%d", success.BeforeFullFileContent != nil, len(success.GetAfterFullFileContent()))
			}
			if got := success.GetDiffString(); got == "" || len(got) > test.limit || !utf8.ValidString(got) {
				t.Fatalf("bounded diff invalid: bytes=%d limit=%d utf8=%v", len(got), test.limit, utf8.ValidString(got))
			}
			if !strings.Contains(success.GetDiffString(), "[truncated:") {
				t.Fatal("oversized diff does not contain truncation notice")
			}
			if success.GetLinesAdded() != 17 || success.GetLinesRemoved() != 9 || success.GetMessage() != "edit applied" {
				t.Fatalf("edit metadata changed: added=%d removed=%d message=%q", success.GetLinesAdded(), success.GetLinesRemoved(), success.GetMessage())
			}
		})
	}
}

func TestCheckpointProjectionKeepsCompactEditDiff(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		compact  func(string, *agentv1.EditResult) *agentv1.EditResult
	}{
		{name: "PatchEdit", toolName: patchEditToolName, compact: compactPatchEditHistoryEditResult},
		{name: "Write", toolName: "Write", compact: compactWriteHistoryEditResult},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				requestID  = "request-edit-diff"
				toolCallID = "tool-call-edit-diff"
				path       = "C:/workspace/example.go"
			)
			liveResult := buildSuccessfulEditResult(path, "before\n", "after\n", "@@ -1 +1 @@\n-before\n+after\n", 1, 1, "edit applied")
			if liveResult.GetSuccess().GetAfterFullFileContent() == "" {
				t.Fatal("live result must retain full content")
			}
			historyResult := test.compact(path, liveResult)
			historyToolCall := buildCompletedEditToolCall(path, historyResult)
			toolCallPayload, err := protojson.Marshal(historyToolCall)
			if err != nil {
				t.Fatalf("marshal history ToolCall: %v", err)
			}
			conversation := &ConversationFile{
				ConversationID:     "conversation-edit-diff",
				RootConversationID: "conversation-edit-diff",
				Mode:               "agent",
				NextTurnSeq:        2,
				Entries: []HistoryEntry{
					testCheckpointUserEntry(t),
					newToolCallEntry(1, requestID, toolCallID, test.toolName, "", "", toolCallPayload),
					newToolResultEntry(1, requestID, toolCallID, test.toolName, `{}`, `{"success":{"path":"C:/workspace/example.go"}}`, "", toolCallPayload),
				},
			}
			projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
			if err != nil {
				t.Fatalf("ProjectCheckpointProjection() error = %v", err)
			}
			projected := projectedCheckpointEditSuccess(t, projection)
			if got, want := projected.GetDiffString(), historyResult.GetSuccess().GetDiffString(); got != want || got == "" {
				t.Fatalf("checkpoint diff = %q, want %q", got, want)
			}
			if projected.BeforeFullFileContent != nil || projected.GetAfterFullFileContent() != "" {
				t.Fatal("checkpoint projection restored full file contents")
			}
		})
	}
}

func TestEditPromptReplayRemainsIndependentlyBounded(t *testing.T) {
	for _, test := range []struct {
		toolName string
		limit    int
	}{
		{toolName: patchEditToolName, limit: projectedPatchEditReplayLimit},
		{toolName: "Write", limit: projectedEditReplayLimit},
	} {
		t.Run(test.toolName, func(t *testing.T) {
			content, err := json.Marshal(map[string]any{
				"success": map[string]any{
					"diff_string": strings.Repeat("重放-diff\n", test.limit),
				},
			})
			if err != nil {
				t.Fatalf("marshal replay payload: %v", err)
			}
			bounded := limitProjectedToolResultReplay(test.toolName, string(content), "", true, false)
			if len(bounded) > test.limit || !utf8.ValidString(bounded) {
				t.Fatalf("prompt replay bytes=%d limit=%d utf8=%v", len(bounded), test.limit, utf8.ValidString(bounded))
			}
		})
	}
}

func projectedCheckpointEditSuccess(t *testing.T, projection *CheckpointProjection) *agentv1.EditSuccess {
	t.Helper()
	if projection == nil || projection.State == nil || len(projection.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint projection has %d turns, want 1", len(projection.State.GetTurns()))
	}
	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(blobs[string(projection.State.GetTurns()[0])], turn); err != nil {
		t.Fatalf("decode turn Blob: %v", err)
	}
	for _, stepID := range turn.GetAgentConversationTurn().GetSteps() {
		step := &agentv1.ConversationStep{}
		if err := proto.Unmarshal(blobs[string(stepID)], step); err != nil {
			t.Fatalf("decode step Blob: %v", err)
		}
		if success := step.GetToolCall().GetEditToolCall().GetResult().GetSuccess(); success != nil {
			return success
		}
	}
	t.Fatal("checkpoint has no completed edit ToolCall")
	return nil
}
