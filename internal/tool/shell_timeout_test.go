//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// setShellTimeoutForTest overrides the global default shell timeout and
// restores the previous value when the test finishes.
func setShellTimeoutForTest(t *testing.T, d time.Duration) {
	t.Helper()
	prev := defaultShellTimeout
	SetShellTimeout(d)
	t.Cleanup(func() { SetShellTimeout(prev) })
}

// TestShellTool_TimeoutKillsHangingCommand verifies the global default timeout:
// a hanging command is killed and the error reports the timeout plus the
// partial output produced before the kill.
func TestShellTool_TimeoutKillsHangingCommand(t *testing.T) {
	setShellTimeoutForTest(t, 2*time.Second)

	tool := ShellTool{}
	args := json.RawMessage(`{"command": "echo start; sleep 300"}`)

	start := time.Now()
	_, err := tool.Execute(approvedContext(), args)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected error to mention the timeout, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "start") {
		t.Fatalf("expected partial output 'start' in the timeout error, got %q", err.Error())
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("command should have been killed after ~2s, took %s", elapsed)
	}
}

// TestShellTool_CancelReturnsDespiteGrandchildHoldingPipes verifies that an
// explicit context cancellation returns promptly even when the command spawns
// descendants: the process-group kill reaps the tree and WaitDelay caps any
// pipe-holding straggler.
func TestShellTool_CancelReturnsDespiteGrandchildHoldingPipes(t *testing.T) {
	setShellTimeoutForTest(t, 0) // unlimited; the bound comes from ctx cancellation

	tool := ShellTool{}
	args := json.RawMessage(`{"command": "echo start; (sleep 300 &) ; sleep 300"}`)

	ctx, cancel := context.WithCancel(approvedContext())
	defer cancel()

	type outcome struct {
		result string
		err    error
	}
	outcomeCh := make(chan outcome, 1)
	go func() {
		result, err := tool.Execute(ctx, args)
		outcomeCh <- outcome{result: result, err: err}
	}()

	time.Sleep(200 * time.Millisecond)
	cancelAt := time.Now()
	cancel()

	select {
	case <-outcomeCh:
	case <-time.After(10 * time.Second):
		// Fail-safe: WaitDelay is 5s, so a hang here means the group kill did
		// not fire and pipes are holding Wait open.
		t.Fatal("Execute did not return within 10s of cancellation")
	}
	if elapsed := time.Since(cancelAt); elapsed >= 9*time.Second {
		t.Fatalf("Execute took %s to return after cancellation, want < 9s", elapsed)
	}
}

// TestShellTool_PerCallTimeoutOverridesGlobal verifies the per-call timeout
// parameter beats the (longer) global default.
func TestShellTool_PerCallTimeoutOverridesGlobal(t *testing.T) {
	setShellTimeoutForTest(t, 10*time.Minute)

	tool := ShellTool{}
	args := json.RawMessage(`{"command": "echo start; sleep 300", "timeout": "1s"}`)

	start := time.Now()
	_, err := tool.Execute(approvedContext(), args)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected error to mention the timeout, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "start") {
		t.Fatalf("expected partial output 'start' in the timeout error, got %q", err.Error())
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("command should have been killed after ~1s, took %s", elapsed)
	}
}

// TestShellTool_PerCallTimeoutUnlimited verifies that "0" parses as unlimited
// without hanging a fast command.
func TestShellTool_PerCallTimeoutUnlimited(t *testing.T) {
	setShellTimeoutForTest(t, 10*time.Minute)

	tool := ShellTool{}
	args := json.RawMessage(`{"command": "echo ok", "timeout": "0"}`)

	result, err := tool.Execute(approvedContext(), args)
	if err != nil {
		t.Fatalf("expected success with unlimited timeout, got %v", err)
	}
	if !strings.Contains(result, "ok") {
		t.Fatalf("expected command output 'ok', got %q", result)
	}
}

// TestShellTool_InvalidTimeoutIsErrorResult verifies that an unparsable timeout
// is reported as an error RESULT (nil Go error) and that the command never runs.
func TestShellTool_InvalidTimeoutIsErrorResult(t *testing.T) {
	setShellTimeoutForTest(t, 10*time.Minute)

	tool := ShellTool{}
	args := json.RawMessage(`{"command": "echo should-not-run", "timeout": "banana"}`)

	result, err := tool.Execute(approvedContext(), args)
	if err != nil {
		t.Fatalf("expected an error result with nil Go error, got %v", err)
	}
	if !strings.Contains(result, "invalid timeout") {
		t.Fatalf("expected result to report the invalid timeout, got %q", result)
	}
	if strings.Contains(result, "should-not-run") {
		t.Fatalf("command must not run when the timeout is invalid, got %q", result)
	}
}

// TestResolveShellTimeout covers the pure timeout resolution function directly.
func TestResolveShellTimeout(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		wantDuration   time.Duration
		wantDeadline   bool
		wantCancelFunc bool
		wantErr        bool
	}{
		{
			name:           "empty uses global default",
			raw:            "",
			wantDuration:   10 * time.Minute,
			wantDeadline:   true,
			wantCancelFunc: true,
		},
		{
			name:         "zero is unlimited",
			raw:          "0",
			wantDuration: 0,
			wantDeadline: false,
		},
		{
			name:         "negative is unlimited",
			raw:          "-5m",
			wantDuration: 0,
			wantDeadline: false,
		},
		{
			name:           "valid duration is honored",
			raw:            "90m",
			wantDuration:   90 * time.Minute,
			wantDeadline:   true,
			wantCancelFunc: true,
		},
		{
			name:    "invalid string errors",
			raw:     "banana",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setShellTimeoutForTest(t, 10*time.Minute)

			ctx, cancel, gotDuration, err := resolveShellTimeout(context.Background(), tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for raw=%q, got nil", tt.raw)
				}
				if !strings.Contains(err.Error(), "invalid timeout") {
					t.Fatalf("expected error to mention 'invalid timeout', got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for raw=%q: %v", tt.raw, err)
			}
			if gotDuration != tt.wantDuration {
				t.Fatalf("resolved duration = %s, want %s", gotDuration, tt.wantDuration)
			}
			_, hasDeadline := ctx.Deadline()
			if hasDeadline != tt.wantDeadline {
				t.Fatalf("ctx hasDeadline = %v, want %v", hasDeadline, tt.wantDeadline)
			}
			if (cancel != nil) != tt.wantCancelFunc {
				t.Fatalf("cancel func presence = %v, want %v", cancel != nil, tt.wantCancelFunc)
			}
			if cancel != nil {
				cancel()
			}
		})
	}
}
