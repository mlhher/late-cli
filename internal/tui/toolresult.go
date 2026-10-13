package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Tool results reach the history as plain strings. This file turns one into
// what the transcript shows next to the tool call: a one-line outcome, and on
// request the detail behind it.

// Prefixes the tool layer uses for results that did not succeed. Several tools
// report failure as an ordinary result starting with "Error", not as a Go error.
const (
	deniedPrefix       = "Tool execution cancelled by user"
	deniedReasonPrefix = deniedPrefix + ". The user said: "
	shellFailedPrefix  = "Command failed with exit code "
	shellErrorPrefix   = "Error executing command: "
	shellTruncatedTail = "... (output truncated)"
)

type toolOutcome struct {
	failed bool
	denied bool
	// summary is a single plain-text line: "+3 -1", "exit 1 · FAIL", "denied".
	summary string
}

// oneLine collapses every run of whitespace (tabs included, whose width a
// terminal decides, not us) to a single space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = oneLine(line); line != "" {
			return line
		}
	}
	return ""
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := oneLine(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// textLines counts the lines of s, ignoring one trailing newline.
func textLines(s string) int { return len(splitDiffLines(s)) }

// plainSummary turns the first line of a free-form result into something fit
// for a badge: Markdown emphasis and heading marks are dropped.
func plainSummary(s string) string {
	line := strings.TrimLeft(firstLine(s), "#*->• ")
	return strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(line))
}

// summarizeToolOutcome classifies one finished tool call from its name,
// arguments (JSON) and result text.
func summarizeToolOutcome(name, args, result string) toolOutcome {
	// Command output may carry its own colours; none of it belongs in a badge.
	trimmed := strings.TrimSpace(ansi.Strip(result))

	if reason, ok := strings.CutPrefix(trimmed, deniedReasonPrefix); ok {
		return toolOutcome{denied: true, summary: "denied · “" + firstLine(reason) + "”"}
	}
	if strings.HasPrefix(trimmed, deniedPrefix) {
		return toolOutcome{denied: true, summary: "denied"}
	}

	if name == "bash" {
		switch {
		case strings.HasPrefix(trimmed, shellFailedPrefix):
			var code int
			_, _ = fmt.Sscanf(strings.TrimPrefix(trimmed, shellFailedPrefix), "%d", &code)
			summary := fmt.Sprintf("exit %d", code)
			// The header is the first line; the last line of output is usually
			// the verdict ("FAIL", "error: ...").
			if _, body, found := strings.Cut(trimmed, "\n"); found {
				if tail := lastLine(body); tail != "" {
					summary += " · " + tail
				}
			}
			return toolOutcome{failed: true, summary: summary}
		case strings.HasPrefix(trimmed, shellErrorPrefix):
			return toolOutcome{failed: true, summary: firstLine(strings.TrimPrefix(trimmed, shellErrorPrefix))}
		case trimmed == "(binary output detected)":
			return toolOutcome{summary: "binary output"}
		case trimmed == "":
			return toolOutcome{summary: "no output"}
		}
		summary := plural(textLines(strings.TrimSpace(strings.TrimSuffix(trimmed, shellTruncatedTail))), "line", "lines")
		if strings.HasSuffix(trimmed, shellTruncatedTail) {
			summary += ", truncated"
		}
		return toolOutcome{summary: summary}
	}

	switch {
	case strings.HasPrefix(trimmed, "Error executing tool "):
		_, msg, _ := strings.Cut(trimmed, ": ")
		return toolOutcome{failed: true, summary: firstLine(msg)}
	case strings.HasPrefix(trimmed, "Error"):
		msg := trimmed
		if _, after, found := strings.Cut(trimmed, ": "); found {
			msg = after
		}
		return toolOutcome{failed: true, summary: firstLine(msg)}
	}

	switch name {
	case "target_edit":
		var p struct{ Search, Replace string }
		if json.Unmarshal([]byte(args), &p) == nil {
			added, deleted := diffStats(diffLines(p.Search, p.Replace, 0))
			return toolOutcome{summary: fmt.Sprintf("+%d -%d", added, deleted)}
		}
		return toolOutcome{summary: "edited"}
	case "write_file":
		var p struct{ Content string }
		if json.Unmarshal([]byte(args), &p) == nil {
			return toolOutcome{summary: "wrote " + plural(textLines(p.Content), "line", "lines")}
		}
		return toolOutcome{summary: "written"}
	case "read_file":
		return toolOutcome{summary: plural(textLines(result), "line", "lines")}
	case "search_content", "find_files":
		if strings.HasPrefix(trimmed, "No ") {
			return toolOutcome{summary: "no matches"}
		}
		return toolOutcome{summary: plural(textLines(result), "line", "lines")}
	}
	if line := plainSummary(trimmed); line != "" {
		return toolOutcome{summary: line}
	}
	return toolOutcome{summary: "done"}
}

// toolBadgeWithOutcome renders "  ↳ label  ▸ ✓ summary" for a finished call.
// When the row is too narrow the summary gives way first, then the label; the
// mark is always kept so success and failure stay visible.
func toolBadgeWithOutcome(label string, o toolOutcome, expanded bool, width int) string {
	mark, markColor := "✓", accentEmerald
	switch {
	case o.denied:
		mark, markColor = "⊘", primaryColor
	case o.failed:
		mark, markColor = "✗", accentCoral
	}
	caret := "▸"
	if expanded {
		caret = "▾"
	}

	const (
		prefix      = "  ↳ "
		minLabel    = 8
		keepSummary = 16
	)
	// Cells outside the label and the summary: two-space gap, caret, space, mark.
	fixed := lipgloss.Width(prefix) + 2 + lipgloss.Width(caret) + 1 + lipgloss.Width(mark)
	room := max(1, width-fixed) // for label + " " + summary

	summary := o.summary
	labelW, sumW := lipgloss.Width(label), lipgloss.Width(summary)
	if labelW+1+sumW > room {
		keep := min(sumW, keepSummary)
		switch {
		case labelW+1+keep <= room:
			summary = ansi.Truncate(summary, room-labelW-1, "…")
		case room-1-keep >= minLabel:
			label = ansi.Truncate(label, room-1-keep, "...")
			summary = ansi.Truncate(summary, room-1-lipgloss.Width(label), "…")
		default:
			// No room for a readable summary: label and mark only.
			label = ansi.Truncate(label, room, "...")
			summary = ""
		}
	}

	bg := lipgloss.NewStyle().Background(appBgColor)
	out := tagStyle.Render(prefix+label) +
		bg.Foreground(mutedTextColor).Render("  "+caret+" ") +
		bg.Foreground(markColor).Bold(true).Render(mark)
	if summary != "" {
		out += bg.Foreground(subtextColor).Render(" " + summary)
	}
	return out
}

const (
	maxToolDetailRows = 30
	toolDetailIndent  = 4
	toolDetailLead    = toolDetailIndent + 2 // indent + "│ "
)

// detailLead is the left gutter for detail rows: an indent and a thin rule.
func detailLead() string {
	return lipgloss.NewStyle().Background(appBgColor).Render(strings.Repeat(" ", toolDetailIndent)) +
		lipgloss.NewStyle().Foreground(activeBorder).Background(appBgColor).Render("│ ")
}

// toolDetailRows draws the detail of a finished call, already styled and at
// most width cells wide per row, ready to sit under the call's badge row.
func toolDetailRows(name, args, result string, width int) []string {
	inner := max(10, width-toolDetailLead)
	note := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)

	var body []string
	switch name {
	case "target_edit":
		var p struct{ Search, Replace string }
		if json.Unmarshal([]byte(args), &p) == nil {
			var hidden int
			body, hidden = renderDiffRows(collapseDiff(diffLines(p.Search, p.Replace, 0), 2), inner, maxToolDetailRows)
			if hidden > 0 {
				body = append(body, note.Render(fmt.Sprintf("… %d more lines not shown", hidden)))
			}
		}
	case "write_file":
		var p struct{ Content string }
		if json.Unmarshal([]byte(args), &p) == nil {
			body = plainDetailRows(p.Content, inner)
		}
	}
	if body == nil {
		body = plainDetailRows(result, inner)
	}

	lead := detailLead()
	rows := make([]string, len(body))
	for i, row := range body {
		rows[i] = lead + row
	}
	return rows
}

// plainDetailRows wraps text to width and, past maxToolDetailRows rows, keeps
// the start and the end (where a failing build or test prints its verdict) and
// says how many rows sit between. ANSI sequences in the text are dropped: a
// command's own colours must not leak into the layout.
func plainDetailRows(text string, width int) []string {
	dim := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
	note := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)

	var wrapped []string
	for _, line := range strings.Split(strings.TrimRight(ansi.Strip(text), "\r\n"), "\n") {
		line = strings.ReplaceAll(strings.TrimRight(line, "\r"), "\t", "    ")
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}

	if len(wrapped) <= maxToolDetailRows {
		rows := make([]string, len(wrapped))
		for i, line := range wrapped {
			rows[i] = dim.Render(line)
		}
		return rows
	}
	head := maxToolDetailRows / 3
	tail := maxToolDetailRows - head - 1
	rows := make([]string, 0, maxToolDetailRows)
	for _, line := range wrapped[:head] {
		rows = append(rows, dim.Render(line))
	}
	rows = append(rows, note.Render(fmt.Sprintf("⋯ %d lines hidden ⋯", len(wrapped)-head-tail)))
	for _, line := range wrapped[len(wrapped)-tail:] {
		rows = append(rows, dim.Render(line))
	}
	return rows
}

const maxReportRows = 80

// reportDetailRows lays an already Markdown-rendered report under a badge row,
// keeping its start when it is longer than maxReportRows.
func reportDetailRows(rendered string, width int) []string {
	inner := max(10, width-toolDetailLead)
	note := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)

	lines := strings.Split(strings.Trim(rendered, "\r\n"), "\n")
	hidden := 0
	if len(lines) > maxReportRows {
		hidden = len(lines) - maxReportRows
		lines = lines[:maxReportRows]
	}
	lead := detailLead()
	rows := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		rows = append(rows, lead+ansi.Truncate(line, inner, ""))
	}
	if hidden > 0 {
		rows = append(rows, lead+note.Render(fmt.Sprintf("⋯ %d more lines not shown", hidden)))
	}
	return rows
}

// overTodoPane reports whether screen column x lies over the todo pane.
func (m *Model) overTodoPane(x int) bool {
	return m.ShowTodoPane && m.Width >= 85 && x >= m.Width-todoPaneWidth
}

// toggleToolDetail flips one tool call between expanded and collapsed.
func (m *Model) toggleToolDetail(s *AppState, id string) {
	if s.ToggledTools == nil {
		s.ToggledTools = make(map[string]bool)
	}
	if s.ToggledTools[id] {
		delete(s.ToggledTools, id)
	} else {
		s.ToggledTools[id] = true
	}
	m.updateViewport()
}
