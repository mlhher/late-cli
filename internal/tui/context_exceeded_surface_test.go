package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"late/internal/client"
	"late/internal/common"
)

// contextExceededErr mirrors the real production error chain for terminal
// context exhaustion after the executor guard gave up: the client classified
// the provider rejection as *ContextExceededError (sentinel
// ErrContextExceeded) and the executor wrapped it with the compaction
// outcome via "stream error: %w: <summary>".
func contextExceededErr() error {
	return fmt.Errorf("stream error: %w: %s", &client.ContextExceededError{
		Status: &client.StatusError{
			StatusCode: 400,
			Body:       "This model's maximum context length is 8192 tokens",
		},
		Reason: "http 400",
	}, "context limit hit — auto-compaction unavailable (compaction-mode off or no scorer)")
}

// TestContextExceededErrorSurface pins the status surface for a terminal
// context-exhaustion failure: the error text (carrying the compaction
// outcome) lands in the status line and the error box, and the warning
// toast repeats the full guidance. No TUI-side recovery compaction runs —
// recovery is the executor guard's job, and it already happened (or could
// not) before this event surfaced.
func TestContextExceededErrorSurface(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{
		ID:     m.Focused.ID(),
		Status: "error",
		Error:  contextExceededErr(),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	// The status shows the outcome the executor observed.
	if !strings.Contains(s.StatusText, "auto-compaction unavailable") {
		t.Errorf("StatusText = %q, want the compaction outcome", s.StatusText)
	}
	if s.Error == nil {
		t.Fatal("the error must be pinned for the transcript card")
	}

	// Drain the produced command like Bubble Tea would: it is (a batch
	// containing) the guidance toast (8s warning, same duration as the other
	// guidance toasts) carrying the full error text.
	msgs := flattenCmd(cmd)
	var toast *ToastMsg
	for i := range msgs {
		if t2, ok := msgs[i].(ToastMsg); ok {
			toast = &t2
		}
	}
	if toast == nil {
		t.Fatalf("no ToastMsg among %d drained message(s)", len(msgs))
	}
	if !toast.Warning || toast.Duration != 8*time.Second {
		t.Errorf("toast = %+v, want an 8s warning toast", toast)
	}
	if !strings.Contains(toast.Text, "context window") {
		t.Errorf("toast = %q, want the recovery guidance", toast.Text)
	}
}

// flattenCmd executes a Bubble Tea command and flattens tea.BatchMsg one
// level deep, the way the Update loop consumes batches.
func flattenCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, sub := range batch {
			out = append(out, sub())
		}
		return out
	}
	return []tea.Msg{msg}
}

// TestTranscriptErrorRendersContextCard pins the transcript branch: the
// typed sentinel renders the dedicated context-limit card carrying the
// compaction outcome, while a generic error keeps the plain prefix.
func TestTranscriptErrorRendersContextCard(t *testing.T) {
	rendered := transcriptError(contextExceededErr())
	if !strings.Contains(rendered, "**Context Limit Exceeded**") {
		t.Errorf("rendered = %q, want the context-limit card", rendered)
	}
	if !strings.Contains(rendered, "auto-compaction unavailable") {
		t.Errorf("rendered = %q, want the compaction outcome", rendered)
	}
	// The legacy text match still stands for pre-typing surfaces.
	legacy := transcriptError(fmt.Errorf("stream error: exceeds the available context size"))
	if !strings.Contains(legacy, "start a new session") {
		t.Errorf("rendered = %q, want the legacy card", legacy)
	}
	plain := transcriptError(fmt.Errorf("something else broke"))
	if strings.Contains(plain, "Context Limit Exceeded") {
		t.Errorf("rendered = %q, want the plain error rendering", plain)
	}
}

// TestContextExceededRetryEventVerb pins the RetryEvent branch: the guard's
// compaction+retry round carries the typed sentinel, so the status line
// shows the immediate "retrying" verb with the wrapped compaction outcome
// instead of a backoff tail, and the recovery toast wording differs from
// the connection-regained case.
func TestContextExceededRetryEventVerb(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 2,
		Delay:       0,
		Err: fmt.Errorf("%w (%s)", &client.ContextExceededError{Reason: "http 400"},
			"context limit hit — auto-compacted (saved ~500 tokens)"),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != retryVerbContextCompacted {
		t.Errorf("RetryVerb = %q, want %q", s.RetryVerb, retryVerbContextCompacted)
	}
	if !strings.Contains(s.StatusText, "retrying") {
		t.Errorf("StatusText = %q, want the immediate-retry wording", s.StatusText)
	}
	if !strings.Contains(s.StatusText, "saved ~500 tokens") {
		t.Errorf("StatusText = %q, want the wrapped compaction outcome", s.StatusText)
	}
}
