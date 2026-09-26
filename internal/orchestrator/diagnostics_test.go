package orchestrator

import (
	"io"
	"os"
	"testing"
)

// captureTestStderr runs fn while redirecting os.Stderr to a pipe and returns
// everything written during fn. Used to pin both sink-routed (stderr must
// stay clean) and fallback (stderr must receive the line) diagnostics.
func captureTestStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- buf
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return string(<-done)
}

// TestReportfSinkReceivesLineStderrClean: with a diagnostics sink installed,
// reportf routes the line to the sink and writes nothing to os.Stderr.
func TestReportfSinkReceivesLineStderrClean(t *testing.T) {
	o := NewBaseOrchestrator("diag-test", nil, nil, 0)

	var got []string
	o.SetDiagnostics(func(msg string) { got = append(got, msg) })
	defer o.SetDiagnostics(nil)

	stderr := captureTestStderr(t, func() {
		o.reportf("late: %d events dropped (consumer stalled)\n", 44)
	})

	if len(got) != 1 {
		t.Fatalf("sink received %d messages (%q), want 1", len(got), got)
	}
	if want := "late: 44 events dropped (consumer stalled)\n"; got[0] != want {
		t.Fatalf("sink = %q, want %q", got[0], want)
	}
	if stderr != "" {
		t.Fatalf("os.Stderr received %q with a sink installed, want nothing", stderr)
	}
}

// TestReportfFallbackWritesStderr: without a sink, the exact pre-sink line
// lands on os.Stderr (pins the CLI/test fallback).
func TestReportfFallbackWritesStderr(t *testing.T) {
	o := NewBaseOrchestrator("diag-test", nil, nil, 0)

	stderr := captureTestStderr(t, func() {
		o.reportf("late: %d events dropped (consumer stalled)\n", 7)
	})

	if want := "late: 7 events dropped (consumer stalled)\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

// TestSetDiagnosticsLastSinkWins pins the documented last-wins semantics:
// installing a second sink replaces the first, which stops receiving.
func TestSetDiagnosticsLastSinkWins(t *testing.T) {
	o := NewBaseOrchestrator("diag-test", nil, nil, 0)

	var firstGot, secondGot []string
	o.SetDiagnostics(func(msg string) { firstGot = append(firstGot, msg) })
	o.SetDiagnostics(func(msg string) { secondGot = append(secondGot, msg) })
	defer o.SetDiagnostics(nil)

	o.reportf("second sink only\n")

	if len(firstGot) != 0 {
		t.Fatalf("replaced sink received %q, want nothing", firstGot)
	}
	if len(secondGot) != 1 || secondGot[0] != "second sink only\n" {
		t.Fatalf("active sink = %q, want [\"second sink only\\n\"]", secondGot)
	}
}

// TestSetDiagnosticsNilRestoresStderrFallback pins nil-safety: installing a
// nil sink removes it and reportf falls back to os.Stderr.
func TestSetDiagnosticsNilRestoresStderrFallback(t *testing.T) {
	o := NewBaseOrchestrator("diag-test", nil, nil, 0)
	o.SetDiagnostics(func(msg string) {})
	o.SetDiagnostics(nil)

	stderr := captureTestStderr(t, func() {
		o.reportf("fallback after nil\n")
	})
	if stderr != "fallback after nil\n" {
		t.Fatalf("stderr = %q, want the exact pre-sink line", stderr)
	}
}

// TestReportfCallableWhileMuHeld pins the deadlock hardening: reportf and
// SetDiagnostics guard diagnosticsFn with a DEDICATED diagMu (the same
// reasoning as the plugin manager's), not the orchestrator-wide mu. reportf
// must therefore stay callable from code that already holds mu (or may hold
// it in the future) — under the old mu.RLock the nested RLock below deadlocked
// against this goroutine's held write lock.
func TestReportfCallableWhileMuHeld(t *testing.T) {
	o := NewBaseOrchestrator("diag-test", nil, nil, 0)

	var got []string
	o.SetDiagnostics(func(msg string) { got = append(got, msg) })
	defer o.SetDiagnostics(nil)

	o.mu.Lock()
	o.reportf("diagnostic while holding mu\n")
	o.mu.Unlock()

	if len(got) != 1 || got[0] != "diagnostic while holding mu\n" {
		t.Fatalf("sink = %q, want the reportf line routed while mu was held", got)
	}
}
