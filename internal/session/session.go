package session

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/assets"
	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
	"path/filepath"
	"strings"
	"time"
)

// Session manages the chat state and interacts with the LLM client.
type Session struct {
	client       *client.Client
	HistoryPath  string
	ID           string
	onEvent      func(common.Event)
	History      []client.ChatMessage
	systemPrompt string
	useTools     bool
	compressionThreshold int
	Registry     *tool.Registry
}

func New(c *client.Client, historyPath string, history []client.ChatMessage, systemPrompt string, useTools bool, compressionThreshold int) *Session {
	return &Session{
		client:       c,
		HistoryPath:  historyPath,
		History:      history,
		systemPrompt: systemPrompt,
		useTools:     useTools,
		compressionThreshold: compressionThreshold,
		Registry:     tool.NewRegistry(),
	}
}

// SetID sets the ID of the session.
func (s *Session) SetID(id string) {
	s.ID = id
}

// GetID returns the ID of the session.
func (s *Session) GetID() string {
	return s.ID
}

// SetOnEvent registers a callback function to be called when a relevant event occurs.
func (s *Session) SetOnEvent(fn func(common.Event)) {
	s.onEvent = fn
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

func (s *Session) AddToolResultMessage(toolCallID, content string) error {
	s.History = append(s.History, client.ChatMessage{
		Role:       "tool",
		ToolCallID: toolCallID,
		Content:    client.TextContent(content),
	})

	if err := s.saveAndNotify(); err != nil {
		return err
	}

	if s.compressionThreshold > 0 {
		tokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())
		if tokens > s.compressionThreshold {
			return s.SummarizeHistory(context.Background())
		}
	}

	return nil
}

func (s *Session) AddAssistantMessageWithTools(content string, reasoning string, toolCalls []client.ToolCall) error {
	s.History = append(s.History, client.ChatMessage{
		Role:             "assistant",
		Content:          client.TextContent(content),
		ReasoningContent: reasoning,
		ToolCalls:        toolCalls,
	})

	if err := s.saveAndNotify(); err != nil {
		return err
	}

	if s.compressionThreshold > 0 {
		tokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())
		if tokens > s.compressionThreshold {
			return s.SummarizeHistory(context.Background())
		}
	}

	return nil
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
	s.History = append(s.History, client.ChatMessage{Role: "user", Content: client.TextContent(content)})

	if err := s.saveAndNotify(); err != nil {
		return err
	}

	if s.compressionThreshold > 0 {
		tokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())
		if tokens > s.compressionThreshold {
			return s.SummarizeHistory(context.Background())
		}
	}

	return nil
}

// AddMessage adds an arbitrary message to history and persists it.
func (s *Session) AddMessage(msg client.ChatMessage) error {
	s.History = append(s.History, msg)

	if err := s.saveAndNotify(); err != nil {
		return err
	}

	if s.compressionThreshold > 0 {
		tokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())
		if tokens > s.compressionThreshold {
			return s.SummarizeHistory(context.Background())
		}
	}

	return nil
}

// AddAssistantMessage adds an assistant message to history and persists it.
func (s *Session) AddAssistantMessage(content, reasoning string) error {
	s.History = append(s.History, client.ChatMessage{
		Role:             "assistant",
		Content:          client.TextContent(content),
		ReasoningContent: reasoning,
	})

	if err := s.saveAndNotify(); err != nil {
		return err
	}

	if s.compressionThreshold > 0 {
		tokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())
		if tokens > s.compressionThreshold {
			return s.SummarizeHistory(context.Background())
		}
	}

	return nil
}

// AppendToLastMessage appends content to the last message (continuation).
func (s *Session) AppendToLastMessage(content, reasoning string) error {
	if len(s.History) == 0 {
		return fmt.Errorf("no history to append to")
	}
	lastIdx := len(s.History) - 1
	if len(s.History[lastIdx].Content.Parts) > 0 {
		// If it's multimodal, we append to the last text part if it exists, or add a new one
		// For now, let's just append to the simple text field if it's used, or the last part.
		// Actually, let's keep it simple: if Parts is not empty, append to the last part if it's text.
		found := false
		for i := len(s.History[lastIdx].Content.Parts) - 1; i >= 0; i-- {
			if s.History[lastIdx].Content.Parts[i].Type == client.ContentPartText {
				s.History[lastIdx].Content.Parts[i].Text += content
				found = true
				break
			}
		}
		if !found {
			s.History[lastIdx].Content.Parts = append(s.History[lastIdx].Content.Parts, client.ContentPart{
				Type: client.ContentPartText,
				Text: content,
			})
		}
	} else {
		s.History[lastIdx].Content.Text += content
	}
	if reasoning != "" {
		if s.History[lastIdx].ReasoningContent != "" {
			s.History[lastIdx].ReasoningContent += "\n" + reasoning
		} else {
			s.History[lastIdx].ReasoningContent = reasoning
		}
	}

	if err := s.saveAndNotify(); err != nil {
		return err
	}

	if s.compressionThreshold > 0 {
		tokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())
		if tokens > s.compressionThreshold {
			return s.SummarizeHistory(context.Background())
		}
	}

	return nil
}

// StartStream initiates a streaming response.
// It returns a standard Go channel for results and error.
func (s *Session) StartStream(ctx context.Context, extraBody map[string]any) (<-chan common.StreamResult, <-chan error) {
	outCh := make(chan common.StreamResult)
	errCh := make(chan error, 1)

	// Prepare messages with system prompt
	messages := make([]client.ChatMessage, 0, len(s.History)+1)
	if s.systemPrompt != "" {
		messages = append(messages, client.ChatMessage{Role: "system", Content: client.TextContent(s.systemPrompt)})
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
				var content, reasoning, finishReason string
				var toolCalls []client.ToolCall
				if len(chunk.Choices) > 0 {
					content = chunk.Choices[0].Delta.Content.String()
					reasoning = chunk.Choices[0].Delta.ReasoningContent
					toolCalls = chunk.Choices[0].Delta.ToolCalls
					finishReason = chunk.Choices[0].FinishReason
				}

				res := common.StreamResult{
					Content:          content,
					ReasoningContent: reasoning,
					ToolCalls:        toolCalls,
					Usage:            chunk.Usage,
					FinishReason:     finishReason,
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
		sb.WriteString(fmt.Sprintf("%s\n%s\n", msg.Role, msg.Content.String()))
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
				truncated := msg.Content.String()
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
				lastPrompt = s.History[i].Content.String()
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

// Client exposes the underlying LLM client.
func (s *Session) Client() *client.Client {
	return s.client
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

// SummarizeHistory constructs a summary of the current conversation history using the LLM client.
// The history is compressed and replaced in the session state.
func (s *Session) SummarizeHistory(ctx context.Context) error {
	if len(s.History) == 0 {
		return nil // Nothing to summarize
	}

	if s.onEvent != nil {
		s.onEvent(common.CompressionStartedEvent{ID: s.GetID()})
	}

	oldTokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())

	compressionPromptPath := "prompts/compression-prompt.md"
	summaryPrefixPath := "prompts/compression-summary-prefix.md"

	// Load Assets
	templateBytes, err := assets.PromptsFS.ReadFile(compressionPromptPath)
	if err != nil {
		return fmt.Errorf("failed to load compression prompt template (%s): %w", compressionPromptPath, err)
	}
	templateContent := string(templateBytes)

	prefixBytes, err := assets.PromptsFS.ReadFile(summaryPrefixPath)
	if err != nil {
		return fmt.Errorf("failed to load compression summary prefix (%s): %w", summaryPrefixPath, err)
	}
	prefixContent := string(prefixBytes)

	// Token Selection (Max 10k tokens)
	const maxTokens = 10000
	selectedUserMessages := make([]client.ChatMessage, 0)
	totalTokens := 0
	userMessageIndices := []int{}

	// Iterate backward to find the most recent user messages
	for i := len(s.History) - 1; i >= 0; i-- {
		msg := s.History[i]
		if msg.Role != "user" {
			continue
		}

		// Calculate tokens for this message against the current history context
		currentMsgTokens := common.CalculateHistoryTokens([]client.ChatMessage{msg}, s.systemPrompt, s.GetToolDefinitions())

		if totalTokens+currentMsgTokens > maxTokens && len(selectedUserMessages) > 0 {
			break // Stop selection once limit is hit, having already included some messages
		}

		// Select the message (add to front to maintain original order later)
		userMessageIndices = append([]int{i}, userMessageIndices...)
		totalTokens += currentMsgTokens
	}

	// Extract selected messages in their original order
	for _, idx := range userMessageIndices {
		selectedUserMessages = append(selectedUserMessages, s.History[idx])
	}

	// Summarization Prompt Construction
	userMessagesContent := make([]string, len(selectedUserMessages))
	for i, msg := range selectedUserMessages {
		userMessagesContent[i] = msg.Content.String()
	}

	historyStrBuilder := strings.Builder{}
	for i, content := range userMessagesContent {
		historyStrBuilder.WriteString(content)
		if i < len(userMessagesContent)-1 {
			historyStrBuilder.WriteString("\n\n---\n\n") // Use a clear delimiter between messages in the prompt
		}
	}

	summarizationPrompt := fmt.Sprintf("%s\n\n--- Conversation History ---\n%s", templateContent, historyStrBuilder.String())

	// Call LLM and Reconstruct History
	req := client.ChatCompletionRequest{
		Messages: []client.ChatMessage{
			{
				Role:    "user",
				Content: client.TextContent(summarizationPrompt),
			},
		},
	}

	resp, err := s.client.ChatCompletion(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to get history summary: %w", err)
	}

	summaryText := resp.Choices[0].Message.Content.String()
	finalUserInstruction := fmt.Sprintf("%s %s", prefixContent, summaryText)
	summaryInstructionMsg := client.ChatMessage{
		Role:    "user",
		Content: client.TextContent(finalUserInstruction),
	}

	// Reconstruct new history: [selected_user_messages..., new_summary_instruction_message]
	var newHistory []client.ChatMessage
	newHistory = append(newHistory, selectedUserMessages...)
	newHistory = append(newHistory, summaryInstructionMsg)

	s.History = newHistory
	newTokens := common.CalculateHistoryTokens(s.History, s.systemPrompt, s.GetToolDefinitions())

	if s.onEvent != nil {
		s.onEvent(common.CompressionSummaryEvent{ID: s.GetID(), Gains: oldTokens - newTokens})
	}

	return s.saveAndNotify()
}