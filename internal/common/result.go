package common

import "late/internal/client"

// StreamResult represents a chunk of data from the stream.
// It is moved here to avoid circular imports between session and common.
type StreamResult struct {
	Content          string
	ReasoningContent string
	ToolCalls        []client.ToolCall
	Usage            client.Usage
	FinishReason     string
	// Timings and Progress are the server's own speed numbers for this
	// request (llama.cpp); zero / nil when the backend sends none.
	Timings  client.Timings
	Progress *client.PromptProgress
}
