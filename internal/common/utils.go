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

// EstimateTokenCount returns the true cl100k_base BPE token count for text.
// cl100k_base is the de-facto reference tokenizer used by OpenAI-compatible
// APIs, so the displayed count matches what the model's context window sees.
// It falls back to a character heuristic only if the embedded vocab fails to
// load (it should not).
func EstimateTokenCount(text string) int {
	if text == "" {
		return 0
	}
	enc, err := bpe()
	if err != nil || enc == nil {
		// Defensive fallback: ~3.5 chars/token. Only reached if the embedded
		// vocabulary is unavailable.
		return int(float64(len(text)) / 3.5)
	}
	return len(enc.Encode(text, nil, nil))
}

// EstimateTokenCountFast estimates token count using a fast character heuristic
// if BPE vocabulary is still loading in background.
func EstimateTokenCountFast(text string) int {
	if text == "" {
		return 0
	}
	if enc := bpeIfReady(); enc != nil {
		return len(enc.Encode(text, nil, nil))
	}
	count := int(float64(len(text)) / 3.5)
	if count < 1 {
		return 1
	}
	return count
}

// CalculateHistoryTokensFast calculates token count quickly without blocking on BPE load,
// ensuring the initial TUI frame renders immediately with a populated token bar.
func CalculateHistoryTokensFast(history []client.ChatMessage, systemPrompt string, tools []client.ToolDefinition) int {
	total := EstimateTokenCountFast(systemPrompt) + 10 // System prompt + overhead
	for _, t := range tools {
		total += EstimateTokenCountFast(t.Function.Name) + EstimateTokenCountFast(t.Function.Description)
		total += len(t.Function.Parameters) / 4
	}
	if len(tools) > 0 {
		total += 10
	}
	for _, msg := range history {
		total += EstimateTokenCountFast(msg.Content.String()) + EstimateTokenCountFast(msg.ReasoningContent) + 4
	}
	return total
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
