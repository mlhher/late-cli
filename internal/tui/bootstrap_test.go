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
