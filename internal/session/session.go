package session

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session manages the chat state and interacts with the LLM client.
type Session struct {
	client       *client.Client
	HistoryPath  string
	History      []client.ChatMessage
	systemPrompt string
	useTools     bool
	Registry     *tool.Registry

	// ContextRecoveryEnabled enables automatic history pruning and disk restore
	// when the context window approaches its limit. Enable via --context-rot.
	ContextRecoveryEnabled bool
}

func New(c *client.Client, historyPath string, history []client.ChatMessage, systemPrompt string, useTools bool) *Session {
	return &Session{
		client:       c,
		HistoryPath:  historyPath,
		History:      history,
		systemPrompt: systemPrompt,
		useTools:     useTools,
		Registry:     tool.NewRegistry(),
	}
}

// ExecuteTool executes a tool call and returns the response as a string.
func (s *Session) ExecuteTool(ctx context.Context, tc client.ToolCall) (string, error) {
	// First check registry
	t := s.Registry.Get(tc.Function.Name)
	if t == nil {
		return "", fmt.Errorf("tool not found: %s", tc.Function.Name)
	}
	return t.Execute(ctx, json.RawMessage(tc.Function.Arguments))
}

// AddToolResultMessage adds a tool response message to history.
func (s *Session) AddToolResultMessage(toolCallID, content string) error {
	s.History = append(s.History, client.ChatMessage{
		Role:       "tool",
		ToolCallID: toolCallID,
		Content:    content,
	})
	return s.saveAndNotify()
}

// AddAssistantMessageWithTools adds an assistant message with tool calls.
func (s *Session) AddAssistantMessageWithTools(content string, reasoning string, toolCalls []client.ToolCall) error {
	s.History = append(s.History, client.ChatMessage{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
		ToolCalls:        toolCalls,
	})
	return s.saveAndNotify()
}

func (s *Session) GetToolDefinitions() []client.ToolDefinition {
	var defs []client.ToolDefinition
	for _, t := range s.Registry.All() {
		// Skip bash tool if disabled is handled by registry being empty of it
		defs = append(defs, client.ToolDefinition{
			Type: "function",
			Function: client.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return defs
}

// AddUserMessage adds a user message to history and persists it.
func (s *Session) AddUserMessage(content string) error {
	s.History = append(s.History, client.ChatMessage{Role: "user", Content: content})
	return s.saveAndNotify()
}

// AddAssistantMessage adds an assistant message to history and persists it.
func (s *Session) AddAssistantMessage(content, reasoning string) error {
	s.History = append(s.History, client.ChatMessage{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
	})
	return s.saveAndNotify()
}

// AppendToLastMessage appends content to the last message (continuation).
func (s *Session) AppendToLastMessage(content, reasoning string) error {
	if len(s.History) == 0 {
		return fmt.Errorf("no history to append to")
	}
	lastIdx := len(s.History) - 1
	s.History[lastIdx].Content += content
	if reasoning != "" {
		if s.History[lastIdx].ReasoningContent != "" {
			s.History[lastIdx].ReasoningContent += "\n" + reasoning
		} else {
			s.History[lastIdx].ReasoningContent = reasoning
		}
	}
	return s.saveAndNotify()
}

// StartStream initiates a streaming response.
// It returns a standard Go channel for results and error.
func (s *Session) StartStream(ctx context.Context, extraBody map[string]any) (<-chan common.StreamResult, <-chan error) {
	outCh := make(chan common.StreamResult)
	errCh := make(chan error, 1)

	// Prepare messages with system prompt
	messages := make([]client.ChatMessage, 0, len(s.History)+1)
	if s.systemPrompt != "" {
		messages = append(messages, client.ChatMessage{Role: "system", Content: s.systemPrompt})
	}
	messages = append(messages, s.History...)

	req := client.ChatCompletionRequest{
		Messages:  messages,
		ExtraBody: extraBody,
	}

	if s.useTools {
		req.Tools = s.GetToolDefinitions()
	}

	streamOut, streamErr := s.client.ChatCompletionStream(ctx, req)

	go func() {
		defer close(outCh)
		defer close(errCh)

		for {
			select {
			case chunk, ok := <-streamOut:
				if !ok {
					return
				}
				var content, reasoning string
				var toolCalls []client.ToolCall
				if len(chunk.Choices) > 0 {
					content = chunk.Choices[0].Delta.Content
					reasoning = chunk.Choices[0].Delta.ReasoningContent
					toolCalls = chunk.Choices[0].Delta.ToolCalls
				}

				res := common.StreamResult{
					Content:          content,
					ReasoningContent: reasoning,
					ToolCalls:        toolCalls,
					Usage:            chunk.Usage,
				}

				select {
				case outCh <- res:
				case <-ctx.Done():
					return
				}

			case err, ok := <-streamErr:
				if !ok {
					return
				}
				select {
				case errCh <- err:
				case <-ctx.Done():
					return
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return outCh, errCh
}

// Impersonate returns a raw completion suggestion using the legacy format.
func (s *Session) Impersonate(ctx context.Context) (string, error) {
	var sb strings.Builder
	for _, msg := range s.History {
		sb.WriteString(fmt.Sprintf("%s\n%s\n", msg.Role, msg.Content))
	}
	prompt := sb.String() + "user\n"

	req := client.CompletionRequest{
		Prompt:    prompt,
		Stop:      []string{"\n", ""},
		N_Predict: 50,
	}

	resp, err := s.client.Completion(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// GenerateSessionMeta creates metadata from session state
func (s *Session) GenerateSessionMeta() SessionMeta {
	title := "Untitled Session"
	lastPrompt := ""

	if len(s.History) > 0 {
		// Find first user message for title
		for _, msg := range s.History {
			if msg.Role == "user" && title == "Untitled Session" {
				truncated := msg.Content
				if len(truncated) > 100 {
					truncated = truncateUTF8(truncated, 100)
				}
				title = truncated
				break
			}
		}
		// Last user message for last prompt
		for i := len(s.History) - 1; i >= 0; i-- {
			if s.History[i].Role == "user" {
				lastPrompt = s.History[i].Content
				if len(lastPrompt) > 50 {
					lastPrompt = truncateUTF8(lastPrompt, 50)
				}
				break
			}
		}
	}

	id := filepath.Base(s.HistoryPath)
	id = strings.TrimSuffix(id, ".json")

	return SessionMeta{
		ID:             id,
		Title:          title,
		CreatedAt:      time.Now(),
		LastUpdated:    time.Now(),
		HistoryPath:    s.HistoryPath,
		LastUserPrompt: lastPrompt,
		MessageCount:   len(s.History),
	}
}

// UpdateSessionMetadata updates the session metadata file
func (s *Session) UpdateSessionMetadata() error {
	meta := s.GenerateSessionMeta()
	return SaveSessionMeta(meta)
}

// SystemPrompt returns the system prompt for this session
func (s *Session) SystemPrompt() string {
	return s.systemPrompt
}

func (s *Session) saveAndNotify() error {
	if len(s.History) == 0 {
		return nil
	}
	if s.HistoryPath == "" {
		return nil // Skip saving if no path provided (e.g., subagents)
	}
	if err := SaveHistory(s.HistoryPath, s.History); err != nil {
		return err
	}
	return s.UpdateSessionMetadata()
}

func (s *Session) Client() *client.Client {
	return s.client
}

func (s *Session) IsLlamaCPP() bool {
	return s.client.IsLlamaCPP()
}

// PruneAndRestoreFromDisk performs a deterministic context-recovery reset.
// It preserves the last 10 messages (trimmed to a clean user-turn boundary),
// optionally re-injects the on-disk implementation plan, and syncs to disk.
// s.systemPrompt is never touched; StartStream re-injects it automatically.
func (s *Session) PruneAndRestoreFromDisk() error {
	// 1. Tail extraction: capture last 10 messages.
	tail := make([]client.ChatMessage, len(s.History[max(0, len(s.History)-10):]))
	copy(tail, s.History[max(0, len(s.History)-10):])

	// 2. Boundary guard: trim leading non-user messages.
	for len(tail) > 0 && tail[0].Role != "user" {
		tail = tail[1:]
	}

	// 3. Structural sanitizer: remove any assistant message whose tool_calls
	// are not fully resolved within the tail. This prevents 400 errors caused
	// by orphaned tool_call_ids when the tail window splits a tool exchange.
	tail = sanitizeTailToolCalls(tail)

	// 4. Re-apply boundary guard in case sanitization exposed a new non-user head.
	for len(tail) > 0 && tail[0].Role != "user" {
		tail = tail[1:]
	}

	// 5. History reset (s.systemPrompt is a separate field and is unaffected).
	s.History = []client.ChatMessage{}

	// 6. Mission injection: read implementation_plan.md from CWD.
	if planBytes, err := os.ReadFile("implementation_plan.md"); err == nil {
		if len(planBytes) < 8000 {
			s.History = append(s.History, client.ChatMessage{
				Role:    "user",
				Content: "RESTORED MISSION PLAN: " + string(planBytes),
			})
		}
	}

	// 7. Continuity restoration: re-append the sanitized tail.
	s.History = append(s.History, tail...)

	// 8. Persistence sync: write the pruned history to disk immediately.
	return s.saveAndNotify()
}

// sanitizeTailToolCalls removes assistant messages whose tool_calls are not
// fully resolved within tail (i.e. the corresponding tool results were cut
// by the 10-message window). Sending unresolved tool_calls to an OpenAI-
// compatible API produces a 400 error.
func sanitizeTailToolCalls(tail []client.ChatMessage) []client.ChatMessage {
	// Build the set of tool_call_ids that have results present in the tail.
	resolved := make(map[string]bool)
	for _, m := range tail {
		if m.Role == "tool" && m.ToolCallID != "" {
			resolved[m.ToolCallID] = true
		}
	}

	var result []client.ChatMessage
	i := 0
	for i < len(tail) {
		m := tail[i]
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// Check every tool call in this assistant turn is resolved.
			allResolved := true
			for _, tc := range m.ToolCalls {
				if !resolved[tc.ID] {
					allResolved = false
					break
				}
			}
			if !allResolved {
				// Drop this assistant message and any immediately following
				// tool messages that belong to it.
				i++
				for i < len(tail) && tail[i].Role == "tool" {
					i++
				}
				continue
			}
		}
		result = append(result, m)
		i++
	}
	return result
}
