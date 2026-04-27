package tui

import (
	"encoding/json"
	"fmt"
	"math"
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
		Background(appBgColor).
		Render(m.Viewport.View())

	iStr := m.inputView()
	sStr := m.statusBarView()

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		vStr,
		iStr,
		sStr,
	)

	v := tea.NewView(content)
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
		statusText = "Authorize Tool Execution (y/s/p/g/n)"
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

	// Only breathe when agent is active
	active := s.State == StateStreaming || s.State == StateThinking

	var mode string
	if active {
		ms := float64(time.Now().UnixNano()) / 1e6
		pulseFactor := (math.Sin(ms/700.0) + 1.0) / 2.0 // 0.0 to 1.0
		grad := lipgloss.Blend1D(100, lipgloss.Color("#4A235A"), primaryColor)
		breathColor := grad[int(pulseFactor*99)]
		mode = statusModeStyle.Copy().Background(breathColor).Render(modeStr)
	} else {
		mode = statusModeStyle.Render(modeStr)
	}

	status := statusTextStyle.Render(statusText)

	// Build key hints
	stopKey := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Ctrl+g"), statusTextStyle.Render(" Stop "))

	// Add hierarchy hints
	var hierarchyHint string
	if m.Focused.Parent() != nil {
		hierarchyHint = lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Esc"), statusTextStyle.Render(" Back "))
	}
	if len(m.Focused.Children()) > 0 {
		hNext := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Tab"), statusTextStyle.Render(" Subagents "))
		hierarchyHint = lipgloss.JoinHorizontal(lipgloss.Left, hierarchyHint, hNext)
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
	// Important: Use a style WITHOUT a border for the internal space filler to avoid duplication
	spaceStyle := lipgloss.NewStyle().Background(appBgColor).MarginBackground(appBgColor)
	space := spaceStyle.Width(spaceWidth).Render("")

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
				assistantParts = append(assistantParts, thoughtHeaderStyle.Width(msgWidth+1).Render("Thoughts:"))
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
			activeParts = append(activeParts, thoughtHeaderStyle.Width(msgWidth+1).Render("Thoughts:"))
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
			var chunks []string
			var tail string
			if s.StreamingState.Content == s.LastStreamingContent {
				// Optimization: use cached chunks if content hasn't changed
				chunks = s.LastChunks
				tail = s.LastTail
			} else {
				chunks, tail = splitMarkdownChunks(s.StreamingState.Content)
				s.LastStreamingContent = s.StreamingState.Content
				s.LastChunks = chunks
				s.LastTail = tail
			}

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
				// Trim leading newlines from tail to prevent "jumping" when a new paragraph starts
				t := strings.TrimLeft(tail, "\n")
				if t != "" {
					// Pulsing Caret for streaming effect
					ms := float64(time.Now().UnixNano()) / 1e6
					caretOpacity := (math.Sin(ms/150.0) + 1.0) / 2.0
					caretGrad := lipgloss.Blend1D(100, appBgColor, primaryColor)
					caretCol := caretGrad[int(caretOpacity*99)]
					caret := lipgloss.NewStyle().Foreground(caretCol).Render("█")

					tailStyled = aiMsgStyle.Copy().Foreground(textColor).Width(msgWidth + 1).Render(t + caret)
				}
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
			activeParts = append(activeParts, m.renderAnimatedTag(fmt.Sprintf("%s %s", m.Spinner.View(), callStr), tagStyle, msgWidth+1, true))
		}
		if len(activeParts) > 0 {
			blocks = append(blocks, strings.Join(activeParts, "\n"))
		} else if s.State == StateThinking {
			blocks = append(blocks, m.renderAnimatedTag("Thinking", thinkingStyle, msgWidth-2, true))
		}
	}

	// Render Interactions
	if s.State == StateConfirmTool && s.PendingConfirm != nil {
		tc := s.PendingConfirm.ToolCall
		displayName := tc.Function.Name
		if runtime.GOOS == "windows" && displayName == "bash" {
			displayName = "PowerShell"
		}
		prompt := fmt.Sprintf("The agent wants to execute a **%s** command.\n\n```json\n%s\n```\n\n> Press **[y]** Allow once | **[s]** Allow session | **[p]** Allow project | **[g]** Allow global | **[n]** Deny", displayName, tc.Function.Arguments)
		md, _ := m.Renderer.Render(prompt)
		blocks = append(blocks, aiMsgStyle.Width(msgWidth+1).Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color("#FFD700")).Render(md))
	}

	if m.Err != nil {
		blocks = append(blocks, thinkingStyle.Foreground(lipgloss.Color("#FF0000")).Render(fmt.Sprintf("Error: %v", m.Err)))
	}

	fullContent := strings.Join(blocks, "\n")
	if fullContent == s.LastTotalContent && m.LastFocusedID == m.Focused.ID() {
		return
	}
	s.LastTotalContent = fullContent
	m.LastFocusedID = m.Focused.ID()

	atBottom := m.Viewport.AtBottom()
	m.Viewport.SetContent(fullContent)
	if atBottom {
		m.Viewport.GotoBottom()
	}
}

func (m *Model) renderAnimatedTag(text string, baseStyle lipgloss.Style, width int, active bool) string {
	textWidth := lipgloss.Width(text)

	isTruncated := textWidth > width
	shouldAnimate := active && (isTruncated || text == "Thinking" || strings.HasSuffix(text, "..."))

	if !shouldAnimate {
		if isTruncated {
			text = m.truncateWithEllipsis(text, width)
		}
		return baseStyle.Copy().Width(width).Render(text)
	}

	// Use millisecond timestamp for smooth movement
	ms := float64(time.Now().UnixNano()) / 1e6

	// Use width instead of textWidth for truncated tags to prevent violent shifting
	// when characters are appended during streaming. For small tags (Thinking, etc),
	// use the actual text width so the animation doesn't feel too slow.
	period := float64(textWidth)
	if isTruncated {
		text = m.truncateWithEllipsis(text, width)
		textWidth = lipgloss.Width(text)
		period = float64(width)
	}

	// Get base and shine colors from the provided style if possible
	fg := baseStyle.GetForeground()
	bg := baseStyle.GetBackground()

	// If background is unset, use the app background to prevent leakage
	if bg == nil {
		bg = appBgColor
	}

	// Dynamic speed and waveWidth based on period:
	// Short strings loop fast, long strings loop reasonably fast without
	// the shine moving at light speed.
	waveWidth := 4.0 + math.Sqrt(period)*0.5
	speed := 10.0 + 1400.0/(period+10.0)
	totalLoop := period + waveWidth
	cycle := math.Mod(ms/speed, totalLoop)

	grad := lipgloss.Blend1D(100, fg, textColor)
	var sb strings.Builder
	for i, r := range text {
		pos := float64(i)
		dist := math.Abs(pos - cycle)
		if dist > totalLoop/2 {
			dist = totalLoop - dist
		}

		factor := 0.0
		if dist < waveWidth {
			factor = 1.0 - (dist / waveWidth)
			factor = math.Pow(math.Sin(factor*math.Pi/2), 2)
		}

		step := int(factor * 99)
		charStyle := lipgloss.NewStyle().
			Foreground(grad[step]).
			Background(bg)
		sb.WriteString(charStyle.Render(string(r)))
	}

	return baseStyle.Copy().Width(width).Render(sb.String())
}

func (m *Model) truncateWithEllipsis(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 3 {
		return "..."
	}

	limit := w - 3
	runes := []rune(s)
	res := ""
	currW := 0
	for _, r := range runes {
		rw := lipgloss.Width(string(r))
		if currW+rw > limit {
			break
		}
		res += string(r)
		currW += rw
	}
	return res + "..."
}

func (m *Model) renderMarkdownBlock(content string, innerWidth int) string {
	// Use new renderer to avoid background color issues
	md, _ := m.GetRenderer(innerWidth).Render(content)
	//md = strings.TrimRight(md, "\n")

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
