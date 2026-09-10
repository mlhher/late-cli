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

type transcriptEntry struct {
	index     int
	role      string
	content   string
	reasoning string
	labels    []string
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
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if t.offset+i < end {
			row := t.rows[t.offset+i]
			if t.thinking && t.offset+i == t.thinkingLine {
				row = m.renderAnimatedTag("· thinking...", thinkingStyle, max(1, m.Viewport.Width()-thinkingStyle.GetHorizontalFrameSize()), true)
				row = ansi.Truncate(row, max(1, m.Viewport.Width()), "")
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
	if result.generation != t.generation || result.width != m.Viewport.Width() || result.theme != string(styles) {
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
	toolLabels := func(calls []client.ToolCall, active bool) []string {
		labels := make([]string, 0, len(calls))
		for _, tc := range calls {
			label := tc.Function.Name
			if registry := m.Focused.Registry(); registry != nil {
				if tool := registry.Get(label); tool != nil && len(tc.Function.Arguments) > 0 {
					label = tool.CallString([]byte(tc.Function.Arguments))
				}
			}
			if active {
				label += " · running"
			}
			labels = append(labels, m.renderToolBadge(tc.Function.Name, label, false, width))
		}
		return labels
	}
	history := m.Focused.History()
	start := 0
	if m.LazyHistory && t.rows == nil && t.cache == nil {
		start = max(0, len(history)-4)
	}
	partial := start > 0
	for i := start; i < len(history); i++ {
		msg := history[i]
		content := msg.Content.String()
		if msg.Role == "user" {
			content = msg.Content.UIString()
		}
		entry := transcriptEntry{index: i, role: msg.Role, content: content, reasoning: msg.ReasoningContent, labels: toolLabels(msg.ToolCalls, false)}
		if len(msg.AttachedFiles) > 0 {
			names := make([]string, len(msg.AttachedFiles))
			for j, f := range msg.AttachedFiles {
				names[j] = filepath.Base(f)
			}
			entry.labels = append(entry.labels, attachmentStyle.Render("  ↳ attached: "+strings.Join(names, ", ")))
		}
		entries = append(entries, entry)
	}
	if (s.State == StateStreaming || s.State == StateThinking) && !s.StreamingState.Completed {
		active := s.StreamingState
		if active.Content != "" || active.ReasoningContent != "" || len(active.ToolCalls) > 0 {
			entries = append(entries, transcriptEntry{index: len(history), role: "assistant", content: active.Content, reasoning: active.ReasoningContent, labels: toolLabels(active.ToolCalls, true)})
		} else if s.State == StateThinking {
			entries = append(entries, transcriptEntry{index: len(history), role: "thinking", content: "· thinking..."})
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
	userStyle, thoughtStyle, headerStyle := userMsgStyle, thinkingStyle, thoughtHeaderStyle
	promptStyle, queueStyle := promptSymbolStyle, queuedMsgStyle
	noticeStyle := aiMsgStyle.MarginLeft(1).Border(boxBorderStyle).BorderForeground(warningColor)
	errorStyle := noticeStyle.BorderForeground(errorBorderColor)
	bg := appBgColor
	return func() tea.Msg {
		renderer, err := glamour.NewTermRenderer(glamour.WithStylesFromJSONBytes([]byte(theme)), glamour.WithWordWrap(max(1, width-AIMsgOverhead)), glamour.WithPreservedNewLines())
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
		result := transcriptRenderedMsg{partial: partial, welcome: welcome, id: id, generation: generation, width: width, theme: theme, cache: make(map[string][]string, len(entries))}
		for _, entry := range entries {
			key := fmt.Sprintf("%d:%s:%d:%s:%d:%s:%v", len(entry.role), entry.role, len(entry.content), entry.content, len(entry.reasoning), entry.reasoning, entry.labels)
			rows, ok := oldCache[key]
			if !ok {
				parts := make([]string, 0, 4)
				switch entry.role {
				case "user":
					text := strings.TrimRight(entry.content, "\r\n")
					if strings.TrimSpace(text) != "" || len(entry.labels) > 0 {
						block := promptStyle.Render("❯ ") + userStyle.Width(max(1, width-2)).Render(text)
						if len(entry.labels) > 0 {
							block += "\n" + strings.Join(entry.labels, "\n")
						}
						parts = append(parts, "\n"+block+"\n")
					}
				case "assistant":
					if entry.reasoning != "" {
						parts = append(parts, headerStyle.Render("· thinking"), thoughtStyle.Width(max(1, width-4)).Render(entry.reasoning))
					}
					if entry.content != "" {
						parts = append(parts, strings.TrimRight(markdown(entry.content), "\r\n"))
					}
					parts = append(parts, entry.labels...)
				case "notice", "error":
					style := noticeStyle
					if entry.role == "error" {
						style = errorStyle
					}
					parts = append(parts, style.Width(max(1, width-style.GetHorizontalFrameSize())).Render(markdown(entry.content)))
				case "queued":
					parts = append(parts, queueStyle.Width(width).Render(entry.content))
				case "raw":
					parts = append(parts, entry.content)
				case "thinking":
					parts = append(parts, thoughtStyle.Width(max(1, width-thoughtStyle.GetHorizontalFrameSize())).Render(entry.content))
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
			}
			result.rows = append(result.rows, rows...)
			result.blocks = append(result.blocks, RenderBlock{MessageIndex: entry.index, Content: entry.content, StartLine: start, EndLine: len(result.rows) - 1})
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
