package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

// openRetirementTestStream 构造一个「provider 正在跑」的流：
// 中断闩已 arm、pass cancel 已注册、还挂着待办动作与 resume timer。
// 这正是用户按下停止、或回合自然收口那一刻的真实状态。
func openRetirementTestStream(t *testing.T, service *Service, onPassCancel func()) *ActiveStream {
	t.Helper()
	stream, err := service.broker.OpenStream(
		"request-1", "conversation-1", 1, "model", "model",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	armStreamInterrupt(stream)
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderActive = true
	stream.ProviderCancel = onPassCancel
	stream.PendingProviderAction = providerActionResume
	stream.PendingCompaction = &PendingCompaction{Trigger: "auto"}
	stream.CurrentProviderToken = 5
	stream.CurrentCompactionToken = 3
	stream.TimerTokens = map[string]uint64{
		providerTimerKey(streamTimerProviderResume, ""): 9,
	}
	stream.mu.Unlock()
	return stream
}

func countTerminalEvents(t *testing.T, service *Service, requestID string) (total int, ends int) {
	t.Helper()
	events, err := service.broker.ReadFromCursor(requestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	for _, event := range events {
		if event.End {
			ends++
		}
	}
	return len(events), ends
}

// 终态发布与 worker 退休必须是同一个动作。分成两步时会留下一个窗口：
// UI 已经收到 EndStream，后台 provider 仍在跑，于是「回合结束了还在输出」。
func TestTerminalRetirementDisarmsWorkersBeforeEndStream(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	endEventsWhenCanceled := -1
	var stream *ActiveStream
	stream = openRetirementTestStream(t, service, func() {
		// 掐断 pass 的时刻必须早于 EndStream 入队，这里直接数 backlog 里的终态事件。
		stream.mu.Lock()
		endEventsWhenCanceled = 0
		for _, event := range stream.Backlog {
			if event.End {
				endEventsWhenCanceled++
			}
		}
		stream.mu.Unlock()
	})
	passContext := streamInterruptContext(stream)

	if err := service.broker.Complete("request-1", "", ""); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if endEventsWhenCanceled != 0 {
		t.Fatalf("worker 退休晚于终态发布：取消时已有 %d 条终态事件", endEventsWhenCanceled)
	}
	if passContext.Err() == nil {
		t.Fatal("终态没有掐断本回合的中断闩，迟到 pass 仍可继续运行")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ProviderActive || stream.ProviderCancel != nil {
		t.Fatalf("终态后 provider 仍处于活动状态：active=%v cancel=%v", stream.ProviderActive, stream.ProviderCancel != nil)
	}
	if stream.PendingProviderAction != providerActionNone {
		t.Fatalf("终态后仍留有待办 provider 动作：%q", stream.PendingProviderAction)
	}
	if stream.PendingCompaction != nil {
		t.Fatal("终态后仍留有待办压缩任务")
	}
	if stream.CurrentProviderToken == 5 || stream.CurrentCompactionToken == 3 {
		t.Fatalf(
			"generation 未作废：provider=%d compaction=%d",
			stream.CurrentProviderToken,
			stream.CurrentCompactionToken,
		)
	}
	if _, ok := stream.TimerTokens[providerTimerKey(streamTimerProviderResume, "")]; ok {
		t.Fatal("终态后仍留有 provider resume timer，可能再起一趟 pass")
	}
}

// provider goroutine 与终态是并发的，事件必然会有迟到的。
// 迟到事件只允许被静默丢弃：既不能再输出文本，也不能触发第二次收口。
func TestLateProviderEventsAreDroppedAfterTerminal(t *testing.T) {
	tests := []struct {
		name  string
		event streamProviderEvent
	}{
		{
			name: "text delta",
			event: streamProviderEvent{
				Token: 5,
				Event: modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "迟到的输出"},
			},
		},
		{
			name:  "stream done",
			event: streamProviderEvent{Token: 5, Done: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
			service := newBackgroundCompletionTestService(t, provider)
			stream := openRetirementTestStream(t, service, nil)
			if err := service.broker.Complete("request-1", "", ""); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			totalBefore, endsBefore := countTerminalEvents(t, service, "request-1")

			event := test.event
			if err := service.handleProviderEvent(stream, &event); err != nil {
				t.Fatalf("handleProviderEvent() error = %v", err)
			}

			totalAfter, endsAfter := countTerminalEvents(t, service, "request-1")
			if totalAfter != totalBefore {
				t.Fatalf("迟到事件仍被发布：事件数 %d -> %d", totalBefore, totalAfter)
			}
			if endsAfter != endsBefore || endsAfter != 1 {
				t.Fatalf("终态事件数 = %d，want 恰好 1 条", endsAfter)
			}
		})
	}
}

// 停止可能被重复点击，失败与完成也可能在收口之后才到达。
// 第一个终态即最终结果，其余一律空操作，否则界面会在停止之后再弹一次报错。
func TestRepeatedTerminalRequestsPublishSingleEndStream(t *testing.T) {
	provider := &backgroundCompletionTestProvider{seen: make(chan ProviderRequest, 1)}
	service := newBackgroundCompletionTestService(t, provider)
	stream := openRetirementTestStream(t, service, nil)

	for attempt := 1; attempt <= 3; attempt++ {
		if err := service.broker.Cancel("request-1", "user aborted"); err != nil {
			t.Fatalf("第 %d 次 Cancel error = %v", attempt, err)
		}
	}
	if err := service.broker.Fail("request-1", "unknown", "late failure"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if err := service.broker.Complete("request-1", "", ""); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	events, err := service.broker.ReadFromCursor("request-1", 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	terminals := make([]StreamEvent, 0, 1)
	for _, event := range events {
		if event.End {
			terminals = append(terminals, event)
		}
	}
	if len(terminals) != 1 {
		t.Fatalf("终态事件数 = %d，want 恰好 1 条：%#v", len(terminals), terminals)
	}
	if terminals[0].TerminalErrorCode != "canceled" {
		t.Fatalf("终态被后到的结果覆盖：code = %q", terminals[0].TerminalErrorCode)
	}
	stream.mu.Lock()
	status := stream.Status
	stream.mu.Unlock()
	if status != StreamStatusCanceled {
		t.Fatalf("流状态 = %q，want canceled", status)
	}
}
