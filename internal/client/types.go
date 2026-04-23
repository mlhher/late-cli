package client

import "encoding/json"

// CompletionRequest represents a standard prompt to llama.cpp
type CompletionRequest struct {
	Prompt      string   `json:"prompt"`
	Temperature float64  `json:"temperature,omitempty"`
	N_Predict   int      `json:"n_predict,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	Stream      bool     `json:"stream,omitempty"`
}

// CompletionResponse represents the response
type CompletionResponse struct {
	Content string `json:"content"`
	Stop    bool   `json:"stop"`
}

type ChatMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"` // For tool responses
}

type ToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ChatCompletionRequest struct {
	Model       string           `json:"model,omitempty"`
	Messages    []ChatMessage    `json:"messages"`
	Temperature float64          `json:"temperature,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
	Stop        []string         `json:"stop,omitempty"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	ToolChoice  any              `json:"tool_choice,omitempty"`
	ExtraBody   map[string]any   `json:"extra_body,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   Usage                  `json:"usage,omitempty"`
}

type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// Chunk types for streaming
type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
	Usage   Usage                       `json:"usage,omitempty"`
	Timings Timings                     `json:"timings,omitempty"`
}

type ChatCompletionChunkChoice struct {
	Index        int         `json:"index"`
	Delta        ChatMessage `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type Timings struct {
	PredictedPerSecond float64 `json:"predicted_per_second"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type PropsResponse struct {
	DefaultGenerationSettings GenerationSettings `json:"default_generation_settings"`
	NCtx                      int                `json:"n_ctx"`
}

type GenerationSettings struct {
	Params GenerationParams `json:"params"`
}

type GenerationParams struct {
	Seed        int64   `json:"seed"`
	Temperature float64 `json:"temperature"`
	TopK        int     `json:"top_k"`
	TopP        float64 `json:"top_p"`
}
