package plugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
)

// The diagnostics-sink contract: every mid-session hook diagnostic (hook
// stderr forwarding, hook errors from the onToolCall/onToolResult/
// onMessageSend/fanout runners) goes to the installed sink instead of
// os.Stderr, so a TUI session can surface it as a toast instead of raw text
// painting over the alt-screen. Without a sink the exact pre-sink stderr
// lines are preserved (pinned below).

// TestDiagnosticsSink_HookTimeoutAndStderrRoutedToSink installs a sink and
// runs (1) an OnToolCall hook that exceeds its context deadline — the
// "hook timed out after 15s" line observed garbling the TUI footer — and
// (2) an OnMessageSend hook that writes to its own stderr. Both messages
// must reach the sink and NOTHING may reach os.Stderr.
func TestDiagnosticsSink_HookTimeoutAndStderrRoutedToSink(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// (1) OnToolCall hook that sleeps past its deadline.
	pTimeout := writeTestPlugin(t, t.TempDir(), "diag-timeout", &LateManifest{
		Hooks: &LateHooksManifest{OnToolCall: []string{"sleep.sh"}},
	})
	writeExecutableShell(t, pTimeout.Path+"/sleep.sh", "sleep 30")
	pTimeout.Enabled = true
	pm.Add(pTimeout)

	// (2) OnMessageSend hook that writes to its own stderr.
	pStderr := writeTestPlugin(t, t.TempDir(), "diag-stderr", &LateManifest{
		Hooks: &LateHooksManifest{OnMessageSend: []string{"bad.sh"}},
	})
	writeExecutableShell(t, pStderr.Path+"/bad.sh", "echo boom >&2")
	pStderr.Enabled = true
	pm.Add(pStderr)

	var captured []string
	pm.SetDiagnostics(func(msg string) {
		captured = append(captured, msg)
	})
	defer pm.SetDiagnostics(nil)

	stderr := captureStderr(t, func() {
		// (1) OnToolCall middleware with a short ctx: the hook times out.
		mws := pm.BuildHookMiddlewares()
		if len(mws) != 1 {
			t.Fatalf("expected 1 onToolCall middleware, got %d", len(mws))
		}
		next := common.ToolRunner(func(ctx context.Context, call client.ToolCall) (string, error) {
			return "ok", nil
		})
		runner := mws[0](next)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, _ = runner(ctx, client.ToolCall{Function: client.FunctionCall{Name: "anything", Arguments: "{}"}})

		// (2) onMessageSend hook writing to its own stderr.
		_ = pm.HookedMessage(context.Background(), "hi")
	})

	if len(captured) != 2 {
		t.Fatalf("sink received %d messages (%q), want 2", len(captured), captured)
	}
	wantTimeout := "[diag-timeout/onToolCall/sleep.sh] hook timed out after 15s\n"
	if captured[0] != wantTimeout {
		t.Fatalf("sink[0] = %q, want %q", captured[0], wantTimeout)
	}
	wantStderr := "[hook diag-stderr:bad.sh] boom\n"
	if captured[1] != wantStderr {
		t.Fatalf("sink[1] = %q, want %q", captured[1], wantStderr)
	}
	if stderr != "" {
		t.Fatalf("os.Stderr received %q with a sink installed, want nothing", stderr)
	}
}

// TestDiagnosticsSink_LastSinkWins pins the documented last-wins semantics:
// installing a second sink replaces the first, which stops receiving.
func TestDiagnosticsSink_LastSinkWins(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	var firstGot, secondGot []string
	pm.SetDiagnostics(func(msg string) { firstGot = append(firstGot, msg) })
	pm.SetDiagnostics(func(msg string) { secondGot = append(secondGot, msg) })
	defer pm.SetDiagnostics(nil)

	pm.reportf("hello %s\n", "world")

	if len(firstGot) != 0 {
		t.Fatalf("replaced sink received %q, want nothing", firstGot)
	}
	if len(secondGot) != 1 || secondGot[0] != "hello world\n" {
		t.Fatalf("active sink = %q, want [\"hello world\\n\"]", secondGot)
	}
}

// TestDiagnosticsSink_NilSinkIsStderrFallback pins nil-safety: installing a
// nil sink removes it and reportf falls back to os.Stderr.
func TestDiagnosticsSink_NilSinkIsStderrFallback(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	pm.SetDiagnostics(func(msg string) {})
	pm.SetDiagnostics(nil)

	stderr := captureStderr(t, func() {
		pm.reportf("late: %d events dropped (consumer stalled)\n", 44)
	})
	if stderr != "late: 44 events dropped (consumer stalled)\n" {
		t.Fatalf("stderr = %q, want the exact pre-sink line", stderr)
	}
}

// TestDiagnosticsFallback_WritesStderr pins the no-sink fallback: the same
// failing hooks produce the exact pre-sink stderr lines on os.Stderr, and
// the sink (never installed here) receives nothing.
func TestDiagnosticsFallback_WritesStderr(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	pTimeout := writeTestPlugin(t, t.TempDir(), "diag-timeout", &LateManifest{
		Hooks: &LateHooksManifest{OnToolCall: []string{"sleep.sh"}},
	})
	writeExecutableShell(t, pTimeout.Path+"/sleep.sh", "sleep 30")
	pTimeout.Enabled = true
	pm.Add(pTimeout)

	pStderr := writeTestPlugin(t, t.TempDir(), "diag-stderr", &LateManifest{
		Hooks: &LateHooksManifest{OnMessageSend: []string{"bad.sh"}},
	})
	writeExecutableShell(t, pStderr.Path+"/bad.sh", "echo boom >&2")
	pStderr.Enabled = true
	pm.Add(pStderr)

	stderr := captureStderr(t, func() {
		mws := pm.BuildHookMiddlewares()
		if len(mws) != 1 {
			t.Fatalf("expected 1 onToolCall middleware, got %d", len(mws))
		}
		next := common.ToolRunner(func(ctx context.Context, call client.ToolCall) (string, error) {
			return "ok", nil
		})
		runner := mws[0](next)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, _ = runner(ctx, client.ToolCall{Function: client.FunctionCall{Name: "anything", Arguments: "{}"}})

		_ = pm.HookedMessage(context.Background(), "hi")
	})

	for _, want := range []string{
		"[diag-timeout/onToolCall/sleep.sh] hook timed out after 15s\n",
		"[hook diag-stderr:bad.sh] boom\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q does not contain the pinned fallback line %q", stderr, want)
		}
	}
}
