package compaction

import (
	"io"
	"strings"
	"testing"
)

// TestPipeline_WarningSinkReceivesAuthWarning pins the mid-session routing:
// with a warning sink installed (the TUI wiring), the one-time auth-poison
// warning goes to the sink — which renders it as a toast instead of a raw
// stderr write painting over the alt-screen — and warnTo stays clean.
func TestPipeline_WarningSinkReceivesAuthWarning(t *testing.T) {
	var warnTo strings.Builder
	p := &Pipeline{warnTo: &warnTo}

	var got []string
	p.SetWarningSink(func(msg string) { got = append(got, msg) })
	defer p.SetWarningSink(nil)

	p.noteAuthFailure("401: bad key")

	if len(got) != 1 {
		t.Fatalf("sink received %d messages (%q), want exactly the one-time warning", len(got), got)
	}
	want := "Warning: compaction scoring disabled for this session (401: bad key)\n"
	if got[0] != want {
		t.Fatalf("sink = %q, want %q", got[0], want)
	}
	if warnTo.Len() != 0 {
		t.Fatalf("warnTo received %q with a sink installed, want nothing", warnTo.String())
	}
	if !p.authDead || p.authReason != "401: bad key" {
		t.Fatalf("poisoning state = (dead=%v, reason=%q), want the auth failure recorded", p.authDead, p.authReason)
	}

	// The one-time guard: a second failure emits nothing further.
	p.noteAuthFailure("401: bad key again")
	if len(got) != 1 {
		t.Fatalf("sink received %d messages after the second failure, want 1 (one-time warning)", len(got))
	}
}

// TestPipeline_WarningSinkNilFallsBackToWarnTo pins the fallback: without a
// sink (headless CLI flows, tests), the warning goes to warnTo exactly as
// before the sink existed.
func TestPipeline_WarningSinkNilFallsBackToWarnTo(t *testing.T) {
	var warnTo strings.Builder
	p := &Pipeline{warnTo: &warnTo}

	p.noteAuthFailure("403: forbidden")

	want := "Warning: compaction scoring disabled for this session (403: forbidden)\n"
	if warnTo.String() != want {
		t.Fatalf("warnTo = %q, want the exact pre-sink line %q", warnTo.String(), want)
	}
}

// TestPipeline_WarningSinkDiscardCapable documents that any io.Writer-shaped
// fallback remains valid after SetWarningSink(nil): the tests swap warnTo
// freely (errors_test.go does), and the sink must not change that.
func TestPipeline_WarningSinkDiscardCapable(t *testing.T) {
	p := &Pipeline{warnTo: io.Discard}
	p.SetWarningSink(func(msg string) { t.Errorf("sink called %q with no auth failure", msg) })
	p.SetWarningSink(nil)

	// No panic, no sink call: only noteAuthFailure ever emits.
	if p.authDead {
		t.Fatal("a fresh pipeline must not start poisoned")
	}
}
