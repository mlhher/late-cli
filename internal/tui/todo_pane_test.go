package tui

import (
	"reflect"
	"strings"
	"testing"

	"late/internal/common"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWrapTodoText(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxLen int
		want   []string
	}{
		{
			name:   "short text fits on single line",
			text:   "Setup Reviewer",
			maxLen: 28,
			want:   []string{"Setup Reviewer"},
		},
		{
			name:   "long text wraps to multiple lines",
			text:   "Setup Implementation Reviewer Subagent Configuration",
			maxLen: 28,
			want: []string{
				"Setup Implementation",
				"Reviewer Subagent",
				"Configuration",
			},
		},
		{
			name:   "empty text",
			text:   "",
			maxLen: 28,
			want:   []string{""},
		},
		{
			name:   "wraps by terminal cell width",
			text:   "界界界 next",
			maxLen: 6,
			want:   []string{"界界界", "next"},
		},
		{
			name:   "does not split emoji grapheme clusters",
			text:   "👩‍💻👩‍💻 done",
			maxLen: 4,
			want:   []string{"👩‍💻👩‍💻", "done"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapTodoText(tt.text, tt.maxLen)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wrapTodoText(%q, %d) = %q, want %q", tt.text, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTodoContentLinesAlignWrappedText(t *testing.T) {
	todos := []common.TodoItem{
		{Text: "A task whose description needs to wrap cleanly", Done: false},
	}

	lines := todoContentLines(todos, 24)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped content, got %q", lines)
	}

	firstLine := ansi.Strip(lines[0])
	if !strings.Contains(firstLine, "A task") {
		t.Fatalf("first line does not contain task text: %q", lines[0])
	}
	firstIndent := lipgloss.Width(firstLine) - lipgloss.Width(strings.TrimLeft(firstLine, " 1○✓"))
	continuation := ansi.Strip(lines[1])
	if got := lipgloss.Width(continuation) - lipgloss.Width(strings.TrimLeft(continuation, " ")); got != firstIndent {
		t.Fatalf("continuation indent = %d, want %d; lines: %q", got, firstIndent, lines)
	}
}

func TestTodoContentLinesUseCompactCompletedStyle(t *testing.T) {
	lines := todoContentLines([]common.TodoItem{{Text: "Finished", Done: true}}, 30)
	rendered := strings.Join(lines, "\n")
	if !strings.Contains(rendered, "✓") {
		t.Fatalf("completed item has no check mark: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[9m") {
		t.Fatalf("completed item should not use strikethrough: %q", rendered)
	}
}

func TestTodoMaxScrollOffset(t *testing.T) {
	todos := make([]common.TodoItem, 10)
	for i := range todos {
		todos[i] = common.TodoItem{Text: "A short task"}
	}
	content := todoContentLines(todos, todoPaneWidth-1)
	height := 7
	want := len(content) - (height - 2)
	if want < 0 {
		want = 0
	}
	if got := todoMaxScrollOffsetFor(todos, height); got != want {
		t.Fatalf("max scroll offset = %d, want %d", got, want)
	}
}
