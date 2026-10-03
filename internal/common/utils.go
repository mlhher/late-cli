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
//
// It walks exactly the same fields as CalculateHistoryTokens — message
// Content + ReasoningContent + every tool call's name and arguments, plus
// the system prompt, tool definitions, and the per-message/per-block
// overhead constants — through the shared calculateHistoryTokens walk; the
// ONLY divergence from the slow path is per-token precision:
// EstimateTokenCountFast falls back to the ~3.5 chars/token heuristic while
// the BPE vocabulary is still loading in the background. Acceptable for the
// pre-discovery frame the async token-count traffic replaces (update.go
// recomputes with CalculateHistoryTokens); do not use this for any
// accounting that persists or gates.
func CalculateHistoryTokensFast(history []client.ChatMessage, systemPrompt string, tools []client.ToolDefinition) int {
	return calculateHistoryTokens(history, systemPrompt, tools, EstimateTokenCountFast)
}

// EstimateToolDefinitionTokens estimates tokens used by tool definitions.
func EstimateToolDefinitionTokens(tools []client.ToolDefinition) int {
	return estimateToolDefinitionTokensWith(tools, EstimateTokenCount)
}

// tokenEstimator is the per-string estimator the shared token walks are
// parameterized by: the exact cl100k_base BPE count (the slow path) or the
// fast heuristic used while the BPE vocabulary is still loading.
type tokenEstimator func(string) int

// estimateMessageTokensWith walks one message's token-bearing fields —
// Content, ReasoningContent, and every tool call's name and arguments —
// counting each string with est and adding the per-message overhead for
// roles and delimiters (approx 4 tokens). It is the single structural
// definition the slow (EstimateMessageTokens) and fast first-paint walks
// share, so the two can only ever disagree on per-token precision, never on
// which fields count.
func estimateMessageTokensWith(msg client.ChatMessage, est tokenEstimator) int {
	tokens := est(msg.Content.String()) + est(msg.ReasoningContent)
	for _, tc := range msg.ToolCalls {
		tokens += est(tc.Function.Name) + est(tc.Function.Arguments)
	}
	return tokens + 4
}

// estimateToolDefinitionTokensWith estimates tokens used by tool
// definitions, counting name and description with est and the JSON
// parameters as raw bytes/4, plus the base overhead for the tools block
// (none when there are no tools).
func estimateToolDefinitionTokensWith(tools []client.ToolDefinition, est tokenEstimator) int {
	if len(tools) == 0 {
		return 0
	}
	total := 0
	for _, t := range tools {
		total += est(t.Function.Name) + est(t.Function.Description)
		total += len(t.Function.Parameters) / 4
	}
	return total + 10 // Base overhead for tools block
}

// calculateHistoryTokens is the shared history walk both public counters are
// one-line parameterizations of: system prompt + overhead, the tool
// definitions, and every message walked by estimateMessageTokensWith — the
// same fields for every estimator, so CalculateHistoryTokens and
// CalculateHistoryTokensFast agree structurally by construction.
func calculateHistoryTokens(history []client.ChatMessage, systemPrompt string, tools []client.ToolDefinition, est tokenEstimator) int {
	total := est(systemPrompt) + 10 // System prompt + overhead
	total += estimateToolDefinitionTokensWith(tools, est)
	for _, msg := range history {
		total += estimateMessageTokensWith(msg, est)
	}
	return total
}

// EstimateMessageTokens estimates tokens for a full chat message including tool calls and role overhead.
func EstimateMessageTokens(msg client.ChatMessage) int {
	return estimateMessageTokensWith(msg, EstimateTokenCount)
}

// EstimateEventTokens estimates tokens for a content event.
func EstimateEventTokens(event ContentEvent) int {
	return EstimateTokenCount(event.Content) + EstimateTokenCount(event.ReasoningContent)
}

// CalculateHistoryTokens calculates the total token count from history, system prompt, and tools.
func CalculateHistoryTokens(history []client.ChatMessage, systemPrompt string, tools []client.ToolDefinition) int {
	return calculateHistoryTokens(history, systemPrompt, tools, EstimateTokenCount)
}
