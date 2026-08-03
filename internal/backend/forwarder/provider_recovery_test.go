package forwarder

import (
	"errors"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func TestIncompleteProviderStreamRecoveryGuards(t *testing.T) {
	base := &modeladapter.IncompleteStreamError{Provider: "anthropic", HasText: true}
	tests := []struct {
		name       string
		err        error
		hadTool    bool
		hadPartial bool
		attempts   int
		want       bool
	}{
		{name: "partial text", err: base, want: true},
		{name: "wrapped", err: providerTerminalError{cause: base}, want: true},
		{name: "tool invoked", err: base, hadTool: true},
		{name: "partial tool args", err: base, hadPartial: true},
		{name: "adapter tool activity", err: &modeladapter.IncompleteStreamError{Provider: "anthropic", HasToolActivity: true}},
		{name: "two recovery limit", err: base, attempts: 2},
		{name: "refusal", err: &modeladapter.ProviderRefusalError{Provider: "anthropic"}},
		{name: "generic error", err: errors.New("connection failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, got := shouldRecoverIncompleteProviderStream(test.err, test.hadTool, test.hadPartial, test.attempts)
			if got != test.want {
				t.Fatalf("recoverable = %v, want %v", got, test.want)
			}
		})
	}
}

func TestTransientRecoveryDirectiveDoesNotChangeStablePrefix(t *testing.T) {
	compiled := CompiledConversation{
		Messages:           []modeladapter.Message{{Role: "user", Content: "original"}},
		StableMessageCount: 1,
	}
	withDirective := appendTransientProviderRecoveryDirective(compiled, "  continue now  ")
	if withDirective.StableMessageCount != 1 {
		t.Fatalf("stable message count changed: %d", withDirective.StableMessageCount)
	}
	if len(withDirective.Messages) != 2 || withDirective.Messages[1].Content != "continue now" {
		t.Fatalf("messages = %#v", withDirective.Messages)
	}
	withoutDirective := appendTransientProviderRecoveryDirective(compiled, "")
	if len(withoutDirective.Messages) != 1 {
		t.Fatalf("empty directive leaked a message: %#v", withoutDirective.Messages)
	}
}

func TestActionCommitmentShortStopDetection(t *testing.T) {
	positive := []string{
		"I found the issue. I'll now update the retry loop.",
		"问题已经定位。接下来我会修改流终止检测。",
		"我现在来执行这个修复：",
	}
	for _, text := range positive {
		if !isActionCommitmentShortStop(text) {
			t.Fatalf("expected commitment: %q", text)
		}
	}
	negative := []string{
		"OK",
		"修复已经完成并通过测试。",
		"You can continue manually if needed.",
		string(make([]rune, 601)),
	}
	for _, text := range negative {
		if isActionCommitmentShortStop(text) {
			t.Fatalf("unexpected commitment: %q", text)
		}
	}
	if !isNormalShortStopFinishReason("completed") || isNormalShortStopFinishReason("tool_calls") {
		t.Fatal("finish reason guard mismatch")
	}
}
