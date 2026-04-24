package tui

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m Model) View() tea.View {
	if m.Width == 0 || m.Height == 0 {
		return tea.NewView("")
	}

	// Force each component to its strict allocated height to prevent layout shifts
	vStr := lipgloss.NewStyle().
		Height(m.Viewport.Height()).
		Width(m.Width).
		Render(m.Viewport.View())

	iStr := m.inputView()
	sStr := m.statusBarView()

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		vStr,
		iStr,
		sStr,
	)

	// Use lipgloss natively to pad and format the final visible viewport exactly to terminal bounds, eliminating "transparent" padding bugs.
	v := tea.NewView(appStyle.Width(m.Width).Height(m.Height).Render(content))
	v.AltScreen = true
	v.BackgroundColor = appBgColor
	return v
}

func (m *Model) inputView() string {
	w := m.Width - 4 // Internal padding for input
	if w < 1 {
		w = 1
	}

	// Render textarea directly — its styles already set background via FocusedStyle/BlurredStyle
	textareaView := m.Input.View()
	// Sync width precisely: inputStyle (border 2 + padding 2) + w (m.Width - 4) = m.Width
	// Internal width of inputStyle becomes m.Width - 8, matching m.Input.SetWidth()
	content := inputStyle.Width(w).Render(textareaView)

	// Wrap in a fixed-size container that fills the background
	return baseStyle.Copy().
		Width(m.Width).
		Height(InputHeight).
		Padding(0, 2).
		AlignVertical(lipgloss.Bottom).
		Render(content)
}

func (m *Model) statusBarView() string {
	w := max(m.Width, 1)

	s := m.GetAgentState(m.Focused.ID())

	modeStr := " CHAT "
	statusText := s.StatusText

	switch s.State {
	case StateThinking:
		modeStr = " THINKING "
	case StateStreaming:
		modeStr = " STREAMING "
	case StateConfirmTool:
		modeStr = " CONFIRM "
		statusText = "Authorize Tool Execution (y/n)"
	}

	// Check if any other agent is waiting for confirmation
	otherWaiting := false
	for id, state := range m.AgentStates {
		if id != m.Focused.ID() && state.State == StateConfirmTool {
			otherWaiting = true
			break
		}
	}

	var warning string
	if otherWaiting {
		warning = statusWarningStyle.Render(" SUBAGENT CONFIRMATION REQUIRED ")
		if strings.Contains(statusText, "Spawned") {
			statusText = ""
		}
	}

	mode := statusModeStyle.Render(modeStr)
	status := statusTextStyle.Render(statusText)

	// Build key hints
	stopKey := statusKeyStyle.Render("Ctrl+g") + " Stop "

	// Add hierarchy hints
	var hierarchyHint string
	if m.Focused.Parent() != nil {
		hierarchyHint = statusKeyStyle.Render("Esc") + " Back "
	}
	if len(m.Focused.Children()) > 0 {
		hierarchyHint += statusKeyStyle.Render("Tab") + " Subagents "
	}

	// Token count display (after status, before space)
	maxTokens := m.Focused.MaxTokens()
	tokenDisplay := fmt.Sprintf(" | %d", s.CumulativeTokenCount)
	if maxTokens > 0 {
		pct := (s.CumulativeTokenCount * 100) / maxTokens
		tokenDisplay = fmt.Sprintf(" | %d/%d (%d%%)", s.CumulativeTokenCount, maxTokens, pct)
	}
	tokenStyled := statusKeyStyle.Render(tokenDisplay)
	hints := lipgloss.JoinHorizontal(lipgloss.Left, hierarchyHint, stopKey)

	spaceWidth := w - lipgloss.Width(mode) - lipgloss.Width(status) - lipgloss.Width(warning) - lipgloss.Width(tokenStyled) - lipgloss.Width(hints)
	if spaceWidth < 0 {
		spaceWidth = 0
	}
	space := strings.Repeat(" ", spaceWidth)

	content := lipgloss.JoinHorizontal(lipgloss.Left, mode, status, warning, tokenStyled, space, hints)
	return statusBarBaseStyle.Width(w).Render(content)
}

func (m *Model) updateViewport() {
	if m.Focused == nil {
		return
	}

	history := m.Focused.History()
	msgWidth := m.Viewport.Width() - 2
	if msgWidth < 1 {
		msgWidth = 80
	}

	s := m.GetAgentState(m.Focused.ID())
	s.LastRenderTime = time.Now().UnixMilli()

	// If history was reset or messages were removed, clear the cache
	if len(history) < len(s.RenderedHistory) {
		s.RenderedHistory = nil
	}

	// Render only new messages and add to cache
	for i := len(s.RenderedHistory); i < len(history); i++ {
		msg := history[i]
		var rendered string
		switch msg.Role {
		case "user":
			rendered = userMsgStyle.Width(msgWidth + 1).Render(msg.Content)
		case "assistant":
			var assistantParts []string
			if msg.ReasoningContent != "" {
				assistantParts = append(assistantParts, tagStyle.Width(msgWidth+1).Render("Thinking Process:"))
				assistantParts = append(assistantParts, thinkingStyle.Width(msgWidth-2).Render(msg.ReasoningContent))
			}
			if msg.Content != "" {
				innerWidth := m.Viewport.Width() - AIMsgOverhead
				if innerWidth < 1 {
					innerWidth = 1
				}
				md := m.renderMarkdownBlock(msg.Content, innerWidth)
				assistantParts = append(assistantParts, aiMsgStyle.Width(msgWidth+1).Render(md))
			}
			for _, tc := range msg.ToolCalls {
				// Try to use CallString() for meaningful display
				callStr := tc.Function.Name
				if registry := m.Focused.Registry(); registry != nil {
					if tool := registry.Get(tc.Function.Name); tool != nil {
						if args := json.RawMessage(tc.Function.Arguments); len(args) > 0 {
							callStr = tool.CallString(args)
						}
					}
				}
				assistantParts = append(assistantParts, tagStyle.Width(msgWidth+1).Render(fmt.Sprintf("◆ %s", callStr)))
			}
			rendered = strings.Join(assistantParts, "\n")
		}
		// We always append to keep cache in sync with history length
		s.RenderedHistory = append(s.RenderedHistory, rendered)
	}

	// Build the full block list from cached history + active content
	var blocks []string
	for _, r := range s.RenderedHistory {
		if r != "" {
			blocks = append(blocks, r)
		}
	}

	// Render streaming content if active
	// Dedup check: Only render streaming if NOT in an interaction state (where history already has the tools)
	if (s.State == StateStreaming || s.State == StateThinking) && s.State != StateConfirmTool {
		var activeParts []string
		if s.StreamingState.ReasoningContent != "" {
			activeParts = append(activeParts, tagStyle.Width(msgWidth+1).Render("Thinking Process:"))
			activeParts = append(activeParts, thinkingStyle.Width(msgWidth-2).Render(s.StreamingState.ReasoningContent))
		}
		if s.StreamingState.Content != "" {
			innerWidth := m.Viewport.Width() - AIMsgOverhead
			if innerWidth < 1 {
				innerWidth = 1
			}

			// Incremental paragraph-chunked rendering:
			// Chunks are glamour-rendered once, styled, and APPENDED to a
			// cached string. The tail (current incomplete paragraph) skips
			// glamour entirely for speed — just plain text with background.
			chunks, tail := splitMarkdownChunks(s.StreamingState.Content)

			// Render + style NEW chunks and append to cache
			for i := s.StreamingChunkCount; i < len(chunks); i++ {
				rendered := m.renderMarkdownBlock(chunks[i], innerWidth)
				styled := aiMsgStyle.Width(msgWidth + 1).Render(rendered)
				if s.StreamingStyledCache != "" {
					s.StreamingStyledCache += "\n"
				}
				s.StreamingStyledCache += styled
			}
			s.StreamingChunkCount = len(chunks)

			// Render tail as plain text (no glamour — too expensive per frame)
			var tailStyled string
			if tail != "" {
				tailStyled = aiMsgStyle.Copy().Foreground(textColor).Width(msgWidth + 1).Render(tail)
			}

			// Combine: simple string concat, NO lipgloss processing
			var assembled string
			if s.StreamingStyledCache != "" && tailStyled != "" {
				assembled = s.StreamingStyledCache + "\n" + tailStyled
			} else if s.StreamingStyledCache != "" {
				assembled = s.StreamingStyledCache
			} else {
				assembled = tailStyled
			}
			if assembled != "" {
				activeParts = append(activeParts, assembled)
			}
		}
		for _, tc := range s.StreamingState.ToolCalls {
			// Try to use CallString() for meaningful display (no trailing ... since CallString adds it)
			callStr := tc.Function.Name
			if registry := m.Focused.Registry(); registry != nil {
				if tool := registry.Get(tc.Function.Name); tool != nil {
					if args := json.RawMessage(tc.Function.Arguments); len(args) > 0 {
						callStr = tool.CallString(args)
					}
				}
			}
			activeParts = append(activeParts, tagStyle.Width(msgWidth+1).Render(fmt.Sprintf("%s %s", m.Spinner.View(), callStr)))
		}
		if len(activeParts) > 0 {
			blocks = append(blocks, strings.Join(activeParts, "\n"))
		} else if s.State == StateThinking {
			blocks = append(blocks, thinkingStyle.Width(msgWidth-2).Render("Thinking..."))
		}
	}

	// Render Interactions
	if s.State == StateConfirmTool && s.PendingConfirm != nil {
		tc := s.PendingConfirm.ToolCall
		displayName := tc.Function.Name
		if runtime.GOOS == "windows" && displayName == "bash" {
			displayName = "PowerShell"
		}
		prompt := fmt.Sprintf("The agent wants to execute a **%s** command.\n\n```json\n%s\n```\n\n> Press **[ y ]** to Approve  |  **[ n ]** to Deny", displayName, tc.Function.Arguments)
		md, _ := m.Renderer.Render(prompt)
		blocks = append(blocks, aiMsgStyle.Width(msgWidth+1).Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color("#FFD700")).Render(md))
	}

	if m.Err != nil {
		blocks = append(blocks, thinkingStyle.Foreground(lipgloss.Color("#FF0000")).Render(fmt.Sprintf("Error: %v", m.Err)))
	}

	fullContent := strings.Join(blocks, "\n")
	atBottom := m.Viewport.AtBottom()
	m.Viewport.SetContent(fullContent)
	if atBottom {
		m.Viewport.GotoBottom()
	}
}

func (m *Model) renderMarkdownBlock(content string, innerWidth int) string {
	// Use new renderer to avoid background color issues
	md, _ := m.GetRenderer(innerWidth).Render(content)
	md = strings.TrimRight(md, "\n")

	return md
}

// splitMarkdownChunks splits markdown content at paragraph boundaries (\n\n)
// that are NOT inside fenced code blocks. Returns complete paragraphs (stable,
// cacheable during streaming) and the trailing incomplete content (must be
// re-rendered each frame).
func splitMarkdownChunks(content string) (complete []string, tail string) {
	inFence := false
	lastSplit := 0

	for i := 0; i < len(content); i++ {
		// Detect code fence toggles at line starts
		if (i == 0 || content[i-1] == '\n') && i+3 <= len(content) && content[i:i+3] == "```" {
			inFence = !inFence
		}
		// Split at \n\n outside code fences
		if !inFence && i+1 < len(content) && content[i] == '\n' && content[i+1] == '\n' {
			complete = append(complete, content[lastSplit:i+2])
			lastSplit = i + 2
		}
	}
	tail = content[lastSplit:]
	return
}
