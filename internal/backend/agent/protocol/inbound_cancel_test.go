package protocol

import (
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestCancelSubagentActionHasDedicatedCommandKind(t *testing.T) {
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_CancelSubagentAction{
					CancelSubagentAction: &agentv1.CancelSubagentAction{SubagentId: "agent-1"},
				},
			},
		},
	}
	kind, err := MapClientMessageToCommandKind(message, "conversation_action")
	if err != nil {
		t.Fatalf("MapClientMessageToCommandKind() error = %v", err)
	}
	if kind != runtimecore.CommandKindCancelSubagentRequested {
		t.Fatalf("command kind = %q, want %q", kind, runtimecore.CommandKindCancelSubagentRequested)
	}
}
