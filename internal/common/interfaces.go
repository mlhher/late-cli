package common

import (
	"context"
	"encoding/json"
	"late/internal/client"
)

// ToolRunner defines the functional signature for executing a single tool call.
type ToolRunner func(ctx context.Context, tc client.ToolCall) (string, error)

// ToolMiddleware wraps a ToolRunner, allowing interception of tool execution.
type ToolMiddleware func(next ToolRunner) ToolRunner

// Orchestrator defines the interface for an agentic conversation manager.
type Orchestrator interface {
	ID() string
	Submit(text string) error
	Execute(text string) (string, error)
	Reset() error
	Cancel()
	Events() <-chan Event
	History() []client.ChatMessage
	Context() context.Context
	Middlewares() []ToolMiddleware
	Registry() *ToolRegistry

	// Hierarchy
	Children() []Orchestrator
	Parent() Orchestrator

	// Configuration
	SetMaxTurns(int)
}

// Event represents something that happened in the orchestrator.
type Event interface {
	OrchestratorID() string
}

// ContentEvent is sent when content or reasoning is streamed.
type ContentEvent struct {
	ID               string
	Content          string
	ReasoningContent string
	ToolCalls        []client.ToolCall
}

func (e ContentEvent) OrchestratorID() string { return e.ID }

// ChildAddedEvent is sent when a new subagent is spawned.
type ChildAddedEvent struct {
	ParentID string
	Child    Orchestrator
}

func (e ChildAddedEvent) OrchestratorID() string { return e.ParentID }

// StatusEvent is sent when the orchestrator's state changes.
type StatusEvent struct {
	ID     string
	Status string // "thinking", "idle", "error", etc.
	Error  error  // Optional error info
}

func (e StatusEvent) OrchestratorID() string { return e.ID }

// StopRequestedEvent is sent when a stop is requested for an orchestrator.
type StopRequestedEvent struct {
	ID string
}

func (e StopRequestedEvent) OrchestratorID() string { return e.ID }

// PromptRequest defines a generic requirement for user input.
type PromptRequest struct {
	ID          string
	Title       string
	Description string
	Schema      json.RawMessage // JSON Schema for validation
}

// InputProvider is the abstract capability tools use to get user data.
type InputProvider interface {
	Prompt(ctx context.Context, req PromptRequest) (json.RawMessage, error)
}

// Context keys
type contextKey string

const (
	InputProviderKey  contextKey = "input_provider"
	OrchestratorIDKey contextKey = "orchestrator_id"
	SkipConfirmationKey contextKey = "skip_confirmation"
)

// GetInputProvider returns the InputProvider from the context.
func GetInputProvider(ctx context.Context) InputProvider {
	if p, ok := ctx.Value(InputProviderKey).(InputProvider); ok {
		return p
	}
	return nil
}

// GetOrchestratorID returns the Orchestrator ID from the context.
func GetOrchestratorID(ctx context.Context) string {
	if id, ok := ctx.Value(OrchestratorIDKey).(string); ok {
		return id
	}
	return ""
}
