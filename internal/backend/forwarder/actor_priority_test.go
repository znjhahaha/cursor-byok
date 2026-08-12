package forwarder

import (
	"errors"
	"testing"
)

// 分类表本身就是保序契约：一旦有人把 provider 终态或 timer 挪进 urgent，
// 终态就会插队到同源 delta / exec 输出之前，本测试用于拦住这种改动。
func TestUrgentStreamCommandClassification(t *testing.T) {
	for _, kind := range []streamCommandKind{streamCommandCancel, streamCommandCancelSubagent} {
		if !isUrgentStreamCommand(kind) {
			t.Fatalf("expected %s to use the urgent mailbox", kind)
		}
	}
	for _, kind := range []streamCommandKind{
		streamCommandRun,
		streamCommandMetadata,
		streamCommandExecResult,
		streamCommandExecControl,
		streamCommandInteractionResult,
		streamCommandProviderEvent,
		streamCommandTimerFired,
		streamCommandCompactionEvent,
		streamCommandMaybeOrphaned,
	} {
		if isUrgentStreamCommand(kind) {
			t.Fatalf("expected %s to stay in the ordered mailbox", kind)
		}
	}
}

func TestReceiveStreamCommandPrefersUrgentMailbox(t *testing.T) {
	channels := streamActorChannels{
		normal: make(chan streamCommandEnvelope, 8),
		urgent: make(chan streamCommandEnvelope, 4),
		done:   make(chan struct{}),
	}
	for index := 0; index < 8; index++ {
		channels.normal <- streamCommandEnvelope{command: streamCommand{Kind: streamCommandProviderEvent}}
	}
	channels.urgent <- streamCommandEnvelope{command: streamCommand{Kind: streamCommandCancel}}

	envelope, ok := receiveStreamCommand(channels)
	if !ok {
		t.Fatal("expected a command from the actor mailboxes")
	}
	if envelope.command.Kind != streamCommandCancel {
		t.Fatalf("expected cancel to bypass the queued provider events, got %s", envelope.command.Kind)
	}

	envelope, ok = receiveStreamCommand(channels)
	if !ok || envelope.command.Kind != streamCommandProviderEvent {
		t.Fatalf("expected ordered mailbox drain after urgent is empty, got %s ok=%v", envelope.command.Kind, ok)
	}
	if len(channels.normal) != 7 {
		t.Fatalf("expected 7 provider events still queued, got %d", len(channels.normal))
	}
}

func TestDeliverStreamCommandRoutesByPriority(t *testing.T) {
	channels := streamActorChannels{
		normal: make(chan streamCommandEnvelope, 1),
		urgent: make(chan streamCommandEnvelope, 1),
		done:   make(chan struct{}),
	}
	if err := deliverStreamCommand(channels, streamCommandEnvelope{command: streamCommand{Kind: streamCommandProviderEvent}}); err != nil {
		t.Fatalf("deliver provider event: %v", err)
	}
	if err := deliverStreamCommand(channels, streamCommandEnvelope{command: streamCommand{Kind: streamCommandCancel}}); err != nil {
		t.Fatalf("deliver cancel: %v", err)
	}
	if len(channels.normal) != 1 || len(channels.urgent) != 1 {
		t.Fatalf("unexpected routing: normal=%d urgent=%d", len(channels.normal), len(channels.urgent))
	}

	// 两条队列此时都已满，唯一可选分支是已关闭的 done，
	// 因此投递必须报告 actor 已退出，而不是无限阻塞。
	close(channels.done)
	if err := deliverStreamCommand(channels, streamCommandEnvelope{command: streamCommand{Kind: streamCommandCancel}}); !errors.Is(err, errProviderLoopInterrupted) {
		t.Fatalf("expected errProviderLoopInterrupted after actor exit, got %v", err)
	}
}
