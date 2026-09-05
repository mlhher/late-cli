package tui

import (
	"encoding/json"
	"late/internal/git"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLateThemeJSONValid(t *testing.T) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(LateTheme, &parsed); err != nil {
		t.Fatalf("LateTheme JSON is invalid: %v", err)
	}

	for _, key := range []string{"document", "code_block", "heading", "task", "bullet"} {
		if _, exists := parsed[key]; !exists {
			t.Errorf("expected LateTheme to contain key %q", key)
		}
	}

	doc, ok := parsed["document"].(map[string]interface{})
	if !ok || doc["background_color"] != "#0B0C0E" {
		t.Errorf("expected document background_color to be #0B0C0E, got %v", doc["background_color"])
	}
}

func TestCodeBlockRendering(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	rendered := model.renderMarkdownBlock("```go\nfmt.Println(\"hello\")\n```", 80)

	// Ensure chroma doesn't emit background color sequences for tokens
	if strings.Contains(rendered, "\x1b[48;5;233m") || strings.Contains(rendered, "48;2;18;20;25") {
		t.Errorf("rendered code block contains chroma background escape sequence:\n%q", rendered)
	}
}

func TestRenderWelcomeMessage(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.ModelName = "test-model-4"
	model.GitBranch = "main"

	// Wide viewport
	model.Viewport.SetWidth(90)
	welcomeWide := model.renderWelcomeMessage()
	if !strings.Contains(welcomeWide, "test-model-4") {
		t.Errorf("expected welcome message to contain model name, got:\n%s", welcomeWide)
	}
	if !strings.Contains(welcomeWide, "main") {
		t.Errorf("expected welcome message to contain git branch, got:\n%s", welcomeWide)
	}
	if !strings.Contains(welcomeWide, "Essential Commands") {
		t.Errorf("expected welcome message to contain quick start card, got:\n%s", welcomeWide)
	}

	// Narrow viewport
	model.Viewport.SetWidth(50)
	welcomeNarrow := model.renderWelcomeMessage()
	if !strings.Contains(welcomeNarrow, "test-model-4") {
		t.Errorf("expected narrow welcome message to contain model name, got:\n%s", welcomeNarrow)
	}

	// Verify card borders are vertically aligned (no shift on top or bottom border)
	var topCol, leftCol, bottomCol int = -1, -1, -1
	var topRightCol, rightCol, bottomRightCol int = -1, -1, -1
	for _, rawLine := range strings.Split(welcomeWide, "\n") {
		if idx := strings.Index(rawLine, "╭"); idx != -1 && topCol == -1 {
			topCol = lipgloss.Width(rawLine[:idx])
		}
		if idx := strings.Index(rawLine, "│"); idx != -1 && leftCol == -1 {
			leftCol = lipgloss.Width(rawLine[:idx])
		}
		if idx := strings.Index(rawLine, "╰"); idx != -1 && bottomCol == -1 {
			bottomCol = lipgloss.Width(rawLine[:idx])
		}
		if idx := strings.Index(rawLine, "╮"); idx != -1 && topRightCol == -1 {
			topRightCol = lipgloss.Width(rawLine[:idx])
		}
		if idx := strings.LastIndex(rawLine, "│"); idx != -1 && rightCol == -1 {
			rightCol = lipgloss.Width(rawLine[:idx])
		}
		if idx := strings.Index(rawLine, "╯"); idx != -1 && bottomRightCol == -1 {
			bottomRightCol = lipgloss.Width(rawLine[:idx])
		}
	}
	if topCol == -1 || leftCol == -1 || bottomCol == -1 {
		t.Errorf("expected to find rounded card borders, got top=%d, left=%d, bottom=%d", topCol, leftCol, bottomCol)
	} else if topCol != leftCol || leftCol != bottomCol {
		t.Errorf("card left borders are not aligned: top=%d, left=%d, bottom=%d", topCol, leftCol, bottomCol)
	}
	if topRightCol == -1 || rightCol == -1 || bottomRightCol == -1 {
		t.Errorf("expected to find right card borders, got topRight=%d, right=%d, bottomRight=%d", topRightCol, rightCol, bottomRightCol)
	} else if topRightCol != rightCol || rightCol != bottomRightCol {
		t.Errorf("card right borders are not aligned: topRight=%d, right=%d, bottomRight=%d", topRightCol, rightCol, bottomRightCol)
	}
}

func TestRenderToolBadge(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)

	tests := []struct {
		toolName string
		callStr  string
		wantText string
	}{
		{"bash", "bash: go test ./...", "$"},
		{"write_file", "write_file: main.go", "edit"},
		{"read_file", "read_file: config.json", "read"},
		{"grep_search", "grep: TODO", "find"},
		{"spawn_subagent", "spawn_subagent: coder", "agent"},
		{"git_log", "git: commit", "git"},
		{"custom_tool", "custom: something", "call"},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			renderedHistory := model.renderToolBadge(tt.toolName, tt.callStr, false, 80)
			if !strings.Contains(renderedHistory, tt.wantText) {
				t.Errorf("renderToolBadge (history) missing text %q in %q", tt.wantText, renderedHistory)
			}

			renderedStreaming := model.renderToolBadge(tt.toolName, tt.callStr, true, 80)
			if !strings.Contains(renderedStreaming, tt.wantText) {
				t.Errorf("renderToolBadge (streaming) missing text %q in %q", tt.wantText, renderedStreaming)
			}
			if !strings.Contains(renderedStreaming, "running") {
				t.Errorf("renderToolBadge (streaming) missing running indicator in %q", renderedStreaming)
			}
		})
	}
}

func TestRenderContextBar(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)

	// Known max, low usage
	barLow := model.renderContextBar(2000, 20000)
	if !strings.Contains(barLow, "10%") || !strings.Contains(barLow, "2k/20k") {
		t.Errorf("unexpected bar output: %q", barLow)
	}

	// Known max, high usage
	barHigh := model.renderContextBar(18000, 20000)
	if !strings.Contains(barHigh, "90%") || !strings.Contains(barHigh, "18k/20k") {
		t.Errorf("unexpected bar output: %q", barHigh)
	}

	// Unlimited max (0)
	barUnlimited := model.renderContextBar(5000, 0)
	if !strings.Contains(barUnlimited, "∞") {
		t.Errorf("expected infinity symbol for max=0, got %q", barUnlimited)
	}

	// Unknown max (-1)
	barUnknown := model.renderContextBar(5000, -1)
	if !strings.Contains(barUnknown, "?") {
		t.Errorf("expected '?' for max=-1, got %q", barUnknown)
	}
}

func TestRenderAnimations(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)

	eq := model.renderMinimalEqualizer()
	if !strings.Contains(eq, "[") || !strings.Contains(eq, "]") {
		t.Errorf("unexpected equalizer format: %q", eq)
	}

	track := model.renderScannerTrack("✦", primaryColor)
	if !strings.Contains(track, "✦") {
		t.Errorf("expected scanner track to contain symbol, got %q", track)
	}
}

func TestStatusBarViewTelemetry(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Width = 120
	model.ShowCWD = true
	model.CWD = "/home/user/myproject"
	model.GitBranch = "feat/theme-upgrade"

	view := model.statusBarView()
	if !strings.Contains(view, "feat/theme-upgrade") {
		t.Errorf("expected status bar to contain git branch, got:\n%s", view)
	}
	// CWD name should NOT be displayed when git branch is present
	if strings.Contains(view, "myproject") {
		t.Errorf("expected status bar not to contain CWD folder when git branch is present, got:\n%s", view)
	}
	// Idle equalizer brackets should be present, but no "ready" text label
	if !strings.Contains(view, "[") || !strings.Contains(view, "]") {
		t.Errorf("expected status bar to contain idle equalizer brackets, got:\n%s", view)
	}
	if strings.Contains(view, "ready") {
		t.Errorf("expected status bar not to contain 'ready' text label, got:\n%s", view)
	}

	eqIdx := strings.Index(view, "[")
	branchIdx := strings.Index(view, "feat/theme-upgrade")
	if eqIdx == -1 || branchIdx == -1 || eqIdx > branchIdx {
		t.Errorf("expected equalizer indicator to appear before git branch, got eqIdx=%d, branchIdx=%d in:\n%s", eqIdx, branchIdx, view)
	}

	// Non-git scenario: fallback to CWD toplevel
	model.GitBranch = ""
	viewNonGit := model.statusBarView()
	if !strings.Contains(viewNonGit, "myproject") {
		t.Errorf("expected status bar to fallback to CWD toplevel when not under git, got:\n%s", viewNonGit)
	}
}

func TestCommitLogAndRewindView(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Width = 100
	model.Viewport.SetWidth(100)
	model.Viewport.SetHeight(20)

	// Commit log
	model.Mode = ViewCommitLog
	model.CommitEntries = []git.CommitEntry{
		{Hash: "abc1234", Author: "Dev", Date: "2 hours ago", Message: "Initial commit", IsHEAD: true},
	}
	model.renderCommitLogView()
	content := model.Viewport.GetContent()
	if !strings.Contains(content, "abc1234") || !strings.Contains(content, "HEAD") {
		t.Errorf("expected commit log view to show commit, got:\n%s", content)
	}

	// Rewind view
	model.Mode = ViewRewind
	model.RewindEntries = []RewindEntry{
		{Index: 0, Content: "First prompt"},
	}
	model.renderRewindView()
	rewindContent := model.Viewport.GetContent()
	if !strings.Contains(rewindContent, "First prompt") {
		t.Errorf("expected rewind view to show prompt, got:\n%s", rewindContent)
	}
}

func TestChatPromptAndThinkingRendering(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Width = 100

	state := model.GetAgentState(model.Focused.ID())
	state.StreamingState.ReasoningContent = "Analyzing code structure..."
	streaming := model.renderFullStreamingResponse(state, 80)
	if !strings.Contains(streaming, "thinking") || !strings.Contains(streaming, "Analyzing code structure...") {
		t.Errorf("expected streaming response to contain thinking gutter, got:\n%s", streaming)
	}
}

func TestStatusBarStatePills(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Width = 120

	s := model.GetAgentState(model.Focused.ID())

	// Ready / Idle (inactive equalizer brackets, no text label)
	s.State = StateIdle
	viewReady := model.statusBarView()
	if !strings.Contains(viewReady, "[") || !strings.Contains(viewReady, "]") {
		t.Errorf("expected status bar idle equalizer, got:\n%s", viewReady)
	}
	if strings.Contains(viewReady, "ready") {
		t.Errorf("expected status bar not to contain 'ready' label, got:\n%s", viewReady)
	}

	// Working (pure animation indicator, no redundant 'working' text label)
	s.State = StateThinking
	viewWorking := model.statusBarView()
	if !strings.Contains(viewWorking, "✦") {
		t.Errorf("expected status bar working scanner track, got:\n%s", viewWorking)
	}

	// Streaming (pure equalizer animation, no redundant 'streaming' text label)
	s.State = StateStreaming
	viewStreaming := model.statusBarView()
	if !strings.Contains(viewStreaming, "[") || !strings.Contains(viewStreaming, "]") {
		t.Errorf("expected status bar streaming equalizer, got:\n%s", viewStreaming)
	}

	// Confirm (shows authorize execution in center status, no redundant 'confirm required' label on left)
	s.State = StateConfirmTool
	viewConfirm := model.statusBarView()
	if !strings.Contains(viewConfirm, "authorize execution") {
		t.Errorf("expected status bar authorize message, got:\n%s", viewConfirm)
	}
	if strings.Contains(viewConfirm, "confirm required") {
		t.Errorf("expected status bar not to contain 'confirm required' label, got:\n%s", viewConfirm)
	}
}

func TestCentralizedBoxBorders(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Viewport.SetWidth(90)
	model.Viewport.SetHeight(100)

	// Help view uses rounded borders
	model.Mode = ViewHelp
	model.updateViewport()
	content := model.Viewport.View()
	if !strings.Contains(content, "╭") || !strings.Contains(content, "╰") {
		t.Errorf("expected /help to use rounded border corners (╭/╰), got:\n%s", content)
	}
	if strings.Contains(content, "╔") || strings.Contains(content, "╚") {
		t.Errorf("expected /help not to use double border corners (╔/╚), got:\n%s", content)
	}

	// Verify symmetric margins: 1 column on left, 1 column on right
	for _, rawLine := range strings.Split(content, "\n") {
		if strings.Contains(rawLine, "╭") {
			idx := strings.Index(rawLine, "╭")
			leftMargin := lipgloss.Width(rawLine[:idx])
			if leftMargin != 1 {
				t.Errorf("expected left margin of 1, got %d", leftMargin)
			}
			topRightIdx := strings.Index(rawLine, "╮")
			boxWidth := lipgloss.Width(rawLine[idx : topRightIdx+len("╮")])
			if boxWidth != 88 { // 90 viewport - 2 margin
				t.Errorf("expected box width 88, got %d", boxWidth)
			}
			break
		}
	}

	// Commit detail view uses rounded borders
	model.Mode = ViewChat
	model.CommitDetail = "commit abc\nAuthor: test\n\nfeat: something"
	model.renderCommitLogView()
	commitContent := model.Viewport.View()
	if !strings.Contains(commitContent, "╭") || !strings.Contains(commitContent, "╰") {
		t.Errorf("expected commit detail to use rounded border corners (╭/╰), got:\n%s", commitContent)
	}
	if strings.Contains(commitContent, "╔") || strings.Contains(commitContent, "╚") {
		t.Errorf("expected commit detail not to use double border corners (╔/╚), got:\n%s", commitContent)
	}

	for _, rawLine := range strings.Split(commitContent, "\n") {
		if strings.Contains(rawLine, "╭") {
			idx := strings.Index(rawLine, "╭")
			leftMargin := lipgloss.Width(rawLine[:idx])
			if leftMargin != 1 {
				t.Errorf("expected commit detail left margin of 1, got %d", leftMargin)
			}
			topRightIdx := strings.Index(rawLine, "╮")
			boxWidth := lipgloss.Width(rawLine[idx : topRightIdx+len("╮")])
			if boxWidth != 88 {
				t.Errorf("expected commit detail box width 88, got %d", boxWidth)
			}
			break
		}
	}
}

