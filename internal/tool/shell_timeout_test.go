//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestShellTool_TimeoutKillsHangingCommand verifies that a command which never
// exits is killed by the shell timeout and that Execute surfaces a timeout
// error containing the partial output produced before the kill.
func TestShellTool_TimeoutKillsHangingCommand(t *testing.T) {
	old := defaultShellTimeout
	// 2s (not a tight bound like 300ms): under back-to-back -race load, bash
	// startup + echo can exceed a few-hundred-ms timeout before "start" is
	// written, making the partial-output assertion below flaky. 2s keeps the
	// kill proof and the strong assertion deterministic while staying fast.
	SetShellTimeout(2 * time.Second)
	t.Cleanup(func() { SetShellTimeout(old) })

	start := time.Now()
	out, err := (&ShellTool{}).Execute(approvedContext(), json.RawMessage(`{"command": "echo start; sleep 300"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (out=%q)", out)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected error to mention the timeout, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "start") {
		t.Fatalf("expected partial output 'start' in error, got %q", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Execute took %v, expected < 5s", elapsed)
	}
}

// TestShellTool_CancelReturnsDespiteGrandchildHoldingPipes is THE regression
// test for the subagent-hang bug: `(sleep 300 &)` leaves a grandchild that
// inherits bash's stdout/stderr pipes. Plain CommandContext cancellation kills
// only the direct bash process, so CombinedOutput's Wait used to block forever
// even after the context was cancelled. With the process-group kill in
// newShellCommand the whole group dies, the pipes close, and Execute returns.
func TestShellTool_CancelReturnsDespiteGrandchildHoldingPipes(t *testing.T) {
	old := defaultShellTimeout
	SetShellTimeout(0) // disable the timeout; this test exercises explicit cancel
	t.Cleanup(func() { SetShellTimeout(old) })

	ctx, cancel := context.WithCancel(approvedContext())
	defer cancel()

	type execResult struct {
		out string
		err error
	}
	results := make(chan execResult, 1)
	go func() {
		out, err := (&ShellTool{}).Execute(ctx, json.RawMessage(`{"command": "echo start; (sleep 300 &) ; sleep 300"}`))
		results <- execResult{out, err}
	}()

	// Let bash start and spawn the detached sleeper before cancelling.
	select {
	case res := <-results:
		t.Fatalf("Execute returned before cancel: out=%q err=%v", res.out, res.err)
	case <-time.After(200 * time.Millisecond):
	}

	cancelledAt := time.Now()
	cancel()

	select {
	case res := <-results:
		if elapsed := time.Since(cancelledAt); elapsed > 9*time.Second {
			t.Errorf("Execute returned %v after cancel, expected < 9s (out=%q err=%v)", elapsed, res.out, res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Execute did not return within 10s after cancel — pipe-holding grandchild is still blocking Wait")
	}
}

// TestShellTool_TimeoutLeavesNoProcesses verifies the process-group kill end
// to end: after a timed-out shell command returns, no descendant (bash or its
// sleep child) may still be running.
func TestShellTool_TimeoutLeavesNoProcesses(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	old := defaultShellTimeout
	SetShellTimeout(300 * time.Millisecond)
	t.Cleanup(func() { SetShellTimeout(old) })

	_, _ = (&ShellTool{}).Execute(approvedContext(), json.RawMessage(`{"command": "sleep 300"}`))

	deadline := time.Now().Add(2 * time.Second)
	for {
		// Anchored pattern so unrelated processes merely containing "sleep 300"
		// in a longer command line do not produce false positives. pgrep exits
		// non-zero with empty output when nothing matches.
		out, _ := exec.Command("pgrep", "-f", `^sleep 300$`).Output()
		matches := strings.TrimSpace(string(out))
		if matches == "" {
			return // no leftover processes
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes still running after shell timeout: %s", matches)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
