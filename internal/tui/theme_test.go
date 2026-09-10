package tui

import (
	"encoding/json"
	"late/internal/client"
	"late/internal/git"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	if !ok || doc["color"] != "#E6EDF3" {
		t.Errorf("expected document color to be #E6EDF3 (textColor), got %v", doc["color"])
	}

	heading, ok := parsed["heading"].(map[string]interface{})
	if !ok || heading["color"] != "#E5A85C" {
		t.Errorf("expected heading color to be #E5A85C (primaryColor), got %v", heading["color"])
	}

	quote, ok := parsed["block_quote"].(map[string]interface{})
	if !ok || quote["color"] != "#7D8590" {
		t.Errorf("expected block_quote color to be #7D8590 (subtextColor), got %v", quote["color"])
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

func TestChatContentAndTableFullWidth(t *testing.T) {
	viewportWidth := 100
	orch := &benchmarkOrchestrator{
		history: []client.ChatMessage{
			{
				Role:    "assistant",
				Content: client.TextContent("| Header A | Header B |\n| --- | --- |\n| Cell 1 | Cell 2 |"),
			},
		},
	}
	model := NewModel(orch, nil, nil)
	model.Width = viewportWidth
	model.Height = 30
	model.updateLayout()

	content := model.Viewport.GetContent()
	lines := strings.Split(content, "\n")

	// Find the horizontal border of the table
	foundTableBorder := false
	for _, line := range lines {
		if strings.Contains(line, "─") {
			w := lipgloss.Width(line)
			if w == viewportWidth {
				foundTableBorder = true
				break
			}
		}
	}
	if !foundTableBorder {
		t.Errorf("expected table border line to span full viewport width %d, content was:\n%s", viewportWidth, content)
	}

	// Test user prompt width
	userMsg := "A short user prompt"
	orch.history = []client.ChatMessage{
		{
			Role:    "user",
			Content: client.TextContent(userMsg),
		},
	}
	// Invalidate and update
	model.AgentStates[model.Focused.ID()].CachedWidth = -1
	model.updateViewport()

	userContent := model.Viewport.GetContent()
	if !strings.Contains(ansi.Strip(userContent), "❯ "+userMsg) {
		t.Errorf("expected prompt prefix and user message, got:\n%s", userContent)
	}

	// Test resizing to 120 columns
	orch.history = []client.ChatMessage{
		{
			Role:    "assistant",
			Content: client.TextContent("| Header A | Header B |\n| --- | --- |\n| Cell 1 | Cell 2 |"),
		},
	}
	newWidth := 120
	updatedModel, _ := model.Update(tea.WindowSizeMsg{Width: newWidth, Height: 30})
	model = updatedModel.(Model)

	resizedContent := model.Viewport.GetContent()
	foundResizedTable := false
	for _, line := range strings.Split(resizedContent, "\n") {
		if strings.Contains(line, "─") {
			w := lipgloss.Width(line)
			if w == newWidth {
				foundResizedTable = true
				break
			}
		}
	}
	if !foundResizedTable {
		t.Errorf("expected table border line to span full resized width %d, content was:\n%s", newWidth, resizedContent)
	}
}

func TestCentralizedChipStyles(t *testing.T) {
	// Verify centralized chip style background colors
	if bg := commitHashChipStyle.GetBackground(); bg != chipBgColor {
		t.Errorf("expected commitHashChipStyle background to be %v, got %v", chipBgColor, bg)
	}
	if bg := commitSelectedChipStyle.GetBackground(); bg != chipBgColor {
		t.Errorf("expected commitSelectedChipStyle background to be %v, got %v", chipBgColor, bg)
	}
	if bg := modelPickerChipStyle.GetBackground(); bg != chipBgColor {
		t.Errorf("expected modelPickerChipStyle background to be %v, got %v", chipBgColor, bg)
	}
	if bg := headBadgeStyle.GetBackground(); bg != accentEmerald {
		t.Errorf("expected headBadgeStyle background to be %v, got %v", accentEmerald, bg)
	}

	// Verify welcome screen rendering contains chip background color
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Viewport.SetWidth(90)
	welcome := model.renderWelcomeMessage()
	// #1B1E28 in RGB is 27;30;40
	if !strings.Contains(welcome, "48;2;27;30;40") {
		t.Errorf("expected welcome message to contain elevated chip background #1B1E28 (48;2;27;30;40), got:\n%s", welcome)
	}

	// Verify /log rendering contains chip background color for commit hash
	model.CommitEntries = []git.CommitEntry{
		{Hash: "abc1234", Author: "dev", Date: "today", Message: "initial commit", IsHEAD: true},
	}
	model.renderCommitLogView()
	logContent := model.Viewport.GetContent()
	if !strings.Contains(logContent, "48;2;27;30;40") {
		t.Errorf("expected /log view to contain elevated chip background #1B1E28 (48;2;27;30;40), got:\n%s", logContent)
	}
}

func TestRewindCleanTimeline(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Viewport.SetWidth(100)
	model.RewindEntries = []RewindEntry{
		{Index: 0, Content: "First prompt"},
		{Index: 1, Content: "Second prompt"},
	}
	model.RewindIndex = 1
	model.renderRewindView()

	content := model.Viewport.GetContent()

	plainContent := ansi.Strip(content)

	// Should contain timeline indicators
	if !strings.Contains(plainContent, "○ First prompt") {
		t.Errorf("expected unselected entry with ○ indicator, got:\n%s", plainContent)
	}
	if !strings.Contains(plainContent, "▸ ● Second prompt") {
		t.Errorf("expected selected entry with ▸ ● indicator, got:\n%s", plainContent)
	}

	// Should NOT contain the redundant trailing chip
	if strings.Contains(plainContent, "Rewind target") {
		t.Errorf("expected no redundant 'Rewind target' chip in rewind view, got:\n%s", plainContent)
	}
}

func TestUnifiedThemeAndStyles(t *testing.T) {
	// 1. Centralized View Styles
	if fg := viewHeaderStyle.GetForeground(); fg != primaryColor {
		t.Errorf("expected viewHeaderStyle foreground to be %v, got %v", primaryColor, fg)
	}
	if fg := viewFooterStyle.GetForeground(); fg != mutedTextColor {
		t.Errorf("expected viewFooterStyle foreground to be %v, got %v", mutedTextColor, fg)
	}
	if fg := viewEmptyStyle.GetForeground(); fg != subtextColor {
		t.Errorf("expected viewEmptyStyle foreground to be %v, got %v", subtextColor, fg)
	}

	// 2. File Picker Styles
	if fg := filePickerSelectedStyle.GetForeground(); fg != secondaryColor {
		t.Errorf("expected filePickerSelectedStyle foreground to be %v, got %v", secondaryColor, fg)
	}
	if fg := filePickerFileStyle.GetForeground(); fg != textColor {
		t.Errorf("expected filePickerFileStyle foreground to be %v, got %v", textColor, fg)
	}
	if fg := filePickerDirectoryStyle.GetForeground(); fg != primaryColor {
		t.Errorf("expected filePickerDirectoryStyle foreground to be %v, got %v", primaryColor, fg)
	}

	// 3. Status Bar Breadcrumbs Styling
	root := &focusTestOrchestrator{id: "parent-agent"}
	child := &focusTestOrchestrator{id: "sub-agent", parent: root}
	model := NewModel(child, nil, nil)
	model.Width = 120
	sbView := model.statusBarView()
	// activeBorder in RGB is 45;50;62
	if !strings.Contains(sbView, "45;50;62") {
		t.Errorf("expected status bar to render breadcrumb separator with activeBorder (45;50;62), got:\n%s", sbView)
	}
}

func TestUserMessageRendering_EmptyAndTrailingNewlines(t *testing.T) {
	mock := &mockOrchestrator{
		history: []client.ChatMessage{
			{Role: "user", Content: client.TextContent("Goal: Test poem\n\n")},
			{Role: "user", Content: client.TextContent("")},
			{Role: "assistant", Content: client.TextContent("Here is the poem.")},
		},
	}
	model := NewModel(mock, nil, nil)
	model.Width = 100
	model.Viewport.SetWidth(100)
	model.Viewport.SetHeight(20)

	model.updateViewport()
	content := model.Viewport.GetContent()

	// 1. Should render the goal
	if !strings.Contains(content, "Goal: Test poem") {
		t.Errorf("expected content to contain 'Goal: Test poem', got:\n%s", content)
	}

	// 2. Count prompt symbols '❯'
	promptCount := strings.Count(content, "❯")
	if promptCount != 1 {
		t.Errorf("expected exactly 1 prompt indicator '❯', got %d in:\n%s", promptCount, content)
	}

	// 3. Should not have gigantic vertical gaps (no 3+ consecutive empty lines)
	if strings.Contains(content, "\n\n\n\n") {
		t.Errorf("expected no excessive blank lines in viewport, got:\n%s", content)
	}
}

func validateNoVTELeaks(t *testing.T, name, content string) {
	lines := strings.Split(content, "\n")
	for lineIdx, line := range lines {
		bgSet := false
		i := 0
		bytes := []byte(line)
		for i < len(bytes) {
			if bytes[i] == 0x1b && i+1 < len(bytes) && bytes[i+1] == '[' {
				end := i + 2
				for end < len(bytes) && bytes[end] != 'm' {
					end++
				}
				if end < len(bytes) {
					seq := string(bytes[i : end+1])
					if seq == "\x1b[m" || seq == "\x1b[0m" || seq == "\x1b[49m" {
						bgSet = false
					} else if strings.Contains(seq, "48;2;") || strings.Contains(seq, "48;5;") {
						bgSet = true
					}
					i = end + 1
					continue
				}
			}
			if !bgSet && bytes[i] == ' ' {
				t.Errorf("[%s] line %d col %d: unstyled space with default background in VTE: %q", name, lineIdx, i, line)
				return
			}
			i++
		}
	}
}

func TestHolisticVTELeaks(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, nil)
	model.Width = 100
	model.Height = 30
	model.Viewport.SetWidth(100)
	model.Viewport.SetHeight(25)

	// 1. Welcome Screen
	model.updateViewport()
	vWelcome := model.View()
	validateNoVTELeaks(t, "Welcome Screen", vWelcome.Content)

	// 2. Commit Log View (/log)
	model.Mode = ViewCommitLog
	model.CommitEntries = []git.CommitEntry{
		{Hash: "18aad01", Author: "ml", Date: "vor 14 Minuten", Message: "fix: vte rendering", IsHEAD: true},
		{Hash: "e19acc7", Author: "ml", Date: "vor 2 Tagen", Message: "fix: harmonize theme"},
	}
	model.renderCommitLogView()
	vLog := model.View()
	validateNoVTELeaks(t, "Commit Log View", vLog.Content)

	// 3. Model Picker View (/model)
	model.Mode = ViewModelPicker
	model.ModelPickerAgents = []string{"orchestrator", "coder"}
	model.ModelPickerModels = []string{"default", "deepseek-v4-flash"}
	model.ModelPickerAgentSelections = map[string]int{"orchestrator": 0, "coder": 1}
	model.renderModelPickerView()
	vModel := model.View()
	validateNoVTELeaks(t, "Model Picker View", vModel.Content)

	// 4. Rewind View (/rewind)
	model.Mode = ViewRewind
	model.RewindEntries = []RewindEntry{
		{Index: 0, Content: "First prompt"},
		{Index: 1, Content: "Second prompt"},
	}
	model.renderRewindView()
	vRewind := model.View()
	validateNoVTELeaks(t, "Rewind View", vRewind.Content)

	// 5. Chat History
	model.Mode = ViewChat
	agent := &mockOrchestrator{
		history: []client.ChatMessage{
			{Role: "user", Content: client.TextContent("Hello Late!")},
			{Role: "assistant", Content: client.TextContent("Hello! How can I help you today?")},
		},
	}
	model.Focused = agent
	model.updateViewport()
	vChat := model.View()
	validateNoVTELeaks(t, "Chat View", vChat.Content)
}

// TestResolveRenderTheme_EmptyReturnsBase: with no overrides, the merged
// output is byte-for-byte equal to the bundled LateTheme (no wasted
// copy, no schema-pollution markers).
func TestResolveRenderTheme_EmptyReturnsBase(t *testing.T) {
	got, err := ResolveRenderTheme("", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != string(LateTheme) {
		t.Fatalf("expected byte-equal base theme when overrides are nil")
	}
}

// TestResolveRenderTheme_MergesGlamourKeys: a top-level glamour override
// is woven into the merged result AND the theme-name marker is present
// downstream consumers (the TUI/helpers) can read.
func TestResolveRenderTheme_MergesGlamourKeys(t *testing.T) {
	mod := map[string]any{
		"document": map[string]any{
			"color": "#FF0000",
		},
	}
	got, err := ResolveRenderTheme("p:red", mod)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "#FF0000") {
		t.Fatal("expected merged colour in output")
	}
	if !strings.Contains(s, `"_late_theme_name":"p:red"`) &&
		!strings.Contains(s, `_late_theme_name`) {
		t.Fatal("expected theme name marker in output")
	}

	// Sanity: roundtrip through json.Unmarshal to confirm valid JSON, so
	// we don't accidentally feed glamour invalid bytes.
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("merged theme is not valid json: %v", err)
	}
}
