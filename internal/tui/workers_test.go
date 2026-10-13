package tui

import (
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func setWorker(m *Model, id string, tokens int) {
	m.GetAgentState(id).CumulativeTokenCount = tokens
}

func TestWorkerTokensSumsEveryWorkerButNotTheOrchestrator(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.CumulativeTokenCount = 38_000 // the orchestrator itself: not a worker
	setWorker(m, "researcher-subagent-1", 120_000)
	setWorker(m, "coder-subagent-2", 92_000)
	setWorker(m, "coder-subagent-3", 0) // spawned, has not produced anything yet
	m.GetAgentState("coder-subagent-2").Closed = true

	tokens, workers := m.workerTokens()
	if tokens != 212_000 || workers != 2 {
		t.Fatalf("tokens=%d workers=%d, want 212000 and 2: finished workers count, idle ones do not", tokens, workers)
	}
}

func TestWorkerTokensChip(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	if chip := m.workerTokensChip(); chip != "" {
		t.Fatalf("no workers yet, chip = %q", ansi.Strip(chip))
	}
	setWorker(m, "coder-subagent-1", 212_000)
	if got := ansi.Strip(m.workerTokensChip()); got != "workers 212k" {
		t.Fatalf("chip = %q", got)
	}
	setWorker(m, "coder-subagent-2", 1_300_000)
	if got := ansi.Strip(m.workerTokensChip()); got != "workers 1.5m" {
		t.Fatalf("chip = %q", got)
	}
}

func TestStatusBarShowsWorkersBesideTheContextBar(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	m.SetSize(160, 40)
	s.CumulativeTokenCount = 38_000
	setWorker(m, "coder-subagent-1", 212_000)

	bar := ansi.Strip(m.statusBarView())
	if !strings.Contains(bar, "workers 212k") {
		t.Fatalf("status bar lacks the worker counter: %q", bar)
	}
	// The orchestrator's own context comes first, then the workers' total.
	if ctx, wk := strings.Index(bar, "38k"), strings.Index(bar, "workers"); ctx < 0 || ctx > wk {
		t.Fatalf("expected the context figure before the worker counter: %q", bar)
	}
}

func TestStatusBarDropsWorkersBeforeOverflowing(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 160} {
		without, s := newViewportBenchmarkModel(nil)
		without.SetSize(width, 40)
		s.CumulativeTokenCount = 38_000
		withWorkers, s2 := newViewportBenchmarkModel(nil)
		withWorkers.SetSize(width, 40)
		s2.CumulativeTokenCount = 38_000
		setWorker(withWorkers, "coder-subagent-1", 212_000)

		a, b := without.statusBarView(), withWorkers.statusBarView()
		if lipgloss.Width(b) > max(lipgloss.Width(a), width) {
			t.Errorf("width %d: the counter pushed the bar from %d to %d cells", width, lipgloss.Width(a), lipgloss.Width(b))
		}
		if width <= 60 && strings.Contains(ansi.Strip(b), "workers") {
			t.Errorf("width %d: the counter should give way on a narrow terminal: %q", width, ansi.Strip(b))
		}
	}
}

func TestWithWorkerTokensKeepsTheContextBarWhenThereIsNoRoom(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	setWorker(m, "coder-subagent-1", 5_000)
	if got := m.withWorkerTokens("RIGHT", 50, 60); got != "RIGHT" {
		t.Fatalf("got %q: with no room the right section must stay exactly as it was", ansi.Strip(got))
	}
	if got := ansi.Strip(m.withWorkerTokens("RIGHT", 10, 80)); got != "RIGHT  workers 5k" {
		t.Fatalf("got %q", got)
	}
	if got := ansi.Strip(m.withWorkerTokens("", 10, 80)); got != "workers 5k" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkerCounterGrowsWithUsageEventsAndFollowsNewConversation(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.SetSize(160, 40)

	for _, total := range []int{4_000, 9_000} {
		updated, _ := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
			ID: "coder-subagent-1", Usage: client.Usage{TotalTokens: total},
		}})
		*m = updated.(Model)
	}
	if got := ansi.Strip(m.workerTokensChip()); got != "workers 9k" {
		t.Fatalf("chip after usage events = %q", got)
	}

	m.Input.SetValue("/new")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	*m = updated.(Model)
	if chip := m.workerTokensChip(); chip != "" {
		t.Fatalf("a new conversation should start the count over, chip = %q", ansi.Strip(chip))
	}
}
