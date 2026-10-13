package tool

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderrTest runs fn while redirecting os.Stderr to a pipe and returns
// everything written during fn.
func captureStderrTest(t *testing.T, fn func()) string {
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

// shellToolForTest builds a ShellTool the same way the bash gate tests do.
func shellToolForTest() *ShellTool {
	return &ShellTool{}
}

// TestBashGateWarnRoutedThroughDiagnosticsSink: with LATE_BASH_GATE=warn and
// a sink installed, the "[WARN] Consider using ..." line goes to the sink —
// mid-session, that means the TUI toast instead of raw stderr over the
// alt-screen — and os.Stderr stays clean.
func TestBashGateWarnRoutedThroughDiagnosticsSink(t *testing.T) {
	t.Setenv("LATE_BASH_GATE", "warn")

	var got []string
	SetDiagnostics(func(msg string) { got = append(got, msg) })
	defer SetDiagnostics(nil)

	stderr := captureStderrTest(t, func() {
		_ = shellToolForTest().ValidateBashCommand("grep -r needle .", ".")
	})

	if len(got) != 1 {
		t.Fatalf("sink received %d messages (%q), want 1", len(got), got)
	}
	want := "[WARN] Consider using search_content or find_files instead of bash grep\n"
	if got[0] != want {
		t.Fatalf("sink = %q, want %q", got[0], want)
	}
	if stderr != "" {
		t.Fatalf("os.Stderr received %q with a sink installed, want nothing", stderr)
	}
}

// TestBashGateWarnFallbackWritesStderr pins the no-sink fallback: the exact
// pre-sink WARN line lands on os.Stderr.
func TestBashGateWarnFallbackWritesStderr(t *testing.T) {
	t.Setenv("LATE_BASH_GATE", "warn")

	stderr := captureStderrTest(t, func() {
		_ = shellToolForTest().ValidateBashCommand("grep -r needle .", ".")
	})

	want := "[WARN] Consider using search_content or find_files instead of bash grep\n"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
}
