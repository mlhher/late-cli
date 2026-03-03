package tui

import (
	"late/internal/common"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
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
	StatusBarHeight = 1
	AppPadding      = 0
)

// AppState tracks the interactive state of a single orchestrator.
type AppState struct {
	State           ValidationState
	StreamingState  common.ContentEvent
	PendingConfirm  *ConfirmRequestMsg
	StatusText      string
	RenderedHistory []string // Cache for rendered messages
	Closed          bool     // Whether the agent has finished its task
	PendingStop     bool     // Whether a stop has been requested
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
}

func (m *Model) GetAgentState(id string) *AppState {
	if m.AgentStates == nil {
		m.AgentStates = make(map[string]*AppState)
	}
	if s, ok := m.AgentStates[id]; ok {
		return s
	}
	s := &AppState{
		State:      StateIdle,
		StatusText: "Ready",
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
