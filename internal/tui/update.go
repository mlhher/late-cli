package tui

import (
	"fmt"
	"late/internal/common"
	"late/internal/git"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

// StreamMsg is the TUI-wrapper for session stream events
type StreamMsg struct {
	Result common.StreamResult
	Err    error
	Done   bool
}

type clearToastMsg struct{}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(clearToastMsg); ok {
		m.ToastMessage = ""
		m.updateViewport()
		return m, nil
	}

	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	// Global Key Handling (Ctrl+C, Ctrl+D)
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			return m, tea.Quit
		}
		if msg.String() == "ctrl+a" {
			m.ShowFilePicker = !m.ShowFilePicker
			if m.ShowFilePicker {
				m.Mode = ViewFilePicker
			} else {
				m.Mode = ViewChat
			}
			return m, m.FilePicker.Init()
		}
		if msg.String() == "ctrl+x" {
			m.AttachedFiles = nil
			return m, nil
		}
		if msg.String() == "ctrl+h" {
			if m.Mode == ViewHelp {
				m.Mode = ViewChat
			} else {
				m.Mode = ViewHelp
			}
			focusedState := m.GetAgentState(m.Focused.ID())
			focusedState.RenderedHistory = nil // Force re-render of history on toggle back
			m.updateLayout()
			return m, nil
		}
	}

	// Window Sizing
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		m.Width = msg.Width
		m.Height = msg.Height
		for _, s := range m.AgentStates {
			s.RenderedHistory = nil
		}
		m.updateLayout()
	}

	// Internal Messages
	if msg, ok := msg.(SetMessengerMsg); ok {
		m.Messenger = msg.Messenger
		return m, nil
	}

	// Snapshot state before updateChat processes the key and potentially changes it
	var stateBefore ValidationState
	escBefore := false
	if _, ok := msg.(tea.KeyMsg); ok {
		stateBefore = m.GetAgentState(m.Focused.ID()).State
		escBefore = m.EscConfirmPending
	}

	// Main Chat Update Logic
	newM, cmd := m.updateChat(msg)
	m = newM

	// Filter key events that were consumed by updateChat during confirmation
	forwardToInput := true
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "y", "Y", "n", "N", "s", "S", "p", "P", "g", "G":
			if escBefore || (stateBefore == StateConfirmTool && strings.TrimPrefix(m.Input.Value(), "> ") == "") {
				forwardToInput = false
			}
		case "up", "down":
			if m.Mode == ViewChat {
				forwardToInput = false
			}
		}
	}

	// Update Sub-models
	if forwardToInput {
		m.Input, tiCmd = m.Input.Update(msg)
		// Prevent cursor from moving before the "> " prompt on the first line
		if m.Input.Line() == 0 && m.Input.Column() < 2 {
			m.Input.SetCursorColumn(2)
		}

		if !strings.HasPrefix(m.Input.Value(), "> ") {
			val := m.Input.Value()
			if strings.HasPrefix(val, ">") {
				m.Input.SetValue("> " + strings.TrimPrefix(val, ">"))
			} else {
				m.Input.SetValue("> " + val)
			}
			m.Input.CursorEnd()
		}
	}

	// Update autocomplete state whenever the input changes
	m.updateAutocomplete()

	// Detect large pastes (input grew by > 50 chars in one update cycle)
	currentLen := len(m.Input.Value())
	if currentLen-m.lastInputLen > 50 && m.lastInputLen > 0 {
		pastedText := m.Input.Value()[m.lastInputLen:]
		lineCount := strings.Count(pastedText, "\n") + 1
		charCount := currentLen - m.lastInputLen
		if lineCount >= 3 {
			m.ToastMessage = fmt.Sprintf("pasted %d lines (%d chars)", lineCount, charCount)
			m.ToastExpireTime = time.Now().UnixMilli() + 2500
			clearCmd := tea.Tick(2500*time.Millisecond, func(t time.Time) tea.Msg {
				return clearToastMsg{}
			})
			m.lastInputLen = currentLen
			return m, clearCmd
		}
	}
	m.lastInputLen = currentLen

	var spCmd tea.Cmd
	m.Spinner, spCmd = m.Spinner.Update(msg)

	// Only forward key/mouse events to viewport when the user is NOT typing.
	// The viewport has default keybindings (g, G, space, j, k, d, u, pgup, pgdn)
	// that conflict with textarea input and cause chat messages to shift.
	// Forward key events to viewport selectively to prevent conflict with typing
	var forwardToViewport bool
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "pgup", "pgdown", "home", "end":
			forwardToViewport = true
		default:
			// Never forward character keys to the viewport to prevent conflicts with textarea input.
			// The viewport binds keys like space, j, k, d, u which cause shifting if typed.
			forwardToViewport = false
		}
	case tea.MouseWheelMsg:
		// Wheel events forwarded to viewport for scroll handling.
		// Bubbletea v2 dispatches these as a distinct type from MouseMsg.
		forwardToViewport = true
	case tea.MouseMsg:
		forwardToViewport = true
		if clickMsg, ok := msg.(tea.MouseClickMsg); ok {
			mouseMsg := clickMsg.Mouse()
			if mouseMsg.Button == tea.MouseLeft {
				if mouseMsg.Y >= 0 && mouseMsg.Y < m.Viewport.Height() {
					now := time.Now().UnixMilli()
					if now-m.LastClickTime < 500 && m.LastClickX == mouseMsg.X && m.LastClickY == mouseMsg.Y {
						m.LastClickTime = 0 // prevent triple click from double-triggering
						clickedLine := m.Viewport.YOffset() + mouseMsg.Y
						s := m.GetAgentState(m.Focused.ID())
						var foundBlock *RenderBlock
						for _, block := range s.RenderBlocks {
							if clickedLine >= block.StartLine && clickedLine <= block.EndLine {
								foundBlock = &block
								break
							}
						}
						if foundBlock != nil && foundBlock.Content != "" {
							err := clipboard.WriteAll(foundBlock.Content)
							if err == nil {
								m.ToastMessage = "copied response to clipboard"
								if foundBlock.MessageIndex >= 0 {
									history := m.Focused.History()
									if foundBlock.MessageIndex < len(history) {
										if history[foundBlock.MessageIndex].Role == "user" {
											m.ToastMessage = "copied prompt to clipboard"
										}
									}
								}
								m.ToastExpireTime = time.Now().UnixMilli() + 3000
								clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
									return clearToastMsg{}
								})
								m.updateViewport()
								return m, clearCmd
							}
						}
					} else {
						m.LastClickX = mouseMsg.X
						m.LastClickY = mouseMsg.Y
						m.LastClickTime = now
					}
				}
			}
		}
	case spinner.TickMsg:
		// Only redraw on tick to animate tool calls/thinking if an agent is actually active
		// AND showing a spinner inside the viewport. Status bar spinner animates via View().
		s := m.GetAgentState(m.Focused.ID())
		if s.State == StateThinking || s.State == StateStreaming {
			if s.State == StateThinking || len(s.StreamingState.ToolCalls) > 0 {
				m.updateViewport()
			}
		}
		forwardToViewport = false
	default:
		forwardToViewport = true
	}

	if forwardToViewport {
		m.Viewport, vpCmd = m.Viewport.Update(msg)
	}

	var fpCmd tea.Cmd
	if m.ShowFilePicker {
		m.FilePicker, fpCmd = m.FilePicker.Update(msg)
		if didSelect, file := m.FilePicker.DidSelectFile(msg); didSelect {
			info, err := os.Stat(file)
			if err == nil && info.IsDir() {
				// Should not happen with DirAllowed=false, but good for safety.
				// If we got here, it means we don't want to close the picker yet.
				return m, fpCmd
			}

			// Content-based validation for image support
			data, err := os.ReadFile(file)
			if err != nil {
				m.Err = fmt.Errorf("failed to read file: %w", err)
			} else {
				mimeType := http.DetectContentType(data)
				isImage := strings.HasPrefix(mimeType, "image/")
				if isImage && !m.Focused.SupportsVision() {
					focusedState := m.GetAgentState(m.Focused.ID())
					focusedState.StatusText = "Images not supported by current model"
				} else {
					m.AttachedFiles = append(m.AttachedFiles, file)
					m.ShowFilePicker = false
					m.Mode = ViewChat
					// Show toast with just the filename
					fname := filepath.Base(file)
					m.ToastMessage = "attached " + fname
					m.ToastExpireTime = time.Now().UnixMilli() + 2500
					clearCmd := tea.Tick(2500*time.Millisecond, func(t time.Time) tea.Msg {
						return clearToastMsg{}
					})
					return m, tea.Batch(fpCmd, clearCmd)
				}
			}
			m.ShowFilePicker = false
			m.Mode = ViewChat
		}
	}

	return m, tea.Batch(cmd, tiCmd, vpCmd, spCmd, fpCmd)
}

func (m Model) updateChat(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		focusedState := m.GetAgentState(m.Focused.ID())

		// Commit log view key handling
		if m.Mode == ViewCommitLog {
			if m.CommitDetail != "" {
				// In detail view, any key goes back to list
				if msg.String() == "esc" || msg.String() == "enter" {
					m.CommitDetail = ""
					m.updateViewport()
					return m, nil
				}
				return m, nil
			}
			switch msg.String() {
			case "up":
				m.CommitIndex = max(0, m.CommitIndex-1)
				m.updateViewport()
				return m, nil
			case "down":
				m.CommitIndex = min(len(m.CommitEntries)-1, m.CommitIndex+1)
				m.updateViewport()
				return m, nil
			case "enter":
				if m.CommitIndex >= 0 && m.CommitIndex < len(m.CommitEntries) {
					detail, err := git.ShowCommit(m.CWD, m.CommitEntries[m.CommitIndex].Hash)
					if err != nil {
						m.Err = err
					} else {
						m.CommitDetail = detail
					}
					m.updateViewport()
				}
				return m, nil
			case "esc":
				// Let esc fall through to the esc handler below
				m.Mode = ViewChat
				m.CommitEntries = nil
				focusedState.RenderedHistory = nil
				m.updateViewport()
				return m, nil
			}
			return m, nil
		}

		// Esc confirmation handling
		if m.EscConfirmPending {
			switch msg.String() {
			case "y", "Y":
				m.EscConfirmPending = false
				focusedState := m.GetAgentState(m.Focused.ID())
				if focusedState.State == StateThinking || focusedState.State == StateStreaming || focusedState.State == StateStopping {
					m, _ = m.interruptFocusedAgent()
					if s := m.GetAgentState(m.Focused.ID()); s != nil {
						s.LastTotalContent = ""
					}
					m.updateViewport()
				} else {
					return m, tea.Quit
				}
				return m, nil
			case "n", "N", "esc":
				m.EscConfirmPending = false
				if s := m.GetAgentState(m.Focused.ID()); s != nil {
					s.LastTotalContent = ""
				}
				m.updateViewport()
				return m, nil
			}
		}

		// Autocomplete takes priority when active
		if m.ShowAutocomplete {
			switch msg.String() {
			case "up":
				m.AutocompleteIndex = max(0, m.AutocompleteIndex-1)
				return m, nil
			case "down", "tab":
				m.AutocompleteIndex = min(len(m.AutocompleteItems)-1, m.AutocompleteIndex+1)
				return m, nil
			case "enter":
				m = m.acceptAutocomplete()
				// Fall through to normal "enter" handling for submission
			case "esc":
				m.ShowAutocomplete = false
				return m, nil
			}
		}

		switch msg.String() {
		case "esc", "ctrl+g":
			if msg.String() == "esc" {
				if m.ShowFilePicker {
					m.ShowFilePicker = false
					m.Mode = ViewChat
					return m, nil
				}
				if m.EscConfirmPending {
					m.EscConfirmPending = false
					m.updateViewport()
					return m, nil
				}
				if m.Mode == ViewCommitLog {
					if m.CommitDetail != "" {
						m.CommitDetail = ""
						m.updateViewport()
						return m, nil
					}
					m.Mode = ViewChat
					m.CommitEntries = nil
					focusedState.RenderedHistory = nil
					m.updateViewport()
					return m, nil
				}
				if m.Mode != ViewChat {
					m.Mode = ViewChat
					focusedState.RenderedHistory = nil
					m.updateViewport()
					return m, nil
				}
				// Main view Esc — always show confirmation
				m.escBgContent = m.Viewport.View()
				m.EscConfirmPending = true
				if s := m.GetAgentState(m.Focused.ID()); s != nil {
					s.LastTotalContent = ""
				}
				m.updateViewport()
				return m, nil
			}
			return m.interruptFocusedAgent()

		case "enter":
			if m.ShowFilePicker {
				return m, nil
			}
			input := strings.TrimPrefix(m.Input.Value(), "> ")
			if strings.TrimSpace(input) == "" {
				return m, nil
			}

			// Slash commands (trim spaces so autocomplete-added trailing space still works)
			cmd := strings.TrimSpace(input)
			if cmd == "/quit" {
				return m, tea.Quit
			}
			if cmd == "/help" {
				m.Input.Reset()
				m.Input.SetValue("> ")
				m.Mode = ViewHelp
				focusedState.RenderedHistory = nil
				m.updateLayout()
				return m, nil
			}
			if cmd == "/clear" {
				m.Input.Reset()
				m.Input.SetValue("> ")
				m.Focused.Reset()
				for _, state := range m.AgentStates {
					state.RenderedHistory = nil
					state.CumulativeTokenCount = 0
					state.CachedHistoryLen = 0
					state.CachedHistoryTokens = 0
					state.LastTotalContent = ""
				}
				m.LastFocusedID = ""
				m.updateViewport()
				m.ToastMessage = "conversation cleared"
				m.ToastExpireTime = time.Now().UnixMilli() + 3000
				clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
					return clearToastMsg{}
				})
				return m, clearCmd
			}
			if cmd == "/log" {
				m.Input.Reset()
				m.Input.SetValue("> ")
				entries, err := git.LogCommits(m.CWD, 30)
				if err != nil {
					m.Err = err
					return m, nil
				}
				m.CommitEntries = entries
				m.CommitIndex = 0
				m.CommitDetail = ""
				m.Mode = ViewCommitLog
				m.updateViewport()
				return m, nil
			}

			// Preflight context check
			maxTokens := m.Focused.MaxTokens()
			if focusedState.State == StateIdle && maxTokens > 0 && !focusedState.ContextWarningShown {
				// Use 10% safety margin (90% threshold)
				threshold := 0.9
				if float64(focusedState.CumulativeTokenCount) >= float64(maxTokens)*threshold {
					focusedState.State = StateContextWarning
					focusedState.ContextWarningShown = true
					m.updateViewport()
					return m, nil
				}
			}

			// Re-validate attachments in case the model changed since file selection
			if len(m.AttachedFiles) > 0 && !m.Focused.SupportsVision() {
				var filtered []string
				for _, f := range m.AttachedFiles {
					data, err := os.ReadFile(f)
					if err != nil {
						continue
					}
					mimeType := http.DetectContentType(data)
					if !strings.HasPrefix(mimeType, "image/") {
						filtered = append(filtered, f)
					}
				}
				if len(filtered) != len(m.AttachedFiles) {
					m.AttachedFiles = filtered
					focusedState.StatusText = "Images dropped: model no longer supports vision"
					return m, nil
				}
			}

			if err := m.Focused.Submit(input, m.AttachedFiles); err != nil {
				m.Err = err
				return m, nil
			}

			// Save to input history (avoid consecutive duplicates)
			if len(m.InputHistory) == 0 || m.InputHistory[len(m.InputHistory)-1] != input {
				m.InputHistory = append(m.InputHistory, input)
			}
			m.HistoryIndex = -1
			m.HistoryWorking = ""

			m.Input.Reset()
			m.Input.SetValue("> ")
			m.AttachedFiles = nil // Clear attachments after submit

			// Only update state to thinking if it was idle, else let it stay in its current busy state
			if focusedState.State == StateIdle || focusedState.State == StateContextWarning {
				focusedState.State = StateThinking
				focusedState.ContextWarningShown = false // Reset after successful submission
			}
			// Token count will be calculated in ContentEvent handler
			m.updateViewport()
			return m, nil

		case "alt+enter":
			m.Input.InsertString("\n")
			return m, nil

		case "shift+home":
			m.Viewport.GotoTop()
			m.updateViewport()
			return m, nil

		case "shift+end":
			m.Viewport.GotoBottom()
			m.updateViewport()
			return m, nil

		case "home":
			if strings.TrimPrefix(m.Input.Value(), "> ") == "" {
				m.Viewport.GotoTop()
				m.updateViewport()
				return m, nil
			}
			return m, nil

		case "end":
			if strings.TrimPrefix(m.Input.Value(), "> ") == "" {
				m.Viewport.GotoBottom()
				m.updateViewport()
				return m, nil
			}
			return m, nil

		case "up":
			if m.Mode == ViewChat {
				return m.navigateHistory(-1), nil
			}

		case "down":
			if m.Mode == ViewChat {
				return m.navigateHistory(1), nil
			}

		case "tab":
			// Allow focus switching regardless of agent state
			all := []common.Orchestrator{m.Root}
			for _, child := range m.Root.Children() {
				if !m.GetAgentState(child.ID()).Closed {
					all = append(all, child)
				}
			}

			idx := -1
			for i, a := range all {
				if a.ID() == m.Focused.ID() {
					idx = i
					break
				}
			}

			next := (idx + 1) % len(all)
			m.Focused = all[next]
			// Initialize state if missing
			m.GetAgentState(m.Focused.ID())
			m.updateViewport()
			return m, nil

		case "y", "Y":
			if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil && strings.TrimPrefix(m.Input.Value(), "> ") == "" {
				focusedState.PendingConfirm.ResultCh <- "y"
				focusedState.PendingConfirm = nil
				focusedState.State = StateThinking
				m.updateViewport()
				return m, nil
			}

		case "n", "N":
			if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil && strings.TrimPrefix(m.Input.Value(), "> ") == "" {
				focusedState.PendingConfirm.ResultCh <- "n"
				focusedState.PendingConfirm = nil
				focusedState.State = StateThinking
				m.updateViewport()
				return m, nil
			}

		case "s", "S", "p", "P", "g", "G":
			if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil && strings.TrimPrefix(m.Input.Value(), "> ") == "" {
				focusedState.PendingConfirm.ResultCh <- msg.String()
				focusedState.PendingConfirm = nil
				focusedState.State = StateThinking
				m.updateViewport()
				return m, nil
			}

		}

	case OrchestratorEventMsg:
		s := m.GetAgentState(msg.Event.OrchestratorID())
		now := time.Now().UnixMilli()

		switch event := msg.Event.(type) {
		case common.ContentEvent:
			s.StreamingState = event
			if s.State != StateConfirmTool {
				s.State = StateStreaming
			}
			s.Usage = event.Usage
			// Update token count: use real usage if available, otherwise estimate
			if event.Usage.TotalTokens > 0 {
				s.CumulativeTokenCount = event.Usage.TotalTokens
				s.LastRealTokenCount = event.Usage.TotalTokens
				s.CachedHistoryLen = len(m.Focused.History())
			} else {
				orch := m.FindOrchestrator(event.ID)
				if orch == nil {
					orch = m.Focused
				}
				history := orch.History()
				if len(history) != s.CachedHistoryLen {
					s.CachedHistoryTokens = common.CalculateHistoryTokens(history, orch.SystemPrompt(), orch.ToolDefinitions())
					s.CachedHistoryLen = len(history)
				}
				s.CumulativeTokenCount = s.CachedHistoryTokens + common.EstimateEventTokens(event)
			}

			// Throttle viewport updates to ~33 FPS during streaming
			if event.ID == m.Focused.ID() {
				if now-s.LastRenderTime > 30 {
					m.updateViewport()
				}
			}
		case common.StatusEvent:
			switch event.Status {
			case "thinking":
				if s.State != StateConfirmTool {
					s.State = StateThinking
				}
				s.StatusText = "Working..."
				s.StreamingState = common.ContentEvent{ID: event.ID}
				// Clear streaming render cache for new turn
				s.StreamingStyledCache = ""
				s.StreamingChunkCount = 0
			case "closed":
				s.State = StateIdle
				s.StatusText = "Closed"
				s.Closed = true
				// If the focused agent closed, switch back to parent (if any) or root
				if event.ID == m.Focused.ID() && s.State == StateIdle {
					if m.Focused.Parent() != nil {
						m.Focused = m.Focused.Parent()
					} else {
						m.Focused = m.Root
					}
					m.updateViewport()
				}
			case "error":
				s.State = StateIdle
				if event.Error != nil && event.Error.Error() == "image_unsupported" {
					s.StatusText = "Model does not support images"
					s.RenderedHistory = nil // Re-render to remove rolled-back message
				} else {
					s.StatusText = fmt.Sprintf("Error: %v", event.Error)
					s.Error = event.Error
				}
				// We don't clear rendered history so user can see what happened
			default:
				s.State = StateIdle
				s.StatusText = "Ready"
				s.RenderedHistory = nil
				s.StreamingStyledCache = ""
				s.StreamingChunkCount = 0
			}
			if event.ID == m.Focused.ID() {
				m.updateViewport()
			}
		case common.ChildAddedEvent:
			s.StatusText = "Subagent spawned"
			m.updateViewport()
		case common.StopRequestedEvent:
			s.PendingStop = false
			s.State = StateIdle
			s.StatusText = "Stopped"
			s.RenderedHistory = nil
			s.StreamingStyledCache = ""
			s.StreamingChunkCount = 0
			if event.ID == m.Focused.ID() {
				m.updateViewport()
			}
		case common.MessageQueuedEvent:
			if event.ID == m.Focused.ID() {
				m.updateViewport()
			}
		}

	case ConfirmRequestMsg:
		s := m.GetAgentState(msg.OrchestratorID)
		s.State = StateConfirmTool
		s.PendingConfirm = &msg
		m.updateViewport()
		return m, nil

	}

	return m, nil
}

func (m *Model) updateLayout() {
	if m.Width == 0 || m.Height == 0 {
		return
	}

	availableWidth := m.Width
	m.Input.SetWidth(availableWidth - 8)

	m.Viewport.SetWidth(availableWidth)
	vHeight := m.Height - InputHeight - StatusBarHeight - AppPadding

	// Reserve space for autocomplete dropdown
	if m.ShowAutocomplete && len(m.AutocompleteItems) > 0 {
		autoH := min(len(m.AutocompleteItems), 6) + 2 // items + border
		vHeight -= autoH
	}

	if vHeight < 1 {
		vHeight = 1
	}
	m.Viewport.SetHeight(vHeight)

	// Ensure file picker also respects the layout height to prevent pushing the status bar off-screen
	// We subtract StatusBarHeight. If we have a 2-line picker status bar, we subtract 3.
	fpHeight := m.Height - 3
	if fpHeight < 1 {
		fpHeight = 1
	}
	m.FilePicker.SetHeight(fpHeight)

	m.updateViewport()
}

// updateAutocomplete checks if the input looks like a slash command and updates
// the autocomplete dropdown items.
func (m *Model) updateAutocomplete() {
	input := strings.TrimPrefix(m.Input.Value(), "> ")

	// Only show autocomplete when input starts with "/" and has no space yet
	if strings.HasPrefix(input, "/") && !strings.Contains(input, " ") {
		prefix := strings.ToLower(input)
		var matches []string
		for _, cmd := range AvailableCommands {
			if strings.HasPrefix(strings.ToLower(cmd), prefix) {
				matches = append(matches, cmd)
			}
		}
		if len(matches) > 0 {
			m.ShowAutocomplete = true
			m.AutocompleteItems = matches
			if m.AutocompleteIndex >= len(matches) {
				m.AutocompleteIndex = 0
			}
			return
		}
	}

	m.ShowAutocomplete = false
	m.AutocompleteItems = nil
	m.AutocompleteIndex = 0
}

// acceptAutocomplete replaces the current input with the selected command.
func (m Model) acceptAutocomplete() Model {
	if m.AutocompleteIndex >= 0 && m.AutocompleteIndex < len(m.AutocompleteItems) {
		selected := m.AutocompleteItems[m.AutocompleteIndex]
		m.Input.SetValue("> " + selected + " ")
		m.Input.CursorEnd()
	}
	m.ShowAutocomplete = false
	m.AutocompleteItems = nil
	m.AutocompleteIndex = 0
	return m
}

// navigateHistory navigates the input history by `dir` steps (+1 forward, -1 backward).
// When first entering history browsing, the current input is saved as the "working"
// buffer so it can be restored when the user navigates past the newest entry.
func (m Model) navigateHistory(dir int) Model {
	currentInput := strings.TrimPrefix(m.Input.Value(), "> ")
	historyLen := len(m.InputHistory)

	if historyLen == 0 {
		return m
	}

	if m.HistoryIndex == -1 {
		m.HistoryWorking = currentInput
		if dir < 0 {
			// First press of ↑: go to the newest (last) entry
			m.HistoryIndex = historyLen - 1
			m.Input.SetValue("> " + m.InputHistory[m.HistoryIndex])
			m.Input.CursorEnd()
			return m
		}
		return m
	}

	newIndex := m.HistoryIndex + dir

	if newIndex < 0 {
		// Already at the oldest entry
		return m
	}

	if newIndex >= historyLen {
		// Past the newest entry: restore working buffer
		m.HistoryIndex = -1
		m.Input.SetValue("> " + m.HistoryWorking)
		m.Input.CursorEnd()
		return m
	}

	m.HistoryIndex = newIndex
	m.Input.SetValue("> " + m.InputHistory[newIndex])
	m.Input.CursorEnd()
	return m
}

func (m Model) interruptFocusedAgent() (Model, tea.Cmd) {
	focusedState := m.GetAgentState(m.Focused.ID())
	if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil {
		focusedState.PendingConfirm.ResultCh <- "n"
		focusedState.PendingConfirm = nil
		focusedState.PendingStop = true
		focusedState.State = StateStopping
		focusedState.StatusText = "Stopping..."
		focusedState.TokenCount = 0
		m.Focused.Cancel()
		m.updateViewport()
		return m, nil
	}
	if focusedState.State == StateThinking || focusedState.State == StateStreaming {
		focusedState.PendingStop = true
		focusedState.State = StateStopping
		focusedState.StatusText = "Stopping..."
		focusedState.TokenCount = 0
		m.Focused.Cancel()
		m.updateViewport()
		return m, nil
	}
	return m, nil
}
