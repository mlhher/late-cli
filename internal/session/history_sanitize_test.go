package session

import (
	"late/internal/client"
	"reflect"
	"testing"
)

// placeholderContent pins the exact text SanitizeForRequest synthesizes for
// interrupted tool calls, asserted literally rather than via the constant.
const placeholderContent = "(tool execution was interrupted; no result was recorded)"

func userMsg(content string) client.ChatMessage {
	return client.ChatMessage{Role: "user", Content: client.TextContent(content)}
}

func systemMsg(content string) client.ChatMessage {
	return client.ChatMessage{Role: "system", Content: client.TextContent(content)}
}

func toolCall(id, name string) client.ToolCall {
	return client.ToolCall{
		ID:       id,
		Type:     "function",
		Function: client.FunctionCall{Name: name, Arguments: "{}"},
	}
}

func assistantWithCalls(calls ...client.ToolCall) client.ChatMessage {
	return client.ChatMessage{Role: "assistant", Content: client.TextContent(""), ToolCalls: calls}
}

func toolResult(id, content string) client.ChatMessage {
	return client.ChatMessage{Role: "tool", ToolCallID: id, Content: client.TextContent(content)}
}

func toolPlaceholder(id string) client.ChatMessage {
	return toolResult(id, placeholderContent)
}

// cloneHistory deep-copies msgs (including ToolCalls and Content.Parts) so
// tests can prove SanitizeForRequest leaves its input untouched. A nil input
// stays nil so the post-call comparison is exact.
func cloneHistory(msgs []client.ChatMessage) []client.ChatMessage {
	if msgs == nil {
		return nil
	}
	out := make([]client.ChatMessage, len(msgs))
	for i, m := range msgs {
		m.ToolCalls = append([]client.ToolCall(nil), m.ToolCalls...)
		m.AttachedFiles = append([]string(nil), m.AttachedFiles...)
		m.Content.Parts = append([]client.ContentPart(nil), m.Content.Parts...)
		out[i] = m
	}
	return out
}

func TestSanitizeForRequest(t *testing.T) {
	tests := []struct {
		name  string
		input []client.ChatMessage
		want  []client.ChatMessage
	}{
		{
			name: "dangling tool_calls at end of history get placeholders in tool-call ID order",
			input: []client.ChatMessage{
				userMsg("list the files"),
				assistantWithCalls(toolCall("call_b", "read_file"), toolCall("call_a", "list_dir")),
			},
			want: []client.ChatMessage{
				userMsg("list the files"),
				assistantWithCalls(toolCall("call_b", "read_file"), toolCall("call_a", "list_dir")),
				toolPlaceholder("call_b"),
				toolPlaceholder("call_a"),
			},
		},
		{
			name: "dangling tool_calls group mid-history closes before the following user turn",
			input: []client.ChatMessage{
				userMsg("inspect the repo"),
				assistantWithCalls(toolCall("call_1", "read_file"), toolCall("call_2", "list_dir")),
				userMsg("never mind, continue"),
			},
			want: []client.ChatMessage{
				userMsg("inspect the repo"),
				assistantWithCalls(toolCall("call_1", "read_file"), toolCall("call_2", "list_dir")),
				toolPlaceholder("call_1"),
				toolPlaceholder("call_2"),
				userMsg("never mind, continue"),
			},
		},
		{
			name: "partially answered tool group keeps the real result and fills only the gap",
			input: []client.ChatMessage{
				assistantWithCalls(toolCall("call_1", "read_file"), toolCall("call_2", "list_dir")),
				toolResult("call_1", "file contents"),
				userMsg("thanks"),
			},
			want: []client.ChatMessage{
				assistantWithCalls(toolCall("call_1", "read_file"), toolCall("call_2", "list_dir")),
				toolResult("call_1", "file contents"),
				toolPlaceholder("call_2"),
				userMsg("thanks"),
			},
		},
		{
			name: "well-formed history is passed through unchanged",
			input: []client.ChatMessage{
				systemMsg("you are a coding agent"),
				userMsg("read main.go"),
				assistantWithCalls(toolCall("call_1", "read_file"), toolCall("call_2", "list_dir")),
				toolResult("call_1", "package main"),
				toolResult("call_2", "main.go\nutils.go"),
				userMsg("now summarize"),
			},
			want: []client.ChatMessage{
				systemMsg("you are a coding agent"),
				userMsg("read main.go"),
				assistantWithCalls(toolCall("call_1", "read_file"), toolCall("call_2", "list_dir")),
				toolResult("call_1", "package main"),
				toolResult("call_2", "main.go\nutils.go"),
				userMsg("now summarize"),
			},
		},
		{
			name:  "leading orphan tool result with no preceding assistant is dropped",
			input: []client.ChatMessage{toolResult("call_orphan", "stale result")},
			want:  []client.ChatMessage{},
		},
		{
			name:  "empty input is returned unchanged",
			input: nil,
			want:  nil,
		},
		{
			name: "assistant with only empty-ID tool_calls is sent without tool_calls, content preserved",
			input: []client.ChatMessage{
				{
					Role:             "assistant",
					Content:          client.TextContent("let me look that up"),
					ReasoningContent: "reasoning about the request",
					ToolCalls:        []client.ToolCall{toolCall("", "read_file")},
				},
			},
			want: []client.ChatMessage{
				{
					Role:             "assistant",
					Content:          client.TextContent("let me look that up"),
					ReasoningContent: "reasoning about the request",
				},
			},
		},
		{
			name: "assistant with one empty-ID and one real tool_call keeps only the real call and its result",
			input: []client.ChatMessage{
				assistantWithCalls(toolCall("", "read_file"), toolCall("call_1", "list_dir")),
				toolResult("call_1", "main.go\nutils.go"),
			},
			want: []client.ChatMessage{
				assistantWithCalls(toolCall("call_1", "list_dir")),
				toolResult("call_1", "main.go\nutils.go"),
			},
		},
		{
			name: "consecutive assistant tool_call groups close the first before the second",
			input: []client.ChatMessage{
				assistantWithCalls(toolCall("call_1", "read_file")),
				assistantWithCalls(toolCall("call_2", "list_dir")),
			},
			want: []client.ChatMessage{
				assistantWithCalls(toolCall("call_1", "read_file")),
				toolPlaceholder("call_1"),
				assistantWithCalls(toolCall("call_2", "list_dir")),
				toolPlaceholder("call_2"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneHistory(tt.input)

			got := SanitizeForRequest(tt.input)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SanitizeForRequest() mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.input, before) {
				t.Errorf("SanitizeForRequest() mutated its input\n got: %+v\nwant: %+v", tt.input, before)
			}
			if len(got) > 0 && len(tt.input) > 0 && &got[0] == &tt.input[0] {
				t.Errorf("SanitizeForRequest() returned a slice aliasing its input")
			}
		})
	}
}
