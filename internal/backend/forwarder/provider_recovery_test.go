package forwarder

import (
	"errors"
	"testing"

	"cursor/gen/agentv1"
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

// 正常 provider stop 具有权威性。后端曾按模型措辞推断「只承诺没干活」并强制
// 再跑一趟 provider，于是出现「UI 已结束本轮、模型却接着改口」。
// 现在只有协议层面的续跑理由（工具结果、待办完成）才能推进回合。
func TestNormalStopDoesNotResumeProviderInAnyMode(t *testing.T) {
	for _, mode := range []agentv1.AgentMode{
		agentv1.AgentMode_AGENT_MODE_AGENT,
		agentv1.AgentMode_AGENT_MODE_MULTITASK,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
			service := newBackgroundCompletionTestService(t, provider)
			conversation, err := service.store.CreateConversation(
				"conversation-1", mode, "", "", "conversation-1",
			)
			if err != nil {
				t.Fatalf("CreateConversation() error = %v", err)
			}
			stream, err := service.broker.OpenStream(
				"request-1", "conversation-1", 1, "model", "model", mode, "hello",
			)
			if err != nil {
				t.Fatalf("OpenStream() error = %v", err)
			}
			if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
				t.Fatalf("replaceCheckpointConversation() error = %v", err)
			}
			stream.mu.Lock()
			stream.Status = StreamStatusStreaming
			stream.Phase = TurnPhaseProviderRunning
			stream.CurrentModelCallID = "model-call-1"
			stream.CurrentProviderToken = 7
			stream.ProviderActive = true
			// 典型的「行动承诺」收尾：旧实现正是在这种措辞上强制 resume。
			stream.ProviderAccumulatedText = "问题已经定位。接下来我会修改流终止检测。"
			stream.ProviderFinishReason = "stop"
			stream.mu.Unlock()

			if err := service.handleProviderEvent(stream, &streamProviderEvent{Token: 7, Done: true}); err != nil {
				t.Fatalf("handleProviderEvent() error = %v", err)
			}

			stream.mu.Lock()
			pendingAction := stream.PendingProviderAction
			stream.mu.Unlock()
			if pendingAction != providerActionNone {
				t.Fatalf("正常 stop 触发了续跑：pending action = %q", pendingAction)
			}
			if provider.requestCount() != 0 {
				t.Fatalf("正常 stop 之后又向模型发出了 %d 次请求", provider.requestCount())
			}
		})
	}
}
