package tui

import (
	"fmt"
	"late/internal/client"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// FrameRate controls screen assembly and Bubble Tea's terminal refresh rate.
const FrameRate = 120

const transcriptFrameInterval = time.Second / FrameRate

type transcriptFrameMsg struct{}

type transcriptState struct {
	activities   map[int]string
	thinkingLine int
	thinking     bool
	welcome      bool
	rows         []string
	blocks       []RenderBlock
	cache        map[string][]string
	offset       int
	detached     bool
	dirty        bool
	busy         bool
	generation   uint64
	width        int
	theme        string
}

type transcriptRenderedMsg struct {
	activities   map[int]string
	thinkingLine int
	thinking     bool
	partial      bool
	welcome      bool
	id           string
	generation   uint64
	width        int
	theme        string
	rows         []string
	blocks       []RenderBlock
	cache        map[string][]string
}

type transcriptLabel struct {
	rendered string
	activity string
}

type transcriptEntry struct {
	active    bool
	index     int
	role      string
	content   string
	reasoning string
	labels    []transcriptLabel
}

const (
	assistantReplyPadding = 6
)

func transcriptInnerWidth(width int, style lipgloss.Style) int {
	return max(1, width-style.GetHorizontalFrameSize())
}

func assistantReplyStyle(width int) lipgloss.Style {
	return aiMsgStyle.Padding(0, assistantReplyPadding).Width(width)
}

func assistantReplyContentWidth(width int) int {
	return max(1, width-assistantReplyPadding*2)
}

func thoughtBodyStyle(width int) lipgloss.Style {
	return thinkingStyle.Width(transcriptInnerWidth(width, thinkingStyle))
}

func toolCallWidth(width int) int {
	return max(1, width-assistantReplyPadding)
}

func userPromptContentWidth(width int) int {
	return max(1, width-userMsgStyle.GetHorizontalFrameSize()-userMsgStyle.GetHorizontalMargins())
}

func userPromptStyle(width int) lipgloss.Style {
	return userMsgStyle.Width(max(1, width-userMsgStyle.GetHorizontalMargins()))
}

// Invalidate content independently of viewport position. Rendering is started
// by the presentation clock, never by a wheel or paging event.
func (m *Model) refreshTranscript() {
	s := m.GetAgentState(m.Focused.ID())
	s.Transcript.dirty = true
	s.LastRenderTime = time.Now().UnixMilli()
}

func (m *Model) present(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m.screenDirty = true
	if !m.framePending {
		m.framePending = true
		delay := max(time.Duration(0), transcriptFrameInterval-time.Since(m.lastFrame))
		cmd = tea.Batch(cmd, tea.Tick(delay, func(time.Time) tea.Msg { return transcriptFrameMsg{} }))
	}
	return *m, cmd
}

func (m *Model) transcriptFrame() (tea.Model, tea.Cmd) {
	m.framePending = false
	var render tea.Cmd
	if m.Focused != nil && m.Mode == ViewChat && !m.EscConfirmPending && !m.ShowFilePicker {
		render = m.renderTranscriptCmd()
	}
	if m.screenDirty || !m.screenReady {
		m.cachedScreen = m.buildScreen()
		m.screenReady = true
		m.screenDirty = false
		m.lastFrame = time.Now()
	}
	return *m, render
}

func (m *Model) transcriptView() string {
	if m.Focused == nil {
		return ""
	}
	t := &m.GetAgentState(m.Focused.ID()).Transcript
	h := max(m.Viewport.Height(), 1)
	if !t.detached {
		t.offset = max(0, len(t.rows)-h)
	}
	t.offset = min(max(0, t.offset), max(0, len(t.rows)-h))
	end := min(len(t.rows), t.offset+h)
	now := time.Now()
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if t.offset+i < end {
			row := t.rows[t.offset+i]
			if activity, ok := t.activities[t.offset+i]; ok {
				row = m.renderActivityAt(activity, toolCallWidth(m.Viewport.Width()), now)
			}
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m *Model) scrollTranscript(amount int, edge int) {
	t := &m.GetAgentState(m.Focused.ID()).Transcript
	bottom := max(0, len(t.rows)-m.Viewport.Height())
	if !t.detached {
		t.offset = bottom
	}
	switch edge {
	case -1:
		t.offset = 0
		t.detached = true
	case 1:
		t.offset = bottom
		t.detached = false
	default:
		t.offset = min(bottom, max(0, t.offset+amount))
		if amount < 0 {
			t.detached = true
		} else if amount > 0 && t.offset == bottom {
			t.detached = false
		}
	}
}

func (m *Model) applyTranscript(result transcriptRenderedMsg) {
	s := m.GetAgentState(result.id)
	t := &s.Transcript
	if result.generation != t.generation {
		return
	}
	t.busy = false
	styles := m.activeThemeStyles
	if styles == nil {
		styles = LateTheme
	}
	if result.width != m.Viewport.Width() || result.theme != string(styles) {
		t.dirty = true
		return
	}
	// Preserve the first visible passage inside its message when preceding
	// blocks grow or Markdown wrapping changes. The active response keeps its
	// history index when it becomes a completed message.
	if t.detached && len(t.rows) > 0 {
		oldOffset := min(t.offset, len(t.rows)-1)
		for _, old := range t.blocks {
			if oldOffset < old.StartLine || oldOffset > old.EndLine {
				continue
			}
			for _, next := range result.blocks {
				if old.MessageIndex != next.MessageIndex {
					continue
				}
				relative := oldOffset - old.StartLine
				position := min(next.EndLine, next.StartLine+relative)
				needle := strings.TrimSpace(ansi.Strip(t.rows[oldOffset]))
				if needle != "" {
					bestDistance := len(result.rows) + 1
					for i := next.StartLine; i <= next.EndLine && i < len(result.rows); i++ {
						line := strings.TrimSpace(ansi.Strip(result.rows[i]))
						if line == needle {
							distance := i - (next.StartLine + relative)
							if distance < 0 {
								distance = -distance
							}
							if distance < bestDistance {
								position = i
								bestDistance = distance
							}
						}
					}
				}
				t.offset = position
				break
			}
			break
		}
	}
	if t.welcome && !result.welcome {
		t.detached = false
	}
	t.welcome = result.welcome
	t.rows, t.blocks, t.cache = result.rows, result.blocks, result.cache
	t.thinking, t.thinkingLine = result.thinking, result.thinkingLine
	t.activities = result.activities
	t.width, t.theme = result.width, result.theme
	t.offset = min(t.offset, max(0, len(t.rows)-m.Viewport.Height()))
	s.RenderBlocks = result.blocks
	if result.partial {
		t.dirty = true
	}
	if result.welcome {
		t.offset = 0
		t.detached = true
	}
}

func (m *Model) renderTranscriptCmd() tea.Cmd {
	s := m.GetAgentState(m.Focused.ID())
	t := &s.Transcript
	styles := m.activeThemeStyles
	if styles == nil {
		styles = LateTheme
	}
	width := max(1, m.Viewport.Width())
	if t.width != width || t.theme != string(styles) {
		t.dirty = true
	}
	if t.busy || !t.dirty {
		return nil
	}
	t.busy = true
	t.dirty = false
	id, generation := m.Focused.ID(), t.generation
	oldCache := t.cache
	if t.width != width || t.theme != string(styles) {
		oldCache = nil
	}
	theme := string(styles)
	entries := make([]transcriptEntry, 0, len(m.Focused.History())+3)
	toolWidth := toolCallWidth(width)
	toolLabels := func(calls []client.ToolCall, active bool) []transcriptLabel {
		labels := make([]transcriptLabel, 0, len(calls))
		for _, tc := range calls {
			label := tc.Function.Name
			if registry := m.Focused.Registry(); registry != nil {
				if tool := registry.Get(label); tool != nil && len(tc.Function.Arguments) > 0 {
					label = tool.CallString([]byte(tc.Function.Arguments))
				}
			}
			item := transcriptLabel{rendered: m.renderToolBadge(tc.Function.Name, label, false, toolWidth)}
			if active {
				item.activity = toolBadgeText(tc.Function.Name, label) + " · running"
				item.rendered = m.renderActivityAt(item.activity, toolWidth, time.Unix(0, 0))
			}
			labels = append(labels, item)
		}
		return labels
	}
	history := m.Focused.History()
	start := 0
	if m.LazyHistory && t.rows == nil && t.cache == nil {
		start = max(0, len(history)-4)
	}
	partial := start > 0
	hasActiveTool := false
	for i := start; i < len(history); i++ {
		msg := history[i]
		content := msg.Content.String()
		if msg.Role == "user" {
			content = msg.Content.UIString()
		}
		isLatestAssistant := (i == len(history)-1) || (i == len(history)-2 && history[len(history)-1].Role == "tool")
		calls := msg.ToolCalls
		labels := make([]transcriptLabel, 0, len(calls))
		for _, tc := range calls {
			label := tc.Function.Name
			if registry := m.Focused.Registry(); registry != nil {
				if tool := registry.Get(label); tool != nil && len(tc.Function.Arguments) > 0 {
					label = tool.CallString([]byte(tc.Function.Arguments))
				}
			}
			isActive := false
			if isLatestAssistant && (s.State == StateThinking || s.State == StateStreaming) {
				hasResult := false
				for _, h := range history[i+1:] {
					if h.Role == "tool" && h.ToolCallID == tc.ID {
						hasResult = true
						break
					}
				}
				if !hasResult {
					isActive = true
					hasActiveTool = true
				}
			}
			item := transcriptLabel{rendered: m.renderToolBadge(tc.Function.Name, label, false, toolWidth)}
			if isActive {
				item.activity = toolBadgeText(tc.Function.Name, label) + " · running"
				item.rendered = m.renderActivityAt(item.activity, toolWidth, time.Unix(0, 0))
			}
			labels = append(labels, item)
		}
		entry := transcriptEntry{index: i, role: msg.Role, content: content, reasoning: msg.ReasoningContent, labels: labels}
		if len(msg.AttachedFiles) > 0 {
			names := make([]string, len(msg.AttachedFiles))
			for j, f := range msg.AttachedFiles {
				names[j] = filepath.Base(f)
			}
			entry.labels = append(entry.labels, transcriptLabel{rendered: attachmentStyle.Render("  ↳ attached: " + strings.Join(names, ", "))})
		}
		entries = append(entries, entry)
	}
	if (s.State == StateStreaming || s.State == StateThinking) && !s.StreamingState.Completed {
		active := s.StreamingState
		if active.Content != "" || active.ReasoningContent != "" || len(active.ToolCalls) > 0 {
			entries = append(entries, transcriptEntry{active: true, index: len(history), role: "assistant", content: active.Content, reasoning: active.ReasoningContent, labels: toolLabels(active.ToolCalls, true)})
		} else if !hasActiveTool {
			entries = append(entries, transcriptEntry{index: len(history), role: "thinking", content: "thinking..."})
		}
	}
	if s.State == StateConfirmTool && s.PendingConfirm != nil {
		tc := s.PendingConfirm.ToolCall
		name := tc.Function.Name
		if runtime.GOOS == "windows" && name == "bash" {
			name = "PowerShell"
		}
		entries = append(entries, transcriptEntry{index: -1, role: "notice", content: fmt.Sprintf("The agent wants to execute **%s**.\n\n```json\n%s\n```\n\n**[y]** Allow once · **[s]** Session · **[p]** Project · **[g]** Global · **[n]** Deny", name, tc.Function.Arguments)})
	}
	if s.State == StateContextWarning {
		entries = append(entries, transcriptEntry{index: -1, role: "notice", content: "**Context Limit Warning**\n\nOver 90% of the context is used. Press Enter again to proceed, or start a new session."})
	}
	if s.Error != nil {
		entries = append(entries, transcriptEntry{index: -1, role: "error", content: transcriptError(s.Error)})
	} else if m.Err != nil {
		entries = append(entries, transcriptEntry{index: -1, role: "error", content: transcriptError(m.Err)})
	}
	for _, q := range m.Focused.QueuedMessages() {
		entries = append(entries, transcriptEntry{index: -1, role: "queued", content: q})
	}
	welcome := len(entries) == 0
	if welcome {
		entries = append(entries, transcriptEntry{index: -1, role: "raw", content: m.renderWelcomeMessage()})
	}
	// Capture styles by value. No worker accesses the live model or a shared
	// Glamour renderer; only immutable strings and cached rows cross threads.
	headerStyle := thoughtHeaderStyle
	queueStyle := queuedMsgStyle
	noticeStyle := aiMsgStyle.MarginLeft(1).Border(boxBorderStyle).BorderForeground(warningColor)
	errorStyle := noticeStyle.BorderForeground(errorBorderColor)
	bg := appBgColor
	activityHeader := m.renderActivityAt("thinking...", toolWidth, time.Unix(0, 0))
	answerStyle := assistantReplyStyle(width)
	return func() tea.Msg {
		renderer, err := glamour.NewTermRenderer(glamour.WithStylesFromJSONBytes([]byte(theme)), glamour.WithWordWrap(assistantReplyContentWidth(width)), glamour.WithPreservedNewLines())
		markdown := func(source string) string {
			if err != nil {
				return source
			}
			out, e := renderer.Render(source)
			if e != nil {
				return source
			}
			return out
		}
		result := transcriptRenderedMsg{activities: make(map[int]string), partial: partial, welcome: welcome, id: id, generation: generation, width: width, theme: theme, cache: make(map[string][]string, len(entries))}
		for _, entry := range entries {
			key := fmt.Sprintf("%t:%d:%s:%d:%s:%d:%s:%v", entry.active, len(entry.role), entry.role, len(entry.content), entry.content, len(entry.reasoning), entry.reasoning, entry.labels)
			rows, ok := oldCache[key]
			if !ok {
				parts := make([]string, 0, 4)
				switch entry.role {
				case "user":
					text := strings.TrimRight(entry.content, "\r\n")
					if strings.TrimSpace(text) != "" || len(entry.labels) > 0 {
						// Prompt cards use the full transcript width, with a tighter
						// outer gap than assistant output. The right inset remains the
						// comfortable one-column surface gap.
						innerWidth := userPromptContentWidth(width)
						block := ansi.Wordwrap(text, innerWidth, "")
						if len(entry.labels) > 0 {
							for _, label := range entry.labels {
								block += "\n" + label.rendered
							}
						}
						parts = append(parts, "\n"+userPromptStyle(width).Render(block)+"\n")
					}
				case "assistant":
					if entry.reasoning != "" {
						header := headerStyle.Render("· thinking")
						if entry.active && entry.content == "" && len(entry.labels) == 0 {
							header = activityHeader
						}
						parts = append(parts, header, thoughtBodyStyle(width).Render(entry.reasoning))
					}
					if entry.content != "" {
						if entry.reasoning != "" {
							parts = append(parts, "")
						}
						parts = append(parts, answerStyle.Render(strings.Trim(markdown(entry.content), "\r\n")))
					}
					if len(entry.labels) > 0 {
						if len(parts) > 0 && parts[len(parts)-1] != "" {
							parts = append(parts, "")
						}
						for _, label := range entry.labels {
							parts = append(parts, label.rendered)
						}
						parts = append(parts, "")
					}
				case "notice", "error":
					style := noticeStyle
					if entry.role == "error" {
						style = errorStyle
					}
					parts = append(parts, style.Width(max(1, width-style.GetHorizontalFrameSize())).Render(markdown(entry.content)))
				case "queued":
					parts = append(parts, queueStyle.Width(max(1, width-queueStyle.GetHorizontalMargins())).Render(entry.content))
				case "raw":
					parts = append(parts, entry.content)
				case "thinking":
					// Reserve the same header and gutter rows used by streamed reasoning.
					parts = append(parts, activityHeader, thoughtBodyStyle(width).Render(""))
				}
				if len(parts) == 0 {
					continue
				}
				rows = strings.Split(strings.Join(parts, "\n"), "\n")
				padding := lipgloss.NewStyle().Background(bg)
				for i, row := range rows {
					if ansi.StringWidth(row) > width {
						row = ansi.Truncate(row, width, "")
					}
					missing := width - ansi.StringWidth(row)
					if missing > 0 {
						row += padding.Render(strings.Repeat(" ", missing))
					}
					rows[i] = row
				}
			}
			result.cache[key] = rows
			start := len(result.rows)
			if entry.role == "thinking" {
				result.thinking = true
				result.thinkingLine = start
				result.activities[start] = "thinking..."
			}
			if entry.active && entry.reasoning != "" && entry.content == "" && len(entry.labels) == 0 {
				result.activities[start] = "thinking..."
			}
			if len(entry.labels) > 0 {
				for j, label := range entry.labels {
					if label.activity != "" {
						result.activities[start+len(rows)-1-len(entry.labels)+j] = label.activity
					}
				}
			}
			result.rows = append(result.rows, rows...)
			result.blocks = append(result.blocks, RenderBlock{MessageIndex: entry.index, Content: entry.content, StartLine: start, EndLine: len(result.rows) - 1})
		}
		if !welcome && len(result.rows) > 0 {
			spacer := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", width))
			trailingEmpty := 0
			for i := len(result.rows) - 1; i >= 0; i-- {
				if strings.TrimSpace(result.rows[i]) == "" {
					trailingEmpty++
				} else {
					break
				}
			}
			for trailingEmpty < 2 {
				result.rows = append(result.rows, spacer)
				trailingEmpty++
			}
		}
		return result
	}
}

func transcriptError(err error) string {
	text := err.Error()
	if strings.Contains(text, "exceeds the available context size") || strings.Contains(text, "context_length_exceeded") {
		return "**Context Limit Exceeded**\n\nThis session has hit the model's absolute context limit. Please **start a new session** to continue your work."
	}
	return fmt.Sprintf("Error: %v", err)
}
