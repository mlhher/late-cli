package common

import (
	"late/internal/client"
	"testing"
)

func TestReplacePlaceholders(t *testing.T) {
	tests := []struct {
		text         string
		placeholders map[string]string
		expected     string
	}{
		{
			text:         "Hello ${{CWD}}",
			placeholders: map[string]string{"${{CWD}}": "/tmp"},
			expected:     "Hello /tmp",
		},
		{
			text:         "No placeholder here",
			placeholders: map[string]string{"${{CWD}}": "/tmp"},
			expected:     "No placeholder here",
		},
		{
			text:         "Multiple ${{CWD}} in ${{CWD}}",
			placeholders: map[string]string{"${{CWD}}": "/home"},
			expected:     "Multiple /home in /home",
		},
	}

	for _, tt := range tests {
		result := ReplacePlaceholders(tt.text, tt.placeholders)
		if result != tt.expected {
			t.Errorf("ReplacePlaceholders(%q, %v) = %q; want %q", tt.text, tt.placeholders, result, tt.expected)
		}
	}
}

func TestEstimateTokenCount(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"a", 0},        // 1/3.5 = 0.28 -> 0
		{"abcd", 1},     // 4/3.5 = 1.14 -> 1
		{"abcde", 1},    // 5/3.5 = 1.42 -> 1
		{"12345678", 2}, // 8/3.5 = 2.28 -> 2
		{"123456789", 2}, // 9/3.5 = 2.57 -> 2
		{"1234567890", 2}, // 10/3.5 = 2.85 -> 2
		{"this is a test", 4}, // 14/3.5 = 4.0 -> 4
	}

	for _, tt := range tests {
		result := EstimateTokenCount(tt.text)
		if result != tt.expected {
			t.Errorf("EstimateTokenCount(%q) = %d; want %d", tt.text, result, tt.expected)
		}
	}
}

func TestEstimateMessageTokens(t *testing.T) {
	msg := client.ChatMessage{
		Role:             "assistant",
		Content:          client.TextContent("Hello"),
		ReasoningContent: "Thinking...",
		ToolCalls: []client.ToolCall{
			{
				Function: client.FunctionCall{
					Name:      "test_tool",
					Arguments: `{"arg1": "val1"}`,
				},
			},
		},
	}

	// "Hello" = 5 chars -> 1 token
	// "Thinking..." = 11 chars -> 3 tokens
	// "test_tool" = 9 chars -> 2 tokens
	// `{"arg1": "val1"}` = 16 chars -> 4 tokens
	// Message overhead = 4 tokens
	// Total = 1 + 3 + 2 + 4 + 4 = 14 tokens
	expected := 14
	result := EstimateMessageTokens(msg)
	if result != expected {
		t.Errorf("EstimateMessageTokens() = %d; want %d", result, expected)
	}
}

func TestEstimateEventTokens(t *testing.T) {
	event := ContentEvent{
		Content:          "Part1",
		ReasoningContent: "Reason",
	}

	// "Part1" = 5 chars -> 1 token
	// "Reason" = 6 chars -> 1 token
	// Total = 1 + 1 = 2 tokens
	expected := 2
	result := EstimateEventTokens(event)
	if result != expected {
		t.Errorf("EstimateEventTokens() = %d; want %d", result, expected)
	}
}

func TestCalculateHistoryTokens(t *testing.T) {
	tests := []struct {
		name         string
		history      []client.ChatMessage
		systemPrompt string
		tools        []client.ToolDefinition
		expected     int
	}{
		{
			name:         "empty history with system prompt",
			history:      []client.ChatMessage{},
			systemPrompt: "You are an assistant",
			tools:        nil,
			expected:     15, // "You are an assistant" = 20 chars -> 5 tokens + 10 overhead = 15
		},
		{
			name: "single user message",
			history: []client.ChatMessage{
				{
					Role:    "user",
					Content: client.TextContent("Hello"),
				},
			},
			systemPrompt: "",
			tools:        nil,
			expected:     15, // System overhead (10) + msg content (1) + msg overhead (4) = 15
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateHistoryTokens(tt.history, tt.systemPrompt, tt.tools)
			if result != tt.expected {
				t.Errorf("CalculateHistoryTokens() = %d; want %d", result, tt.expected)
			}
		})
	}
}
