package tui

import (
	"bytes"
	"context"
	"late/internal/client"
	"late/internal/common"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type mockOrchestrator struct {
	submittedText   string
	submittedImages []string
	submitCount     int
	submitErr       error
	supportsVision  bool
}

func (m *mockOrchestrator) ID() string { return "mock" }
func (m *mockOrchestrator) Submit(text string, images []string) error {
	m.submittedText = text
	m.submittedImages = append([]string(nil), images...)
	m.submitCount++
	return m.submitErr
}
func (m *mockOrchestrator) Execute(text string) (string, error)      { return "", nil }
func (m *mockOrchestrator) Reset() error                             { return nil }
func (m *mockOrchestrator) Rewind(index int) error                   { return nil }
func (m *mockOrchestrator) Cancel()                                  {}
func (m *mockOrchestrator) IsStopRequested() bool                    { return false }
func (m *mockOrchestrator) Events() <-chan common.Event              { return nil }
func (m *mockOrchestrator) History() []client.ChatMessage            { return nil }
func (m *mockOrchestrator) Context() context.Context                 { return context.Background() }
func (m *mockOrchestrator) Middlewares() []common.ToolMiddleware     { return nil }
func (m *mockOrchestrator) Registry() *common.ToolRegistry           { return nil }
func (m *mockOrchestrator) SystemPrompt() string                     { return "" }
func (m *mockOrchestrator) ToolDefinitions() []client.ToolDefinition { return nil }
func (m *mockOrchestrator) Children() []common.Orchestrator          { return nil }
func (m *mockOrchestrator) Parent() common.Orchestrator              { return nil }
func (m *mockOrchestrator) SetMaxTurns(int)                          {}
func (m *mockOrchestrator) RefreshContextSize(context.Context)       {}
func (m *mockOrchestrator) MaxTokens() int                           { return 100 }
func (m *mockOrchestrator) SupportsVision() bool                     { return m.supportsVision }
func (m *mockOrchestrator) QueuedMessages() []string                 { return nil }

type mockKey struct {
	code rune
	text string
}

func (k mockKey) String() string { return k.text }
func (k mockKey) Key() tea.Key {
	return tea.Key{Code: k.code, Text: k.text}
}

func TestPastePlaceholderReplacement(t *testing.T) {
	orch := &mockOrchestrator{}
	model := NewModel(orch, nil, nil)

	// Simulate PasteMsg of 5 lines
	pasteText := "line1\nline2\nline3\nline4\nline5"
	msg := tea.PasteMsg{Content: pasteText}

	// Update
	res, _ := model.Update(msg)
	model = res.(Model)

	// Verify placeholder is inserted (token is unique: [Pasted #5 lines <id>])
	inputVal := model.Input.Value()
	if !strings.Contains(inputVal, "[Pasted #5 lines") {
		t.Errorf("Expected input to contain a [Pasted #5 lines placeholder, got %q", inputVal)
	}
	if len(model.Pastes) != 1 {
		t.Fatalf("Expected exactly 1 paste mapping, got %d", len(model.Pastes))
	}
	var placeholder, original string
	for k, v := range model.Pastes {
		placeholder, original = k, v
	}
	if original != pasteText {
		t.Errorf("Expected mapping value %q, got %q", pasteText, original)
	}
	if !strings.Contains(inputVal, placeholder) {
		t.Errorf("Expected input %q to contain generated placeholder %q", inputVal, placeholder)
	}

	// Simulate pressing enter/submitting
	msgEnter := mockKey{code: '\r', text: "enter"}
	res, _ = model.Update(msgEnter)
	model = res.(Model)

	// Verify original content was submitted
	if orch.submittedText != pasteText {
		t.Errorf("Expected submitted text to be %q, got %q", pasteText, orch.submittedText)
	}

	// Verify pastes mapping was cleared
	if len(model.Pastes) != 0 {
		t.Errorf("Expected model.Pastes to be empty after submission, got %d items", len(model.Pastes))
	}
}

func TestStartPromptMsgSubmitsPrompt(t *testing.T) {
	orch := &mockOrchestrator{}
	model := NewModel(orch, nil, nil)

	res, cmd := model.Update(StartPromptMsg("fix the tests"))
	model = res.(Model)
	if cmd == nil {
		t.Fatal("expected command to submit the startup prompt")
	}

	res, _ = model.Update(cmd())
	model = res.(Model)

	if orch.submittedText != "fix the tests" {
		t.Fatalf("expected startup prompt to be submitted, got %q", orch.submittedText)
	}
	if model.Input.Value() != "> " {
		t.Fatalf("expected input to be cleared after submission, got %q", model.Input.Value())
	}
}

func TestPasteBinaryIgnored(t *testing.T) {
	orch := &mockOrchestrator{}
	model := NewModel(orch, nil, nil)

	// Set initial state
	model.Input.SetValue("> hello")
	model.lastInputLen = len(model.Input.Value())

	// 1. Simulate PasteMsg of binary content
	binaryText := "line1\nline2\x00\nline3"
	msg := tea.PasteMsg{Content: binaryText}

	res, _ := model.Update(msg)
	model = res.(Model)

	// Verify binary PasteMsg content is not in Pastes map and not inserted into Input
	if len(model.Pastes) != 0 {
		t.Errorf("Expected model.Pastes to be empty after binary paste, got %d items", len(model.Pastes))
	}
	if strings.Contains(model.Input.Value(), "line1") || strings.Contains(model.Input.Value(), "line2") {
		t.Errorf("Expected binary paste to be ignored, but input contains pasted content: %q", model.Input.Value())
	}
}

func TestPastePlaceholderSubmitNoCollision(t *testing.T) {
	orch := &mockOrchestrator{}
	model := NewModel(orch, nil, nil)

	// Two multi-line pastes. The second paste's CONTENT contains a string
	// that looks exactly like a placeholder; it must survive submission
	// verbatim and never be expanded into the first paste's content.
	first := "alpha\nbeta\ngamma\ndelta\nepsilon"
	second := "one\ntwo\nthree\nfour\nfive [Pasted #5 lines 000000] end"

	for _, p := range []string{first, second} {
		res, _ := model.Update(tea.PasteMsg{Content: p})
		model = res.(Model)
	}

	if len(model.Pastes) != 2 {
		t.Fatalf("Expected 2 paste mappings, got %d", len(model.Pastes))
	}

	res, _ := model.Update(mockKey{code: '\r', text: "enter"})
	model = res.(Model)

	want := first + second
	if orch.submittedText != want {
		t.Errorf("Paste collision on submit.\n got %q\nwant %q", orch.submittedText, want)
	}
	if len(model.Pastes) != 0 {
		t.Errorf("Expected pastes cleared after submit, got %d", len(model.Pastes))
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte(""), false},
		{"plain text", []byte("hello\nworld"), false},
		// Existing NUL-byte behavior must still hold.
		{"nul byte", []byte("line1\nline2\x00\nline3"), true},
		// Invalid UTF-8 (e.g. a pasted image/gzip blob) is now rejected.
		{"invalid utf8", []byte{0xff, 0xfe, 0x41, 0x42}, true},
		// Valid multibyte UTF-8 must NOT be treated as binary.
		{"utf8 multibyte", []byte("héllo 世界\n"), false},
		// Base64 of binary is valid ASCII text and should pass through.
		{"base64 ascii", []byte("aGVsbG8gd29ybGQgdGhpcyBpcyBvbmx5IHRleHQ="), false},
		// Control-heavy content (>>10% raw control bytes) is binary.
		{"control heavy", append(bytes.Repeat([]byte("\x01\x02"), 60), []byte("ab")...), true},
		// Mostly-text with a few incidental control bytes stays text.
		{"sparse control", append([]byte("normal text here\x01\x02"), bytes.Repeat([]byte("x"), 200)...), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinary(tt.data); got != tt.want {
				t.Errorf("isBinary(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}
