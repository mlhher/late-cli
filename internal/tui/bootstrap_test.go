package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestBootstrapIndicatorKeepsInputUsable(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(120, 30)
	m.ShowCWD = false

	updated, _ := m.Update(BootstrapStatusMsg{Text: "Discovering model backend...", Active: true})
	m = updated.(Model)
	status := ansi.Strip(m.statusBarView())
	if !strings.Contains(status, "✦") || !strings.Contains(status, "Discovering model backend...") {
		t.Fatalf("missing startup activity indicator: %q", status)
	}
	if m.GetAgentState(m.Root.ID()).State != StateIdle {
		t.Fatal("bootstrap must not mark the agent as executing")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(Model)
	if m.Input.Value() != "a" {
		t.Fatal("bootstrap blocked typing")
	}

	updated, _ = m.Update(BootstrapStatusMsg{Text: "Ready", Active: false})
	m = updated.(Model)
	if m.BootstrapStatus != "" || strings.Contains(ansi.Strip(m.statusBarView()), "✦") {
		t.Fatal("startup activity indicator remained after bootstrap completed")
	}
	if m.Input.Value() != "a" {
		t.Fatal("bootstrap completion changed the draft")
	}
}

func TestBootstrapNextToastSequence(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(120, 30)

	nextToast := &ToastMsg{Text: "Applied logit biases"}
	updated, cmd := m.Update(BootstrapStatusMsg{
		Text:      "Backend: llama.cpp (4096k)",
		Active:    false,
		NextToast: nextToast,
	})
	m = updated.(Model)
	if m.ToastMessage != "Backend: llama.cpp (4096k)" {
		t.Fatalf("expected backend message in toast, got %q", m.ToastMessage)
	}
	if cmd == nil {
		t.Fatal("expected command for toast transition")
	}

	// When ToastMsg arrives, toast message updates to logit bias message
	updated, clearCmd := m.Update(*nextToast)
	m = updated.(Model)
	if m.ToastMessage != "Applied logit biases" {
		t.Fatalf("expected toast message to be 'Applied logit biases', got %q", m.ToastMessage)
	}
	if clearCmd == nil {
		t.Fatal("expected clear command after logit bias toast")
	}
}

