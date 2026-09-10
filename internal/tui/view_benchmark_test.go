package tui

import (
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"strings"
	"testing"
)

// benchmarkOrchestrator supplies mutable history while reusing the test
// orchestrator implementation shared by the tui test package.
type benchmarkOrchestrator struct {
	mockOrchestrator
	history []client.ChatMessage
}

func (o *benchmarkOrchestrator) History() []client.ChatMessage {
	return o.history
}

type focusTestOrchestrator struct {
	mockOrchestrator
	id       string
	history  []client.ChatMessage
	children []common.Orchestrator
	parent   common.Orchestrator
}

func (o *focusTestOrchestrator) ID() string                    { return o.id }
func (o *focusTestOrchestrator) History() []client.ChatMessage { return o.history }
func (o *focusTestOrchestrator) Children() []common.Orchestrator {
	return o.children
}
func (o *focusTestOrchestrator) Parent() common.Orchestrator { return o.parent }

func benchmarkHistory(messageCount int) []client.ChatMessage {
	history := make([]client.ChatMessage, 0, messageCount)
	for i := 0; i < messageCount; i++ {
		if i%2 == 0 {
			history = append(history, client.ChatMessage{
				Role:    "user",
				Content: client.TextContent(fmt.Sprintf("Question %d: explain the relevant behavior in a concise way.", i/2)),
			})
			continue
		}
		history = append(history, client.ChatMessage{
			Role: "assistant",
			Content: client.TextContent(
				"Here is the answer.\n\nIt contains enough text to wrap across terminal lines and exercise the cached viewport composition path.",
			),
		})
	}
	return history
}

func newViewportBenchmarkModel(history []client.ChatMessage) (*Model, *AppState) {
	orch := &benchmarkOrchestrator{history: history}
	model := NewModel(orch, nil, nil)
	model.Width = 100
	model.Height = 40
	model.updateLayout()
	renderTestTranscript(&model)

	state := model.GetAgentState(orch.ID())
	return &model, state
}

// BenchmarkUpdateViewportCachedHistory exposes work that grows with completed
// chat history even after every historical Markdown block has been cached.
func BenchmarkUpdateViewportCachedHistory(b *testing.B) {
	for _, messageCount := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("messages_%d", messageCount), func(b *testing.B) {
			model, state := newViewportBenchmarkModel(benchmarkHistory(messageCount))
			state.State = StateStreaming

			// Populate RenderedHistory before timing. Only the active tail changes
			// during the benchmark.
			state.StreamingState = common.ContentEvent{ID: model.Focused.ID(), Content: "streaming a"}
			renderTestTranscript(model)
			variants := [...]string{"streaming a", "streaming b"}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state.StreamingState.Content = variants[i&1]
				renderTestTranscript(model)
			}
		})
	}
}

// BenchmarkUpdateViewportStreamingTail exposes rescanning and restyling of a
// single growing/incomplete response independently of completed history.
func BenchmarkUpdateViewportStreamingTail(b *testing.B) {
	for _, size := range []int{1 << 10, 16 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			model, state := newViewportBenchmarkModel(nil)
			state.State = StateStreaming

			prefix := strings.Repeat("streaming text ", size/len("streaming text "))
			variants := [...]string{prefix + "a", prefix + "b"}
			state.StreamingState = common.ContentEvent{ID: model.Focused.ID(), Content: variants[0]}
			renderTestTranscript(model)

			b.ReportAllocs()
			b.SetBytes(int64(len(variants[0])))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state.StreamingState.Content = variants[i&1]
				renderTestTranscript(model)
			}
		})
	}
}

// BenchmarkUpdateViewportStreamingReasoning measures the uncached reasoning
// path, which currently renders the complete reasoning text on every frame.
func BenchmarkUpdateViewportStreamingReasoning(b *testing.B) {
	for _, size := range []int{1 << 10, 16 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			model, state := newViewportBenchmarkModel(nil)
			state.State = StateStreaming

			prefix := strings.Repeat("reasoning text ", size/len("reasoning text "))
			variants := [...]string{prefix + "a", prefix + "b"}
			state.StreamingState = common.ContentEvent{ID: model.Focused.ID(), ReasoningContent: variants[0]}
			renderTestTranscript(model)

			b.ReportAllocs()
			b.SetBytes(int64(len(variants[0])))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state.StreamingState.ReasoningContent = variants[i&1]
				renderTestTranscript(model)
			}
		})
	}
}

// BenchmarkUpdateViewportHistoryCacheReset captures the end-of-turn pause
// caused when status handling clears RenderedHistory and every completed
// Markdown message must be rendered again.
func BenchmarkUpdateViewportHistoryCacheReset(b *testing.B) {
	for _, messageCount := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("messages_%d", messageCount), func(b *testing.B) {
			model, state := newViewportBenchmarkModel(benchmarkHistory(messageCount))
			renderTestTranscript(model)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state.RenderedHistory = nil
				state.LastTotalContent = ""
				renderTestTranscript(model)
			}
		})
	}
}

func BenchmarkSanitizeVTE(b *testing.B) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprintf("\x1b[38;2;255;255;255mLine %02d: Some text with \x1b[0m reset and \x1b[1mbold\x1b[m text", i)
	}
	input := strings.Join(lines, "\n")
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = sanitizeVTE(input, 120)
	}
}

func BenchmarkModelView(b *testing.B) {
	model, _ := newViewportBenchmarkModel(benchmarkHistory(50))
	model.Width = 120
	model.Height = 40
	model.Viewport.SetWidth(120)
	model.Viewport.SetHeight(35)
	renderTestTranscript(model)

	model.transcriptFrame()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = model.View()
	}
}
