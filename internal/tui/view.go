package tui

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"late/internal/client"
	"late/internal/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	if m.Mode == ViewModelPicker {
		hUpDn := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("↑/↓"), statusTextStyle.Render(" Select Agent "))
		hLfRt := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("←/→"), statusTextStyle.Render(" Choose Model "))
		hEnter := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Enter"), statusTextStyle.Render(" Save "))
		hEsc := lipgloss.JoinHorizontal(lipgloss.Left, statusKeyStyle.Render("Esc"), statusTextStyle.Render(" Cancel "))
		pickerHints := lipgloss.JoinHorizontal(lipgloss.Left, hUpDn, "  ", hLfRt, "  ", hEnter, "  ", hEsc)

		iStr = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(borderColor).
			BorderBackground(appBgColor).
			Background(appBgColor).
			Width(m.Width).
			Padding(1, 2).
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

	v := tea.NewView(sanitizeVTE(content, m.Width))
	v.AltScreen = true
	v.BackgroundColor = appBgColor
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

const appBgAnsi = "\x1b[48;2;11;12;14m"

// sanitizeVTE ensures all character cells and line-end paddings across the
// visible screen strictly maintain appBgColor in VTE-based terminals.
func sanitizeVTE(s string, screenWidth int) string {
	if s == "" || screenWidth <= 0 {
		return s
	}

	// 1. Re-assert appBgColor immediately after any ANSI reset (\e[m, \e[0m, \e[49m).
	// In VTE, \e[m resets background to the terminal emulator's profile color.
	// Re-asserting appBgAnsi guarantees that any subsequent space, separator,
	// or padding character will be painted with Late's #0B0C0E background.
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+appBgAnsi)
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+appBgAnsi)
	s = strings.ReplaceAll(s, "\x1b[49m", appBgAnsi)

	// 2. Pad every line to screenWidth with background-painted cells.
	// When every cell from column 0 to screenWidth has explicit background,
	// Bubble Tea v2's ultraviolet engine never treats trailing cells as EmptyCell,
	// preventing it from issuing unstyled \x1b[K (EraseLineRight) into VTE.
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w < screenWidth {
			line = line + appBgAnsi + strings.Repeat(" ", screenWidth-w)
		}
		lines[i] = appBgAnsi + line
	}
	return strings.Join(lines, "\n")
}

func (m *Model) inputView() string {
	// Render textarea directly
	textareaView := m.Input.View()
	paddedTextarea := lipgloss.NewStyle().Padding(0, 1).Background(appBgColor).Render(textareaView)

	outerStyle := baseStyle.Copy().
		Width(m.Width).
		AlignVertical(lipgloss.Bottom).
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(borderColor).
		BorderBackground(appBgColor).
		MarginBackground(appBgColor)

	s := m.GetAgentState(m.Focused.ID())
	if s.State == StateThinking || s.State == StateStreaming {
		outerStyle = outerStyle.BorderForeground(activeBorder)
	} else if s.State == StateConfirmTool {
		outerStyle = outerStyle.BorderForeground(warnBorderColor)
	}

	return outerStyle.Render(paddedTextarea)
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
		nameStyle := lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(cardBgColor).
			PaddingLeft(1)

		descStyle := lipgloss.NewStyle().
			Foreground(mutedTextColor).
			Background(cardBgColor)

		if i == m.AutocompleteIndex {
			prefix = "▸ "
			nameStyle = nameStyle.Foreground(primaryColor).Bold(true)
			descStyle = descStyle.Foreground(textColor)
		}

		nameStr := nameStyle.Render(fmt.Sprintf("%s%-10s", prefix, item.Name))

		descWidth := (w - 4) - lipgloss.Width(nameStr)
		if descWidth < 0 {
			descWidth = 0
		}
		descStyle = descStyle.Width(descWidth)

		descStr := descStyle.Render(" " + item.Description)

		lines = append(lines, nameStr+descStr)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(secondaryColor).
		BorderBackground(cardBgColor).
		Background(cardBgColor).
		Width(w).
		MaxHeight(len(lines) + 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return box
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
		return countStr + " " + unknownStyle
	}

	if max == 0 {
		// Reported unlimited: show with infinity indicator
		countStr := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor).Render(m.formatTokenCount(current))
		infStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor).Render("∞")
		return countStr + " " + infStyle
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
		barColor = accentCoral
	case pct >= 60:
		barColor = primaryColor
	default:
		barColor = secondaryColor
	}

	fillStyle := lipgloss.NewStyle().Foreground(barColor).Background(appBgColor)
	emptyStyle := lipgloss.NewStyle().Foreground(borderColor).Background(appBgColor)

	bar := ""
	for range filled {
		bar += fillStyle.Render("█")
	}
	for range empty {
		bar += emptyStyle.Render("░")
	}

	bracketStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	barStr := bracketStyle.Render("[") + bar + bracketStyle.Render("]")

	pctStyle := lipgloss.NewStyle().Foreground(barColor).Background(appBgColor).Bold(true)
	label := pctStyle.Render(fmt.Sprintf("%d%%", pct))

	size := fmt.Sprintf("%s/%s", m.formatTokenCount(current), m.formatTokenCount(max))
	sizeStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)

	return barStr + " " + label + " (" + sizeStyle.Render(size) + ")"
}

func (m *Model) renderMinimalEqualizer() string {
	t := float64(time.Now().UnixMilli()) / 140.0
	bars := []rune(" ▂▃▄▅▆▇█")
	numBars := len(bars)

	var cols [5]rune
	phases := [5]float64{0.0, 1.3, 2.7, 4.0, 5.2}
	for i := 0; i < 5; i++ {
		val := (math.Sin(t+phases[i]) + 1.0) / 2.0 // oscillates 0 to 1
		idx := int(val * float64(numBars-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= numBars {
			idx = numBars - 1
		}
		cols[i] = bars[idx]
	}

	bracketStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	equalizerStyle := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for _, col := range cols {
		sb.WriteString(equalizerStyle.Render(string(col)))
	}
	sb.WriteString(bracketStyle.Render("]"))
	return sb.String()
}

func (m *Model) renderIdleEqualizer() string {
	bracketStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	barStyle := lipgloss.NewStyle().Foreground(borderColor).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for i := 0; i < 5; i++ {
		sb.WriteString(barStyle.Render(" "))
	}
	sb.WriteString(bracketStyle.Render("]"))
	return sb.String()
}

func (m *Model) renderScannerTrack(symbol string, symbolColor color.Color) string {
	t := float64(time.Now().UnixMilli()) / 120.0
	pos := int(math.Round(3.0 + 3.0*math.Sin(t)))

	track := []rune("·······")
	if pos >= 0 && pos < len(track) {
		track[pos] = []rune(symbol)[0]
	}

	bracketStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	symbolStyle := lipgloss.NewStyle().Foreground(symbolColor).Background(appBgColor)
	dotStyle := lipgloss.NewStyle().Foreground(borderColor).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for _, r := range track {
		if string(r) == symbol {
			sb.WriteString(symbolStyle.Render(string(r)))
		} else {
			sb.WriteString(dotStyle.Render(string(r)))
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

	if m.Mode == ViewModelPicker {
		leftSection := lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("models")
		status := lipgloss.NewStyle().Foreground(subtextColor).Render("configure agent models")
		hasToast := m.ToastMessage != "" && time.Now().UnixMilli() < m.ToastExpireTime
		if hasToast {
			if m.ToastWarning {
				status = statusWarningStyle.Render(m.ToastMessage)
			} else {
				status = statusSuccessStyle.Render(m.ToastMessage)
			}
		}

		rightSection := lipgloss.NewStyle().Foreground(subtextColor).Render("enter save · esc cancel")

		usableW := w - 2
		leftWidth := lipgloss.Width(leftSection)
		rightWidth := lipgloss.Width(rightSection)
		statusWidth := lipgloss.Width(status)

		spaceWidth := usableW - leftWidth - rightWidth - statusWidth - 2
		if spaceWidth < 0 {
			spaceWidth = 0
		}
		space := strings.Repeat(" ", spaceWidth)

		parts := []string{leftSection, "  ", status, space, rightSection}
		content := lipgloss.JoinHorizontal(lipgloss.Left, parts...)
		return statusBarBaseStyle.Width(w).Render(" " + content + " ")
	}

	s := m.GetAgentState(m.Focused.ID())

	var leftItems []string

	// State (far left)
	var statePart string
	statusText := s.StatusText
	switch s.State {
	case StateThinking:
		statePart = m.renderScannerTrack("✦", primaryColor)
	case StateStreaming:
		statePart = m.renderMinimalEqualizer()
	case StateConfirmTool:
		statePart = m.renderIdleEqualizer()
		statusText = "authorize execution (y/s/p/g/n)"
	default:
		statePart = m.renderIdleEqualizer()
	}
	leftItems = append(leftItems, statePart)

	// Branch or CWD (whisper-muted, unobtrusive context)
	if m.ShowCWD {
		if m.GitBranch != "" {
			branchPart := lipgloss.NewStyle().Foreground(mutedTextColor).Render(m.GitBranch)
			leftItems = append(leftItems, branchPart)
		} else if m.CWD != "" {
			display := filepath.Base(m.CWD)
			if display == "/" || display == "." {
				display = m.CWD
			}
			repoPart := lipgloss.NewStyle().Foreground(mutedTextColor).Render(display)
			leftItems = append(leftItems, repoPart)
		}
	}

	leftSection := strings.Join(leftItems, statusDivider)

	// Center: toast or active action text
	var status string
	hasToast := m.ToastMessage != "" && time.Now().UnixMilli() < m.ToastExpireTime
	if hasToast {
		if m.ToastWarning {
			status = statusWarningStyle.Render(m.ToastMessage)
		} else {
			status = statusSuccessStyle.Render(m.ToastMessage)
		}
	} else if statusText != "" && statusText != "Working..." && statusText != "Ready" && statusText != "Closed" {
		if s.State == StateConfirmTool {
			status = statusWarningStyle.Render(statusText)
		} else {
			status = lipgloss.NewStyle().Foreground(subtextColor).Italic(true).Render(statusText)
		}
	}

	// Check if any other agent is waiting for confirmation
	otherWaiting := false
	for id, state := range m.AgentStates {
		if id != m.Focused.ID() && state.State == StateConfirmTool {
			otherWaiting = true
			break
		}
	}
	if otherWaiting {
		warn := statusWarningStyle.Render("subagent confirm required")
		if status != "" {
			status += " · " + warn
		} else {
			status = warn
		}
	}

	// Right: Attachments, Context, Breadcrumbs, Help
	var rightItems []string
	if len(m.AttachedFiles) > 0 {
		rightItems = append(rightItems, statusAttachedStyle.Render(fmt.Sprintf("%d files", len(m.AttachedFiles))))
	}

	maxTokens := m.Focused.MaxTokens()
	if s.CumulativeTokenCount > 0 {
		rightItems = append(rightItems, m.renderContextBar(s.CumulativeTokenCount, maxTokens))
	}

	// Breadcrumbs
	var pathParts []string
	curr := m.Focused
	for curr != nil {
		pathParts = append([]string{breadcrumbAgentStyle.Render(curr.ID())}, pathParts...)
		curr = curr.Parent()
	}
	if len(pathParts) > 1 {
		rightItems = append(rightItems, strings.Join(pathParts, breadcrumbSeparatorStyle.Render(" › ")))
	}

	rightItems = append(rightItems, lipgloss.NewStyle().Foreground(mutedTextColor).Render("ctrl+h help"))
	rightSection := strings.Join(rightItems, statusDivider)

	// Layout spacing
	usableW := w - 2
	if usableW < 1 {
		usableW = 1
	}

	leftWidth := lipgloss.Width(leftSection)
	rightWidth := lipgloss.Width(rightSection)

	spaceWidth := usableW - leftWidth - rightWidth
	if status != "" {
		statusWidth := lipgloss.Width(status)
		if statusWidth+2 > spaceWidth {
			maxStatusW := spaceWidth - 2
			if maxStatusW < 0 {
				maxStatusW = 0
			}
			status = m.truncateWithEllipsis(status, maxStatusW)
			statusWidth = lipgloss.Width(status)
		}
		spaceWidth = spaceWidth - statusWidth - 2
	}
	if spaceWidth < 0 {
		spaceWidth = 0
	}

	space := strings.Repeat(" ", spaceWidth)

	var parts []string
	parts = append(parts, leftSection)
	if status != "" {
		parts = append(parts, "  ", status)
	}
	parts = append(parts, space, rightSection)

	content := lipgloss.JoinHorizontal(lipgloss.Left, parts...)
	return statusBarBaseStyle.Width(w).Render(" " + content + " ")
}

func (m *Model) updateViewport() {
	if m.Focused == nil {
		return
	}

	if m.Mode == ViewModelPicker {
		m.renderModelPickerView()
		return
	}

	if m.Mode == ViewCommitLog {
		m.renderCommitLogView()
		return
	}

	if m.Mode == ViewRewind {
		m.renderRewindView()
		return
	}

	if m.EscConfirmPending {
		s := m.GetAgentState(m.Focused.ID())
		busy := s.State == StateThinking || s.State == StateStreaming || s.State == StateStopping

		var prompt string
		if busy {
			prompt = "**Stop active agent?**\n\nThe agent is currently executing. Stopping will immediately halt tool execution and streaming.\n\n> Press **[y]** Yes, stop it  ·  **[n]** No, continue"
		} else {
			prompt = "**Exit Late?**\n\nAre you sure you want to exit the session?\n\n> Press **[y]** Yes, quit  ·  **[n]** No, stay"
		}
		md, _ := m.Renderer.Render(prompt)
		dialog := modalBoxStyle.
			MarginLeft(0).
			BorderForeground(warnBorderColor).
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

### Navigation & Focus
  **tab**             Switch active agent / subagent tab
  **shift+home/end**  Scroll to top / bottom of chat history
  **↑ / ↓**           Browse prompt input history

### Prompt & Composition
  **enter**           Submit prompt
  **alt+enter**       Insert newline into prompt
  **/compose**        Draft prompt in external $EDITOR

### Attachments & Tools
  **ctrl+o**          Toggle file / image attachment picker
  **ctrl+x**          Clear attached files
  **ctrl+g** / **esc** Interrupt / stop active agent

### Slash Commands
  **/model**          Configure AI models for agents
  **/new**            Start fresh conversation session
  **/log**            Browse git commit history & diffs
  **/rewind**         Time-travel back to previous prompt
  **/quit**           Exit Late

Press **ctrl+h** or **esc** to return to chat.`

		// Symmetric inset: 1 margin on left (via modalBoxStyle.MarginLeft(1)), 1 on right
		outerWidth := m.Viewport.Width() - 2
		if outerWidth < 1 {
			outerWidth = 74
		}
		innerWidth := outerWidth - 6
		if innerWidth < 1 {
			innerWidth = 70
		}
		rendered := m.renderMarkdownBlock(helpText, innerWidth)
		boxed := modalBoxStyle.
			Width(outerWidth).
			Render(rendered)

		m.Viewport.SetContent(boxed)
		return
	}

	history := m.Focused.History()
	msgWidth := m.Viewport.Width()
	if msgWidth < 1 {
		msgWidth = 80
	}

	s := m.GetAgentState(m.Focused.ID())
	s.LastRenderTime = time.Now().UnixMilli()
	streaming := s.State == StateStreaming || s.State == StateThinking

	if s.CachedWidth != m.Viewport.Width() {
		s.RenderedHistory = nil
		s.CachedHistoryLines = nil
		s.CachedHistoryBlocks = nil
		s.CachedHistoryHashes = nil
		s.LastTotalContent = ""
		s.StreamingStyledCache = ""
		s.StreamingChunkCount = 0
		s.CachedWidth = m.Viewport.Width()
	}

	if len(s.CachedHistoryHashes) != len(s.RenderedHistory) {
		s.RenderedHistory = nil
		s.CachedHistoryLines = nil
		s.CachedHistoryBlocks = nil
		s.CachedHistoryHashes = nil
	}

	// Length alone is not a sufficient cache key: rewrites and rollbacks can
	// replace a history entry in place. Avoid this scan on hot streaming frames.
	if !streaming && len(history) == len(s.RenderedHistory) && !historyHashesMatch(history, s.CachedHistoryHashes) {
		s.RenderedHistory = nil
		s.CachedHistoryLines = nil
		s.CachedHistoryBlocks = nil
		s.CachedHistoryHashes = nil
	}

	// If history was reset or messages were removed, clear the cache
	historyCacheChanged := false
	if len(history) < len(s.RenderedHistory) {
		s.RenderedHistory = nil
		s.CachedHistoryLines = nil
		s.CachedHistoryBlocks = nil
		s.CachedHistoryHashes = nil
		historyCacheChanged = true
	}

	// Render only new messages and add to cache
	for i := len(s.RenderedHistory); i < len(history); i++ {
		msg := history[i]
		var rendered string
		switch msg.Role {
		case "user":
			content := strings.TrimRight(msg.Content.UIString(), "\r\n")
			if strings.TrimSpace(content) != "" || len(msg.AttachedFiles) > 0 {
				promptPrefix := promptSymbolStyle.Render("❯ ")
				userText := userMsgStyle.Width(msgWidth - 2).Render(content)
				userBlock := promptPrefix + userText
				if len(msg.AttachedFiles) > 0 {
					var names []string
					for _, f := range msg.AttachedFiles {
						names = append(names, filepath.Base(f))
					}
					userBlock += "\n" + attachmentStyle.Render("  ↳ attached: "+strings.Join(names, ", "))
				}
				rendered = "\n" + userBlock + "\n"
			}
		case "assistant":
			var assistantParts []string
			if msg.ReasoningContent != "" {
				thoughtHeader := thoughtHeaderStyle.Render("· thinking")
				thoughtBody := thinkingStyle.Width(msgWidth - 4).Render(msg.ReasoningContent)
				assistantParts = append(assistantParts, thoughtHeader, thoughtBody)
			}
			if msg.Content.String() != "" {
				innerWidth := m.Viewport.Width() - AIMsgOverhead
				if innerWidth < 1 {
					innerWidth = 1
				}
				md := m.renderMarkdownBlock(msg.Content.String(), innerWidth)
				assistantParts = append(assistantParts, md)
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
				assistantParts = append(assistantParts, m.renderToolBadge(tc.Function.Name, callStr, false, msgWidth))
			}
			rendered = strings.Join(assistantParts, "\n")
		}
		// We always append to keep cache in sync with history length
		s.RenderedHistory = append(s.RenderedHistory, rendered)
		s.CachedHistoryHashes = append(s.CachedHistoryHashes, chatMessageHash(msg))
		historyCacheChanged = true
	}

	// Rebuild completed-history line and copy metadata only when history
	// changes. Streaming frames must not walk the entire completed chat.
	if historyCacheChanged {
		var historyBlocks []string
		var historyRenderBlocks []RenderBlock
		currentLine := 0
		for idx, r := range s.RenderedHistory {
			if r == "" {
				continue
			}
			historyBlocks = append(historyBlocks, r)
			linesCount := strings.Count(r, "\n") + 1
			copyText := history[idx].Content.String()
			if history[idx].Role == "user" {
				copyText = history[idx].Content.UIString()
			}
			historyRenderBlocks = append(historyRenderBlocks, RenderBlock{
				MessageIndex: idx,
				Content:      copyText,
				StartLine:    currentLine,
				EndLine:      currentLine + linesCount - 1,
			})
			currentLine += linesCount
		}
		if len(historyBlocks) == 0 {
			s.CachedHistoryLines = nil
		} else {
			s.CachedHistoryLines = strings.Split(strings.Join(historyBlocks, "\n"), "\n")
		}
		s.CachedHistoryBlocks = historyRenderBlocks
	}

	focusChanged := m.LastFocusedID != m.Focused.ID()
	if streaming && !focusChanged && !s.StreamingWindow && !m.Viewport.AtBottom() {
		// The user is reading older history. Keep that viewport stable and
		// avoid rebuilding the full chat for every token. Streaming state
		// continues to accumulate and catches up when they return to bottom.
		return
	}

	// During streaming, keep only a few screens of completed history in the
	// viewport. bubbles/viewport scans every supplied line on SetContent, so
	// giving it the full chat on every token makes frame cost grow forever.
	var blocks []string
	s.RenderBlocks = nil
	currentLine := 0
	windowStart := 0
	if streaming && (s.StreamingWindow || m.Viewport.AtBottom()) {
		windowSize := max(m.Viewport.Height()*2, 40)
		windowStart = max(0, len(s.CachedHistoryLines)-windowSize)
		s.StreamingWindow = true
		s.StreamingWindowStart = windowStart
	} else {
		s.StreamingWindow = false
		s.StreamingWindowStart = 0
	}
	historyLines := s.CachedHistoryLines[windowStart:]
	if len(historyLines) > 0 {
		blocks = append(blocks, strings.Join(historyLines, "\n"))
		currentLine = len(historyLines)
	}
	for _, block := range s.CachedHistoryBlocks {
		if block.EndLine < windowStart {
			continue
		}
		block.StartLine = max(block.StartLine-windowStart, 0)
		block.EndLine -= windowStart
		s.RenderBlocks = append(s.RenderBlocks, block)
	}

	// Render streaming content if active
	// Dedup check: Only render streaming if NOT in an interaction state (where history already has the tools)
	if (s.State == StateStreaming || s.State == StateThinking) && s.State != StateConfirmTool {
		var activeParts []string

		if s.StreamingState.ReasoningContent != "" {
			thoughtHeader := thoughtHeaderStyle.Render("· thinking")
			reasoning := streamingTextWindow(s.StreamingState.ReasoningContent, msgWidth, m.Viewport.Height()*3)
			thoughtBody := thinkingStyle.Width(msgWidth - 4).Render(reasoning)
			activeParts = append(activeParts, thoughtHeader, thoughtBody)
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
				styled := aiMsgStyle.Width(msgWidth).Render(rendered)
				if s.StreamingStyledCache != "" {
					s.StreamingStyledCache += "\n"
				}
				s.StreamingStyledCache += styled
				s.StreamingStyledCache = lastRenderedLines(
					s.StreamingStyledCache,
					max(m.Viewport.Height()*2, 40),
				)
			}
			s.StreamingChunkCount = len(chunks)

			// Render tail as plain text (no glamour — too expensive per frame)
			var tailStyled string
			if tail != "" {
				// Trim leading newlines from tail to prevent "jumping" when a new paragraph starts
				t := strings.TrimLeft(tail, "\n")
				if t != "" {
					t = streamingTextWindow(t, msgWidth, m.Viewport.Height()*2)
					// Caret for streaming effect
					caret := lipgloss.NewStyle().Foreground(primaryColor).Render("█")
					tailStyled = aiMsgStyle.Copy().Foreground(textColor).Width(msgWidth).Render(t + caret)
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
				assembled = lastRenderedLines(assembled, max(m.Viewport.Height()*2, 40))
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
			activeParts = append(activeParts, m.renderToolBadge(tc.Function.Name, callStr, true, msgWidth))
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
			r := m.renderAnimatedTag("· thinking...", thinkingStyle, msgWidth-2, true)
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
		prompt := fmt.Sprintf("**Security Authorization Required**\n\nThe agent requests permission to execute a **%s** command:\n\n```json\n%s\n```\n\n> Press **[y]** Allow once  ·  **[s]** Allow always (session)  ·  **[p]** Allow always (project)  ·  **[g]** Allow always (global)  ·  **[n]** Deny", displayName, tc.Function.Arguments)
		md, _ := m.Renderer.Render(prompt)
		r := aiMsgStyle.Width(msgWidth).MarginLeft(1).Border(boxBorderStyle).BorderForeground(warnBorderColor).Render(md)
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
		prompt := "**Context Limit Warning**\n\nYou are approaching the maximum context size for this session (over 90% used). It is highly recommended to **start a new session** to ensure the agent maintains full context and accuracy.\n\n> Press **[Enter]** again to proceed anyway, or start a new session."
		md, _ := m.Renderer.Render(prompt)
		r := aiMsgStyle.Width(msgWidth).MarginLeft(1).Border(boxBorderStyle).BorderForeground(warnBorderColor).Render(md)
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
			prompt = "**Context Limit Exceeded**\n\nThis session has hit the model's absolute context limit. The agent cannot proceed further in this session.\n\n**Action Required:** Please **start a new session** to continue your work."
			md, _ := m.Renderer.Render(prompt)
			r = aiMsgStyle.Width(msgWidth).MarginLeft(1).Border(boxBorderStyle).BorderForeground(errorBorderColor).Render(md)
		} else {
			prompt = fmt.Sprintf("Error: %v", s.Error)
			r = thinkingStyle.Foreground(errorBorderColor).Render(prompt)
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
		r := thinkingStyle.Foreground(errorBorderColor).Render(prompt)
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

// streamingTextWindow bounds styling work for an incomplete streaming block.
// The complete source remains in StreamingState and is rendered normally when
// the turn finishes.
func streamingTextWindow(content string, width, lines int) string {
	limit := max(width*lines, 4096)
	if len(content) <= limit {
		return content
	}
	start := len(content) - limit
	for start < len(content) && start > 0 && content[start]&0xc0 == 0x80 {
		start++
	}
	return "…\n" + content[start:]
}

// lastRenderedLines returns a suffix without splitting or copying every line
// in a potentially very large ANSI-rendered response.
func lastRenderedLines(content string, count int) string {
	if count <= 0 {
		return ""
	}
	end := len(content)
	for i := 0; i < count; i++ {
		pos := strings.LastIndexByte(content[:end], '\n')
		if pos < 0 {
			return content
		}
		end = pos
	}
	return content[end+1:]
}

func chatMessageHash(msg client.ChatMessage) uint64 {
	h := fnv.New64a()
	b, _ := json.Marshal(msg)
	_, _ = h.Write(b)
	for _, file := range msg.AttachedFiles {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(file))
	}
	return h.Sum64()
}

func historyHashesMatch(history []client.ChatMessage, hashes []uint64) bool {
	if len(history) != len(hashes) {
		return false
	}
	for i, msg := range history {
		if chatMessageHash(msg) != hashes[i] {
			return false
		}
	}
	return true
}

func (m *Model) renderFullStreamingResponse(s *AppState, msgWidth int) string {
	var activeParts []string

	if s.StreamingState.ReasoningContent != "" {
		thoughtHeader := thoughtHeaderStyle.Render("· thinking")
		reasoningWidth := max(msgWidth-4, 1)
		reasoning := ansi.Wordwrap(s.StreamingState.ReasoningContent, reasoningWidth, "")
		activeParts = append(activeParts, thoughtHeader, thinkingStyle.Width(reasoningWidth).Render(reasoning))
	}
	if s.StreamingState.Content != "" {
		innerWidth := max(m.Viewport.Width()-AIMsgOverhead, 1)
		md := m.renderMarkdownBlock(s.StreamingState.Content, innerWidth)
		activeParts = append(activeParts, aiMsgStyle.Width(msgWidth).Render(md))
	}
	for _, tc := range s.StreamingState.ToolCalls {
		callStr := tc.Function.Name
		if registry := m.Focused.Registry(); registry != nil {
			if tool := registry.Get(tc.Function.Name); tool != nil {
				if args := json.RawMessage(tc.Function.Arguments); len(args) > 0 {
					callStr = tool.CallString(args)
				}
			}
		}
		activeParts = append(activeParts, m.renderToolBadge(tc.Function.Name, callStr, true, msgWidth))
	}
	if len(activeParts) == 0 && s.State == StateThinking {
		activeParts = append(activeParts, m.renderAnimatedTag("· thinking...", thinkingStyle, msgWidth-2, true))
	}
	return strings.Join(activeParts, "\n")
}

func (m *Model) restoreFullHistoryForScroll() {
	s := m.GetAgentState(m.Focused.ID())
	if !s.StreamingWindow {
		return
	}

	// This is an explicit, infrequent user action, so render the full active
	// response once. The hot streaming path remains bounded to recent lines.
	msgWidth := max(m.Viewport.Width(), 1)
	parts := make([]string, 0, 2)
	if len(s.CachedHistoryLines) > 0 {
		parts = append(parts, strings.Join(s.CachedHistoryLines, "\n"))
	}
	active := m.renderFullStreamingResponse(s, msgWidth)
	if active != "" {
		parts = append(parts, active)
	}
	fullContent := strings.Join(parts, "\n")
	padded := lipgloss.NewStyle().
		Width(m.Viewport.Width()).
		Background(appBgColor).
		Render(fullContent)
	m.Viewport.SetContent(padded)
	m.Viewport.GotoBottom()

	// Completed blocks regain their full-history coordinates. Rebuild the
	// active block from the unbounded rendering rather than translating its
	// previously truncated window coordinates.
	fullRenderBlocks := append([]RenderBlock(nil), s.CachedHistoryBlocks...)
	if active != "" {
		startLine := len(s.CachedHistoryLines)
		fullRenderBlocks = append(fullRenderBlocks, RenderBlock{
			MessageIndex: -1,
			Content:      s.StreamingState.Content,
			StartLine:    startLine,
			EndLine:      startLine + strings.Count(active, "\n"),
		})
	}
	s.RenderBlocks = fullRenderBlocks
	s.StreamingWindow = false
	s.StreamingWindowStart = 0
	s.LastTotalContent = ""
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

func (m *Model) renderToolBadge(toolName, callStr string, isStreaming bool, width int) string {
	var icon string
	lower := strings.ToLower(toolName + " " + callStr)
	switch {
	case strings.Contains(lower, "bash") || strings.Contains(lower, "powershell") || strings.Contains(lower, "shell"):
		icon = "$"
	case strings.Contains(lower, "write") || strings.Contains(lower, "edit") || strings.Contains(lower, "replace"):
		icon = "edit"
	case strings.Contains(lower, "read") || strings.Contains(lower, "view") || strings.Contains(lower, "cat"):
		icon = "read"
	case strings.Contains(lower, "find") || strings.Contains(lower, "grep") || strings.Contains(lower, "search"):
		icon = "find"
	case strings.Contains(lower, "subagent") || strings.Contains(lower, "spawn") || strings.Contains(lower, "agent"):
		icon = "agent"
	case strings.Contains(lower, "git"):
		icon = "git"
	default:
		icon = "call"
	}

	badgeStyle := tagStyle.Copy().Foreground(subtextColor)

	if isStreaming {
		text := fmt.Sprintf("  ↳ %s: %s · running", icon, callStr)
		return m.renderAnimatedTag(text, badgeStyle, width, true)
	}

	text := fmt.Sprintf("  ↳ %s: %s", icon, callStr)
	if lipgloss.Width(text) > width {
		text = m.truncateWithEllipsis(text, width)
	}
	return badgeStyle.Copy().Width(width).Render(text)
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
	w := m.Viewport.Width()
	if w < 1 {
		w = 80
	}

	// 1. Brandmark Header
	var banner string
	if w >= 60 {
		l1 := lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("  █    ████▄ ▀█████ █▀ █████")
		l2 := lipgloss.NewStyle().Foreground(primaryGlow).Bold(true).Render("  █    █▄▄█▄   ██   ██ █▄▄  ")
		l3 := lipgloss.NewStyle().Foreground(secondaryColor).Bold(true).Render("  ████ █   █   ██   ██ █████")
		banner = l1 + "\n" + l2 + "\n" + l3
	} else {
		banner = lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("  L A T E")
	}

	tagline := lipgloss.NewStyle().Foreground(subtextColor).Render("  Autonomous Agentic Pair-Programmer") +
		lipgloss.NewStyle().Foreground(mutedTextColor).Render(" · v"+common.Version)

	// 2. System Status Badges
	modelName := m.ModelName
	if modelName == "" {
		modelName = "default"
	}
	modelPill := telemetryChipStyle.Render(
		telemetryLabelStyle.Render("model: ") +
			telemetryValueStyle.Render(modelName),
	)

	maxTokens := m.Focused.MaxTokens()
	var ctxStr string
	if maxTokens > 0 {
		ctxStr = fmt.Sprintf("%s tokens", m.formatTokenCount(maxTokens))
	} else if maxTokens == 0 {
		ctxStr = "unlimited"
	} else {
		ctxStr = "auto"
	}
	ctxPill := telemetryChipStyle.Render(
		telemetryLabelStyle.Render("context: ") +
			telemetryValueStyle.Render(ctxStr),
	)

	cwd := m.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwdBase := filepath.Base(cwd)
	gitInfo := ""
	if m.GitBranch != "" {
		gitInfo = lipgloss.NewStyle().Foreground(secondaryColor).Background(chipBgColor).Render(" " + m.GitBranch)
	}
	repoPill := telemetryChipStyle.Render(
		telemetryLabelStyle.Render("repo: ") +
			telemetryValueStyle.Render(cwdBase) + gitInfo,
	)

	var subagentsPill string
	if m.SubagentInfo != "" {
		subagentsPill = telemetryChipStyle.Render(
			telemetryLabelStyle.Render("subagents: ") +
				telemetryValueStyle.Render(m.SubagentInfo),
		)
	}

	var telemetryRow string
	if w >= 80 {
		pills := []string{modelPill, "  ", ctxPill, "  ", repoPill}
		if subagentsPill != "" {
			pills = append(pills, "  ", subagentsPill)
		}
		telemetryRow = "  " + lipgloss.JoinHorizontal(lipgloss.Left, pills...)
	} else {
		telemetryRow = "  " + lipgloss.JoinHorizontal(lipgloss.Left, modelPill, "  ", ctxPill) + "\n  " + repoPill
		if subagentsPill != "" {
			telemetryRow += "  " + subagentsPill
		}
	}

	// 3. Quick Actions Command Matrix
	cardWidth := min(w-4, 68)
	if cardWidth < 36 {
		cardWidth = 36
	}

	headerStyle := lipgloss.NewStyle().Foreground(primaryColor).Bold(true)
	keyStyle := lipgloss.NewStyle().Foreground(secondaryColor).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(subtextColor)

	quickCard := lipgloss.NewStyle().
		Border(boxBorderStyle).
		BorderForeground(cardBorderColor).
		MarginLeft(2).
		Padding(0, 2).
		Width(cardWidth).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			headerStyle.Render("Essential Commands"),
			"",
			keyStyle.Render(" /model    ") + descStyle.Render("Select AI models for agents"),
			keyStyle.Render(" /new      ") + descStyle.Render("Start fresh conversation"),
			keyStyle.Render(" /log      ") + descStyle.Render("Browse git commit log & diffs"),
			keyStyle.Render(" /rewind   ") + descStyle.Render("Time-travel back to any prompt"),
			keyStyle.Render(" /compose  ") + descStyle.Render("Draft prompt in external $EDITOR"),
			keyStyle.Render(" ctrl+o    ") + descStyle.Render("Attach files or images"),
			keyStyle.Render(" ctrl+h    ") + descStyle.Render("Full keyboard shortcut reference"),
		))

	promptHint := lipgloss.NewStyle().Foreground(subtextColor).Italic(true).Render(
		"  Type a prompt below to get started, or '/' for all commands.",
	)

	body := lipgloss.JoinVertical(lipgloss.Left,
		"",
		banner,
		tagline,
		"",
		telemetryRow,
		"",
		quickCard,
		"",
		promptHint,
	)

	return lipgloss.NewStyle().
		Padding(1, 1).
		Width(m.Viewport.Width()).
		Background(appBgColor).
		Render(body)
}

// renderCommitLogView renders the commit history or commit detail in the viewport.
func (m *Model) renderCommitLogView() {
	s := m.GetAgentState(m.Focused.ID())
	s.LastTotalContent = ""

	outerWidth := m.Viewport.Width() - 2
	if outerWidth < 1 {
		outerWidth = 80
	}

	if m.CommitDetail != "" {
		// Show full commit detail — render through glamour for syntax highlighting
		innerWidth := outerWidth - 6
		if innerWidth < 1 {
			innerWidth = 74
		}
		detail := "```\n" + m.CommitDetail + "\n```"
		rendered := m.renderMarkdownBlock(detail, innerWidth)
		boxed := modalBoxStyle.
			Width(outerWidth).
			Render(rendered)
		m.Viewport.SetContent(boxed)
		return
	}

	// Build commit list
	var lines []string
	header := viewHeaderStyle.Render("── Commit History ──────────────────────────────────")
	lines = append(lines, header, "")

	if len(m.CommitEntries) == 0 {
		lines = append(lines, viewEmptyStyle.Render("No commits found."))
	} else {
		for i, entry := range m.CommitEntries {
			prefix := "  "
			itemStyle := lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor).
				PaddingLeft(2)
			hashStyle := commitHashChipStyle
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
					Background(userMsgBg).
					PaddingLeft(2).
					Bold(true)
				hashStyle = commitSelectedChipStyle
				dateStyle = lipgloss.NewStyle().
					Foreground(primaryColor).
					Background(userMsgBg).
					Italic(true)
				msgStyle = lipgloss.NewStyle().
					Foreground(textColor).
					Background(userMsgBg).
					Bold(true)
			}

			headMarker := ""
			if entry.IsHEAD {
				headMarker = " " + headBadgeStyle.Render("HEAD")
				if i == m.CommitIndex {
					headMarker = " " + headBadgeStyle.Copy().Background(primaryColor).Render("HEAD")
				}
			}

			hashStr := hashStyle.Render(entry.Hash)
			dateStr := dateStyle.Render(entry.Date)
			msgStr := msgStyle.Render(entry.Message)

			line := prefix + hashStr + " " + msgStr + headMarker
			metaLine := "    " + dateStyle.Render(entry.Author) + " · " + dateStr

			lines = append(lines, itemStyle.Render(line))
			if i == m.CommitIndex {
				lines = append(lines, itemStyle.Render(metaLine))
			} else {
				lines = append(lines, "    "+dateStr)
			}
			lines = append(lines, "")
		}
	}

	// Footer hint
	footer := viewFooterStyle.Render(fmt.Sprintf("↑↓ navigate · Enter view · Esc back  (%d commits)", len(m.CommitEntries)))
	lines = append(lines, "", footer)

	m.Viewport.SetContent(strings.Join(lines, "\n"))
}

// renderRewindView renders the user message history for rewinding.
func (m *Model) renderRewindView() {
	s := m.GetAgentState(m.Focused.ID())
	s.LastTotalContent = ""

	msgWidth := m.Viewport.Width() - 2
	if msgWidth < 1 {
		msgWidth = 80
	}

	var lines []string
	header := viewHeaderStyle.Render("── Rewind Conversation (Time-Travel) ───────────────")
	lines = append(lines, header, "")

	if len(m.RewindEntries) == 0 {
		lines = append(lines, viewEmptyStyle.Render("No user messages found to rewind to."))
	} else {
		for i, entry := range m.RewindEntries {
			prefix := "  ○ "
			itemStyle := lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor).
				PaddingLeft(2)
			msgStyle := lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor)

			if i == m.RewindIndex {
				prefix = "▸ ● "
				itemStyle = lipgloss.NewStyle().
					Foreground(primaryColor).
					Background(userMsgBg).
					PaddingLeft(2).
					Bold(true)
				msgStyle = lipgloss.NewStyle().
					Foreground(textColor).
					Background(userMsgBg).
					Bold(true)
			}

			// Clean/truncate message content for list preview
			displayMsg := entry.Content
			// Replace newlines with spaces for single-line display in list
			displayMsg = strings.ReplaceAll(displayMsg, "\n", " ")
			maxMsgW := msgWidth - 8
			if maxMsgW < 20 {
				maxMsgW = 20
			}
			if len(displayMsg) > maxMsgW {
				displayMsg = displayMsg[:maxMsgW-3] + "..."
			}

			msgStr := msgStyle.Render(displayMsg)
			line := prefix + msgStr

			lines = append(lines, itemStyle.Render(line))
			lines = append(lines, "")
		}
	}

	// Footer hint
	footer := viewFooterStyle.Render(fmt.Sprintf("↑↓ choose message · Enter rewind here · Esc cancel  (%d messages)", len(m.RewindEntries)))
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

// renderModelPickerView renders the active agent models configuring list in the viewport.
func (m *Model) renderModelPickerView() {
	s := m.GetAgentState(m.Focused.ID())
	s.LastTotalContent = ""

	msgWidth := m.Viewport.Width() - 2
	if msgWidth < 1 {
		msgWidth = 80
	}

	var lines []string
	header := viewHeaderStyle.Render("── Configure Agent Models ──────────────────────────")
	lines = append(lines, header, "")

	if len(m.ModelPickerModels) <= 1 && (m.AppConfig == nil || len(m.AppConfig.Models) == 0) {
		lines = append(lines, viewEmptyStyle.Copy().Foreground(warnBorderColor).Render("No models configured in ~/.config/late/config.json"))
		lines = append(lines, "", viewEmptyStyle.Render("Please add a 'models' array to your config file first."))
	} else {
		// Instructions
		lines = append(lines, viewEmptyStyle.Render("Use ↑/↓ to choose an agent, and ←/→ to select a model."), "")

		// Print agents and their models
		for aIdx, agentName := range m.ModelPickerAgents {
			agentLabel := agentName
			if agentName == "orchestrator" {
				agentLabel = "main/orchestrator"
			}

			// Highlight the active row/agent
			agentStyle := lipgloss.NewStyle().Foreground(textColor)
			prefix := "  "
			if aIdx == m.ModelPickerAgentIndex {
				prefix = "▸ "
				agentStyle = lipgloss.NewStyle().Foreground(primaryColor).Bold(true)
			}

			// Render agent name, right-padded
			agentNamePart := prefix + agentLabel
			agentNameStr := fmt.Sprintf("%-22s", agentNamePart)
			agentNameRendered := agentStyle.Background(appBgColor).Render(agentNameStr)

			// Build the model choices list for this agent
			var modelChoices []string
			selectedIdx := m.ModelPickerAgentSelections[agentName]

			for mIdx, modelRef := range m.ModelPickerModels {
				modelLabel := modelRef
				if modelRef != "default" && m.AppConfig != nil {
					for _, setting := range m.AppConfig.Models {
						if setting.Reference() == modelRef {
							modelLabel = setting.Model
							if setting.ID != "" {
								modelLabel += " (" + setting.ID + ")"
							}
							break
						}
					}
				}

				var optStr string
				if mIdx == selectedIdx {
					// This option is selected
					if aIdx == m.ModelPickerAgentIndex {
						// Row is active: highlight selected model with primary color
						optStr = lipgloss.NewStyle().
							Foreground(appBgColor).
							Background(primaryColor).
							Bold(true).
							Padding(0, 1).
							Render("✦ " + modelLabel)
					} else {
						// Row is inactive: highlight selected model with secondary color
						optStr = lipgloss.NewStyle().
							Foreground(appBgColor).
							Background(secondaryColor).
							Bold(true).
							Padding(0, 1).
							Render(modelLabel)
					}
				} else {
					// Not selected
					optStr = modelPickerChipStyle.Render(modelLabel)
				}
				modelChoices = append(modelChoices, optStr)
			}

			rowContent := agentNameRendered + strings.Join(modelChoices, "  ")

			// Wrap row in a box style if it's active for extra pop
			rowStyle := lipgloss.NewStyle().Background(appBgColor).Padding(0, 1)
			if aIdx == m.ModelPickerAgentIndex {
				rowStyle = lipgloss.NewStyle().Background(userMsgBg).Padding(0, 1)
			}

			lines = append(lines, rowStyle.Render(rowContent))
		}
	}

	lines = append(lines, "", "")

	// Footer hints
	footer := viewFooterStyle.Render("[Enter] Save & Apply  ·  [Esc] Cancel  ·  [↑/↓] Select Agent  ·  [←/→] Choose Model")
	lines = append(lines, footer)

	m.Viewport.SetContent(strings.Join(lines, "\n"))
}
