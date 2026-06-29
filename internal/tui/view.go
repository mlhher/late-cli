package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"path/filepath"
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

	if m.ShowFilePicker {
		// Build picker hints line
		hEnter := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Enter"), statusTextStyle.Render(" Select/Open "))
		hBack := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Backspace"), statusTextStyle.Render(" Up "))
		hEsc := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Esc"), statusTextStyle.Render(" Exit "))
		pickerHints := lipgloss.JoinHorizontal(lipgloss.Left, hEnter, hBack, hEsc)

		// File picker area: leave room for hints (1 line) + status bar (StatusBarHeight)
		fpHeight := m.Height - StatusBarHeight - 1
		if fpHeight < 1 {
			fpHeight = 1
		}
		vStr = lipgloss.NewStyle().
			Height(fpHeight).
			MaxHeight(fpHeight).
			Width(m.Width).
			Background(appBgColor).
			Render(m.FilePicker.View())
		iStr = lipgloss.NewStyle().
			Background(appBgColor).
			Width(m.Width).
			Render(pickerHints)
	}

	aStr := m.autocompleteView()
	sStr := m.statusBarView()

	// Insert autocomplete between viewport and input when active
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		vStr,
	)
	if aStr != "" {
		content = lipgloss.JoinVertical(lipgloss.Left, content, aStr)
	}
	content = lipgloss.JoinVertical(lipgloss.Left, content, iStr, sStr)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = appBgColor
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *Model) inputView() string {
	w := m.Width - 4 // Internal padding for input
	if w < 1 {
		w = 1
	}

	// Render textarea directly — its styles already set background via FocusedStyle/BlurredStyle
	textareaView := m.Input.View()

	// Dynamic border style: pulse separator color when active (thinking or streaming)
	activeStyle := inputStyle.Copy()
	s := m.GetAgentState(m.Focused.ID())
	if s.State == StateThinking || s.State == StateStreaming {
		ms := float64(time.Now().UnixNano()) / 1e6
		pulse := (math.Sin(ms/250.0) + 1.0) / 2.0 // oscillate 0 to 1

		targetColor := secondaryColor

		borderGrad := lipgloss.Blend1D(100, lipgloss.Color("#232329"), targetColor)
		pulseColor := borderGrad[int(pulse*99)]
		activeStyle = activeStyle.BorderForeground(pulseColor)
	}

	// Sync width precisely: inputStyle (border 2 + padding 2) + w (m.Width - 4) = m.Width
	// Internal width of inputStyle becomes m.Width - 8, matching m.Input.SetWidth()
	content := activeStyle.Width(w).Render(textareaView)

	// Wrap in a fixed-size container that fills the background
	return baseStyle.Copy().
		Width(m.Width).
		Height(InputHeight).
		Padding(0, 2).
		AlignVertical(lipgloss.Bottom).
		Render(content)
}

// autocompleteView renders the slash-command autocomplete dropdown.
// Returns empty string when no autocomplete is active.
func (m *Model) autocompleteView() string {
	if !m.ShowAutocomplete || len(m.AutocompleteItems) == 0 {
		return ""
	}

	w := m.Width
	if w < 1 {
		w = 80
	}

	var lines []string
	for i, item := range m.AutocompleteItems {
		prefix := "  "
		style := lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(thoughtBgColor).
			Width(w - 4).
			PaddingLeft(2)

		if i == m.AutocompleteIndex {
			prefix = "▸ "
			style = lipgloss.NewStyle().
				Foreground(primaryColor).
				Background(thoughtBgColor).
				Width(w - 4).
				PaddingLeft(2).
				Bold(true)
		}

		lines = append(lines, style.Render(prefix+item))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(secondaryColor).
		BorderBackground(thoughtBgColor).
		Background(thoughtBgColor).
		Width(w).
		MaxHeight(len(lines) + 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return box
}

// statusBg wraps a string in the status bar background color.
// VTE-based terminals (Ptyxis, GNOME Console) don't inherit a container's
// background after inner ANSI resets (\e[0m). Every character cell —
// including plain spaces and separators — needs its own explicit background
// to prevent the terminal's theme background from leaking through.
var statusBgStyle = lipgloss.NewStyle().Background(appBgColor)

func statusBg(s string) string {
	return statusBgStyle.Render(s)
}

// formatTokenCount formats a token count with k/m suffix for compact display.
func (m *Model) formatTokenCount(count int) string {
	switch {
	case count >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(count)/1_000_000)
	case count >= 1_000:
		return fmt.Sprintf("%dk", count/1_000)
	default:
		return fmt.Sprintf("%d", count)
	}
}

// renderContextBar renders a compact visual display for token usage.
// When max is known:  [████████░░] 72% (14k/20k t)
// When max is -1:     ⟨14k t⟩ (unknown max)
// When max is 0:      ⟨14k t⟩ (unlimited / default)
func (m *Model) renderContextBar(current, max int) string {
	if max < 0 {
		// Unknown max: show the styled count in angle brackets
		countStr := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor).Render(m.formatTokenCount(current))
		unknownStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Render("?")
		return countStr + statusBg(" ") + unknownStyle
	}

	if max == 0 {
		// Reported unlimited: show with infinity indicator
		countStr := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor).Render(m.formatTokenCount(current))
		infStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Render("∞")
		return countStr + statusBg(" ") + infStyle
	}

	barWidth := 10

	pct := (current * 100) / max
	if pct > 100 {
		pct = 100
	}

	filled := (pct * barWidth) / 100
	empty := barWidth - filled

	// Color selection based on usage level
	var barColor color.Color
	switch {
	case pct >= 85:
		barColor = warningColor
	case pct >= 60:
		barColor = primaryColor
	default:
		barColor = secondaryColor
	}

	fillStyle := lipgloss.NewStyle().Foreground(barColor).Background(appBgColor)
	emptyStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)

	bar := ""
	for range filled {
		bar += fillStyle.Render("█")
	}
	for range empty {
		bar += emptyStyle.Render("░")
	}

	bracketStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
	barStr := bracketStyle.Render("[") + bar + bracketStyle.Render("]")

	pctStyle := lipgloss.NewStyle().Foreground(barColor).Background(appBgColor)
	label := pctStyle.Render(fmt.Sprintf("%d%%", pct))

	size := fmt.Sprintf("%s/%s", m.formatTokenCount(current), m.formatTokenCount(max))
	sizeStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)

	return barStr + statusBg(" ") + label + statusBg(" (") + sizeStyle.Render(size) + statusBg(")")
}

func (m *Model) renderMinimalEqualizer() string {
	t := float64(time.Now().UnixMilli()) / 180.0
	bars := []rune(" ▂▃▄▅▆▇█")
	numBars := len(bars)

	var cols [3]rune
	for i := 0; i < 3; i++ {
		phase := float64(i) * 1.5
		val := (math.Sin(t+phase) + 1.0) / 2.0 // oscillates 0 to 1
		idx := int(val * float64(numBars-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= numBars {
			idx = numBars - 1
		}
		cols[i] = bars[idx]
	}

	bracketStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
	equalizerStyle := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for _, col := range cols {
		sb.WriteString(equalizerStyle.Render(string(col)))
	}
	sb.WriteString(bracketStyle.Render("]"))
	return sb.String()
}

func (m *Model) renderScannerTrack(symbol string, symbolColor color.Color) string {
	t := float64(time.Now().UnixMilli()) / 120.0
	pos := int(math.Round(2.5 + 2.5*math.Sin(t)))

	track := []rune("──────")
	if pos >= 0 && pos < len(track) {
		track[pos] = []rune(symbol)[0]
	}

	bracketStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
	symbolStyle := lipgloss.NewStyle().Foreground(symbolColor).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for _, r := range track {
		if string(r) == symbol {
			sb.WriteString(symbolStyle.Render(string(r)))
		} else {
			sb.WriteString(bracketStyle.Render(string(r)))
		}
	}
	sb.WriteString(bracketStyle.Render("]"))
	return sb.String()
}

func (m *Model) statusBarView() string {
	w := max(m.Width, 1)

	if m.ShowFilePicker {
		return ""
	}

	s := m.GetAgentState(m.Focused.ID())

	var leftSection string
	statusText := s.StatusText

	switch s.State {
	case StateThinking:
		scanner := m.renderScannerTrack("✦", primaryColor)
		label := lipgloss.NewStyle().Foreground(primaryColor).Background(appBgColor).Bold(true).Render("working")
		leftSection = scanner + statusBg(" ") + label
	case StateStreaming:
		eq := m.renderMinimalEqualizer()
		label := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor).Bold(true).Render("streaming")
		leftSection = eq + statusBg(" ") + label
	case StateConfirmTool:
		icon := lipgloss.NewStyle().Foreground(warningColor).Background(appBgColor).Render("❖")
		label := lipgloss.NewStyle().Foreground(warningColor).Background(appBgColor).Bold(true).Render("confirm")
		leftSection = icon + statusBg(" ") + label
		statusText = "Authorize Tool Execution (y/s/p/g/n)"
	default:
		bullet := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor).Render("●")
		label := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Render("idle")
		leftSection = bullet + statusBg(" ") + label
	}

	// Append CWD to the left section
	if m.CWD != "" {
		cwdStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
		display := m.CWD
		// Truncate long paths
		if lipgloss.Width(display) > 40 {
			display = "…" + display[len(display)-38:]
		}
		leftSection += statusBg("  ") + cwdStyle.Render(display)
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
		warning = statusWarningStyle.Render("⚠️ SUBAGENT CONFIRM REQUIRED")
		if strings.Contains(statusText, "spawned") {
			statusText = ""
		}
	}

	// If there's an active toast message, render it. Otherwise, standard status text.
	var status string
	hasToast := m.ToastMessage != "" && time.Now().UnixMilli() < m.ToastExpireTime
	if hasToast {
		status = lipgloss.NewStyle().Foreground(primaryColor).Background(appBgColor).Bold(true).Render("✓ " + m.ToastMessage)
	} else if statusText != "" && statusText != "Working..." && statusText != "Ready" && statusText != "Closed" {
		if s.State == StateConfirmTool {
			status = statusWarningStyle.Render(statusText)
		} else {
			status = lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Italic(true).Render(statusText)
		}
	}

	// Append warning to status area if present
	if warning != "" {
		if status != "" {
			status += statusBg("   ") + warning
		} else {
			status = warning
		}
	}

	// Build breadcrumbs
	var pathParts []string
	curr := m.Focused
	for curr != nil {
		pathParts = append([]string{curr.ID()}, pathParts...)
		curr = curr.Parent()
	}

	var breadcrumbStr string
	if len(pathParts) > 0 {
		breadcrumbStr = breadcrumbLateStyle.Render("late")
		for _, part := range pathParts {
			breadcrumbStr += statusBg(" ") + breadcrumbSeparatorStyle.Render("›") + statusBg(" ") + breadcrumbAgentStyle.Render(part)
		}
	}

	// Build right-side telemetry: Attached files, Context bar, Breadcrumbs, Help
	var attachedStr string
	if len(m.AttachedFiles) > 0 {
		attachedStr = statusAttachedStyle.Render(fmt.Sprintf("📎 %d files", len(m.AttachedFiles)))
	}

	// Context bar
	var tokenStr string
	maxTokens := m.Focused.MaxTokens()
	if s.CumulativeTokenCount > 0 {
		tokenStr = m.renderContextBar(s.CumulativeTokenCount, maxTokens)
	}

	helpStr := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Render("ctrl+h Help")

	var rightParts []string
	if attachedStr != "" {
		rightParts = append(rightParts, attachedStr)
	}
	if tokenStr != "" {
		rightParts = append(rightParts, tokenStr)
	}
	if breadcrumbStr != "" {
		rightParts = append(rightParts, breadcrumbStr)
	}
	rightParts = append(rightParts, helpStr)
	rightSection := strings.Join(rightParts, statusBg("   "))

	// Adjust layout and truncate status text in the middle if necessary
	usableW := w - 2 // Usable width excluding left/right padding space
	if usableW < 1 {
		usableW = 1
	}

	leftWidth := lipgloss.Width(leftSection)
	rightWidth := lipgloss.Width(rightSection)

	spaceWidth := usableW - leftWidth - rightWidth
	if status != "" {
		statusWidth := lipgloss.Width(status)
		if statusWidth+3 > spaceWidth {
			// Truncate status text to fit
			maxStatusW := spaceWidth - 3
			if maxStatusW < 0 {
				maxStatusW = 0
			}
			if hasToast {
				truncated := m.truncateWithEllipsis("✓ "+m.ToastMessage, maxStatusW)
				status = lipgloss.NewStyle().Foreground(primaryColor).Background(appBgColor).Bold(true).Render(truncated)
			} else {
				truncated := m.truncateWithEllipsis(statusText, maxStatusW)
				if s.State == StateConfirmTool {
					status = statusWarningStyle.Render(truncated)
				} else {
					status = lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Italic(true).Render(truncated)
				}
			}
			statusWidth = lipgloss.Width(status)
		}
		if status != "" {
			spaceWidth = spaceWidth - statusWidth - 3
		}
	}

	if spaceWidth < 0 {
		spaceWidth = 0
	}

	space := statusBg(strings.Repeat(" ", spaceWidth))

	var parts []string
	parts = append(parts, leftSection)
	if status != "" {
		parts = append(parts, statusBg("   "), status)
	}
	parts = append(parts, space, rightSection)

	content := lipgloss.JoinHorizontal(lipgloss.Left, parts...)
	paddedContent := statusBg(" ") + content + statusBg(" ")
	return statusBarBaseStyle.Width(w).Render(paddedContent)
}

func (m *Model) updateViewport() {
	if m.Focused == nil {
		return
	}

	if m.Mode == ViewCommitLog {
		m.renderCommitLogView()
		return
	}

	if m.EscConfirmPending {
		s := m.GetAgentState(m.Focused.ID())
		busy := s.State == StateThinking || s.State == StateStreaming || s.State == StateStopping

		var prompt string
		if busy {
			prompt = "**Stop current agent?**\n\nThe agent is still working. Stopping will discard the current response.\n\n> Press **[y]** Yes, stop it  |  **[n]** No, continue"
		} else {
			prompt = "**Exit Late?**\n\n> Press **[y]** Yes, quit  |  **[n]** No, stay"
		}
		md, _ := m.Renderer.Render(prompt)
		dialog := lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(warningColor).
			Padding(1, 2).
			Background(appBgColor).
			Render(md)

		// Center the dialog with a solid background
		r := lipgloss.Place(m.Viewport.Width(), m.Viewport.Height(),
			lipgloss.Center, lipgloss.Center,
			dialog,
			lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(appBgColor)))
		m.Viewport.SetContent(r)
		return
	}

	if m.Mode == ViewHelp {
		// Clear LastTotalContent so that when we toggle back, the cache mismatch is triggered
		s := m.GetAgentState(m.Focused.ID())
		s.LastTotalContent = ""

		helpText := `# Late Help & Keybindings

Here is a list of available keyboard shortcuts:

  **ctrl+a**        Toggle File Picker (attach files to prompt)
  **ctrl+x**        Clear attached files
  **ctrl+g** / **esc**   Interrupt / stop active agent
  **tab**           Switch focus between active subagents
  **alt+enter**     Insert newline in prompt
  **enter**         Submit prompt
  **ctrl+h**        Toggle this Help menu

Press **ctrl+h** or **esc** to return to the chat.`

		// Total outer width is m.Viewport.Width()
		// Usable inner width = outer width - padding (4) - border (2) = outer width - 6
		msgWidth := m.Viewport.Width() - 6
		if msgWidth < 1 {
			msgWidth = 74
		}
		rendered := m.renderMarkdownBlock(helpText, msgWidth)
		boxed := lipgloss.NewStyle().
			Padding(1, 2).
			Border(lipgloss.DoubleBorder()).
			BorderForeground(secondaryColor).
			Width(msgWidth).
			Render(rendered)

		m.Viewport.SetContent(boxed)
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
			content := msg.Content.UIString()
			if len(msg.AttachedFiles) > 0 {
				var names []string
				for _, f := range msg.AttachedFiles {
					name := filepath.Base(f)
					if len(name) > 20 {
						name = name[:17] + "..."
					}
					names = append(names, name)
				}

				attachmentLabel := "Attached: " + strings.Join(names, ", ")
				maxLabelWidth := msgWidth - 4
				if lipgloss.Width(attachmentLabel) > maxLabelWidth {
					attachmentLabel = m.truncateWithEllipsis(attachmentLabel, maxLabelWidth)
				}
				content += "\n\n" + attachmentStyle.Render(attachmentLabel)
			}
			// Render bubble at natural width, then right-align in viewport
			bubble := userMsgStyle.Render(content)
			rendered = lipgloss.NewStyle().
				Width(msgWidth + 2).
				Align(lipgloss.Right).
				Render(bubble)
		case "assistant":
			var assistantParts []string
			if msg.ReasoningContent != "" {
				assistantParts = append(assistantParts, thoughtHeaderStyle.Width(msgWidth+1).Render("Thoughts:"))
				assistantParts = append(assistantParts, thinkingStyle.Width(msgWidth-2).Render(msg.ReasoningContent))
			}
			if msg.Content.String() != "" {
				innerWidth := m.Viewport.Width() - AIMsgOverhead
				if innerWidth < 1 {
					innerWidth = 1
				}
				md := m.renderMarkdownBlock(msg.Content.String(), innerWidth)
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
	s.RenderBlocks = nil
	currentLine := 0

	for idx, r := range s.RenderedHistory {
		if r != "" {
			blocks = append(blocks, r)
			linesCount := strings.Count(r, "\n") + 1

			copyText := history[idx].Content.String()
			if history[idx].Role == "user" {
				copyText = history[idx].Content.UIString()
			}

			s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
				MessageIndex: idx,
				Content:      copyText,
				StartLine:    currentLine,
				EndLine:      currentLine + linesCount - 1,
			})
			currentLine += linesCount
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
			r := strings.Join(activeParts, "\n")
			blocks = append(blocks, r)
			linesCount := strings.Count(r, "\n") + 1

			s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
				MessageIndex: -1,
				Content:      s.StreamingState.Content,
				StartLine:    currentLine,
				EndLine:      currentLine + linesCount - 1,
			})
			currentLine += linesCount
		} else if s.State == StateThinking {
			r := m.renderAnimatedTag("Thinking", thinkingStyle, msgWidth-2, true)
			blocks = append(blocks, r)
			linesCount := strings.Count(r, "\n") + 1

			s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
				MessageIndex: -1,
				Content:      "Thinking...",
				StartLine:    currentLine,
				EndLine:      currentLine + linesCount - 1,
			})
			currentLine += linesCount
		}
	}

	// Render Interactions
	if s.State == StateConfirmTool && s.PendingConfirm != nil {
		tc := s.PendingConfirm.ToolCall
		displayName := tc.Function.Name
		if runtime.GOOS == "windows" && displayName == "bash" {
			displayName = "PowerShell"
		}
		prompt := fmt.Sprintf("The agent wants to execute a **%s** command.\n\n```json\n%s\n```\n\n> Press **[y]** Allow once | **[s]** Allow always (session) | **[p]** Allow always (project) | **[g]** Allow always (global) | **[n]** Deny", displayName, tc.Function.Arguments)
		md, _ := m.Renderer.Render(prompt)
		r := aiMsgStyle.Width(msgWidth + 1).Border(lipgloss.DoubleBorder()).BorderForeground(warningColor).Render(md)
		blocks = append(blocks, r)
		linesCount := strings.Count(r, "\n") + 1

		s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
			MessageIndex: -1,
			Content:      tc.Function.Arguments,
			StartLine:    currentLine,
			EndLine:      currentLine + linesCount - 1,
		})
		currentLine += linesCount
	}

	if s.State == StateContextWarning {
		prompt := "⚠️ **Context Limit Warning**\n\nYou are approaching the maximum context size for this session (over 90% used). It is highly recommended to **start a new session** to ensure the agent maintains full context and accuracy.\n\n> Press **[Enter]** again to proceed anyway, or start a new session."
		md, _ := m.Renderer.Render(prompt)
		r := aiMsgStyle.Width(msgWidth + 1).Border(lipgloss.DoubleBorder()).BorderForeground(warningColor).Render(md)
		blocks = append(blocks, r)
		linesCount := strings.Count(r, "\n") + 1

		s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
			MessageIndex: -1,
			Content:      prompt,
			StartLine:    currentLine,
			EndLine:      currentLine + linesCount - 1,
		})
		currentLine += linesCount
	}

	if s.Error != nil {
		errStr := s.Error.Error()
		var prompt string
		var r string
		if strings.Contains(errStr, "exceeds the available context size") || strings.Contains(errStr, "context_length_exceeded") {
			prompt = "🛑 **Context Limit Exceeded**\n\nThis session has hit the model's absolute context limit. The agent cannot proceed further in this session.\n\n**Action Required:** Please **start a new session** to continue your work."
			md, _ := m.Renderer.Render(prompt)
			r = aiMsgStyle.Width(msgWidth + 1).Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color("#FF0000")).Render(md)
		} else {
			prompt = fmt.Sprintf("Error: %v", s.Error)
			r = thinkingStyle.Foreground(lipgloss.Color("#FF0000")).Render(prompt)
		}
		blocks = append(blocks, r)
		linesCount := strings.Count(r, "\n") + 1

		s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
			MessageIndex: -1,
			Content:      prompt,
			StartLine:    currentLine,
			EndLine:      currentLine + linesCount - 1,
		})
		currentLine += linesCount
	} else if m.Err != nil {
		prompt := fmt.Sprintf("Error: %v", m.Err)
		r := thinkingStyle.Foreground(lipgloss.Color("#FF0000")).Render(prompt)
		blocks = append(blocks, r)
		linesCount := strings.Count(r, "\n") + 1

		s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
			MessageIndex: -1,
			Content:      prompt,
			StartLine:    currentLine,
			EndLine:      currentLine + linesCount - 1,
		})
		currentLine += linesCount
	}

	// Render Queued Messages
	for _, msg := range m.Focused.QueuedMessages() {
		r := queuedMsgStyle.Width(msgWidth + 1).Render(msg)
		blocks = append(blocks, r)
		linesCount := strings.Count(r, "\n") + 1

		s.RenderBlocks = append(s.RenderBlocks, RenderBlock{
			MessageIndex: -1,
			Content:      msg,
			StartLine:    currentLine,
			EndLine:      currentLine + linesCount - 1,
		})
		currentLine += linesCount
	}

	var fullContent string
	if len(blocks) == 0 {
		fullContent = m.renderWelcomeMessage()
	} else {
		fullContent = strings.Join(blocks, "\n")
	}

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
// renderWelcomeMessage builds the rich welcome screen shown when history is empty.
func (m *Model) renderWelcomeMessage() string {
	msgWidth := m.Viewport.Width() - 6
	if msgWidth < 1 {
		msgWidth = 74
	}

	// Build model line
	modelStr := ""
	if m.ModelName != "" {
		modelStr = fmt.Sprintf("**Model:** %s", m.ModelName)
	} else {
		modelStr = "**Model:** —"
	}

	// Build context size line
	maxTokens := m.Focused.MaxTokens()
	ctxStr := ""
	if maxTokens > 0 {
		ctxStr = fmt.Sprintf("**Context:** %s tokens", m.formatTokenCount(maxTokens))
	} else if maxTokens == 0 {
		ctxStr = "**Context:** unlimited"
	} else {
		ctxStr = "**Context:** —"
	}

	welcome := fmt.Sprintf(`# Welcome to **Late**

Your AI coding agent. Type a prompt below to get started.

%s
%s
`, modelStr, ctxStr)

	// Add subagent info if configured
	if m.SubagentInfo != "" {
		welcome += fmt.Sprintf("\n**Subagents:** %s\n", m.SubagentInfo)
	}

	// Render through glamour for markdown formatting
	rendered := m.renderMarkdownBlock(welcome, msgWidth)

	return lipgloss.NewStyle().
		Padding(1, 2).
		Width(msgWidth).
		Render(rendered)
}

// renderCommitLogView renders the commit history or commit detail in the viewport.
func (m *Model) renderCommitLogView() {
	s := m.GetAgentState(m.Focused.ID())
	s.LastTotalContent = ""

	msgWidth := m.Viewport.Width() - 2
	if msgWidth < 1 {
		msgWidth = 80
	}

	if m.CommitDetail != "" {
		// Show full commit detail — render through glamour for syntax highlighting
		innerWidth := msgWidth - 4
		if innerWidth < 1 {
			innerWidth = 74
		}
		detail := "```\n" + m.CommitDetail + "\n```"
		rendered := m.renderMarkdownBlock(detail, innerWidth)
		boxed := lipgloss.NewStyle().
			Padding(1, 2).
			Border(lipgloss.DoubleBorder()).
			BorderForeground(primaryColor).
			Width(msgWidth).
			Render(rendered)
		m.Viewport.SetContent(boxed)
		return
	}

	// Build commit list
	var lines []string
	header := lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		Background(appBgColor).
		PaddingLeft(1).
		Render("── Commit History ──────────────────────────────────")
	lines = append(lines, header, "")

	if len(m.CommitEntries) == 0 {
		lines = append(lines, lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			PaddingLeft(2).
			Render("No commits found."))
	} else {
		for i, entry := range m.CommitEntries {
			prefix := "  "
			itemStyle := lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor).
				PaddingLeft(2)
			hashStyle := lipgloss.NewStyle().
				Foreground(secondaryColor).
				Background(appBgColor)
			dateStyle := lipgloss.NewStyle().
				Foreground(subtextColor).
				Background(appBgColor).
				Italic(true)
			msgStyle := lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor)

			if i == m.CommitIndex {
				prefix = "▸ "
				itemStyle = lipgloss.NewStyle().
					Foreground(primaryColor).
					Background(thoughtBgColor).
					PaddingLeft(2).
					Bold(true)
				hashStyle = lipgloss.NewStyle().
					Foreground(primaryColor).
					Background(thoughtBgColor).
					Bold(true)
				dateStyle = lipgloss.NewStyle().
					Foreground(primaryColor).
					Background(thoughtBgColor).
					Italic(true)
				msgStyle = lipgloss.NewStyle().
					Foreground(textColor).
					Background(thoughtBgColor)
			}

			headMarker := ""
			if entry.IsHEAD {
				headMarker = " " + lipgloss.NewStyle().Foreground(primaryColor).Background(appBgColor).Render("(HEAD)")
				if i == m.CommitIndex {
					headMarker = " " + lipgloss.NewStyle().Foreground(primaryColor).Background(thoughtBgColor).Render("(HEAD)")
				}
			}

			hashStr := hashStyle.Render(entry.Hash)
			dateStr := dateStyle.Render(entry.Date)
			msgStr := msgStyle.Render(entry.Message)

			line := prefix + hashStr + statusBg("  ") + msgStr + headMarker
			metaLine := statusBg("   ") + dateStyle.Render(entry.Author) + statusBg(" · ") + dateStr

			lines = append(lines, itemStyle.Render(line))
			if i == m.CommitIndex {
				lines = append(lines, itemStyle.Render(metaLine))
			} else {
				lines = append(lines, statusBg("   ")+dateStr)
			}
			lines = append(lines, statusBg(""))
		}
	}

	// Footer hint
	footer := lipgloss.NewStyle().
		Foreground(subtextColor).
		Background(appBgColor).
		PaddingLeft(1).
		Render(fmt.Sprintf("↑↓ navigate · Enter view · Esc back  (%d commits)", len(m.CommitEntries)))
	lines = append(lines, "", footer)

	m.Viewport.SetContent(strings.Join(lines, "\n"))
}

// overlayCentered places the dialog string centered over the background string,
// matching the viewport dimensions. The dialog is sized to fit its content.
func overlayCentered(background, dialog string, vpWidth, vpHeight int) string {
	bgLines := strings.Split(background, "\n")
	dialogLines := strings.Split(dialog, "\n")

	// Calculate dialog dimensions from actual content
	dialogW := 0
	for _, line := range dialogLines {
		w := lipgloss.Width(line)
		if w > dialogW {
			dialogW = w
		}
	}
	dialogH := len(dialogLines)

	// Clamp to viewport
	if dialogW > vpWidth {
		dialogW = vpWidth
	}
	if dialogH > vpHeight {
		dialogH = vpHeight
	}

	// Center position
	startX := (vpWidth - dialogW) / 2
	startY := (vpHeight - dialogH) / 2

	// Build result by overlaying dialog onto background
	result := make([]string, 0, vpHeight)
	for y := 0; y < vpHeight; y++ {
		var bgLine string
		if y < len(bgLines) {
			bgLine = bgLines[y]
		} else {
			bgLine = ""
		}

		// Pad background line to full width
		if len(bgLine) < vpWidth {
			bgLine += strings.Repeat(" ", vpWidth-len(bgLine))
		}

		// Overlay dialog
		dialogIdx := y - startY
		if dialogIdx >= 0 && dialogIdx < dialogH {
			dl := dialogLines[dialogIdx]
			// Pad dialog line
			if len(dl) < dialogW {
				dl += strings.Repeat(" ", dialogW-len(dl))
			}
			// Replace characters in the background
			runes := []rune(bgLine)
			for x := 0; x < dialogW && startX+x < len(runes); x++ {
				runes[startX+x] = []rune(dl)[x]
			}
			bgLine = string(runes)
		}

		result = append(result, bgLine)
	}

	return strings.Join(result, "\n")
}

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
