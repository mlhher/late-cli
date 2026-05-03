package tui

import (
	"fmt"
	"late/internal/common"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// StreamMsg is the TUI-wrapper for session stream events
type StreamMsg struct {
	Result common.StreamResult
	Err    error
	Done   bool
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	// Global Key Handling (Ctrl+C, Ctrl+D)
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			return m, tea.Quit
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
	if _, ok := msg.(tea.KeyMsg); ok {
		stateBefore = m.GetAgentState(m.Focused.ID()).State
	}

	// Main Chat Update Logic
	newM, cmd := m.updateChat(msg)
	m = newM

	// Filter key events that were consumed by updateChat during confirmation
	forwardToInput := true
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "y", "Y", "n", "N", "s", "S", "p", "P", "g", "G":
			if stateBefore == StateConfirmTool && strings.TrimPrefix(m.Input.Value(), "> ") == "" {
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
		case "up", "down", "pgup", "pgdown", "home", "end":
			forwardToViewport = true
		default:
			// Only forward other keys if we are NOT typing (e.g. in a modal or viewing)
			forwardToViewport = (m.GetAgentState(m.Focused.ID()).State != StateIdle)
		}
	case tea.MouseMsg:
		forwardToViewport = true
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

	return m, tea.Batch(cmd, tiCmd, vpCmd, spCmd)
}

func (m Model) updateChat(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		focusedState := m.GetAgentState(m.Focused.ID())
		switch msg.String() {
		case "esc", "ctrl+g":
			if msg.String() == "esc" && m.Mode != ViewChat {
				m.Mode = ViewChat
				focusedState.RenderedHistory = nil
				m.updateViewport()
				return m, nil
			}
			return m.interruptFocusedAgent()

		case "enter":
			input := strings.TrimPrefix(m.Input.Value(), "> ")
			if strings.TrimSpace(input) == "" {
				return m, nil
			}

			if focusedState.State == StateIdle || focusedState.State == StateContextWarning {
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

				if err := m.Focused.Submit(input); err != nil {
					m.Err = err
					return m, nil
				}
				m.Input.Reset()
				m.Input.SetValue("> ")
				focusedState.State = StateThinking
				focusedState.ContextWarningShown = false // Reset after successful submission
				// Token count will be calculated in ContentEvent handler
				m.updateViewport()
				return m, nil
			} else {
				// Queue message if agent is busy
				focusedState.QueuedMessages = append(focusedState.QueuedMessages, input)
				m.Input.Reset()
				m.Input.SetValue("> ")
				m.updateViewport()
				return m, nil
			}

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
				// Process next queued message if any
				if len(s.QueuedMessages) > 0 {
					next := s.QueuedMessages[0]
					s.QueuedMessages = s.QueuedMessages[1:]
					orch := m.FindOrchestrator(event.ID)
					if orch != nil {
						if err := orch.Submit(next); err != nil {
							m.Err = err
						} else {
							s.State = StateThinking
							s.Closed = false // Re-open if we submit
						}
					}
				}
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
				s.StatusText = fmt.Sprintf("Error: %v", event.Error)
				s.Error = event.Error
				// We don't clear rendered history so user can see what happened
			default:
				s.State = StateIdle
				s.StatusText = "Ready"
				s.RenderedHistory = nil
				s.StreamingStyledCache = ""
				s.StreamingChunkCount = 0

				// Process next queued message if any
				if len(s.QueuedMessages) > 0 {
					next := s.QueuedMessages[0]
					s.QueuedMessages = s.QueuedMessages[1:]
					orch := m.FindOrchestrator(event.ID)
					if orch != nil {
						if err := orch.Submit(next); err != nil {
							m.Err = err
						} else {
							s.State = StateThinking
						}
					}
				}
			}
			if event.ID == m.Focused.ID() {
				m.updateViewport()
			}
		case common.ChildAddedEvent:
			s.StatusText = fmt.Sprintf("Subagent '%s' Spawned (Tab to switch)", event.Child.ID())
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

	if vHeight < 1 {
		vHeight = 1
	}
	m.Viewport.SetHeight(vHeight)

	m.updateViewport()
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
