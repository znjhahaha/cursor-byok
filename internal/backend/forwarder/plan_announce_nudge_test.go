package forwarder

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

// MiniMax-M3 在 plan 模式下偶发「宣布要调 CreatePlan 却直接结束回合」：
// 输出思考 + “我把方案落实到 CreatePlan”后不再产生 tool_use 块，
// 回合被当成正常完成——用户侧表现为对话中途停止。
// nudge 必须只在这种精确形态下触发一次，其余情况照常收口。
func TestMaybeNudgeAnnouncedPlanCallGating(t *testing.T) {
	service := &Service{}

	planStream := &ActiveStream{RequestID: "request-1", Mode: agentv1.AgentMode_AGENT_MODE_PLAN}
	if !service.maybeNudgeAnnouncedPlanCall(planStream, false, "我把方案落实到 CreatePlan。") {
		t.Fatal("plan 模式 + 零工具调用 + 文本点名 CreatePlan 应触发 nudge")
	}
	planStream.mu.Lock()
	directive := planStream.ProviderRecoveryDirective
	used := planStream.PlanAnnounceNudgeUsed
	planStream.mu.Unlock()
	if !used || !strings.Contains(directive, "CreatePlan") {
		t.Fatalf("nudge 应设置一次性标记与恢复指令，used=%v directive=%q", used, directive)
	}
	if service.maybeNudgeAnnouncedPlanCall(planStream, false, "再次宣布 CreatePlan") {
		t.Fatal("同一回合至多补救一次")
	}

	agentStream := &ActiveStream{RequestID: "request-2", Mode: agentv1.AgentMode_AGENT_MODE_AGENT}
	if service.maybeNudgeAnnouncedPlanCall(agentStream, false, "我要调用 CreatePlan") {
		t.Fatal("非 plan 模式不应触发")
	}
	if service.maybeNudgeAnnouncedPlanCall(planStream, true, "调用了别的工具后提到 CreatePlan") {
		t.Fatal("本 pass 已有工具调用时不应触发")
	}
	if service.maybeNudgeAnnouncedPlanCall(planStream, false, "普通总结性回答") {
		t.Fatal("文本未点名 CreatePlan 时不应触发")
	}
}

// 端到端：pass 正常结束、无工具调用、plan 模式、宣布文本 → 回合不收口而是排队
// 恢复 pass；第二次同样偷懒 → 不再补救，回合正常完成。
func TestProviderDoneWithAnnouncedPlanResumesThenCompletes(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	conversation, err := service.store.CreateConversation(
		"conversation-1", agentv1.AgentMode_AGENT_MODE_PLAN, "", "", "conversation-1",
	)
	if err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	stream, err := service.broker.OpenStream(
		"request-1", "conversation-1", 1, "model", "model",
		agentv1.AgentMode_AGENT_MODE_PLAN, "继续",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}

	startAnnouncedPass := func(text string) {
		t.Helper()
		stream.mu.Lock()
		stream.Status = StreamStatusStreaming
		stream.ProviderActive = true
		stream.CurrentProviderToken = 1
		// driveProvider 启动 pass 时会清掉排队中的 action（actor.go pass 启动路径），
		// 模拟第二个 pass 从干净状态开始。
		stream.PendingProviderAction = providerActionNone
		stream.ProviderAccumulatedText = text
		stream.mu.Unlock()
	}

	startAnnouncedPass("我把方案落实到 CreatePlan。")
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	// 拆掉 200ms 恢复定时器：测试环境没有真实 actor，不让它拉起 provider pass。
	service.cancelScheduledProviderResume(stream)

	stream.mu.Lock()
	action := stream.PendingProviderAction
	status := stream.Status
	directive := stream.ProviderRecoveryDirective
	stream.mu.Unlock()
	if action != providerActionResume || !strings.Contains(directive, "CreatePlan") {
		t.Fatalf("宣布未调用应排队恢复 pass，action=%v directive=%q", action, directive)
	}
	if isTerminalStreamStatus(status) {
		t.Fatalf("宣布未调用时回合不应收口，status=%s", status)
	}

	startAnnouncedPass("再次宣布 CreatePlan。")
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("second handleProviderDoneEvent() error = %v", err)
	}
	service.flushCheckpointWork()

	// 第二次不再补救：回合按正常流程收口（终态 checkpoint 待 blob ack）。
	acknowledgeCheckpointBlobs(t, service, stream)
	stream.mu.Lock()
	status = stream.Status
	stream.mu.Unlock()
	if status != StreamStatusCompleted {
		t.Fatalf("第二次宣布后回合应正常完成，status=%s", status)
	}
	entries, err := service.store.LoadConversation("conversation-1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	turnCompleted := false
	for _, entry := range entries.Entries {
		if entry.Kind == "metadata" && strings.Contains(string(entry.Payload), "turn_completed") {
			turnCompleted = true
		}
	}
	if !turnCompleted {
		t.Fatal("第二次宣布后必须写入 turn_completed")
	}
}
