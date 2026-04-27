package tui

import (
	"late/internal/client"
	"late/internal/common"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
)

// ValidationState represents the current state of the TUI interaction.
type ValidationState int

const (
	StateIdle ValidationState = iota
	StateThinking
	StateStreaming
	StateConfirmTool
	StateConfirmSubagent
	StateStopping
)

type ViewState int

const (
	ViewChat ViewState = iota
	ViewHelp
	ViewDump
	ViewSubagent
)

// Fixed layout heights (crush-style)
const (
	InputHeight     = 9
	StatusBarHeight = 2
	AppPadding      = 0
)

// AppState tracks the interactive state of a single orchestrator.
type AppState struct {
	State                ValidationState
	StreamingState       common.ContentEvent
	PendingConfirm       *ConfirmRequestMsg
	StatusText           string
	RenderedHistory      []string // Cache for rendered messages
	Closed               bool     // Whether the agent has finished its task
	PendingStop          bool     // Whether a stop has been requested
	TokenCount           int      // Estimated token count for current streaming content
	CumulativeTokenCount int      // Total tokens accumulated across entire session (all messages)
	Usage                client.Usage
	LastRenderTime       int64 // Unix milliseconds of the last render during streaming

	// Streaming render cache: paragraph-chunked incremental rendering
	StreamingStyledCache string // Fully assembled + styled output of all completed paragraphs
	StreamingChunkCount  int    // Number of complete source paragraphs already rendered

	// History token cache
	CachedHistoryTokens int // Cached total token count for completed history
	CachedHistoryLen    int // History length when tokens were last computed
	LastRealTokenCount  int // Last ground-truth token count from the API usage data

	// Performance caches
	LastStreamingContent string   // To avoid re-splitting if content hasn't changed
	LastChunks           []string // Cached result of splitMarkdownChunks
	LastTail             string   // Cached result of splitMarkdownChunks
	LastTotalContent     string   // To avoid redundant Viewport.SetContent calls
}

type Model struct {
	Mode           ViewState
	Input          textarea.Model
	Viewport       viewport.Model
	Err            error
	Width          int
	Height         int
	Renderer       *glamour.TermRenderer
	InspectingTool bool

	// Unified Orchestration
	Root    common.Orchestrator
	Focused common.Orchestrator

	// Per-Orchestrator states
	AgentStates map[string]*AppState

	// Messenger for async tasks
	Messenger Messenger

	// Active spinner animation
	Spinner spinner.Model

	// Performance caches
	cachedRenderer      *glamour.TermRenderer
	cachedRendererWidth int
	LastFocusedID       string // To detect context switches and force viewport refresh
}

func (m *Model) GetAgentState(id string) *AppState {
	if m.AgentStates == nil {
		m.AgentStates = make(map[string]*AppState)
	}
	if s, ok := m.AgentStates[id]; ok {
		return s
	}
	s := &AppState{
		State:       StateIdle,
		StatusText:  "Ready",
		PendingStop: false,
	}
	m.AgentStates[id] = s
	return s
}

// Messenger is an interface for sending messages to the TUI (implemented by tea.Program)
type Messenger interface {
	Send(msg tea.Msg)
}

// SetMessengerMsg is sent to initialize the messenger in the model
type SetMessengerMsg struct {
	Messenger Messenger
}

// OrchestratorEventMsg is the bridge between Orchestrator goroutines and the TUI loop.
type OrchestratorEventMsg struct {
	Event common.Event
}

// FindOrchestrator recursively searches for an orchestrator by ID.
func (m *Model) FindOrchestrator(id string) common.Orchestrator {
	var search func(curr common.Orchestrator) common.Orchestrator
	search = func(curr common.Orchestrator) common.Orchestrator {
		if curr.ID() == id {
			return curr
		}
		for _, child := range curr.Children() {
			if res := search(child); res != nil {
				return res
			}
		}
		return nil
	}
	return search(m.Root)
}
