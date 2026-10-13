package tui

import (
	"charm.land/lipgloss/v2"
)

// workerTokens totals the context sizes of every subagent seen this session,
// running or finished, and counts them. A worker's size is how much material it
// read, ran and wrote to do its task: the volume the orchestrator did not have
// to carry. States of closed subagents are kept for this reason; /new resets them.
func (m *Model) workerTokens() (tokens, workers int) {
	for id, s := range m.AgentStates {
		if m.Root != nil && id == m.Root.ID() {
			continue
		}
		if s.CumulativeTokenCount > 0 {
			tokens += s.CumulativeTokenCount
			workers++
		}
	}
	return tokens, workers
}

// workerTokensChip renders "workers 212k", or "" when no worker has done anything.
func (m *Model) workerTokensChip() string {
	tokens, workers := m.workerTokens()
	if workers == 0 {
		return ""
	}
	label := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor).Render("workers ")
	value := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor).Render(m.formatTokenCount(tokens))
	return label + value
}

// withWorkerTokens appends the worker counter to the status bar's right-hand
// section, but only when the whole bar still fits: on a narrow terminal the
// counter is the first thing to go, never the context bar or the status text.
func (m *Model) withWorkerTokens(right string, leftWidth, usableWidth int) string {
	chip := m.workerTokensChip()
	if chip == "" {
		return right
	}
	joined := chip
	if right != "" {
		joined = right + "  " + chip
	}
	const minGap = 2
	if leftWidth+lipgloss.Width(joined)+minGap > usableWidth {
		return right
	}
	return joined
}
