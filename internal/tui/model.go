package tui

import (
	"fmt"
	"late/internal/common"
	"late/internal/config"
	"os"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

func NewModel(root common.Orchestrator, renderer *glamour.TermRenderer, cfg *config.Config) Model {
	ti := textarea.New()
	ti.Placeholder = "Ask Late anything..."
	ti.Focus()
	ti.CharLimit = 100000 // Allow pasting large code blocks
	ti.SetWidth(72)
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = 4
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	ti.Prompt = ""    // Remove the line prompt characters
	ti.SetValue("> ") // Set initial "fake" prompt to force background render logic on first line
	ti.KeyMap.InsertNewline.SetEnabled(false)

	// Set opaque background for textarea content
	bgStyle := lipgloss.NewStyle().Background(lipgloss.Color("#0E0E10")).Foreground(textColor)
	styles := ti.Styles()
	styles.Focused.Base = bgStyle
	styles.Focused.Text = bgStyle
	styles.Focused.Placeholder = bgStyle.Foreground(lipgloss.Color("#4A4B50"))
	styles.Focused.CursorLine = bgStyle
	styles.Focused.Prompt = bgStyle

	styles.Blurred.Base = bgStyle
	styles.Blurred.Text = bgStyle
	styles.Blurred.Placeholder = bgStyle.Foreground(lipgloss.Color("#4A4B50"))
	styles.Blurred.CursorLine = bgStyle
	styles.Blurred.Prompt = bgStyle
	ti.SetStyles(styles)

	// Initialize with 0, so that the first WindowSizeMsg sets correct dimensions
	// This prevents the "50% width" issue if the default 60 is too small for a large terminal
	vp := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	vp.MouseWheelDelta = 6 // Lines per wheel tick; default 3 feels slow on chat history
	// VTE-based terminals: set explicit background on the viewport so its
	// internal padding cells don't become transparent after ANSI resets.
	vp.Style = lipgloss.NewStyle().Background(appBgColor)
	// Initial welcome is set to empty; updateViewport in view.go renders
	// the rich welcome when history is empty using renderWelcomeMessage().
	vp.SetContent("")

	// Determine active state
	initialState := StateIdle
	cwd, _ := os.Getwd()
	if root.History() != nil && len(root.History()) > 0 {
		last := root.History()[len(root.History())-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			// Check if we are waiting for a tool result?
			// For now, default to thinking if history exists, or idle.
		}
	}

	m := Model{
		Mode:                ViewChat,
		Root:                root,
		Focused:             root,
		Input:               ti,
		Viewport:            vp,
		Renderer:            renderer,
		Width:               80,
		Height:              24, // Default start height
		AgentStates:         make(map[string]*AppState),
		InspectingTool:      false,
		Spinner:             spinner.New(spinner.WithSpinner(spinner.Dot)),
		InputHistory:        make([]string, 0),
		HistoryIndex:        -1,
		CWD:                 cwd,
		ShowCWD:             true,
		cachedRendererWidth: -1, // Force first creation
		Pastes:              make(map[string]string),
		AppConfig:           cfg,
	}

	fp := filepicker.New()
	fp.FileAllowed = true
	fp.DirAllowed = false
	fp.ShowHidden = true
	cwd, _ = os.Getwd()
	fp.CurrentDirectory = cwd
	fp.AutoHeight = false
	fp.SetHeight(m.Height - 2)

	// Apply styles for visibility
	s := filepicker.DefaultStyles()
	s.Selected = lipgloss.NewStyle().Foreground(secondaryColor).Bold(true)
	s.File = lipgloss.NewStyle().Foreground(textColor)
	s.Directory = lipgloss.NewStyle().Foreground(primaryColor).Bold(true)
	fp.Styles = s

	m.FilePicker = fp
	// Initialize root state
	history := root.History()
	cumulativeTokens := 0
	if history != nil && len(history) >= 0 {
		cumulativeTokens = common.CalculateHistoryTokens(history, root.SystemPrompt(), root.ToolDefinitions())
	}
	m.AgentStates[root.ID()] = &AppState{
		State:                initialState,
		StatusText:           "Ready",
		CumulativeTokenCount: cumulativeTokens,
	}

	return m
}

func (m *Model) GetRenderer(width int) *glamour.TermRenderer {
	if width < 1 {
		width = 80
	}
	if m.cachedRenderer != nil && m.cachedRendererWidth == width {
		return m.cachedRenderer
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(LateTheme),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	m.cachedRenderer = r
	m.cachedRendererWidth = width
	return r
}

// ReloadTheme swaps in a new glamour renderer (used when a plugin theme
// is applied at startup or at runtime) and clears the per-width cache so
// the next GetRenderer call rebuilds it with the new style JSON.
//
// The first cached-renderer key (m.Viewport) is left intact so the chat
// viewport doesn't flicker; the force-flush happens when the orchestrator
// next emits content.
func (m *Model) ReloadTheme(renderer *glamour.TermRenderer) {
	if renderer == nil {
		return
	}
	m.Renderer = renderer
	m.cachedRenderer = nil
	m.cachedRendererWidth = -1
}

// ApplyTheme installs a plugin-provided theme as the active renderer. It
// rebuilds the glamour renderer with merged JSON bytes, swaps the active
// renderer via ReloadTheme, clears per-agent render caches so the chat
// history re-renders under the new theme, and updates SelectedTheme.
//
// Returns an error only when the theme JSON is malformed; the lookup
// itself is done by the caller (so chat-mode /themes <name> can report a
// distinct "not found" error to the user).
func (m *Model) ApplyTheme(info *ThemeEntry) error {
	if info == nil {
		return fmt.Errorf("ApplyTheme: nil theme")
	}
	merged, err := ResolveRenderTheme(info.ID, info.Glamour, info.Palette)
	if err != nil {
		return fmt.Errorf("resolve theme %q: %w", info.ID, err)
	}

	// Build a new renderer at the current viewport width so the theme
	// doesn't have to re-wrap on the next message. Fall back to a safe
	// default when the viewport hasn't been sized yet.
	width := m.Viewport.Width()
	if width < 1 {
		width = 80
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(merged),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return fmt.Errorf("build renderer for %q: %w", info.ID, err)
	}

	m.ReloadTheme(renderer)
	m.SelectedTheme = info.ID

	// Invalidate per-agent render caches so the next updateViewport call
	// rebuilds message rendering under the new theme. Streaming chunk
	// caches are also dropped so live responses restyle.
	for _, s := range m.AgentStates {
		s.RenderedHistory = nil
		s.LastTotalContent = ""
		s.LastStreamingContent = ""
		s.LastChunks = nil
		s.LastTail = ""
		s.StreamingStyledCache = ""
	}
	return nil
}

// ApplyMessageHook returns the user text after running it through any
// plugin-provided MessageHook. If no hook is set, the input is returned
// unchanged. This is the single integration point the chat submit handler
// uses, so adding more transformations later (e.g. local command rewrite)
// only has to touch this method.
func (m *Model) ApplyMessageHook(text string) string {
	if m.MessageHook == nil || text == "" {
		return text
	}
	out := m.MessageHook(text)
	if out == "" {
		// Hook explicitly cleared the message — treat as a no-op rather
		// than swallowing the user's intent silently.
		return text
	}
	return out
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.Spinner.Tick, m.FilePicker.Init())
}
