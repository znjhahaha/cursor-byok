package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

// 同一 request_id 复用于新回合（撤回重发）时，终态旧流必须被原地重置：
// 否则 Publish 静默丢弃新回合事件、RunSSE 把旧回合积压连同旧 End 回放出去，
// 客户端表现为新消息气泡消失、内容串到上一条。
func TestOpenStreamResetsTerminalStreamForReuse(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "", "", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := broker.Complete("request-1", "", ""); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	reused, err := broker.OpenStream("request-1", "conversation-1", 3, "", "", agentv1.AgentMode_AGENT_MODE_AGENT, "rewind")
	if err != nil {
		t.Fatalf("OpenStream() reuse error = %v", err)
	}
	if reused != stream {
		t.Fatal("同一 request_id 应原地复用同一流对象")
	}
	reused.mu.Lock()
	status := reused.Status
	backlogLen := len(reused.Backlog)
	reused.mu.Unlock()
	if isTerminalStreamStatus(status) {
		t.Fatalf("复用终态流后状态必须是 Created，实际 %q", status)
	}
	if backlogLen != 0 {
		t.Fatalf("旧回合积压必须清空，实际 %d 条", backlogLen)
	}

	// 重置后 Publish 不再被终态守卫丢弃。
	if err := broker.Publish("request-1", StreamEvent{Message: buildHeartbeatMessage()}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	reused.mu.Lock()
	backlogLen = len(reused.Backlog)
	reused.mu.Unlock()
	if backlogLen != 1 {
		t.Fatalf("重置后事件必须进入积压，实际 %d 条", backlogLen)
	}
}
