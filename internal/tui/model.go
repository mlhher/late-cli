package tui

import (
	"late/internal/common"
	"os"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

func NewModel(root common.Orchestrator, renderer *glamour.TermRenderer) Model {
	ti := textarea.New()
	ti.Placeholder = "Ask Late anything..."
	ti.Focus()
	ti.CharLimit = 2000
	ti.SetWidth(72)
	ti.SetHeight(InputHeight - 2)
	ti.ShowLineNumbers = false
	ti.Prompt = ""    // Remove the line prompt characters
	ti.SetValue("> ") // Set initial "fake" prompt to force background render logic on first line
	ti.KeyMap.InsertNewline.SetEnabled(false)

	// Set opaque background for textarea content
	bgStyle := lipgloss.NewStyle().Background(lipgloss.Color("#191919")).Foreground(textColor)
	styles := ti.Styles()
	styles.Focused.Base = bgStyle
	styles.Focused.Text = bgStyle
	styles.Focused.Placeholder = bgStyle.Foreground(lipgloss.Color("#666666"))
	styles.Focused.CursorLine = bgStyle
	styles.Focused.Prompt = bgStyle

	styles.Blurred.Base = bgStyle
	styles.Blurred.Text = bgStyle
	styles.Blurred.Placeholder = bgStyle.Foreground(lipgloss.Color("#666666"))
	styles.Blurred.CursorLine = bgStyle
	styles.Blurred.Prompt = bgStyle
	ti.SetStyles(styles)

	// Initialize with 0, so that the first WindowSizeMsg sets correct dimensions
	// This prevents the "50% width" issue if the default 60 is too small for a large terminal
	vp := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	vp.SetContent("Welcome to Late. Type your prompt below.")

	// Determine active state
	initialState := StateIdle
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
		cachedRendererWidth: -1, // Force first creation
	}

	fp := filepicker.New()
	fp.FileAllowed = true
	fp.DirAllowed = false
	fp.ShowHidden = true
	cwd, _ := os.Getwd()
	fp.CurrentDirectory = cwd
	fp.AutoHeight = false
	fp.SetHeight(m.Height - 2)

	// Apply styles for visibility
	s := filepicker.DefaultStyles()
	s.Selected = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Bold(true)
	s.File = lipgloss.NewStyle().Foreground(textColor)
	s.Directory = lipgloss.NewStyle().Foreground(lipgloss.Color("#5555FF")).Bold(true)
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

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.Spinner.Tick, m.FilePicker.Init())
}
