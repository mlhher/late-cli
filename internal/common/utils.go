package common

import (
	"late/internal/client"
	"strings"
)

// ReplacePlaceholders replaces all occurrences of placeholders with their values.
func ReplacePlaceholders(text string, placeholders map[string]string) string {
	for p, v := range placeholders {
		text = strings.ReplaceAll(text, p, v)
	}
	return text
}

// EstimateTokenCount estimates the number of tokens in text.
// Uses a refined approximation: 1 token ≈ 3.5 characters (more accurate for code/English mix).
func EstimateTokenCount(text string) int {
	if text == "" {
		return 0
	}
	return int(float64(len(text)) / 3.5)
}

// EstimateToolDefinitionTokens estimates tokens used by tool definitions.
func EstimateToolDefinitionTokens(tools []client.ToolDefinition) int {
	if len(tools) == 0 {
		return 0
	}
	// Simplified: estimate based on JSON representation overhead
	total := 0
	for _, t := range tools {
		total += EstimateTokenCount(t.Function.Name) + EstimateTokenCount(t.Function.Description)
		// Parameters are more complex, but we can estimate them too
		total += len(t.Function.Parameters) / 4
	}
	return total + 10 // Base overhead for tools block
}

// EstimateMessageTokens estimates tokens for a full chat message including tool calls and role overhead.
func EstimateMessageTokens(msg client.ChatMessage) int {
	tokens := EstimateTokenCount(msg.Content.String()) + EstimateTokenCount(msg.ReasoningContent)
	for _, tc := range msg.ToolCalls {
		tokens += EstimateTokenCount(tc.Function.Name) + EstimateTokenCount(tc.Function.Arguments)
	}
	// Per-message overhead for roles and delimiters (approx 4 tokens)
	return tokens + 4
}

// EstimateEventTokens estimates tokens for a content event.
func EstimateEventTokens(event ContentEvent) int {
	return EstimateTokenCount(event.Content) + EstimateTokenCount(event.ReasoningContent)
}

// CalculateHistoryTokens calculates the total token count from history, system prompt, and tools.
func CalculateHistoryTokens(history []client.ChatMessage, systemPrompt string, tools []client.ToolDefinition) int {
	total := EstimateTokenCount(systemPrompt) + 10 // System prompt + overhead
	total += EstimateToolDefinitionTokens(tools)

	for _, msg := range history {
		total += EstimateMessageTokens(msg)
	}
	return total
}
