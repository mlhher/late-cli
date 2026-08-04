package tui

import (
	"testing"
)

// TestPluginCommands_Empty verifies that an unconfigured Model has
// nil plugin commands.
func TestPluginCommands_Empty(t *testing.T) {
	m := &Model{}

	if m.PluginCommands != nil {
		t.Errorf("expected nil, got %v", m.PluginCommands)
	}
}

// TestPluginCommands_SetAndGet verifies that assigning to the field
// round-trips cleanly.
func TestPluginCommands_SetAndGet(t *testing.T) {
	m := &Model{}
	expected := []string{"/query", "/graph", "/analyze"}

	m.PluginCommands = expected
	cmds := m.PluginCommands

	if len(cmds) != len(expected) {
		t.Fatalf("expected %d commands, got %d", len(expected), len(cmds))
	}
	for i := range expected {
		if cmds[i] != expected[i] {
			t.Errorf("command[%d]: expected %q, got %q", i, expected[i], cmds[i])
		}
	}
}

// TestSetPluginCommands_Nil sets nil and verifies the field is nil.
func TestSetPluginCommands_Nil(t *testing.T) {
	m := &Model{}
	m.PluginCommands = nil
	if m.PluginCommands != nil {
		t.Errorf("expected nil after setting nil, got %v", m.PluginCommands)
	}
}

// TestSetPluginCommands_EmptySlice sets an empty slice and verifies the field is non-nil but empty.
func TestSetPluginCommands_EmptySlice(t *testing.T) {
	m := &Model{}
	m.PluginCommands = []string{}
	if len(m.PluginCommands) != 0 {
		t.Errorf("expected empty after setting empty slice, got %v", m.PluginCommands)
	}
}

// TestSetPluginCommands_Replace verifies that reassigning replaces the old slice.
func TestSetPluginCommands_Replace(t *testing.T) {
	m := &Model{}
	m.PluginCommands = []string{"/old"}
	m.PluginCommands = []string{"/new"}

	cmds := m.PluginCommands
	if len(cmds) != 1 || cmds[0] != "/new" {
		t.Errorf("expected [\"/new\"], got %v", cmds)
	}
}

// TestAvailableCommands_ContainsBuiltins verifies that the built-in command set
// includes the expected slash commands.
func TestAvailableCommands_ContainsBuiltins(t *testing.T) {
	expected := []string{"/clear", "/compose", "/help", "/log", "/quit", "/rewind"}

	for _, exp := range expected {
		found := false
		for _, cmd := range AvailableCommands {
			if cmd.Name == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AvailableCommands should contain %q", exp)
		}
	}

	if len(AvailableCommands) < 6 {
		t.Errorf("expected at least 6 built-in commands, got %d", len(AvailableCommands))
	}
}

// TestPluginCommands_IsolatedModels verifies that two different Model instances
// have independent PluginCommands slices.
func TestPluginCommands_IsolatedModels(t *testing.T) {
	m1 := &Model{}
	m2 := &Model{}

	m1.PluginCommands = []string{"/m1-cmd"}
	m2.PluginCommands = []string{"/m2-cmd"}

	if c := m1.PluginCommands; len(c) != 1 || c[0] != "/m1-cmd" {
		t.Errorf("expected m1 to have [/m1-cmd], got %v", c)
	}
	if c := m2.PluginCommands; len(c) != 1 || c[0] != "/m2-cmd" {
		t.Errorf("expected m2 to have [/m2-cmd], got %v", c)
	}
}

// TestIsPluginCmd_MatchesWithArgs verifies isPluginCmd matches a registered
// command on its first whitespace-separated field, so handlers can receive
// trailing arguments ("/lint file.go" must match "/lint") while lookalike
// prefixes ("/lint2") still do not match.
func TestIsPluginCmd_MatchesWithArgs(t *testing.T) {
	cmds := []string{"/lint", "/graph", "/analyze"}
	cases := []struct {
		input string
		want  bool
	}{
		{"/lint", true},
		{"/lint file.go", true},
		{"/lint   file.go extra", true},
		{"  /graph --depth 2  ", true},
		{"/analyze", true},
		{"/lint2", false},
		{"/nothing", false},
		{"", false},
		{"   ", false},
	}
	for _, c := range cases {
		if got := isPluginCmd(c.input, cmds); got != c.want {
			t.Errorf("isPluginCmd(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}
