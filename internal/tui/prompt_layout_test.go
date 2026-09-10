package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"late/internal/client"
)

func TestInputPromptGutterAlignsEveryLine(t *testing.T) {
	for _, content := range []string{"FIRST\nSECOND", strings.Repeat("a", 45)} {
		m := NewModel(&mockOrchestrator{}, nil, nil)
		m.SetSize(24, 30)
		m.Input.SetValue(content)
		m.Input.SetHeight(4)
		rows := strings.Split(ansi.Strip(m.Input.View()), "\n")
		if strings.Count(strings.Join(rows, "\n"), "❯") != 1 {
			t.Fatalf("expected one prompt marker: %q", rows)
		}
		seen := 0
		for _, row := range rows {
			text := strings.TrimSpace(strings.TrimPrefix(row, "❯ "))
			if text == "" {
				continue
			}
			if seen == 0 {
				if !strings.HasPrefix(row, "❯ ") {
					t.Fatalf("missing first-line prompt: %q", row)
				}
			} else if !strings.HasPrefix(row, "  ") || strings.HasPrefix(row, "   ") {
				t.Fatalf("continuation line is not aligned: %q", row)
			}
			seen++
		}
		if seen < 2 {
			t.Fatalf("test did not exercise multiple lines: %q", rows)
		}
		if m.Input.Value() != content {
			t.Fatal("prompt marker leaked into input value")
		}
	}
}

func TestInputLiteralQuotePrefixSurvivesSubmission(t *testing.T) {
	orch := &mockOrchestrator{}
	m := NewModel(orch, nil, nil)
	m.Input.SetValue("> quoted text\nsecond line")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if orch.submittedText != "> quoted text\nsecond line" {
		t.Fatalf("modified user text: %q", orch.submittedText)
	}
	if m.Input.Value() != "" {
		t.Fatal("input did not clear")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if updated.(Model).Input.Value() != "" {
		t.Fatal("backspace inserted a fake prompt")
	}
}

func TestUserPromptRailWrapsAndPadsWholeMessage(t *testing.T) {
	m, _ := newViewportBenchmarkModel([]client.ChatMessage{{Role: "user", Content: client.TextContent("FIRST\nSECOND " + strings.Repeat("wrapped text ", 8))}})
	m.SetSize(40, 30)
	rendered := testTranscriptContent(m)
	if !strings.Contains(rendered, "67;143;163") {
		t.Fatal("prompt rail is not the intended calm blue")
	}
	if !strings.Contains(rendered, "18;20;26") {
		t.Fatal("prompt has no subtle surface background")
	}
	rows := strings.Split(ansi.Strip(rendered), "\n")
	seen := 0
	for _, row := range rows {
		if strings.TrimSpace(row) == "" {
			continue
		}
		if !strings.HasPrefix(row, " │ ") {
			t.Fatalf("inconsistent prompt gutter/padding: %q", row)
		}
		if ansi.StringWidth(row) > 40 {
			t.Fatalf("prompt exceeds allocated width: %q", row)
		}
		seen++
	}
	if seen < 3 {
		t.Fatal("test did not exercise wrapping and explicit newline")
	}
}
