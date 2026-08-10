package execbridge

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenTaskBackgroundConfigFollowsConversationMode(t *testing.T) {
	tests := []struct {
		name           string
		mode           agentv1.AgentMode
		wantBackground bool
	}{
		{name: "agent remains synchronous", mode: agentv1.AgentMode_AGENT_MODE_AGENT},
		{name: "multitask runs in background", mode: agentv1.AgentMode_AGENT_MODE_MULTITASK, wantBackground: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverMessage, pending, err := NewBridge().OpenExec(OpenExecContext{
				ConversationID:     "parent-conversation",
				RootConversationID: "root-conversation",
				ModelID:            "parent-model",
				Mode:               test.mode,
			}, runtimecore.ToolInvocation{
				CallID:   "task-call",
				ToolName: "Task",
				ArgsJSON: []byte(`{"subagent_type":"explore","prompt":"inspect the code","readonly":true}`),
			})
			if err != nil {
				t.Fatalf("OpenExec() error = %v", err)
			}
			args := serverMessage.GetExecServerMessage().GetSubagentArgs()
			if args == nil {
				t.Fatal("SubagentArgs is nil")
			}
			if pending.ExecKind != "subagent" || pending.ToolCallID != "task-call" {
				t.Fatalf("pending = %#v", pending)
			}
			if args.GetParentConversationId() != "parent-conversation" {
				t.Fatalf("parent conversation = %q", args.GetParentConversationId())
			}
			if args.GetRootParentConversationId() != "root-conversation" {
				t.Fatalf("root parent conversation = %q", args.GetRootParentConversationId())
			}
			if !args.GetDirectMetaParentChildSubagent() {
				t.Fatal("direct metadata parent-child binding was not enabled")
			}

			if !test.wantBackground {
				if args.RunInBackground != nil {
					t.Fatalf("RunInBackground = %v, want unset", args.GetRunInBackground())
				}
				if args.GetContinuationConfig() != nil {
					t.Fatal("ContinuationConfig should remain unset outside Multitask")
				}
				return
			}

			if args.RunInBackground == nil || !args.GetRunInBackground() {
				t.Fatal("RunInBackground was not enabled for Multitask")
			}
			config := args.GetContinuationConfig()
			if config == nil {
				t.Fatal("ContinuationConfig is nil for Multitask")
			}
			if config.GetMaxLoops() != 1 {
				t.Fatalf("MaxLoops = %d, want 1", config.GetMaxLoops())
			}
			if !config.GetCollectBackgroundChildren() {
				t.Fatal("CollectBackgroundChildren was not enabled")
			}
			if !strings.Contains(config.GetChildrenCompletedMessageTemplate(), "{summaries}") {
				t.Fatalf("children template = %q", config.GetChildrenCompletedMessageTemplate())
			}
		})
	}
}

func TestApplyBackgroundSubagentAckIsTerminal(t *testing.T) {
	bridge := NewBridge()
	result, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     7,
		ExecId: "exec-subagent",
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Success{
					Success: &agentv1.SubagentSuccess{
						AgentId:          "child-agent",
						BackgroundReason: agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_AGENT_REQUEST,
					},
				},
			},
		},
	}, runtimecore.PendingExec{
		MessageID:  7,
		ExecID:     "exec-subagent",
		ToolCallID: "task-call",
		ExecKind:   "subagent",
		ArgsJSON:   []byte(`{"subagent_type":"explore","prompt":"inspect"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage() error = %v", err)
	}
	if !result.IsTerminal {
		t.Fatal("background handoff ack did not release the pending exec")
	}
	if result.ToolCall.GetTaskToolCall().GetResult().GetSuccess().GetIsBackground() != true {
		t.Fatalf("task result = %#v, want background success", result.ToolCall)
	}
	if !strings.Contains(result.ToolResultPayload, "subagent running in background") {
		t.Fatalf("tool result payload = %q", result.ToolResultPayload)
	}
}
