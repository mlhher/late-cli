package tui

import (
	"errors"
	"fmt"
	"late/internal/client"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	expected := []string{"/compose", "/help", "/log", "/model", "/new", "/quit", "/rewind", "/themes"}

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

	// Verify alphabetical order
	for i := 1; i < len(AvailableCommands); i++ {
		if AvailableCommands[i-1].Name > AvailableCommands[i].Name {
			t.Errorf("AvailableCommands not sorted alphabetically: %q before %q",
				AvailableCommands[i-1].Name, AvailableCommands[i].Name)
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

func TestMessageHookLocksInputUntilSubmission(t *testing.T) {
	orch := &mockOrchestrator{supportsVision: true}
	m := NewModel(orch, nil, nil)
	m.Input.SetValue("hello")
	m.AttachedFiles = []string{"image.png"}
	m.MessageHook = strings.ToUpper

	m, cmd := m.submitMessage("hello")
	if cmd == nil {
		t.Fatal("expected asynchronous message-hook command")
	}
	if m.RunningPluginAction != "message hooks" {
		t.Fatalf("running action = %q, want message hooks", m.RunningPluginAction)
	}
	if got := m.Input.Value(); got != "" {
		t.Fatalf("input was not cleared while hooks run: %q", got)
	}
	if len(m.AttachedFiles) != 0 {
		t.Fatalf("attachments were not snapshotted and cleared: %v", m.AttachedFiles)
	}
	if strings.Contains(m.inputView(), "Running message hooks") {
		t.Fatal("input view showed the message-hook action before the delay")
	}
	m.RunningPluginActionVisibleAfter = time.Now().Add(-time.Millisecond)
	if !strings.Contains(m.inputView(), "Running message hooks") {
		t.Fatal("input view does not show the message-hook action after the delay")
	}

	// Ordinary typing, paste, and a second Enter must all be ignored while
	// the asynchronous action owns the input area.
	m, duplicateCmd := m.updateInternal(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if duplicateCmd != nil || orch.submitCount != 0 {
		t.Fatalf("second Enter was not blocked: cmd=%v submits=%d", duplicateCmd != nil, orch.submitCount)
	}
	m, _ = m.updateInternal(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m, _ = m.updateInternal(tea.PasteMsg{Content: "new draft"})
	if got := m.Input.Value(); got != "" {
		t.Fatalf("input changed while hooks were running: %q", got)
	}

	result := cmd()
	m, _ = m.updateInternal(result)
	if m.RunningPluginAction != "" {
		t.Fatalf("running action not cleared: %q", m.RunningPluginAction)
	}
	if strings.Contains(m.inputView(), "Running message hooks") {
		t.Fatal("input view still shows the message-hook action after completion")
	}
	if orch.submitCount != 1 || orch.submittedText != "HELLO" {
		t.Fatalf("submission = (%d, %q), want (1, HELLO)", orch.submitCount, orch.submittedText)
	}
	if len(orch.submittedImages) != 1 || orch.submittedImages[0] != "image.png" {
		t.Fatalf("submitted attachments = %v, want [image.png]", orch.submittedImages)
	}
}

func TestMessageHookRestoresDraftWhenSubmissionFails(t *testing.T) {
	submitErr := errors.New("submission failed")
	orch := &mockOrchestrator{supportsVision: true, submitErr: submitErr}
	m := NewModel(orch, nil, nil)
	m.Input.SetValue("keep this draft")
	m.AttachedFiles = []string{"image.png"}
	m.Pastes = map[string]string{"placeholder": "original paste"}
	m.MessageHook = func(text string) string { return text + " transformed" }

	m, cmd := m.submitMessage("keep this draft")
	if cmd == nil {
		t.Fatal("expected asynchronous message-hook command")
	}
	m, _ = m.updateInternal(cmd())

	if !errors.Is(m.Err, submitErr) {
		t.Fatalf("model error = %v, want %v", m.Err, submitErr)
	}
	if m.RunningPluginAction != "" {
		t.Fatalf("running action not cleared after failure: %q", m.RunningPluginAction)
	}
	if got := m.Input.Value(); got != "keep this draft" {
		t.Fatalf("restored input = %q", got)
	}
	if len(m.AttachedFiles) != 1 || m.AttachedFiles[0] != "image.png" {
		t.Fatalf("restored attachments = %v, want [image.png]", m.AttachedFiles)
	}
	if got := m.Pastes["placeholder"]; got != "original paste" {
		t.Fatalf("paste mapping was not preserved: %q", got)
	}
}

func TestSlashNew_ResetsViewportAndDismissesAutocomplete(t *testing.T) {
	orch := &mockOrchestrator{
		history: []client.ChatMessage{
			{Role: "user", Content: client.TextContent("hello")},
			{Role: "assistant", Content: client.TextContent("world")},
		},
	}
	m := NewModel(orch, nil, nil)
	m.Width = 80
	m.Height = 24
	m.updateLayout()

	// Simulate being scrolled down in chat history
	m.Viewport.SetYOffset(10)
	// Simulate autocomplete being open because user typed /new
	m.ShowAutocomplete = true
	m.AutocompleteItems = []CommandDef{{Name: "/new", Description: "Start fresh conversation"}}

	// Submit /new command
	m.Input.SetValue("/new")
	updatedModel, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	um := updatedModel.(Model)

	if um.ShowAutocomplete {
		t.Errorf("expected ShowAutocomplete to be false after /new")
	}
	if len(um.AutocompleteItems) != 0 {
		t.Errorf("expected AutocompleteItems to be empty, got %d", len(um.AutocompleteItems))
	}
	if um.Viewport.YOffset() != 0 {
		t.Errorf("expected Viewport YOffset to be 0 (top of welcome screen), got %d", um.Viewport.YOffset())
	}
}

func TestAutocompleteHeight_InvariantScreenHeight(t *testing.T) {
	orch := &mockOrchestrator{}
	m := NewModel(orch, nil, nil)
	m.Width = 80
	m.Height = 24
	m.updateLayout()

	// Initial view without autocomplete
	baseView := m.View()
	baseHeight := strings.Count(baseView.Content, "\n") + 1
	if baseHeight != m.Height {
		t.Fatalf("expected initial view height = %d, got %d", m.Height, baseHeight)
	}

	// Test with various numbers of autocomplete items (1 to 12)
	for count := 1; count <= 12; count++ {
		var items []CommandDef
		for i := 0; i < count; i++ {
			items = append(items, CommandDef{
				Name:        fmt.Sprintf("/cmd%d", i),
				Description: fmt.Sprintf("Command %d description", i),
			})
		}
		m.ShowAutocomplete = true
		m.AutocompleteItems = items
		m.AutocompleteIndex = 0
		m.updateLayout()

		rendered := m.View()
		gotHeight := strings.Count(rendered.Content, "\n") + 1
		if gotHeight != m.Height {
			t.Errorf("item count %d: expected view height = %d, got %d", count, m.Height, gotHeight)
		}

		// Test scrolling through items
		for idx := 0; idx < count; idx++ {
			m.AutocompleteIndex = idx
			scrollRendered := m.View()
			scrollH := strings.Count(scrollRendered.Content, "\n") + 1
			if scrollH != m.Height {
				t.Errorf("item count %d, index %d: expected view height = %d, got %d", count, idx, m.Height, scrollH)
			}
		}
	}

	// Close autocomplete and verify height remains identical
	m.ShowAutocomplete = false
	m.AutocompleteItems = nil
	m.updateLayout()
	closedView := m.View()
	closedHeight := strings.Count(closedView.Content, "\n") + 1
	if closedHeight != m.Height {
		t.Errorf("expected closed view height = %d, got %d", m.Height, closedHeight)
	}
}
