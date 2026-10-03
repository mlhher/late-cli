package common

import (
	"late/internal/client"
	"testing"

	"github.com/pkoukk/tiktoken-go"
)

// canonicalTokens returns the true cl100k_base token count for text, used as
// the ground truth the package's EstimateTokenCount must match.
func canonicalTokens(t *testing.T, text string) int {
	t.Helper()
	if text == "" {
		return 0
	}
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		t.Fatalf("failed to load canonical encoder: %v", err)
	}
	return len(enc.Encode(text, nil, nil))
}

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
	tests := []string{
		"",
		"a",
		"abcd",
		"12345678",
		"this is a test",
		"Hello, world!",
		"def main():\n    return 42",
		"The quick brown fox jumps over the lazy dog.",
	}

	for _, tt := range tests {
		got := EstimateTokenCount(tt)
		want := canonicalTokens(t, tt)
		if got != want {
			t.Errorf("EstimateTokenCount(%q) = %d; want %d", tt, got, want)
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

	// Expected = sum of real BPE counts for each text field + 4 msg overhead.
	want := EstimateTokenCount("Hello") +
		EstimateTokenCount("Thinking...") +
		EstimateTokenCount("test_tool") +
		EstimateTokenCount(`{"arg1": "val1"}`) + 4

	if got := EstimateMessageTokens(msg); got != want {
		t.Errorf("EstimateMessageTokens() = %d; want %d", got, want)
	}
}

func TestEstimateEventTokens(t *testing.T) {
	event := ContentEvent{
		Content:          "Part1",
		ReasoningContent: "Reason",
	}

	want := EstimateTokenCount("Part1") + EstimateTokenCount("Reason")
	if got := EstimateEventTokens(event); got != want {
		t.Errorf("EstimateEventTokens() = %d; want %d", got, want)
	}
}

func TestCalculateHistoryTokens(t *testing.T) {
	tests := []struct {
		name         string
		history      []client.ChatMessage
		systemPrompt string
		tools        []client.ToolDefinition
		want         int
	}{
		{
			name:         "empty history with system prompt",
			history:      []client.ChatMessage{},
			systemPrompt: "You are an assistant",
			tools:        nil,
			want:         EstimateTokenCount("You are an assistant") + 10,
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
			want:         10 + EstimateMessageTokens(client.ChatMessage{Role: "user", Content: client.TextContent("Hello")}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateHistoryTokens(tt.history, tt.systemPrompt, tt.tools)
			if got != tt.want {
				t.Errorf("CalculateHistoryTokens() = %d; want %d", got, tt.want)
			}
		})
	}
}

// fixtureHistory builds a history dense in tool calls — the shape the old
// CalculateHistoryTokensFast undercounted, since it skipped ToolCall tokens —
// plus a multimodal-free tool result and a reasoning message.
func fixtureHistory() []client.ChatMessage {
	return []client.ChatMessage{
		{Role: "user", Content: client.TextContent("Please analyze this build log.")},
		{
			Role:             "assistant",
			Content:          client.TextContent("Running the build."),
			ReasoningContent: "The build failed before; check the logs.",
			ToolCalls: []client.ToolCall{
				{Index: 0, ID: "call_1", Type: "function", Function: client.FunctionCall{Name: "Bash", Arguments: `{"cmd":"make build","target":"all"}`}},
				{Index: 1, ID: "call_2", Type: "function", Function: client.FunctionCall{Name: "Read", Arguments: `{"path":"internal/session/compact.go"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: client.TextContent("make: *** [build] Error 1\nverbose failure output line 2")},
		{Role: "assistant", Content: client.TextContent("The build failed because of a missing dependency in the module graph.")},
	}
}

// TestCalculateHistoryTokensFastAgreesStructurally pins the structural
// reconciliation: once the BPE vocabulary is loaded (the warm-up call blocks
// on it), EstimateTokenCountFast and EstimateTokenCount return identical
// counts for every string, so the fast and slow history walks — which share
// one calculateHistoryTokens structure — must return the SAME total for a
// fixture dense in tool calls. (Before the reconciliation the fast walk
// skipped ToolCall tokens and disagreed by construction.)
func TestCalculateHistoryTokensFastAgreesStructurally(t *testing.T) {
	// Warm the BPE: EstimateTokenCount blocks until the embedded vocab is
	// loaded, after which bpeIfReady() is non-nil and the fast estimator IS
	// the exact one.
	EstimateTokenCount("warm up the vocabulary")
	if bpeIfReady() == nil {
		t.Fatal("BPE did not load; the structural comparison below would be meaningless")
	}

	history := fixtureHistory()
	systemPrompt := "You are Late, a coding agent."
	tools := []client.ToolDefinition{
		{Function: client.FunctionDefinition{Name: "Bash", Description: "Run a shell command", Parameters: []byte(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)}},
		{Function: client.FunctionDefinition{Name: "Read", Description: "Read a file", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
	}

	fast := CalculateHistoryTokensFast(history, systemPrompt, tools)
	slow := CalculateHistoryTokens(history, systemPrompt, tools)
	if fast != slow {
		t.Errorf("CalculateHistoryTokensFast() = %d; CalculateHistoryTokens() = %d — the walks must agree structurally (only per-token precision may differ)", fast, slow)
	}

	// The tool calls actually carry weight in the fixture: the shared
	// per-message walk must count them (name + arguments), so a message with
	// tool calls costs more than the same message without them.
	withCalls := estimateMessageTokensWith(fixtureHistory()[1], EstimateTokenCount)
	stripped := fixtureHistory()[1]
	stripped.ToolCalls = nil
	withoutCalls := estimateMessageTokensWith(stripped, EstimateTokenCount)
	if withCalls <= withoutCalls {
		t.Errorf("tool calls must contribute tokens: with = %d, without = %d", withCalls, withoutCalls)
	}

	// The slow public API is a one-line parameterization of the same walk:
	// EstimateMessageTokens and EstimateToolDefinitionTokens must match their
	// shared-walk forms exactly.
	if got, want := EstimateMessageTokens(history[1]), estimateMessageTokensWith(history[1], EstimateTokenCount); got != want {
		t.Errorf("EstimateMessageTokens() = %d; shared walk = %d", got, want)
	}
	if got, want := EstimateToolDefinitionTokens(tools), estimateToolDefinitionTokensWith(tools, EstimateTokenCount); got != want {
		t.Errorf("EstimateToolDefinitionTokens() = %d; shared walk = %d", got, want)
	}
}
