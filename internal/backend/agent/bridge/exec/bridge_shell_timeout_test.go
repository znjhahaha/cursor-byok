package execbridge

import (
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func openShellArgsForTest(t *testing.T, argsJSON string) *agentv1.ShellArgs {
	t.Helper()
	serverMessage, _, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{
		CallID:   "shell-call-1",
		ToolName: "Shell",
		ArgsJSON: []byte(argsJSON),
	})
	if err != nil {
		t.Fatalf("OpenExec() error = %v", err)
	}
	shellArgs := serverMessage.GetExecServerMessage().GetShellStreamArgs()
	if shellArgs == nil {
		t.Fatalf("server message = %#v, want shell_stream_args", serverMessage)
	}
	return shellArgs
}

func TestShellTimeoutDefaultsToBackgroundWithIndependentHardLimit(t *testing.T) {
	args := openShellArgsForTest(t, `{"command":"go test ./..."}`)

	if args.GetTimeout() != 30000 {
		t.Fatalf("timeout = %d, want 30000", args.GetTimeout())
	}
	if args.GetTimeoutBehavior() != agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_BACKGROUND {
		t.Fatalf("timeout behavior = %s, want BACKGROUND", args.GetTimeoutBehavior())
	}
	if args.GetHardTimeout() != defaultShellHardTimeoutMS {
		t.Fatalf("hard timeout = %d, want %d", args.GetHardTimeout(), defaultShellHardTimeoutMS)
	}
}

func TestShellHardTimeoutDoesNotTruncateExplicitLongForegroundWait(t *testing.T) {
	const explicitWaitMS = int32(172800000)
	args := openShellArgsForTest(t, `{"command":"long-running-job","block_until_ms":172800000}`)

	if args.GetTimeout() != explicitWaitMS {
		t.Fatalf("timeout = %d, want %d", args.GetTimeout(), explicitWaitMS)
	}
	if args.GetHardTimeout() != explicitWaitMS {
		t.Fatalf("hard timeout = %d, want at least the explicit foreground wait %d", args.GetHardTimeout(), explicitWaitMS)
	}
}

func TestImmediateBackgroundShellKeepsBackgroundLifetimeLimit(t *testing.T) {
	args := openShellArgsForTest(t, `{"command":"dev-server","block_until_ms":0}`)

	if args.GetTimeout() != 0 {
		t.Fatalf("timeout = %d, want immediate background", args.GetTimeout())
	}
	if args.GetTimeoutBehavior() != agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_BACKGROUND {
		t.Fatalf("timeout behavior = %s, want BACKGROUND", args.GetTimeoutBehavior())
	}
	if args.GetHardTimeout() != defaultShellHardTimeoutMS {
		t.Fatalf("hard timeout = %d, want %d", args.GetHardTimeout(), defaultShellHardTimeoutMS)
	}
}
