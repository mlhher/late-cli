package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"late/internal/common"
	appconfig "late/internal/config"
)

// The info bar is the calm second row under the status bar. The status bar is
// the live row (activity, focus, speed, context); this one carries what the
// session is made of and how it is going:
//
//	host · model   approval ask   todos 3/7   subagents 2 running          turn 1m42s
//
// Model and approval mode are always shown; the rest are there when they have
// something to say. When the window is too narrow, whole segments are dropped
// (the endpoint host first, then turn time, todos, subagents, approval)
// instead of being cut in half; the model name is the last thing truncated.

// infoBarGap is the minimum space kept between the left cluster and the turn
// time.
const infoBarGap = 2

// Caps that keep a long endpoint host or model name from taking the whole row
// on a wide terminal.
const (
	infoBarHostMax  = 24
	infoBarModelMax = 40
)

// clipInfo shortens s to at most n columns, marking the cut with an ellipsis.
func clipInfo(s string, n int) string {
	return ansi.Truncate(s, n, "…")
}

// infoBarSegment is one rendered piece of the row.
type infoBarSegment struct {
	text  string
	width int
}

func newInfoBarSegment(text string) infoBarSegment {
	return infoBarSegment{text: text, width: lipgloss.Width(text)}
}

// runningSubagents counts non-root agents that are working, and those that
// are blocked waiting for the user to approve something.
func (m *Model) runningSubagents() (running, awaiting int) {
	rootID := ""
	if m.Root != nil {
		rootID = m.Root.ID()
	}
	for id, s := range m.AgentStates {
		if id == rootID || id == common.MainAgentID {
			continue
		}
		switch s.State {
		case StateThinking, StateStreaming, StateStopping:
			running++
		case StateConfirmTool, StateConfirmSubagent:
			awaiting++
		}
	}
	return running, awaiting
}

// todoProgress returns how many todos are done out of how many exist.
func (m *Model) todoProgress() (done, total int) {
	for _, td := range m.todoItems() {
		total++
		if td.Done {
			done++
		}
	}
	return done, total
}

// infoBarView renders the optional info row toggled by /infobar and persisted
// as show-info-bar. It is always exactly one row, padded to the window width
// so the background stays opaque.
func (m *Model) infoBarView() string {
	if !m.ShowInfoBar || m.ShowFilePicker {
		return ""
	}
	w := max(m.Width, 1)
	now := time.Now()

	bg := func(c color.Color) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(c).Background(appBgColor)
	}
	label := bg(mutedTextColor)
	value := bg(subtextColor)
	strong := bg(textColor)
	accent := bg(primaryColor)
	good := bg(accentEmerald)
	warn := bg(warningColor).Bold(true)
	sep := label.Render("  ·  ")

	// Model: the one segment that is never dropped, truncated last. The
	// endpoint host in front of it is the first thing to go when space runs
	// out.
	host, name := m.focusedModelInfo()
	bareModel := strong.Render(clipInfo(name, infoBarModelMax))
	fullModel := bareModel
	if host != "" {
		fullModel = value.Render(clipInfo(host, infoBarHostMax)) + label.Render(" · ") + bareModel
	}

	// Approval mode: always shown, loud when nothing asks first.
	approval := label.Render("approval ") + value.Render("ask")
	if m.PermissionMode == appconfig.PermissionModeUnsupervised {
		approval = warn.Render("approval off")
	}

	// Todo progress: always shown; green once everything is done.
	todos := label.Render("todos ") + label.Render("–")
	if done, total := m.todoProgress(); total > 0 {
		style := value
		if done == total {
			style = good
		}
		todos = label.Render("todos ") + style.Render(fmt.Sprintf("%d/%d", done, total))
	}

	// Subagents: only while some are working or waiting on the user.
	var subagents string
	if running, awaiting := m.runningSubagents(); running > 0 || awaiting > 0 {
		var parts []string
		if running > 0 {
			parts = append(parts, value.Render(fmt.Sprintf("%d running", running)))
		}
		if awaiting > 0 {
			parts = append(parts, statusWarningStyle.Render(fmt.Sprintf("%d awaiting approval", awaiting)))
		}
		subagents = label.Render("subagents ") + strings.Join(parts, label.Render(", "))
	}

	// Turn time: ticks while any agent works, then stays as the last turn's.
	var turn string
	if d, running, ok := m.turn.elapsed(now); ok {
		style := value
		if running {
			style = accent
		}
		turn = label.Render("turn ") + style.Render(turnDuration(d))
	}

	// Left cluster in display order, with the order segments are dropped in
	// when the window is narrow (higher drops later; 0 is never dropped).
	type candidate struct {
		seg  infoBarSegment
		keep int
	}
	candidates := []candidate{{newInfoBarSegment(fullModel), 0}}
	candidates = append(candidates, candidate{newInfoBarSegment(approval), 4})
	candidates = append(candidates, candidate{newInfoBarSegment(todos), 2})
	if subagents != "" {
		candidates = append(candidates, candidate{newInfoBarSegment(subagents), 3})
	}
	right := infoBarSegment{}
	if turn != "" {
		right = newInfoBarSegment(turn)
	}

	avail := max(1, w-2) // one column of padding on each side
	sepW := lipgloss.Width(sep)
	layoutWidth := func() int {
		total := 0
		for i, c := range candidates {
			if i > 0 {
				total += sepW
			}
			total += c.seg.width
		}
		if right.width > 0 {
			total += infoBarGap + right.width
		}
		return total
	}
	// Drop the host first, then the turn time, then the lowest-priority left
	// segments.
	hostShown := host != ""
	for layoutWidth() > avail {
		if hostShown {
			hostShown = false
			candidates[0].seg = newInfoBarSegment(bareModel)
			continue
		}
		if right.width > 0 {
			right = infoBarSegment{}
			continue
		}
		drop := -1
		for i, c := range candidates {
			if c.keep > 0 && (drop < 0 || c.keep < candidates[drop].keep) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		candidates = append(candidates[:drop], candidates[drop+1:]...)
	}

	parts := make([]string, len(candidates))
	for i, c := range candidates {
		parts[i] = c.seg.text
	}
	left := strings.Join(parts, sep)
	gap := max(0, avail-lipgloss.Width(left)-right.width)
	row := " " + left + strings.Repeat(" ", gap) + right.text + " "

	row = ansi.Truncate(row, w, "…")
	if rw := ansi.StringWidth(row); rw < w {
		row += lipgloss.NewStyle().Background(appBgColor).Render(strings.Repeat(" ", w-rw))
	}
	return row
}
