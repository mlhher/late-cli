package tui

import (
	"fmt"
	"late/internal/assets"
	"late/internal/common"
	"late/internal/config"
	"late/internal/git"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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

type composeFinishedMsg struct {
	content string
	err     error
}

// StartPromptMsg submits a prompt as soon as the TUI is ready.
type StartPromptMsg string

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	oldHeight := m.Input.Height()
	oldShowAuto := m.ShowAutocomplete
	oldAutoLen := len(m.AutocompleteItems)
	oldMode := m.Mode

	newModel, cmd := m.updateInternal(msg)

	if newModel.Input.Height() != oldHeight || newModel.ShowAutocomplete != oldShowAuto || len(newModel.AutocompleteItems) != oldAutoLen || newModel.Mode != oldMode {
		newModel.updateLayout()
	}
	return newModel, cmd
}

func (m Model) updateInternal(msg tea.Msg) (Model, tea.Cmd) {
	if prompt, ok := msg.(StartPromptMsg); ok {
		m.Input.SetValue("> " + string(prompt))
		m.Input.CursorEnd()
		return m, func() tea.Msg {
			return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
		}
	}

	if _, ok := msg.(clearToastMsg); ok {
		m.ToastMessage = ""
		m.ToastWarning = false
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
		if msg.String() == "ctrl+o" {
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
	if msg, ok := msg.(composeFinishedMsg); ok {
		if msg.err != nil {
			m.Err = msg.err
			return m, nil
		}
		m.Input.SetValue("> " + msg.content)
		m.Input.CursorEnd()
		return m, nil
	}

	// Snapshot state before updateChat processes the key and potentially changes it
	var stateBefore ValidationState
	escBefore := false
	wasAtExactStart := false
	wasAtExactEnd := false
	wasAtTopRow := false
	wasAtBottomRow := false
	if _, ok := msg.(tea.KeyMsg); ok {
		stateBefore = m.GetAgentState(m.Focused.ID()).State
		escBefore = m.EscConfirmPending
		wasAtExactStart = m.isAtExactInputStart()
		wasAtExactEnd = m.isAtExactInputEnd()
		wasAtTopRow = m.isAtTopRow()
		wasAtBottomRow = m.isAtBottomRow()
	}

	// Main Chat Update Logic
	newM, cmd := m.updateChat(msg)
	m = newM

	// Filter key events that were consumed by updateChat during confirmation
	forwardToInput := true

	if pasteMsg, ok := msg.(tea.PasteMsg); ok {
		if isBinary([]byte(pasteMsg.Content)) {
			forwardToInput = false
		} else {
			text := pasteMsg.Content
			lineCount := strings.Count(text, "\n") + 1
			if lineCount > 3 {
				placeholder := m.newPasteToken(lineCount, text)
				m.Input.InsertString(placeholder)

				m.ToastMessage = fmt.Sprintf("pasted %d lines (%d chars)", lineCount, len(text))
				m.ToastWarning = false
				m.ToastExpireTime = time.Now().UnixMilli() + 2500

				forwardToInput = false
			}
		}
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "y", "Y", "n", "N", "s", "S", "p", "P", "g", "G":
			if escBefore || (stateBefore == StateConfirmTool && strings.TrimPrefix(m.Input.Value(), "> ") == "") {
				forwardToInput = false
			}
		case "up":
			if m.Mode == ViewChat {
				if m.ShowAutocomplete || wasAtExactStart {
					forwardToInput = false
				} else if wasAtTopRow {
					m.Input.SetCursorColumn(2)
					forwardToInput = false
				}
			} else {
				forwardToInput = false
			}
		case "down":
			if m.Mode == ViewChat {
				if m.ShowAutocomplete || wasAtExactEnd {
					forwardToInput = false
				} else if wasAtBottomRow {
					m.Input.CursorEnd()
					forwardToInput = false
				}
			} else {
				forwardToInput = false
			}
		}
	}

	if m.Mode == ViewModelPicker || m.Mode == ViewRewind || m.Mode == ViewCommitLog || m.Mode == ViewHelp {
		forwardToInput = false
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
		if isBinary([]byte(pastedText)) {
			m.Input.SetValue(m.Input.Value()[:m.lastInputLen])
			m.Input.CursorEnd()
			currentLen = len(m.Input.Value())
		} else {
			lineCount := strings.Count(pastedText, "\n") + 1
			charCount := currentLen - m.lastInputLen
			if lineCount > 3 {
				placeholder := m.newPasteToken(lineCount, pastedText)

				beforePaste := m.Input.Value()[:m.lastInputLen]
				m.Input.SetValue(beforePaste + placeholder)
				m.Input.CursorEnd()

				m.ToastMessage = fmt.Sprintf("pasted %d lines (%d chars)", lineCount, charCount)
				m.ToastWarning = false
				m.ToastExpireTime = time.Now().UnixMilli() + 2500
				clearCmd := tea.Tick(2500*time.Millisecond, func(t time.Time) tea.Msg {
					return clearToastMsg{}
				})
				m.lastInputLen = len(m.Input.Value())
				return m, clearCmd
			}
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
			if msg.String() == "pgup" || msg.String() == "home" {
				m.restoreFullHistoryForScroll()
			}
			forwardToViewport = true
		default:
			// Never forward character keys to the viewport to prevent conflicts with textarea input.
			// The viewport binds keys like space, j, k, d, u which cause shifting if typed.
			forwardToViewport = false
		}
	case tea.MouseWheelMsg:
		// Wheel events forwarded to viewport for scroll handling.
		// Bubbletea v2 dispatches these as a distinct type from MouseMsg.
		if msg.Mouse().Button == tea.MouseWheelUp {
			m.restoreFullHistoryForScroll()
		}
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
								m.ToastWarning = false
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

			// Content-based validation for image/video support
			data, err := os.ReadFile(file)
			if err != nil {
				m.Err = fmt.Errorf("failed to read file: %w", err)
			} else {
				mimeType := http.DetectContentType(data)
				isImage := strings.HasPrefix(mimeType, "image/")
				isVideo := strings.HasPrefix(mimeType, "video/")
				if (isImage || isVideo) && !m.Focused.SupportsVision() {
					focusedState := m.GetAgentState(m.Focused.ID())
					statusText := "Images not supported by current model"
					if isVideo {
						statusText = "Video not supported by current model"
					}
					focusedState.StatusText = statusText
				} else {
					m.AttachedFiles = append(m.AttachedFiles, file)
					m.ShowFilePicker = false
					m.Mode = ViewChat
					// Show toast with just the filename
					fname := filepath.Base(file)
					m.ToastMessage = "attached " + fname
					m.ToastWarning = false
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

		// Model picker view key handling
		if m.Mode == ViewModelPicker {
			switch msg.String() {
			case "up":
				m.ModelPickerAgentIndex = max(0, m.ModelPickerAgentIndex-1)
				m.updateViewport()
				return m, nil
			case "down":
				m.ModelPickerAgentIndex = min(len(m.ModelPickerAgents)-1, m.ModelPickerAgentIndex+1)
				m.updateViewport()
				return m, nil
			case "left":
				if len(m.ModelPickerAgents) > 0 && len(m.ModelPickerModels) > 0 {
					activeAgent := m.ModelPickerAgents[m.ModelPickerAgentIndex]
					currentSel := m.ModelPickerAgentSelections[activeAgent]
					newSel := max(0, currentSel-1)
					m.ModelPickerAgentSelections[activeAgent] = newSel
					m.updateViewport()
				}
				return m, nil
			case "right":
				if len(m.ModelPickerAgents) > 0 && len(m.ModelPickerModels) > 0 {
					activeAgent := m.ModelPickerAgents[m.ModelPickerAgentIndex]
					currentSel := m.ModelPickerAgentSelections[activeAgent]
					newSel := min(len(m.ModelPickerModels)-1, currentSel+1)
					m.ModelPickerAgentSelections[activeAgent] = newSel
					m.updateViewport()
				}
				return m, nil
			case "enter":
				if m.hasActiveAgent() {
					m.ToastMessage = "Models can be changed when all agents are idle"
					m.ToastWarning = true
					m.ToastExpireTime = time.Now().UnixMilli() + 3000
					clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
						return clearToastMsg{}
					})
					return m, clearCmd
				}

				var applyModelCmd tea.Cmd

				// Save choices to AppConfig
				if m.AppConfig != nil {
					stagedConfig := *m.AppConfig
					stagedConfig.AgentModels = make(map[string]string, len(m.AppConfig.AgentModels))
					for agent, modelRef := range m.AppConfig.AgentModels {
						stagedConfig.AgentModels[agent] = modelRef
					}
					for _, agent := range m.ModelPickerAgents {
						selIdx := m.ModelPickerAgentSelections[agent]
						modelRef := m.ModelPickerModels[selIdx]
						if modelRef == "default" {
							delete(stagedConfig.AgentModels, agent)
						} else {
							stagedConfig.AgentModels[agent] = modelRef
						}
					}
					// Write config to disk
					if err := config.SaveConfig(&stagedConfig); err != nil {
						m.Err = fmt.Errorf("failed to save config: %w", err)
						return m, nil
					}
					m.AppConfig.AgentModels = stagedConfig.AgentModels

					// Update ModelName and SubagentInfo dynamically
					if setting, ok := m.AppConfig.GetModelForAgent("orchestrator"); ok {
						m.ModelName = setting.Model
						if m.ApplyOrchestratorModel != nil {
							applyModelCmd = m.ApplyOrchestratorModel(setting)
						}
					} else {
						resolvedOpenAIConfig := config.ResolveOpenAISettings(m.AppConfig)
						m.ModelName = resolvedOpenAIConfig.Model
						if m.ApplyOrchestratorModel != nil {
							applyModelCmd = m.ApplyOrchestratorModel(config.ModelSetting{
								URL:   resolvedOpenAIConfig.BaseURL,
								Key:   resolvedOpenAIConfig.APIKey,
								Model: resolvedOpenAIConfig.Model,
							})
						}
					}

					var subagentInfos []string
					for _, sub := range assets.GetSubagents() {
						if setting, ok := m.AppConfig.GetModelForAgent(sub.Name); ok {
							subagentInfos = append(subagentInfos, fmt.Sprintf("%s:%s", sub.Name, setting.Model))
						}
					}
					if len(subagentInfos) > 0 {
						m.SubagentInfo = strings.Join(subagentInfos, ", ")
					} else {
						resolvedSubagentConfig := config.ResolveSubagentSettings(m.AppConfig, config.ResolveOpenAISettings(m.AppConfig))
						m.SubagentInfo = resolvedSubagentConfig.Model
					}
				}

				m.ToastMessage = "agent models updated"
				m.ToastWarning = false
				m.ToastExpireTime = time.Now().UnixMilli() + 3000
				clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
					return clearToastMsg{}
				})

				m.Mode = ViewChat
				focusedState.RenderedHistory = nil
				m.updateLayout()
				m.updateViewport()
				return m, tea.Batch(clearCmd, applyModelCmd)

			case "esc":
				m.Mode = ViewChat
				focusedState.RenderedHistory = nil
				m.updateLayout()
				m.updateViewport()
				return m, nil
			}
			return m, nil
		}

		// Rewind view key handling
		if m.Mode == ViewRewind {
			switch msg.String() {
			case "up":
				m.RewindIndex = max(0, m.RewindIndex-1)
				m.updateViewport()
				m.Viewport.SetYOffset(max(0, 2+m.RewindIndex*2-m.Viewport.Height()/2))
				return m, nil
			case "down":
				m.RewindIndex = min(len(m.RewindEntries)-1, m.RewindIndex+1)
				m.updateViewport()
				m.Viewport.SetYOffset(max(0, 2+m.RewindIndex*2-m.Viewport.Height()/2))
				return m, nil
			case "enter":
				if m.RewindIndex >= 0 && m.RewindIndex < len(m.RewindEntries) {
					entry := m.RewindEntries[m.RewindIndex]

					// Place the selected user message into the input box
					m.Input.Reset()
					m.Input.SetValue("> " + entry.Content)
					m.Input.CursorEnd()

					// Remove the selected user message and all subsequent messages from chat history
					if err := m.Focused.Rewind(entry.Index); err != nil {
						m.Err = err
						return m, nil
					}

					m.Mode = ViewChat
					m.RewindEntries = nil

					focusedState.RenderedHistory = nil
					focusedState.CachedHistoryLen = 0
					focusedState.CachedHistoryTokens = 0
					focusedState.LastTotalContent = ""
					focusedState.CumulativeTokenCount = common.CalculateHistoryTokens(
						m.Focused.History(),
						m.Focused.SystemPrompt(),
						m.Focused.ToolDefinitions(),
					)

					m.ToastMessage = "conversation rewound"
					m.ToastWarning = false
					m.ToastExpireTime = time.Now().UnixMilli() + 3000
					clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
						return clearToastMsg{}
					})

					m.updateViewport()
					return m, clearCmd
				}
				return m, nil
			case "esc":
				m.Mode = ViewChat
				m.RewindEntries = nil
				focusedState.RenderedHistory = nil
				m.updateViewport()
				return m, nil
			}
			return m, nil
		}

		// Commit log view key handling
		if m.Mode == ViewCommitLog {
			if m.CommitDetail != "" {
				switch msg.String() {
				case "esc", "enter":
					m.CommitDetail = ""
					m.updateViewport()
					return m, nil
				case "up":
					m.Viewport.SetYOffset(m.Viewport.YOffset() - 1)
					return m, nil
				case "down":
					m.Viewport.SetYOffset(m.Viewport.YOffset() + 1)
					return m, nil
				}
				return m, nil
			}
			switch msg.String() {
			case "up":
				m.CommitIndex = max(0, m.CommitIndex-1)
				m.updateViewport()
				m.Viewport.SetYOffset(max(0, 2+m.CommitIndex*3-m.Viewport.Height()/2))
				return m, nil
			case "down":
				m.CommitIndex = min(len(m.CommitEntries)-1, m.CommitIndex+1)
				m.updateViewport()
				m.Viewport.SetYOffset(max(0, 2+m.CommitIndex*3-m.Viewport.Height()/2))
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
			if strings.HasPrefix(input, "/compose") {
				if input == "/compose" || strings.HasPrefix(input, "/compose ") {
					text := strings.TrimPrefix(input, "/compose")
					text = strings.TrimSpace(text)

					tempFile, err := os.CreateTemp("", "late-compose-*.md")
					if err != nil {
						m.Err = err
						return m, nil
					}

					if text != "" {
						if _, err := tempFile.Write([]byte(text)); err != nil {
							tempFile.Close()
							os.Remove(tempFile.Name())
							m.Err = err
							return m, nil
						}
					}
					tempFile.Close()

					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vi"
					}

					c := exec.Command(editor, tempFile.Name())

					m.Input.Reset()
					m.Input.SetValue("> ")
					m.ShowAutocomplete = false
					m.AutocompleteItems = nil
					m.AutocompleteIndex = 0

					execCmd := tea.ExecProcess(c, func(err error) tea.Msg {
						defer os.Remove(tempFile.Name())
						if err != nil {
							return composeFinishedMsg{err: err}
						}
						data, err := os.ReadFile(tempFile.Name())
						if err != nil {
							return composeFinishedMsg{err: err}
						}
						content := strings.TrimRight(string(data), "\r\n")
						return composeFinishedMsg{content: content}
					})

					return m, execCmd
				}
			}
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
			if cmd == "/model" {
				m.Input.Reset()
				m.Input.SetValue("> ")
				if m.hasActiveAgent() {
					m.ToastMessage = "Models can be changed when all agents are idle"
					m.ToastWarning = true
					m.ToastExpireTime = time.Now().UnixMilli() + 3000
					clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
						return clearToastMsg{}
					})
					m.updateViewport()
					return m, clearCmd
				}
				m.Mode = ViewModelPicker

				// Populate agent types
				m.ModelPickerAgents = []string{"orchestrator"}
				for _, sub := range assets.GetSubagents() {
					m.ModelPickerAgents = append(m.ModelPickerAgents, sub.Name)
				}

				// Populate stable model references.
				m.ModelPickerModels = []string{"default"}
				if m.AppConfig != nil {
					for _, model := range m.AppConfig.Models {
						m.ModelPickerModels = append(m.ModelPickerModels, model.Reference())
					}
				}

				m.ModelPickerAgentIndex = 0
				m.ModelPickerAgentSelections = make(map[string]int)

				// Load current selections
				for _, agentName := range m.ModelPickerAgents {
					selectedModelRef := ""
					if m.AppConfig != nil && m.AppConfig.AgentModels != nil {
						selectedModelRef = m.AppConfig.AgentModels[agentName]
					}
					foundIdx := 0 // default to 0 ("default")
					for idx, modelRef := range m.ModelPickerModels {
						if modelRef == selectedModelRef {
							foundIdx = idx
							break
						}
					}
					m.ModelPickerAgentSelections[agentName] = foundIdx
				}

				focusedState.RenderedHistory = nil
				m.updateLayout()
				return m, nil
			}
			if cmd == "/new" {
				m.Input.Reset()
				m.Input.SetValue("> ")
				m.Focused.Reset()
				m.Pastes = make(map[string]string)
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
				m.ToastWarning = false
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
			if cmd == "/rewind" {
				m.Input.Reset()
				m.Input.SetValue("> ")
				history := m.Focused.History()
				var entries []RewindEntry
				for idx, msg := range history {
					if msg.Role == "user" {
						content := msg.Content.UIString()
						if content == "" {
							content = msg.Content.String()
						}
						entries = append(entries, RewindEntry{
							Index:   idx,
							Content: content,
						})
					}
				}
				if len(entries) == 0 {
					m.ToastMessage = "no messages to rewind to"
					m.ToastWarning = false
					m.ToastExpireTime = time.Now().UnixMilli() + 3000
					clearCmd := tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
						return clearToastMsg{}
					})
					m.updateViewport()
					return m, clearCmd
				}
				m.RewindEntries = entries
				m.RewindIndex = len(entries) - 1 // Default to last user message
				m.Mode = ViewRewind
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
					if !strings.HasPrefix(mimeType, "image/") && !strings.HasPrefix(mimeType, "video/") {
						filtered = append(filtered, f)
					}
				}
				if len(filtered) != len(m.AttachedFiles) {
					m.AttachedFiles = filtered
					focusedState.StatusText = "Media dropped: model no longer supports vision"
					return m, nil
				}
			}

			// Replace pasted placeholders with original content. Use a
			// single left-to-right pass so a paste whose content contains
			// another paste's placeholder token is never corrupted.
			expandedInput := expandPastes(input, m.Pastes)

			if err := m.Focused.Submit(expandedInput, m.AttachedFiles); err != nil {
				m.Err = err
				return m, nil
			}

			// Save to input history (avoid consecutive duplicates)
			if len(m.InputHistory) == 0 || m.InputHistory[len(m.InputHistory)-1] != expandedInput {
				m.InputHistory = append(m.InputHistory, expandedInput)
			}
			m.Pastes = make(map[string]string)
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
			if m.Mode == ViewChat && m.isAtExactInputStart() {
				return m.navigateHistory(-1), nil
			}

		case "down":
			if m.Mode == ViewChat && m.isAtExactInputEnd() {
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
	m.Input.SetWidth(availableWidth - 2)

	m.Viewport.SetWidth(availableWidth)
	vHeight := m.Height - (m.Input.Height() + 1) - StatusBarHeight - AppPadding
	if m.Mode == ViewModelPicker {
		vHeight = m.Height - 3 - StatusBarHeight - AppPadding
	}

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
		var matches []CommandDef
		for _, cmd := range AvailableCommands {
			if strings.HasPrefix(strings.ToLower(cmd.Name), prefix) {
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
		selected := m.AutocompleteItems[m.AutocompleteIndex].Name
		m.Input.SetValue("> " + selected + " ")
		m.Input.CursorEnd()
	}
	m.ShowAutocomplete = false
	m.AutocompleteItems = nil
	m.AutocompleteIndex = 0
	return m
}

func (m Model) isAtExactInputStart() bool {
	if m.Input.Line() != 0 {
		return false
	}
	info := m.Input.LineInfo()
	if info.RowOffset != 0 {
		return false
	}
	return m.Input.Column() <= 2
}

func (m Model) isAtExactInputEnd() bool {
	if m.Input.Line() != m.Input.LineCount()-1 {
		return false
	}
	info := m.Input.LineInfo()
	if info.RowOffset != info.Height-1 {
		return false
	}
	lines := strings.Split(m.Input.Value(), "\n")
	lastLine := lines[len(lines)-1]
	return m.Input.Column() == utf8.RuneCountInString(lastLine)
}

func (m Model) isAtTopRow() bool {
	if m.Input.Line() != 0 {
		return false
	}
	return m.Input.LineInfo().RowOffset == 0
}

func (m Model) isAtBottomRow() bool {
	if m.Input.Line() != m.Input.LineCount()-1 {
		return false
	}
	info := m.Input.LineInfo()
	return info.RowOffset == info.Height-1
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

// newPasteToken returns a human-readable placeholder for a multi-line paste
// (e.g. "[Pasted #5 lines 9f3a2c]") with a unique, collision-resistant suffix,
// records the mapping in m.Pastes, and returns the token. The random suffix
// makes it practically impossible for pasted content to contain a placeholder
// string, which previously caused the submit-time expansion to clobber one
// paste with another ("paste-placeholder collision on submit").
func (m *Model) newPasteToken(lineCount int, text string) string {
	if m.Pastes == nil {
		m.Pastes = make(map[string]string)
	}
	base := fmt.Sprintf("[Pasted #%d lines", lineCount)
	token := fmt.Sprintf("%s %08x]", base, rand.Uint32())
	for counter := 2; ; counter++ {
		if _, exists := m.Pastes[token]; !exists {
			break
		}
		token = fmt.Sprintf("%s %08x-%d]", base, rand.Uint32(), counter)
	}
	m.Pastes[token] = text
	return token
}

// expandPastes replaces each paste placeholder token in input with its
// original content. It scans left-to-right and only expands tokens that
// appear literally in input, never re-scanning already-expanded content, so a
// paste whose content itself contains a placeholder token (or any text that
// looks like one) is left untouched.
func expandPastes(input string, pastes map[string]string) string {
	if len(pastes) == 0 {
		return input
	}
	var b strings.Builder
	b.Grow(len(input))
	i := 0
	for i < len(input) {
		expanded := false
		for ph, orig := range pastes {
			if strings.HasPrefix(input[i:], ph) {
				b.WriteString(orig)
				i += len(ph)
				expanded = true
				break
			}
		}
		if !expanded {
			b.WriteByte(input[i])
			i++
		}
	}
	return b.String()
}

// isBinary reports whether the given bytes look like binary rather than
// text that should be injected into the chat input.
//
// A paste is treated as binary when any of the following hold:
//   - it contains a NUL byte (definitive binary signal),
//   - it is not valid UTF-8 (images, gzip blobs, encodings, etc.),
//   - more than ~10% of its (non-multibyte) bytes are raw control
//     characters other than benign whitespace.
//
// The whole slice is validated for UTF-8 (cheap, allocation-free) so a
// truncated multibyte sequence at the 8 KiB sampling boundary cannot
// produce a false positive. Control-char scanning is bounded to the
// first 8 KiB for performance on very large pastes.
func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	// Valid UTF-8 text is never binary, regardless of length.
	if !utf8.Valid(data) {
		return true
	}

	limit := len(data)
	if limit > 8192 {
		limit = 8192
	}

	// NUL byte is a definitive binary signal.
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}

	// Count raw control bytes (excluding benign whitespace) to catch binary
	// blobs that happen to be valid UTF-8. Only ASCII-range bytes can be raw
	// control characters; multibyte UTF-8 bytes (>= 0x80) are left alone.
	control := 0
	for i := 0; i < limit; i++ {
		b := data[i]
		if b >= 0x80 {
			continue
		}
		switch b {
		case '\t', '\n', '\r', '\v', '\f', ' ':
			// Benign whitespace — not a binary signal.
		default:
			if b < 0x20 || b == 0x7f {
				control++
			}
		}
	}
	if float64(control)/float64(limit) > 0.10 {
		return true
	}

	return false
}
