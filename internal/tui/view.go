package tui

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"late/internal/common"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) View() tea.View {
	if m.screenReady {
		return m.cachedScreen
	}
	return m.buildScreen()
}

func (m Model) buildScreen() tea.View {
	if m.Width == 0 || m.Height == 0 {
		return tea.NewView("")
	}

	// Force each component to its strict allocated height to prevent layout shifts
	var vStr string
	if m.Mode == ViewChat && !m.EscConfirmPending && !m.ShowFilePicker {
		vStr = m.transcriptView()
	} else {
		vStr = m.Viewport.View()
	}

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
	content := vStr
	if aStr != "" {
		content += "\n" + aStr
	}
	content += "\n" + iStr + "\n" + sStr

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
	var textareaView string
	showPluginAction := m.RunningPluginAction != "" &&
		(m.RunningPluginActionVisibleAfter.IsZero() || !time.Now().Before(m.RunningPluginActionVisibleAfter))
	if showPluginAction {
		dots := []string{".", "..", "..."}[(time.Now().UnixMilli()/350)%3]
		ghostText := fmt.Sprintf("❯ Running %s%s", m.RunningPluginAction, dots)
		maxW := m.Width - 4
		if maxW > 0 && len(ghostText) > maxW {
			ghostText = ghostText[:maxW-3] + "..."
		}
		ghostStyle := lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			Italic(true)
		textareaView = ghostStyle.Render(ghostText)
	} else {
		textareaView = m.Input.View()
	}
	paddedTextarea := lipgloss.NewStyle().Padding(0, 1).Background(appBgColor).Render(textareaView)

	outerStyle := baseStyle.Copy().
		Width(m.Width).
		AlignVertical(lipgloss.Bottom).
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(borderColor).
		BorderBackground(appBgColor).
		MarginBackground(appBgColor)

	s := m.GetAgentState(m.Focused.ID())
	if s.State == StateThinking || s.State == StateStreaming || showPluginAction {
		outerStyle = outerStyle.BorderForeground(activeBorder)
	} else if s.State == StateConfirmTool {
		outerStyle = outerStyle.BorderForeground(warnBorderColor)
	}

	return outerStyle.Render(paddedTextarea)
}

const maxAutocompleteVisible = 6

// autocompleteHeight calculates the exact vertical line count needed by autocompleteView.
// Returns 0 when autocomplete is closed.
func (m *Model) autocompleteHeight() int {
	if !m.ShowAutocomplete || len(m.AutocompleteItems) == 0 {
		return 0
	}
	visible := min(len(m.AutocompleteItems), maxAutocompleteVisible)
	return visible + 1 // visible items + 1 line top border
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

	totalItems := len(m.AutocompleteItems)
	visibleCount := min(totalItems, maxAutocompleteVisible)

	// Keep AutocompleteOffset smoothly in bounds of visible window
	if totalItems <= maxAutocompleteVisible {
		m.AutocompleteOffset = 0
	} else {
		if m.AutocompleteIndex < m.AutocompleteOffset {
			m.AutocompleteOffset = m.AutocompleteIndex
		} else if m.AutocompleteIndex >= m.AutocompleteOffset+maxAutocompleteVisible {
			m.AutocompleteOffset = m.AutocompleteIndex - maxAutocompleteVisible + 1
		}
		if m.AutocompleteOffset+maxAutocompleteVisible > totalItems {
			m.AutocompleteOffset = totalItems - maxAutocompleteVisible
		}
		if m.AutocompleteOffset < 0 {
			m.AutocompleteOffset = 0
		}
	}

	start := m.AutocompleteOffset
	end := start + visibleCount

	var lines []string
	for i := start; i < end; i++ {
		item := m.AutocompleteItems[i]
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

	autoH := m.autocompleteHeight()
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(secondaryColor).
		BorderBackground(cardBgColor).
		Background(cardBgColor).
		Width(w).
		Height(autoH).
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
	return m.renderMinimalEqualizerAt(time.Now())
}

func (m *Model) renderMinimalEqualizerAt(now time.Time) string {
	t := float64(now.UnixMilli()) / 220.0
	bars := []rune(" ▂▃▄▅▆▇█") // 9 height levels
	numBars := len(bars)

	var cols [5]rune
	var indices [5]int
	for i := 0; i < 5; i++ {
		// Single cohesive traveling wave with graceful spatial flow
		w1 := math.Sin(t*1.5 - float64(i)*0.85)

		// Gentle incommensurate harmonic (golden ratio 1.618) creates organic, non-repeating crests
		// Low amplitude ensures it never causes erratic snap or jitter
		w2 := 0.35 * math.Sin(t*0.93 + float64(i)*0.55 + 1.2)

		// Breathing envelope gives gentle natural cadence
		swell := 0.88 + 0.20*math.Sin(t*0.38+float64(i)*0.25)

		combined := (w1 + w2) * swell

		// Smooth normalization to [0, 1]
		norm := (combined + 1.45) / 2.90
		if norm < 0.0 {
			norm = 0.0
		}
		if norm > 1.0 {
			norm = 1.0
		}

		// Smoothstep contrast curve: brings out deep troughs and crests without jumpiness
		val := norm * norm * (3.0 - 2.0*norm)

		idx := int(math.Round(val * float64(numBars-1)))
		if idx < 0 {
			idx = 0
		}
		if idx >= numBars {
			idx = numBars - 1
		}
		indices[i] = idx
		cols[i] = bars[idx]
	}

	bracketStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	equalizerStyle := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor)
	peakStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")).Bold(true).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for i, col := range cols {
		if indices[i] >= 6 {
			sb.WriteString(peakStyle.Render(string(col)))
		} else {
			sb.WriteString(equalizerStyle.Render(string(col)))
		}
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
	return m.renderScannerTrackAt(symbol, symbolColor, time.Now())
}

func (m *Model) renderScannerTrackAt(symbol string, symbolColor color.Color, now time.Time) string {
	// Base cruising timing: 250ms divisor gives a smooth 1.57s round-trip
	t := float64(now.UnixMilli()) / 250.0
	p := 2.0 + 2.0*math.Sin(t)
	v := math.Cos(t) // velocity

	headIdx := int(math.Round(p))
	if headIdx < 0 {
		headIdx = 0
	}
	if headIdx > 4 {
		headIdx = 4
	}

	frac := p - float64(headIdx) // sub-cell offset (-0.5 to +0.5)

	var runes [5]rune
	for i := 0; i < 5; i++ {
		runes[i] = '·'
	}
	runes[headIdx] = []rune(symbol)[0]

	if v > 0.12 { // Moving RIGHT
		// Optical wake to the left
		if headIdx > 0 {
			if frac < 0.15 {
				runes[headIdx-1] = '✧'
			} else {
				runes[headIdx-1] = '•'
			}
		}
		// Leading aura to the right (cell starts warming up before arrival)
		if headIdx < 4 && frac > 0.18 {
			runes[headIdx+1] = '•'
		}
	} else if v < -0.12 { // Moving LEFT
		// Optical wake to the right
		if headIdx < 4 {
			if frac > -0.15 {
				runes[headIdx+1] = '✧'
			} else {
				runes[headIdx+1] = '•'
			}
		}
		// Leading aura to the left
		if headIdx > 0 && frac < -0.18 {
			runes[headIdx-1] = '•'
		}
	} else {
		// Turnaround deceleration: the wake smoothly catches up to the head
		if headIdx == 4 {
			runes[3] = '•'
		} else if headIdx == 0 {
			runes[1] = '•'
		}
	}

	bracketStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	headStyle := lipgloss.NewStyle().Foreground(symbolColor).Bold(true).Background(appBgColor)
	trailSparkStyle := lipgloss.NewStyle().Foreground(symbolColor).Background(appBgColor)
	auraBulletStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#C48D46")).Background(appBgColor)
	dotStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)

	var sb strings.Builder
	sb.WriteString(bracketStyle.Render("["))
	for i, r := range runes {
		if i == headIdx {
			sb.WriteString(headStyle.Render(string(r)))
		} else if r == '✧' {
			sb.WriteString(trailSparkStyle.Render(string(r)))
		} else if r == '•' {
			sb.WriteString(auraBulletStyle.Render(string(r)))
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
		if m.BootstrapStatus != "" {
			status = lipgloss.NewStyle().Foreground(subtextColor).Italic(true).Render(m.BootstrapStatus)
		} else if hasToast {
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
		if m.BootstrapStatus != "" {
			statePart = m.renderScannerTrack("✦", primaryColor)
		} else {
			statePart = m.renderIdleEqualizer()
		}
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
	if m.BootstrapStatus != "" {
		status = lipgloss.NewStyle().Foreground(subtextColor).Italic(true).Render(m.BootstrapStatus)
	} else if hasToast {
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

	if m.Mode == ViewThemes {
		m.renderThemeView()
		return
	}

	if m.EscConfirmPending {
		s := m.GetAgentState(m.Focused.ID())
		busy := s.State == StateThinking || s.State == StateStreaming || s.State == StateStopping

		var prompt string
		if busy {
			prompt = "**Stop active agent?**\n\nThe agent is currently executing. Stopping will immediately halt tool execution and streaming.\n\nPress **[y]** Yes, stop it  ·  **[n]** No, continue"
		} else {
			prompt = "**Exit Late?**\n\nAre you sure you want to exit the session?\n\nPress **[y]** Yes, quit  ·  **[n]** No, stay"
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

		// Build help text dynamically to include plugin commands
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
  **/help**           Show help and shortcuts
  **/log**            Browse git commit history & diffs
  **/model**          Configure AI models for agents
  **/new**            Start fresh conversation session
  **/quit**           Exit Late
  **/rewind**         Time-travel back to previous prompt
  **/themes**         List and switch themes
`

		// Plugin-provided slash commands
		if len(m.PluginCommands) > 0 {
			helpText += "\n### Plugin Commands\n"
			for _, cmd := range m.PluginCommands {
				helpText += fmt.Sprintf("  **%s**\n", cmd)
			}
		}

		helpText += `
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

	m.refreshTranscript()
}

func (m *Model) renderAnimatedTagAt(text string, baseStyle lipgloss.Style, width int, active bool, now time.Time) string {
	textWidth := lipgloss.Width(text)

	isTruncated := textWidth > width
	shouldAnimate := active

	if !shouldAnimate {
		if isTruncated {
			text = m.truncateWithEllipsis(text, width)
		}
		return baseStyle.Copy().Width(width).Render(text)
	}

	// Use millisecond timestamp for smooth movement
	ms := float64(now.UnixNano()) / 1e6

	// Use width instead of textWidth for truncated tags to prevent violent shifting
	// when characters are appended during streaming. For small tags (Thinking, etc),
	// use the actual text width so the animation doesn't feel too slow.
	period := float64(textWidth)
	if isTruncated {
		text = m.truncateWithEllipsis(text, width)
		period = float64(width)
	}

	// Get base and shine colors from the provided style if possible
	fg := baseStyle.GetForeground()
	bg := baseStyle.GetBackground()

	// If background is unset, use the app background to prevent leakage
	if bg == (lipgloss.NoColor{}) {
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
	column := 0
	for _, r := range text {
		pos := float64(column)
		column += lipgloss.Width(string(r))
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
		charStyle := baseStyle.Copy().
			Foreground(grad[step]).
			Background(bg).
			UnsetWidth()
		sb.WriteString(charStyle.Render(string(r)))
	}

	return sb.String()
}

func toolBadgeText(toolName, callStr string) string {
	if callStr != "" {
		return callStr
	}
	return toolName
}

func (m *Model) renderToolBadge(toolName, callStr string, isStreaming bool, width int) string {
	label := toolBadgeText(toolName, callStr)
	badgeStyle := tagStyle

	if isStreaming {
		return m.renderActivityAt(label+" · running", width, time.Now())
	}

	text := "  ↳ " + label
	if lipgloss.Width(text) > width {
		text = m.truncateWithEllipsis(text, width)
	}
	return badgeStyle.Copy().Render(text)
}

func (m *Model) truncateWithEllipsis(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 3 {
		return "..."
	}

	limit := w - 3
	res := ""
	currW := 0
	for _, r := range s {
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

// renderWelcomeMessage builds the rich welcome screen shown when history is empty.
func (m *Model) renderWelcomeMessage() string {
	w := m.Viewport.Width()
	if w < 1 {
		w = 80
	}

	// 1. Brandmark Header
	var banner string
	if w >= 60 {
		l1 := lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("  ██      ▄██▄   ██████  ██████")
		l2 := lipgloss.NewStyle().Foreground(primaryGlow).Bold(true).Render("  ██     ██████    ██    ███   ")
		l3 := lipgloss.NewStyle().Foreground(secondaryColor).Bold(true).Render("  █████  ██  ██    ██    ██████")
		banner = l1 + "\n" + l2 + "\n" + l3
	} else {
		banner = lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("  L A T E")
	}

	tagline := lipgloss.NewStyle().Foreground(subtextColor).Render("  Lightweight AI Terminal Environment") +
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
			keyStyle.Render(" /compose  ")+descStyle.Render("Draft prompt in external $EDITOR"),
			keyStyle.Render(" /log      ")+descStyle.Render("Browse git commit log & diffs"),
			keyStyle.Render(" /model    ")+descStyle.Render("Select AI models for agents"),
			keyStyle.Render(" /new      ")+descStyle.Render("Start fresh conversation"),
			keyStyle.Render(" /rewind   ")+descStyle.Render("Time-travel back to any prompt"),
			keyStyle.Render(" ctrl+o    ")+descStyle.Render("Attach files or images"),
			keyStyle.Render(" ctrl+h    ")+descStyle.Render("Full keyboard shortcut reference"),
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
// renderThemeView draws the /themes picker into the viewport. Style
// mirrors the other pickers (ViewCommitLog, ViewRewind): a boxed list with
// a cursor and an "active" marker on the currently applied theme. Empty
// list is handled inline.
func (m *Model) renderThemeView() {
	s := m.GetAgentState(m.Focused.ID())
	s.LastTotalContent = ""

	width := m.Viewport.Width()
	if width < 1 {
		width = 80
	}
	height := m.Viewport.Height()
	if height < 1 {
		height = 20
	}

	if m.ThemeIndex >= len(m.ThemeEntries) {
		m.ThemeIndex = len(m.ThemeEntries) - 1
	}
	if m.ThemeIndex < 0 {
		m.ThemeIndex = 0
	}

	header := lipgloss.NewStyle().
		Foreground(primaryColor).
		Background(appBgColor).
		Bold(true).
		Padding(0, 1).
		Width(width - 8).
		Render("Themes")

	subtitle := lipgloss.NewStyle().
		Foreground(subtextColor).
		Background(appBgColor).
		Padding(0, 1).
		Width(width - 8).
		Render("Select a theme with \u2191/\u2193 and press enter to apply. esc to cancel.")

	if len(m.ThemeEntries) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			Padding(0, 1).
			Width(width - 8).
			Render("No plugin themes installed.")
		box := modalBoxStyle.
			Width(width - 2).
			Render(lipgloss.JoinVertical(lipgloss.Left, header, subtitle, empty))
		paddedContent := lipgloss.NewStyle().
			Width(m.Viewport.Width()).
			Background(appBgColor).
			Render(box)
		m.Viewport.SetContent(paddedContent)
		return
	}

	var rows []string
	for i, t := range m.ThemeEntries {
		isActive := t.ID == m.SelectedTheme || (t.ID == "default" && (m.SelectedTheme == "" || m.SelectedTheme == "default"))
		prefix := "  "
		if i == m.ThemeIndex {
			prefix = "▸ "
		}
		marker := "  "
		if isActive {
			marker = "\u25cf "
		}
		label := fmt.Sprintf("%s%s%s", prefix, marker, t.ThemeName)
		sub := fmt.Sprintf("    %s", t.PluginName)
		if isActive {
			sub += "  \u2022 active"
		}

		var row string
		if i == m.ThemeIndex {
			row = lipgloss.NewStyle().
				Foreground(primaryColor).
				Background(userMsgBg).
				Bold(true).
				Width(width-8).
				Padding(0, 1).
				Render(label) + "\n" +
				lipgloss.NewStyle().
					Foreground(subtextColor).
					Background(userMsgBg).
					Width(width-8).
					Padding(0, 1).
					Render(sub)
		} else {
			row = lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor).
				Width(width-8).
				Padding(0, 1).
				Render(label) + "\n" +
				lipgloss.NewStyle().
					Foreground(subtextColor).
					Background(appBgColor).
					Width(width-8).
					Padding(0, 1).
					Render(sub)
		}
		rows = append(rows, row)
	}

	current := m.ThemeEntries[m.ThemeIndex]
	footer := lipgloss.NewStyle().
		Foreground(subtextColor).
		Background(appBgColor).
		Padding(0, 1).
		Width(width - 8).
		Render(fmt.Sprintf("Selected: %s   (active: %s)",
			current.ThemeName,
			displayThemeNameOrNone(m.SelectedTheme)))

	emptyLine := lipgloss.NewStyle().Background(appBgColor).Width(width - 8).Render("")

	box := modalBoxStyle.
		Width(width - 2).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			header,
			subtitle,
			emptyLine,
			lipgloss.JoinVertical(lipgloss.Left, rows...),
			emptyLine,
			footer,
		))

	maxH := height - 2
	if maxH < 5 {
		maxH = 5
	}
	if boxHeight := lipgloss.Height(box); boxHeight > maxH {
		box = lipgloss.NewStyle().MaxHeight(maxH).Background(appBgColor).Render(box)
	}

	paddedContent := lipgloss.NewStyle().
		Width(m.Viewport.Width()).
		Background(appBgColor).
		Render(box)
	m.Viewport.SetContent(paddedContent)
}

// displayThemeNameOrNone formats the active theme id for the picker
// footer. Empty id or "default" is rendered as default (built-in).
func displayThemeNameOrNone(id string) string {
	if id == "" || id == "default" {
		return "default (built-in)"
	}
	return id
}

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

// renderModelPickerView renders the active agent models configuring list in the viewport.
func (m *Model) renderModelPickerView() {
	s := m.GetAgentState(m.Focused.ID())
	s.LastTotalContent = ""

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

// renderActivityAt is the shared thinking/tool activity row. The marker and
// text use the same clock; only this visible row is repainted on animation ticks.
func (m *Model) renderActivityAt(text string, width int, now time.Time) string {
	text = strings.Join(strings.Fields(text), " ")
	frames := spinner.Dot
	frame := int(now.UnixNano()/int64(frames.FPS)) % len(frames.Frames)
	marker := lipgloss.NewStyle().Foreground(primaryColor).Background(appBgColor).Render(strings.TrimSpace(frames.Frames[frame]))
	fg := subtextColor
	if strings.HasSuffix(text, " · running") {
		fg = primaryColor
	}
	style := lipgloss.NewStyle().Foreground(fg).Background(appBgColor).Italic(true)
	remaining := max(1, width-2-lipgloss.Width(marker)-1)
	glow := m.renderAnimatedTagAt(text, style, remaining, true, now)
	row := "  " + marker + " " + glow
	return ansi.Truncate(row, max(1, width), "")
}
