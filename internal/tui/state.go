package tui

import (
	"late/internal/client"
	"late/internal/common"
	"late/internal/config"
	"late/internal/git"

	"charm.land/bubbles/v2/filepicker"
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
	StateContextWarning
)

type ViewState int

const (
	ViewChat ViewState = iota
	ViewHelp
	ViewDump
	ViewSubagent
	ViewFilePicker
	ViewCommitLog
	ViewRewind
	ViewModelPicker
	ViewReasoningPicker
)

// Fixed layout heights (crush-style)
const (
	InputHeight     = 9
	StatusBarHeight = 2
	AppPadding      = 0
)

// CommandDef defines a slash command and its description.
type CommandDef struct {
	Name        string
	Description string
}

// AvailableCommands lists all slash commands available in the TUI.
var AvailableCommands = []CommandDef{
	{Name: "/new", Description: "Start a new session/chat"},
	{Name: "/compose", Description: "Compose a message with an editor"},
	{Name: "/help", Description: "Show help and shortcuts"},
	{Name: "/log", Description: "View git commit log"},
	{Name: "/model", Description: "Select AI model for agents"},
	{Name: "/quit", Description: "Exit the application"},
	{Name: "/reasoning", Description: "Set reasoning effort for agents"},
	{Name: "/rewind", Description: "Rewind conversation history"},
}

// RenderBlock represents the line bounds of a rendered block in the viewport.
type RenderBlock struct {
	MessageIndex int    // -1 if active/streaming content
	Content      string // raw copyable content
	StartLine    int
	EndLine      int
}

// RewindEntry represents a user message that can be rewound to.
type RewindEntry struct {
	Index   int
	Content string
}

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
	CachedHistoryLines   []string // Completed history, split once for windowed streaming
	CachedHistoryBlocks  []RenderBlock
	CachedHistoryHashes  []uint64 // Content identity for same-length history mutations
	StreamingWindow      bool     // Viewport currently contains only the recent history window
	StreamingWindowStart int      // Full-history line represented by viewport line zero

	RenderBlocks []RenderBlock // Line ranges of rendered blocks

	ContextWarningShown bool // Whether the preflight context warning has been shown for the current input
	Error               error
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

	// File Picker
	FilePicker     filepicker.Model
	AttachedFiles  []string
	ShowFilePicker bool

	// Double-click copy & Toast tracking
	LastClickX      int
	LastClickY      int
	LastClickTime   int64
	ToastMessage    string
	ToastExpireTime int64
	ToastWarning    bool

	// Model and config info (set from main.go after creation)
	ModelName    string // Active model name
	SubagentInfo string // Subagent model/config description, empty if same as main
	CWD          string // Current working directory, shown in status bar
	ShowCWD      bool   // Whether to show current working directory in status bar

	// Configuration
	AppConfig              *config.Config
	ApplyOrchestratorModel func(config.ModelSetting) tea.Cmd

	// Model picker fields
	ModelPickerAgents          []string
	ModelPickerModels          []string
	ModelPickerAgentIndex      int
	ModelPickerAgentSelections map[string]int

	// Reasoning picker fields
	ReasoningPickerAgents          []string
	ReasoningPickerEfforts         []string
	ReasoningPickerAgentIndex      int
	ReasoningPickerAgentSelections map[string]int

	// Esc confirmation
	EscConfirmPending bool   // Show "are you sure?" when Esc pressed at main view
	escBgContent      string // Saved viewport content to show underneath the dialog

	// Paste detection
	lastInputLen int // Length of input after previous update, to detect pastes
	Pastes       map[string]string

	// Input history (ring buffer via slice)
	InputHistory   []string // Previously submitted prompts, oldest first
	HistoryIndex   int      // Current position: -1 = new input, 0 = oldest, len-1 = newest
	HistoryWorking string   // Temp save of current input when browsing history

	// Commit log view
	CommitEntries []git.CommitEntry
	CommitIndex   int
	CommitDetail  string // Full commit detail when viewing a single commit

	// Rewind view
	RewindEntries []RewindEntry
	RewindIndex   int

	// Slash-command autocomplete
	ShowAutocomplete  bool
	AutocompleteItems []CommandDef
	AutocompleteIndex int

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

func (m *Model) hasActiveAgent() bool {
	for _, state := range m.AgentStates {
		if state.State != StateIdle && state.State != StateContextWarning {
			return true
		}
	}
	return false
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
