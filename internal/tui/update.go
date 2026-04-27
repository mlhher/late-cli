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

	// Global Key Handling (Ctrl+C)
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.String() == "ctrl+c" {
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
		case "y", "Y", "n", "N":
			if stateBefore == StateConfirmTool {
				forwardToInput = false
			}
		}
	}

	// Update Sub-models
	if forwardToInput {
		m.Input, tiCmd = m.Input.Update(msg)

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
		case "esc":
			if m.Mode != ViewChat {
				m.Mode = ViewChat
				focusedState.RenderedHistory = nil
				m.updateViewport()
				return m, nil
			}
			return m, tea.Quit

		case "enter":
			if focusedState.State == StateIdle {
				input := strings.TrimPrefix(m.Input.Value(), "> ")
				if strings.TrimSpace(input) == "" {
					return m, nil
				}

				if err := m.Focused.Submit(input); err != nil {
					m.Err = err
					return m, nil
				}
				m.Input.Reset()
				m.Input.SetValue("> ")
				focusedState.State = StateThinking
				// Token count will be calculated in ContentEvent handler
				m.updateViewport()
				return m, nil
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
			if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil {
				focusedState.PendingConfirm.ResultCh <- true
				focusedState.PendingConfirm = nil
				focusedState.State = StateThinking
				m.updateViewport()
				return m, nil
			}

		case "n", "N":
			if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil {
				focusedState.PendingConfirm.ResultCh <- false
				focusedState.PendingConfirm = nil
				focusedState.State = StateThinking
				m.updateViewport()
				return m, nil
			}

		case "ctrl+g":
			if focusedState.State == StateConfirmTool && focusedState.PendingConfirm != nil {
				focusedState.PendingConfirm.ResultCh <- false
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
			if event.Usage.TotalTokens > 0 {
				s.CumulativeTokenCount = event.Usage.TotalTokens
				s.LastRealTokenCount = event.Usage.TotalTokens
				s.CachedHistoryLen = len(m.Focused.History())
			} else {
				// Fallback to estimation if no real usage data yet
				newContentTokens := common.EstimateEventTokens(event)

				orch := m.FindOrchestrator(event.ID)
				if orch == nil {
					orch = m.Focused
				}
				history := orch.History()

				if s.LastRealTokenCount > 0 && len(history) >= s.CachedHistoryLen {
					// Use last real count as baseline and add estimation for new content since then
					baseline := s.LastRealTokenCount
					// Add messages added since the last real count (e.g. the new user prompt)
					for i := s.CachedHistoryLen; i < len(history); i++ {
						baseline += common.EstimateMessageTokens(history[i])
					}
					s.CumulativeTokenCount = baseline + newContentTokens
				} else {
					// Full estimation fallback (accounting for system prompt and tools)
					if len(history) != s.CachedHistoryLen {
						s.CachedHistoryTokens = common.CalculateHistoryTokens(history, orch.SystemPrompt(), orch.ToolDefinitions())
						s.CachedHistoryLen = len(history)
					}
					s.CumulativeTokenCount = s.CachedHistoryTokens + newContentTokens
				}
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
				if event.ID == m.Focused.ID() {
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
