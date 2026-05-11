package client

import (
	"encoding/json"
	"strings"
)

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
	Role             string         `json:"role"`
	Content          MessageContent `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"` // For tool responses
	AttachedFiles    []string       `json:"-"`                      // Purely for UI display
}

type MessageContent struct {
	Text  string
	Parts []ContentPart
}

func (c MessageContent) MarshalJSON() ([]byte, error) {
	if len(c.Parts) > 0 {
		return json.Marshal(c.Parts)
	}
	return json.Marshal(c.Text)
}

func (c *MessageContent) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &c.Text)
	}
	return json.Unmarshal(data, &c.Parts)
}

func (c MessageContent) String() string {
	if len(c.Parts) > 0 {
		var sb strings.Builder
		for i, p := range c.Parts {
			if p.Type == ContentPartText {
				if i > 0 {
					sb.WriteString(" ")
				}
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return c.Text
}

// UIString returns the text content excluding parts marked as attachments.
func (c MessageContent) UIString() string {
	if len(c.Parts) > 0 {
		var sb strings.Builder
		first := true
		for _, p := range c.Parts {
			if p.Type == ContentPartText && !p.IsAttachment {
				if !first {
					sb.WriteString(" ")
				}
				sb.WriteString(p.Text)
				first = false
			}
		}
		return sb.String()
	}
	return c.Text
}

func TextContent(s string) MessageContent {
	return MessageContent{Text: s}
}

type ContentPartType string

const (
	ContentPartText     ContentPartType = "text"
	ContentPartImageURL ContentPartType = "image_url"
)

type ContentPart struct {
	Type         ContentPartType `json:"type"`
	Text         string          `json:"text,omitempty"`
	ImageURL     *ImageURL       `json:"image_url,omitempty"`
	IsAttachment bool            `json:"-"` // Purely for UI filtering
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
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
	Modalities                Modalities         `json:"modalities"`
}

type Modalities struct {
	Vision bool `json:"vision"`
	Audio  bool `json:"audio"`
}

type GenerationSettings struct {
	Params GenerationParams `json:"params"`
	NCtx   int              `json:"n_ctx"`
}

type GenerationParams struct {
	Seed        int64   `json:"seed"`
	Temperature float64 `json:"temperature"`
	TopK        int     `json:"top_k"`
	TopP        float64 `json:"top_p"`
}

type APIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}
