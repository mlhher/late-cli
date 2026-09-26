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
	"sync"
	"time"
)

// Session manages the chat state and interacts with the LLM client.
type Session struct {
	clientMu              sync.RWMutex
	client                *client.Client
	HistoryPath           string
	History               []client.ChatMessage
	systemPrompt          string
	useTools              bool
	skipMetadata          bool   // when true, no top-level .meta.json sidecar is written (subagents)
	workingDir            string // absolute path of the directory where the session was started (project folder)
	subagentSeq           int
	saveSubagentHistories *bool
	Registry              *tool.Registry

	// compactionHighWater is the history compaction high-water mark: the
	// monotonic message index below which the frozen prefix ends. The
	// compactor (compact.go) never re-scores or rewrites a message with a
	// smaller index, so previously frozen bytes — the prompt-cache anchor —
	// never change between runs. It is guarded by compactionMu the same way
	// client guards clientMu; it round-trips through SessionMeta
	// (GenerateSessionMeta) so it survives save/reload.
	compactionHighWater int
	compactionMu        sync.Mutex

	// retrievedBlock holds the ephemeral retrieved-context block staged by
	// InjectRetrieved (retrieve.go) for the NEXT stream request: StartStream
	// appends it as the LAST outgoing message — the tail of the message list
	// is the work area; the frozen prefix (the head) is never touched. It is
	// request-scoped by contract: it exists only in the outgoing request
	// copy, never in History and never on disk, and every InjectRetrieved
	// call overwrites it (an empty result clears it). Guarded by
	// retrievedMu, so a staged block is also safe against the TUI's
	// cross-goroutine paths.
	retrievedMu    sync.Mutex
	retrievedBlock string
}

func New(c *client.Client, historyPath string, history []client.ChatMessage, systemPrompt string, useTools bool) *Session {
	s := &Session{
		client:       c,
		HistoryPath:  historyPath,
		History:      history,
		systemPrompt: systemPrompt,
		useTools:     useTools,
		Registry:     tool.NewRegistry(),
	}
	// Best-effort capture of the project folder; never fail construction.
	if wd, err := os.Getwd(); err == nil {
		s.workingDir = wd
	}
	return s
}

// NewSubagentSession creates a session for a subagent. History is persisted
// to historyPath when non-empty (in-memory otherwise), but the session never
// writes a top-level .meta.json sidecar, keeping the shared sessions
// directory free of subagent entries.
func NewSubagentSession(c *client.Client, historyPath string, history []client.ChatMessage, systemPrompt string) *Session {
	s := New(c, historyPath, history, systemPrompt, true)
	s.skipMetadata = true
	return s
}

// SetSubagentMetadata initializes root-session state that must survive resume.
func (s *Session) SetSubagentMetadata(seq int, saveHistories *bool) {
	s.subagentSeq = seq
	if saveHistories == nil {
		s.saveSubagentHistories = nil
		return
	}
	value := *saveHistories
	s.saveSubagentHistories = &value
}

// SetWorkingDir overrides the project directory recorded in session
// metadata. Used on resume so a session keeps the directory where it
// was originally started.
func (s *Session) SetWorkingDir(dir string) {
	s.workingDir = dir
}

// SubagentSeq returns the next sequence number reserved for a child session.
func (s *Session) SubagentSeq() int {
	return s.subagentSeq
}

// UpdateSubagentSeq durably advances the next child sequence before a child
// history path is created. In-memory and subagent sessions retain their
// existing behavior because neither writes root session metadata.
func (s *Session) UpdateSubagentSeq(seq int) error {
	previous := s.subagentSeq
	s.subagentSeq = seq
	if s.skipMetadata || s.HistoryPath == "" {
		return nil
	}
	if err := s.UpdateSessionMetadata(); err != nil {
		s.subagentSeq = previous
		return err
	}
	return nil
}

// CompactionHighWater returns the history compaction high-water mark: the
// monotonic message index below which the frozen prefix ends (compact.go).
func (s *Session) CompactionHighWater() int {
	s.compactionMu.Lock()
	defer s.compactionMu.Unlock()
	return s.compactionHighWater
}

// SetCompactionHighWater sets the in-memory high-water mark without
// persisting. The resume path (cmd/late) restores the persisted mark into a
// freshly constructed session with it, the reset paths (StartNewConversation)
// zero it with it, and tests use it to pin frozen-prefix behavior.
// Negative values clamp to zero.
func (s *Session) SetCompactionHighWater(n int) {
	if n < 0 {
		n = 0
	}
	s.compactionMu.Lock()
	defer s.compactionMu.Unlock()
	s.compactionHighWater = n
}

// UpdateCompactionHighWater durably advances the high-water mark to n —
// monotonic: a value at or below the current mark is a no-op — and persists
// it through the session meta sidecar, mirroring UpdateSubagentSeq:
// in-memory sessions (no history path) and subagent sessions (no sidecar)
// keep the mark in memory only, and a failed metadata write rolls the
// in-memory advance back so memory and disk never disagree.
func (s *Session) UpdateCompactionHighWater(n int) error {
	s.compactionMu.Lock()
	if n <= s.compactionHighWater {
		s.compactionMu.Unlock()
		return nil
	}
	previous := s.compactionHighWater
	s.compactionHighWater = n
	s.compactionMu.Unlock()

	if s.skipMetadata || s.HistoryPath == "" {
		return nil
	}
	if err := s.UpdateSessionMetadata(); err != nil {
		s.compactionMu.Lock()
		s.compactionHighWater = previous
		s.compactionMu.Unlock()
		return err
	}
	return nil
}

// ClampCompactionHighWater lowers the in-memory high-water mark to at most
// maxIndex — the reset-path hook (rewind, pop, new conversation): the frozen
// prefix never outlives the history it froze, or a stale mark would freeze
// messages that no longer exist. The caller's own metadata write
// (UpdateSessionMetadata) persists the clamp; in-memory sessions need no
// persistence at all.
func (s *Session) ClampCompactionHighWater(maxIndex int) {
	if maxIndex < 0 {
		maxIndex = 0
	}
	s.compactionMu.Lock()
	defer s.compactionMu.Unlock()
	if s.compactionHighWater > maxIndex {
		s.compactionHighWater = maxIndex
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
		Content:    client.TextContent(content),
	})
	return s.saveAndNotify()
}

// AddAssistantMessageWithTools adds an assistant message with tool calls.
func (s *Session) AddAssistantMessageWithTools(content string, reasoning string, toolCalls []client.ToolCall) error {
	s.History = append(s.History, client.ChatMessage{
		Role:             "assistant",
		Content:          client.TextContent(content),
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
	s.History = append(s.History, client.ChatMessage{Role: "user", Content: client.TextContent(content)})
	return s.saveAndNotify()
}

// AddMessage adds an arbitrary message to history and persists it.
func (s *Session) AddMessage(msg client.ChatMessage) error {
	s.History = append(s.History, msg)
	return s.saveAndNotify()
}

// AddAssistantMessage adds an assistant message to history and persists it.
func (s *Session) AddAssistantMessage(content, reasoning string) error {
	s.History = append(s.History, client.ChatMessage{
		Role:             "assistant",
		Content:          client.TextContent(content),
		ReasoningContent: reasoning,
	})
	return s.saveAndNotify()
}

// PopLastUserMessage removes the trailing user message from history and
// persists the change atomically. The bool reports whether a message was
// removed (false = no-op: empty history or non-user tail). The error is a
// persistence error, returned only when a change was made.
func (s *Session) PopLastUserMessage() (bool, error) {
	if len(s.History) == 0 || s.History[len(s.History)-1].Role != "user" {
		return false, nil
	}
	s.History = s.History[:len(s.History)-1]
	// The frozen prefix never outlives the history it froze: the high-water
	// mark clamps to the truncated length, and the metadata write below
	// persists the clamp.
	s.ClampCompactionHighWater(len(s.History))

	// Popping the first-and-only message empties the history. saveAndNotify()
	// treats empty history as "nothing to persist" (its empty-guard exists so
	// fresh sessions don't create files at startup), which would leave the
	// just-popped message stale on disk and let --continue resurrect the
	// rejected turn. So when the history file exists on disk, remove it
	// instead of saving an empty file. The .meta.json sidecar lives in the
	// sessions directory (never next to the history file) and is kept — only
	// refreshed — so --continue scoping still finds this session.
	if len(s.History) == 0 && s.HistoryPath != "" {
		if err := os.Remove(s.HistoryPath); err != nil {
			if !os.IsNotExist(err) {
				return true, fmt.Errorf("failed to remove emptied history file %s: %w", s.HistoryPath, err)
			}
			// Nothing persisted yet (history lived only in memory), so there
			// is no file or sidecar to update either.
			return true, nil
		}
		if err := s.UpdateSessionMetadata(); err != nil {
			return true, err
		}
		return true, nil
	}

	return true, s.saveAndNotify()
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
	return s.saveAndNotify()
}

// StartStream initiates a streaming response.
// It returns a standard Go channel for results and error.
// An optional onConnect callback is called once HTTP 200 headers are received.
func (s *Session) StartStream(ctx context.Context, extraBody map[string]any, onConnect ...func()) (<-chan common.StreamResult, <-chan error) {
	outCh := make(chan common.StreamResult)
	errCh := make(chan error, 1)

	// Prepare messages with system prompt
	messages := make([]client.ChatMessage, 0, len(s.History)+1)
	if s.systemPrompt != "" {
		messages = append(messages, client.ChatMessage{Role: "system", Content: client.TextContent(s.systemPrompt)})
	}
	// Sanitize per request: a history interrupted mid-tool-run (crash, fatal
	// stream error) can end with assistant tool_calls that never got results,
	// which strict OpenAI-compatible endpoints reject with HTTP 400. The
	// sanitizer repairs the copy sent to the API; the saved history is
	// intentionally left untouched.
	messages = append(messages, SanitizeForRequest(s.History)...)

	// Retrieved context (Step 17, compaction-retrieval): the block staged by
	// InjectRetrieved is appended LAST, so it lands in the work area — after
	// the frozen prefix by construction — and is ephemeral: it lives only in
	// this request copy, never in s.History, never on disk, and the TUI
	// transcript (which renders History) never shows it. Role "system" marks
	// it as harness-injected context rather than a user turn.
	if block := s.retrievedBlockForRequest(); block != "" {
		messages = append(messages, client.ChatMessage{
			Role:    retrievedContextRole,
			Content: client.TextContent(block),
		})
	}

	var onConn func()
	if len(onConnect) > 0 && onConnect[0] != nil {
		onConn = onConnect[0]
	}

	req := client.ChatCompletionRequest{
		Messages:  messages,
		ExtraBody: extraBody,
		OnConnect: onConn,
	}

	if s.useTools {
		req.Tools = s.GetToolDefinitions()
	}

	streamOut, streamErr := s.Client().ChatCompletionStream(ctx, req)

	go func() {
		defer close(outCh)
		defer close(errCh)

		for {
			select {
			case chunk, ok := <-streamOut:
				if !ok {
					// The client closes errCh before out (LIFO defers). A
					// random select win here must not swallow a mid-stream
					// failure: drain the terminal error before returning,
					// or ConsumeStream would treat the attempt as a clean,
					// partial success and commit a truncated turn.
					if err, ok := <-streamErr; ok && err != nil {
						select {
						case errCh <- err:
						case <-ctx.Done():
						}
					}
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

	resp, err := s.Client().Completion(ctx, req)
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
		ID:                    id,
		Title:                 title,
		CreatedAt:             time.Now(),
		LastUpdated:           time.Now(),
		HistoryPath:           s.HistoryPath,
		LastUserPrompt:        lastPrompt,
		MessageCount:          len(s.History),
		SubagentSeq:           s.subagentSeq,
		SaveSubagentHistories: s.saveSubagentHistories,
		WorkingDir:            s.workingDir,
		CompactionHighWater:   s.CompactionHighWater(),
	}
}

// UpdateSessionMetadata updates the session metadata file
func (s *Session) UpdateSessionMetadata() error {
	if s.skipMetadata {
		return nil
	}
	meta := s.GenerateSessionMeta()
	return SaveSessionMeta(meta)
}

// StartNewConversation preserves the current conversation and switches this
// session to a fresh history file. The new file is created when the first
// message is saved, matching startup behavior for an empty session.
func (s *Session) StartNewConversation() error {
	if s.HistoryPath != "" && len(s.History) > 0 {
		if err := SaveHistory(s.HistoryPath, s.History); err != nil {
			return err
		}
		if err := s.UpdateSessionMetadata(); err != nil {
			return err
		}
	}

	dir := filepath.Dir(s.HistoryPath)
	if s.HistoryPath == "" || dir == "." {
		var err error
		dir, err = SessionDir()
		if err != nil {
			return err
		}
	}

	now := time.Now()
	sessionID := fmt.Sprintf("session-%s-%09d", now.Format("20060102-150405"), now.Nanosecond())
	s.HistoryPath = filepath.Join(dir, sessionID+".json")
	s.History = []client.ChatMessage{}
	// A fresh conversation has no frozen prefix: the compaction high-water
	// mark resets with the history (the sidecar of the preserved old
	// conversation keeps its own mark for when it is resumed).
	s.SetCompactionHighWater(0)
	return nil
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
		return nil // Skip saving if no path provided (e.g., in-memory sessions, subagents without history opt-in)
	}
	if err := SaveHistory(s.HistoryPath, s.History); err != nil {
		return err
	}
	return s.UpdateSessionMetadata()
}

func (s *Session) Client() *client.Client {
	s.clientMu.RLock()
	defer s.clientMu.RUnlock()
	return s.client
}

// SetClient replaces the client used for subsequent model requests.
// In-flight requests continue using the client they started with.
func (s *Session) SetClient(c *client.Client) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	s.client = c
}

func (s *Session) IsLlamaCPP() bool {
	return s.Client().IsLlamaCPP()
}
