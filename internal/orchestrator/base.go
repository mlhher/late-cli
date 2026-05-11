package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/executor"
	"late/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BaseOrchestrator implements common.Orchestrator and manages an agent's run loop.
type BaseOrchestrator struct {
	id          string
	sess        *session.Session
	middlewares []common.ToolMiddleware
	eventCh     chan common.Event

	mu       sync.RWMutex
	parent   common.Orchestrator
	children []common.Orchestrator

	// Running state tracker
	acc    executor.StreamAccumulator
	ctx    context.Context
	cancel context.CancelFunc

	// Stop mechanism
	stopCh chan struct{}

	// Max turns configuration
	maxTurns int
}

func NewBaseOrchestrator(id string, sess *session.Session, middlewares []common.ToolMiddleware, maxTurns int) *BaseOrchestrator {
	return &BaseOrchestrator{
		id:          id,
		sess:        sess,
		middlewares: middlewares,
		eventCh:     make(chan common.Event, 100),
		ctx:         context.Background(),
		stopCh:      make(chan struct{}),
		maxTurns:    maxTurns,
	}
}

func (o *BaseOrchestrator) SetMiddlewares(middlewares []common.ToolMiddleware) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.middlewares = middlewares
}

func (o *BaseOrchestrator) SetContext(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ctx = ctx
}

func (o *BaseOrchestrator) SetMaxTurns(maxTurns int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maxTurns = maxTurns
}

func (o *BaseOrchestrator) MaxTokens() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sess.Client().ContextSize()
}

func (o *BaseOrchestrator) SupportsVision() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sess.Client().SupportsVision()
}

func (o *BaseOrchestrator) RefreshContextSize(ctx context.Context) {
	o.sess.Client().RefreshContextSize(ctx)
}

func (o *BaseOrchestrator) ID() string { return o.id }

func (o *BaseOrchestrator) Submit(text string, images []string) error {
	o.mu.Lock()
	// Clear any old cancellation state so a new run isn't instantly aborted
	o.cancel = nil
	// Reset the base context if it was already cancelled
	if o.ctx.Err() != nil {
		o.ctx = context.Background()
	}
	o.mu.Unlock()

	msg := client.ChatMessage{Role: "user", AttachedFiles: images}

	if len(images) == 0 {
		msg.Content = client.TextContent(text)
	} else {
		parts := []client.ContentPart{
			{Type: client.ContentPartText, Text: text},
		}
		supportsVision := o.SupportsVision()
		for _, imgPath := range images {
			data, err := os.ReadFile(imgPath)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", imgPath, err)
			}

			mimeType := http.DetectContentType(data)
			// Only attach to LLM content if it's an image AND the model supports vision
			if strings.HasPrefix(mimeType, "image/") && supportsVision {
				encoded := base64.StdEncoding.EncodeToString(data)
				parts = append(parts, client.ContentPart{
					Type: client.ContentPartImageURL,
					ImageURL: &client.ImageURL{
						URL: fmt.Sprintf("data:%s;base64,%s", mimeType, encoded),
					},
				})
			} else {
				// Treat as text if it looks like text or has a common extension.
				// If it's an image but the model doesn't support vision, just include a note.
				content := ""
				isAttachment := true
				if strings.HasPrefix(mimeType, "image/") {
					content = fmt.Sprintf("\nAttached Image: %s (Vision not supported by current model)\n", filepath.Base(imgPath))
				} else {
					content = fmt.Sprintf("\nFilename: %s\nContent: ```\n%s\n```\n", filepath.Base(imgPath), string(data))
				}

				parts = append(parts, client.ContentPart{
					Type:         client.ContentPartText,
					Text:         content,
					IsAttachment: isAttachment,
				})
			}
		}
		msg.Content = client.MessageContent{Parts: parts}
	}

	if err := o.sess.AddMessage(msg); err != nil {
		return err
	}

	o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	// Start the run loop in a background goroutine
	go o.run()
	return nil
}

func (o *BaseOrchestrator) Execute(text string) (string, error) {
	o.mu.Lock()
	if o.ctx.Err() != nil {
		o.ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel = cancel
	o.ctx = ctx // Set the Context for this execution
	o.mu.Unlock()

	defer cancel()

	// Inject orchestrator ID into context for tool interactions
	ctx = context.WithValue(ctx, common.OrchestratorIDKey, o.id)

	if err := o.sess.AddUserMessage(text); err != nil {
		return "", err
	}

	o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	defer func() {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
	}()

	// Build extra body
	var extraBody map[string]any

	onStartTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	}

	onEndTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		usage := o.acc.Usage
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.ContentEvent{ID: o.id, Usage: usage}
	}

	res, err := executor.RunLoop(
		ctx,
		o.sess,
		o.maxTurns,
		extraBody,
		onStartTurn,
		onEndTurn,
		func(res common.StreamResult) {
			o.mu.Lock()
			o.acc.Append(res)
			accCopy := o.acc
			o.mu.Unlock()

			o.eventCh <- common.ContentEvent{
				ID:               o.id,
				Content:          accCopy.Content,
				ReasoningContent: accCopy.Reasoning,
				ToolCalls:        accCopy.ToolCalls,
				Usage:            accCopy.Usage,
			}
		},
		o.middlewares,
	)

	if err != nil {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: err}
	} else {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "closed"}
	}
	return res, err
}

func (o *BaseOrchestrator) run() {
	// Build extra body
	var extraBody map[string]any

	o.mu.Lock()
	if o.ctx.Err() != nil {
		o.ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel = cancel
	o.ctx = ctx // Set the context so Execute/RunLoop can share the cancelable context safely
	o.mu.Unlock()

	defer cancel() // Ensure we don't leak the context when run() finishes

	// Inject orchestrator ID into context for tool interactions
	ctx = context.WithValue(ctx, common.OrchestratorIDKey, o.id)

	onStartTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	}

	onEndTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		usage := o.acc.Usage
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.ContentEvent{ID: o.id, Usage: usage}
	}

	_, err := executor.RunLoop(
		ctx,
		o.sess,
		o.maxTurns,
		extraBody,
		onStartTurn,
		onEndTurn,
		func(res common.StreamResult) {
			o.mu.Lock()
			o.acc.Append(res)
			accCopy := o.acc // Copy for event
			o.mu.Unlock()

			o.eventCh <- common.ContentEvent{
				ID:               o.id,
				Content:          accCopy.Content,
				ReasoningContent: accCopy.Reasoning,
				ToolCalls:        accCopy.ToolCalls,
				Usage:            accCopy.Usage,
			}
		},
		o.middlewares,
	)

	// Reset accumulator after finished or ready for next turn
	o.mu.Lock()
	o.acc.Reset()
	o.mu.Unlock()

	if err != nil {
		// If the error is about unsupported image input, roll back the user message
		// so it doesn't poison the context for future requests.
		errStr := err.Error()
		if strings.Contains(errStr, "image input is not supported") ||
			strings.Contains(errStr, "image_input") ||
			strings.Contains(errStr, "does not support image") {
			// Remove the last user message from history
			if len(o.sess.History) > 0 && o.sess.History[len(o.sess.History)-1].Role == "user" {
				o.sess.History = o.sess.History[:len(o.sess.History)-1]
			}
			o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: fmt.Errorf("image_unsupported")}
		} else {
			o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: err}
		}
	} else {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
	}

	// Check if stop was requested and send StopRequestedEvent
	if o.IsStopRequested() {
		o.eventCh <- common.StopRequestedEvent{ID: o.id}
	}
}

func (o *BaseOrchestrator) Events() <-chan common.Event {
	return o.eventCh
}

func (o *BaseOrchestrator) Cancel() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.cancel != nil {
		o.cancel()
	}

	select {
	case o.stopCh <- struct{}{}:
		// Signal sent
	default:
		// Already signaled, ignore
	}
}

func (o *BaseOrchestrator) IsStopRequested() bool {
	select {
	case <-o.stopCh:
		return true
	default:
		return false
	}
}

func (o *BaseOrchestrator) History() []client.ChatMessage {
	return o.sess.History
}

func (o *BaseOrchestrator) Session() *session.Session {
	return o.sess
}

func (o *BaseOrchestrator) SystemPrompt() string {
	return o.sess.SystemPrompt()
}

func (o *BaseOrchestrator) ToolDefinitions() []client.ToolDefinition {
	return o.sess.GetToolDefinitions()
}

func (o *BaseOrchestrator) Context() context.Context {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.ctx
}

func (o *BaseOrchestrator) Middlewares() []common.ToolMiddleware {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.middlewares
}

func (o *BaseOrchestrator) Registry() *common.ToolRegistry {
	return o.sess.Registry
}

func (o *BaseOrchestrator) Children() []common.Orchestrator {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.children
}

func (o *BaseOrchestrator) Parent() common.Orchestrator {
	return o.parent
}

func (o *BaseOrchestrator) Reset() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sess.History = []client.ChatMessage{}
	return session.SaveHistory(o.sess.HistoryPath, nil)
}

func (o *BaseOrchestrator) AddChild(child common.Orchestrator) {
	o.mu.Lock()
	o.children = append(o.children, child)
	o.mu.Unlock()

	o.eventCh <- common.ChildAddedEvent{
		ParentID: o.id,
		Child:    child,
	}
}
