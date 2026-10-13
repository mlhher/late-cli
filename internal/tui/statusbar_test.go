package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"late/internal/common"
	"late/internal/config"
)

// typedOrchestrator lets a test focus a subagent-style ID
// ("<type>-subagent-n") so the agent-type derivation for the focused-agent
// label is exercised without a resolvable parent chain.
type typedOrchestrator struct {
	mockOrchestrator
	id string
}

func (m *typedOrchestrator) ID() string { return m.id }

// newStatusBarModel builds a sized model the way the other view tests do; the
// status bar reads m.Width for its layout math, so a realistic width keeps the
// left section from being squeezed.
func newStatusBarModel(t *testing.T, root common.Orchestrator) Model {
	t.Helper()
	m := NewModel(root, nil, &config.Config{})
	m.SetSize(120, 30)
	return m
}

// The status bar must always name the focused agent (Diagnosis 0.4): the root
// agent has no breadcrumb chain, so without an explicit label the footer showed
// neither a name nor a type for the orchestrator.

func TestStatusBarShowsOrchestratorForRootFocus(t *testing.T) {
	m := newStatusBarModel(t, &mockOrchestrator{})

	plain := ansi.Strip(m.statusBarView())

	if n := strings.Count(plain, "orchestrator"); n != 1 {
		t.Fatalf("status bar should name the focused root agent exactly once, got %d occurrences in %q", n, plain)
	}
}

func TestStatusBarMainAgentIDShowsOrchestrator(t *testing.T) {
	m := newStatusBarModel(t, &focusTestOrchestrator{id: common.MainAgentID})

	plain := ansi.Strip(m.statusBarView())

	if n := strings.Count(plain, "orchestrator"); n != 1 {
		t.Fatalf("main agent id should render as the orchestrator label exactly once, got %d occurrences in %q", n, plain)
	}
}

func TestStatusBarSubagentFocusUsesBreadcrumbsWithoutDuplicateLabel(t *testing.T) {
	root := &focusTestOrchestrator{id: common.MainAgentID}
	child := &focusTestOrchestrator{id: "coder-subagent-0", parent: root}
	m := newStatusBarModel(t, root)
	m.Focused = child

	plain := ansi.Strip(m.statusBarView())

	// The breadcrumb chain covers the subagent: exactly one "coder" label.
	if n := strings.Count(plain, "coder"); n != 1 {
		t.Fatalf("focused subagent type should appear exactly once via breadcrumbs, got %d occurrences in %q", n, plain)
	}
	if !strings.Contains(plain, "orchestrator › coder #0") {
		t.Errorf("expected breadcrumb chain in %q", plain)
	}
	// The root label appears once as the breadcrumb root, without a duplicate standalone label.
	if n := strings.Count(plain, "orchestrator"); n != 1 {
		t.Errorf("orchestrator should appear exactly once in breadcrumbs, got %d occurrences in %q", n, plain)
	}
}

func TestStatusBarTypedSubagentWithoutParentStillNamed(t *testing.T) {
	m := newStatusBarModel(t, &mockOrchestrator{})
	// No Parent() and a Root with a different ID: the synthetic root→focused
	// breadcrumb path covers the subagent.
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "researcher-subagent-0"}

	plain := ansi.Strip(m.statusBarView())

	if n := strings.Count(plain, "researcher"); n != 1 {
		t.Fatalf("focused subagent type should appear exactly once, got %d occurrences in %q", n, plain)
	}
	if strings.Contains(plain, "orchestrator") {
		t.Errorf("root label should not appear when a subagent is focused: %q", plain)
	}
}

func TestStatusBarUnresolvableAgentFallsBackToRawID(t *testing.T) {
	m := newStatusBarModel(t, &mockOrchestrator{})
	// No root and an ID with no resolvable type (agentTypeForID == ""): the bar
	// falls back to the raw focused ID instead of showing nothing.
	m.Root = nil
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "subagent-9"}

	plain := ansi.Strip(m.statusBarView())

	if !strings.Contains(plain, "subagent-9") {
		t.Fatalf("expected raw focused ID fallback in %q", plain)
	}
}

func TestStatusBarBranchAfterAgentIdentity(t *testing.T) {
	root := &focusTestOrchestrator{id: common.MainAgentID}
	child := &focusTestOrchestrator{id: "coder-subagent-0", parent: root}

	mRoot := newStatusBarModel(t, root)
	mRoot.ShowCWD = true
	mRoot.GitBranch = "feat/ux-polish"

	mChild := newStatusBarModel(t, root)
	mChild.Focused = child
	mChild.ShowCWD = true
	mChild.GitBranch = "feat/ux-polish"

	rootPlain := ansi.Strip(mRoot.statusBarView())
	childPlain := ansi.Strip(mChild.statusBarView())

	if !strings.Contains(rootPlain, "⎇ feat/ux-polish") {
		t.Errorf("root status bar missing git branch: %q", rootPlain)
	}
	if !strings.Contains(childPlain, "⎇ feat/ux-polish") {
		t.Errorf("subagent status bar missing git branch: %q", childPlain)
	}

	// In both cases, branch appears after the agent identity on the left
	rootOrchIdx := strings.Index(rootPlain, "orchestrator")
	rootBranchIdx := strings.Index(rootPlain, "⎇ feat/ux-polish")
	if rootOrchIdx == -1 || rootBranchIdx == -1 || rootOrchIdx >= rootBranchIdx {
		t.Errorf("expected orchestrator before branch in root view: %q", rootPlain)
	}

	childCrumbIdx := strings.Index(childPlain, "orchestrator › coder #0")
	childBranchIdx := strings.Index(childPlain, "⎇ feat/ux-polish")
	if childCrumbIdx == -1 || childBranchIdx == -1 || childCrumbIdx >= childBranchIdx {
		t.Errorf("expected breadcrumbs before branch in child view: %q", childPlain)
	}
}
