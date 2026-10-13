package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInterruptFocusedAgent_DrainsQueuedMessagesToInput(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	orch := m.Focused.(*benchmarkOrchestrator)
	orch.queuedMessages = []string{"queued prompt 1"}
	s.State = StateThinking

	m.InputHistory = []string{"previous prompt", "queued prompt 1"}

	// Simulate ctrl+g
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	*m = updated.(Model)

	if m.Input.Value() != "queued prompt 1" {
		t.Fatalf("Input.Value() = %q, want %q", m.Input.Value(), "queued prompt 1")
	}
	if len(orch.queuedMessages) != 0 {
		t.Fatalf("queued messages were not drained from orchestrator: %v", orch.queuedMessages)
	}
	if len(m.InputHistory) != 1 || m.InputHistory[0] != "previous prompt" {
		t.Fatalf("InputHistory = %v, want [previous prompt]", m.InputHistory)
	}
	if s.State != StateStopping {
		t.Fatalf("State = %v, want StateStopping", s.State)
	}
}

func TestInterruptFocusedAgent_AppendsToExistingDraft(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	orch := m.Focused.(*benchmarkOrchestrator)
	orch.queuedMessages = []string{"queued prompt"}
	s.State = StateStreaming

	m.Input.SetValue("work in progress")
	m.InputHistory = []string{"queued prompt"}

	// Simulate ctrl+g
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	*m = updated.(Model)

	want := "queued prompt\nwork in progress"
	if m.Input.Value() != want {
		t.Fatalf("Input.Value() = %q, want %q", m.Input.Value(), want)
	}
	if len(orch.queuedMessages) != 0 {
		t.Fatalf("queued messages were not drained: %v", orch.queuedMessages)
	}
}

func TestInterruptFocusedAgent_MultipleQueuedMessages(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	orch := m.Focused.(*benchmarkOrchestrator)
	orch.queuedMessages = []string{"queued 1", "queued 2"}
	s.State = StateThinking

	m.InputHistory = []string{"turn 0", "queued 1", "queued 2"}

	// Simulate ctrl+g
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	*m = updated.(Model)

	want := "queued 1\nqueued 2"
	if m.Input.Value() != want {
		t.Fatalf("Input.Value() = %q, want %q", m.Input.Value(), want)
	}
	if len(orch.queuedMessages) != 0 {
		t.Fatalf("queued messages were not drained: %v", orch.queuedMessages)
	}
	if len(m.InputHistory) != 1 || m.InputHistory[0] != "turn 0" {
		t.Fatalf("InputHistory = %v, want [turn 0]", m.InputHistory)
	}
}

func TestInterruptFocusedAgent_EscConfirmDrains(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	orch := m.Focused.(*benchmarkOrchestrator)
	orch.queuedMessages = []string{"queued prompt"}
	s.State = StateThinking

	m.EscConfirmPending = true
	m.InputHistory = []string{"turn 0", "queued prompt"}

	// Simulate 'y' confirmation
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	*m = updated.(Model)

	if m.Input.Value() != "queued prompt" {
		t.Fatalf("Input.Value() = %q, want %q", m.Input.Value(), "queued prompt")
	}
	if len(orch.queuedMessages) != 0 {
		t.Fatalf("queued messages were not drained: %v", orch.queuedMessages)
	}
	if m.EscConfirmPending {
		t.Fatalf("EscConfirmPending should be false")
	}
	if s.State != StateStopping {
		t.Fatalf("State = %v, want StateStopping", s.State)
	}
}

// TestEscReleaseKeepsConfirmPending pins the release-event fix for the esc
// confirm flow: KeyReleaseMsg implements KeyMsg, so on release-reporting
// terminals the esc RELEASE (same String() as the press) used to land in the
// EscConfirmPending handler — gated on !EscConfirmPending only for the
// chat-mode swallow — and instantly cancel the just-opened "esc again to
// confirm" prompt before it could be answered. Releases must be inert: the
// prompt stays open until a real press answers it.
func TestEscReleaseKeepsConfirmPending(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	// The confirm dialog renders through Model.Renderer (production always
	// builds one); tests that pass nil must install the cached default.
	m.Renderer = m.GetRenderer(m.Width)
	m.EscConfirmPending = true

	updated, _ := m.Update(tea.KeyReleaseMsg(tea.Key{Code: tea.KeyEscape}))
	*m = updated.(Model)

	if !m.EscConfirmPending {
		t.Fatal("an esc key RELEASE must not cancel the pending esc confirmation")
	}
}

// TestEscPressStillOpensConfirm pins the press path against overcorrection:
// swallowing releases globally must not affect the esc PRESS that opens the
// confirmation.
func TestEscPressStillOpensConfirm(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Renderer = m.GetRenderer(m.Width)

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	*m = updated.(Model)

	if !m.EscConfirmPending {
		t.Fatal("an esc key PRESS at the main view must open the esc confirmation")
	}
}

// TestYReleaseAfterConfirmInterruptDoesNotDoubleFire pins the confirm path:
// after the "y" PRESS interrupts (clearing EscConfirmPending), the "y"
// RELEASE must not re-enter the confirm handler or reach the input.
func TestYReleaseAfterConfirmInterruptDoesNotDoubleFire(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	m.Renderer = m.GetRenderer(m.Width) // the confirm dialog render path
	orch := m.Focused.(*benchmarkOrchestrator)
	orch.queuedMessages = nil
	s.State = StateThinking
	m.EscConfirmPending = true

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	*m = updated.(Model)
	if m.EscConfirmPending {
		t.Fatal("precondition: the y press must consume the pending confirmation")
	}

	updated, _ = m.Update(tea.KeyReleaseMsg(tea.Key{Code: 'y', Text: "y"}))
	*m = updated.(Model)

	if got := m.Input.Value(); got != "" {
		t.Fatalf("input = %q after the y release, want nothing typed", got)
	}
}
