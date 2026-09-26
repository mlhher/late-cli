package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"late/internal/client"
	"late/internal/common"
	"late/internal/config"
	"late/internal/session"
)

// payloadTooLargeErr mirrors the real production error chain for an HTTP 413:
// the client wraps the StatusError in *PayloadTooLargeError (sentinel
// ErrPayloadTooLarge) and the executor wraps that in "stream error: %w".
func payloadTooLargeErr() error {
	return fmt.Errorf("stream error: %w", &client.PayloadTooLargeError{
		Status: &client.StatusError{
			StatusCode: 413,
			Status:     "413 Payload Too Large",
			Body:       "Request body too large",
		},
	})
}

// newPayloadRecoveryModel builds a model wired for 413 recovery tests: a
// counting Compactor stub and CompactionApplies (mode "enabled").
func newPayloadRecoveryModel(runs *int) *Model {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(80, 24)
	m.Compactor = func(context.Context) (session.CompactionReport, error) {
		*runs++
		return session.CompactionReport{TokensSaved: 400, SegmentsElided: 1}, nil
	}
	m.CompactionApplies = true
	return &m
}

// dispatchErrorEvent feeds one orchestrator error status event through Update
// and returns the updated model plus the command Bubble Tea would run.
func dispatchErrorEvent(t *testing.T, m *Model, agentID string, eventErr error) (*Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{
		ID:     agentID,
		Status: "error",
		Error:  eventErr,
	}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	return &next, cmd
}

// collectMsgs flattens cmd's result, recursively expanding BatchMsg (the
// outer Update wraps the handler's commands in extra batches with the
// spinner/frame ticks, so children can be batches themselves).
func collectMsgs(msg tea.Msg, into *[]tea.Msg) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			collectMsgs(child(), into)
		}
		return
	}
	*into = append(*into, msg)
}

// drainCmd runs cmd and feeds every produced message back through Update
// exactly as Bubble Tea would. It reports whether a compactionResultMsg was
// among them.
func drainCmd(t *testing.T, m *Model, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	var msgs []tea.Msg
	collectMsgs(cmd(), &msgs)
	compacted := false
	for _, child := range msgs {
		if _, isResult := child.(compactionResultMsg); isResult {
			compacted = true
		}
		followUp, _ := m.Update(child)
		*m = followUp.(Model)
	}
	return compacted
}

// runErrorEvent is dispatch + drain in one step.
func runErrorEvent(t *testing.T, m *Model, agentID string, eventErr error) bool {
	t.Helper()
	next, cmd := dispatchErrorEvent(t, m, agentID, eventErr)
	*m = *next
	return drainCmd(t, m, cmd)
}

// TestPayloadRecoveryTriggersOnce pins the one-shot semantics: the first 413
// on the root agent fires exactly one recovery compaction (with the recovery
// status while in flight); a second 413 on the same conversation must not
// start another run.
func TestPayloadRecoveryTriggersOnce(t *testing.T) {
	runs := 0
	m := newPayloadRecoveryModel(&runs)

	// First 413: dispatch synchronously sets the guard, the in-flight flag,
	// and the recovery status, and returns the compaction command.
	next, cmd := dispatchErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr())
	m = next
	s := m.GetAgentState(m.Focused.ID())
	if !m.CompactionRunning {
		t.Fatal("the first 413 must start the recovery compaction")
	}
	if !s.PayloadRecoveryUsed {
		t.Fatal("the first 413 must set the one-shot guard")
	}
	if s.StatusText != payloadRecoveryStatus {
		t.Fatalf("StatusText = %q, want the recovery status %q", s.StatusText, payloadRecoveryStatus)
	}
	if cmd == nil {
		t.Fatal("the first 413 must return the compaction command")
	}

	// Drain the command (and the toast) like Bubble Tea would: the runner
	// executes exactly once and its report clears the guard.
	if !drainCmd(t, m, cmd) {
		t.Fatal("the drained command must produce a compactionResultMsg")
	}
	if runs != 1 {
		t.Fatalf("compaction runner invoked %d times, want 1", runs)
	}
	if m.CompactionRunning {
		t.Fatal("the result message must clear the in-flight guard")
	}
	if !strings.Contains(m.GetAgentState(m.Focused.ID()).StatusText, "compacted: saved") {
		t.Fatalf("StatusText = %q, want the compaction report", m.GetAgentState(m.Focused.ID()).StatusText)
	}

	// Second 413 (provider still over the limit): no second run, no loop.
	if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); compacted {
		t.Fatal("the second 413 must not run another recovery compaction")
	}
	if runs != 1 {
		t.Fatalf("compaction runner invoked %d times after the second 413, want 1 (recovery fires once per conversation)", runs)
	}
	if m.CompactionRunning {
		t.Fatal("the second 413 must not start another compaction run")
	}
	if !m.GetAgentState(m.Focused.ID()).PayloadRecoveryUsed {
		t.Fatal("the one-shot guard must stay set until /new")
	}
}

// TestPayloadRecoveryReArmsOnNew pins the /new re-arm: a fresh conversation
// clears PayloadRecoveryUsed, so a later 413 can fire the recovery once
// again.
func TestPayloadRecoveryReArmsOnNew(t *testing.T) {
	runs := 0
	m := newPayloadRecoveryModel(&runs)

	if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); !compacted {
		t.Fatal("the first 413 must run the recovery compaction")
	}
	if runs != 1 {
		t.Fatalf("compaction runner invoked %d times, want 1", runs)
	}

	// /new starts a fresh conversation and re-arms the trigger.
	m.Input.SetValue("/new")
	*m = pressEnter(t, *m)
	if m.GetAgentState(m.Focused.ID()).PayloadRecoveryUsed {
		t.Fatal("/new must clear the one-shot payload-recovery guard")
	}

	if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); !compacted {
		t.Fatal("after /new the next 413 must run the recovery compaction again")
	}
	if runs != 2 {
		t.Fatalf("compaction runner invoked %d times after /new, want 2", runs)
	}
}

// TestPayloadRecoveryGuards pins every inert combination: the recovery must
// never fire without an actionable compaction (mode enabled + runner), for
// non-413 errors, while a compaction is already running, or for a non-root
// agent (the recovery always compacts the ROOT session).
func TestPayloadRecoveryGuards(t *testing.T) {
	t.Run("shadow mode never triggers", func(t *testing.T) {
		runs := 0
		m := newPayloadRecoveryModel(&runs)
		m.CompactionApplies = false

		if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); compacted {
			t.Fatal("shadow mode (report-only) must not fire the recovery")
		}
		if runs != 0 || m.CompactionRunning || m.GetAgentState(m.Focused.ID()).PayloadRecoveryUsed {
			t.Fatal("shadow mode must not start a run or set the guard")
		}
	})

	t.Run("nil Compactor never triggers", func(t *testing.T) {
		runs := 0
		m := newPayloadRecoveryModel(&runs)
		m.Compactor = nil

		if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); compacted {
			t.Fatal("a nil Compactor must not fire the recovery")
		}
		if m.CompactionRunning {
			t.Fatal("a nil Compactor must not set the in-flight guard")
		}
	})

	t.Run("non-413 error never triggers", func(t *testing.T) {
		runs := 0
		m := newPayloadRecoveryModel(&runs)

		non413 := fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 500, Body: "boom"})
		if compacted := runErrorEvent(t, m, m.Focused.ID(), non413); compacted {
			t.Fatal("a non-413 error must not fire the recovery")
		}
		if runs != 0 || m.GetAgentState(m.Focused.ID()).PayloadRecoveryUsed {
			t.Fatal("a non-413 error must not start a run or set the guard")
		}
	})

	t.Run("subagent 413 never triggers", func(t *testing.T) {
		runs := 0
		m := newPayloadRecoveryModel(&runs)

		if compacted := runErrorEvent(t, m, "subagent-1", payloadTooLargeErr()); compacted {
			t.Fatal("a subagent's 413 must not fire the ROOT-session recovery")
		}
		if runs != 0 || m.CompactionRunning {
			t.Fatal("a subagent's 413 must not start a run")
		}
	})

	t.Run("compaction already running never triggers", func(t *testing.T) {
		runs := 0
		m := newPayloadRecoveryModel(&runs)
		m.CompactionRunning = true // a manual command is in flight

		if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); compacted {
			t.Fatal("a 413 while a compaction is in flight must not start another")
		}
		if runs != 0 {
			t.Fatalf("compaction runner invoked %d times, want 0", runs)
		}
	})
}

// TestPayloadRecoveryToastOn413 pins the warning-toast path (FIX 1b): the
// 413 error surfaces the recovery guidance as a warning toast in addition to
// the error box, which renders the same text.
func TestPayloadRecoveryToastOn413(t *testing.T) {
	runs := 0
	m := newPayloadRecoveryModel(&runs)

	next, cmd := dispatchErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr())
	if cmd == nil {
		t.Fatal("the 413 error must return a command (toast and/or compaction)")
	}

	var toasts []ToastMsg
	var msgs []tea.Msg
	collectMsgs(cmd(), &msgs)
	for _, msg := range msgs {
		if toast, ok := msg.(ToastMsg); ok {
			toasts = append(toasts, toast)
		}
	}

	found := false
	for _, toast := range toasts {
		if strings.Contains(toast.Text, client.PayloadTooLargeGuidance) && toast.Warning {
			found = true
		}
	}
	if !found {
		t.Fatalf("toasts %+v, want a warning toast carrying the recovery guidance", toasts)
	}
	_ = next
}

// TestCompactionSuccessStatusCarriesEstimateSuffix pins the counter-honesty
// suffix (FIX 3): after a mutating run the token bar was recomputed by the
// LOCAL estimator while steady state tracks the provider-reported usage, so
// the success status must say the next request's usage refreshes the bar.
func TestCompactionSuccessStatusCarriesEstimateSuffix(t *testing.T) {
	const wantSuffix = "… (estimate — the next request's usage refreshes the bar)"

	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(80, 24)
	m.CompactionRunning = true

	result := compactionResultMsg{report: session.CompactionReport{
		TokensSaved: 25, SegmentsElided: 2, MessagesScanned: 4, MessagesScored: 3,
	}}
	updated, _ := m.Update(result)
	next := updated.(Model)

	got := next.GetAgentState(next.Focused.ID()).StatusText
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("StatusText = %q, want it to end with %q", got, wantSuffix)
	}
	if !strings.Contains(got, "compacted: saved ~25 tokens") {
		t.Fatalf("StatusText = %q, want the saved report before the suffix", got)
	}
}

// TestRewindReArmsPayloadRecovery pins the rewind re-arm: a rewind rewrote
// the focused agent's history, so the one-shot 413 recovery must re-arm for
// it exactly like /new does. Without this, a conversation that already burned
// its recovery pass keeps a later 413 unrecoverable even after the user
// rolled back to a smaller history.
func TestRewindReArmsPayloadRecovery(t *testing.T) {
	runs := 0
	m := newPayloadRecoveryModel(&runs)

	// Burn the one shot on a first 413.
	if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); !compacted {
		t.Fatal("the first 413 must run the recovery compaction")
	}
	if !m.GetAgentState(m.Focused.ID()).PayloadRecoveryUsed {
		t.Fatal("precondition: the one-shot guard must be set after the first 413")
	}

	// Rewind the focused (root) agent: the guard must clear.
	m.RewindEntries = []RewindEntry{{Index: 0, Content: "earlier message"}}
	m.RewindIndex = 0
	m.Mode = ViewRewind
	m.Input.SetValue("")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	*m = updated.(Model)

	if m.Mode != ViewChat {
		t.Fatalf("Mode = %v, want ViewChat after the rewind", m.Mode)
	}
	if m.GetAgentState(m.Focused.ID()).PayloadRecoveryUsed {
		t.Fatal("rewind must re-arm the one-shot 413 payload-recovery guard")
	}

	// And the re-armed trigger really fires again on the next 413.
	if compacted := runErrorEvent(t, m, m.Focused.ID(), payloadTooLargeErr()); !compacted {
		t.Fatal("after a rewind the next 413 must run the recovery compaction again")
	}
	if runs != 2 {
		t.Fatalf("compaction runner invoked %d times, want 2 (once before, once after the rewind)", runs)
	}
}
