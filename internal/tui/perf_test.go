package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"late/internal/client"
	"late/internal/common"
)

var t0 = time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)

func chipText(p *perfState, now time.Time) string { return ansi.Strip(perfChip(p, now)) }

func TestPerfChipShowsPrefillProgressThenSpeed(t *testing.T) {
	var p perfState
	p.begin(t0)

	// Too early to say anything.
	if got := chipText(&p, t0.Add(300*time.Millisecond)); got != "" {
		t.Errorf("chip right after sending = %q, want nothing", got)
	}
	// A slow start without progress information admits it is waiting.
	if got := chipText(&p, t0.Add(2300*time.Millisecond)); got != "waiting 2.3s" {
		t.Errorf("chip = %q, want waiting 2.3s", got)
	}

	// llama.cpp reports progress: 500 of 2000 tokens were cached, 1250 are
	// processed, so 750 of the 1500 still to evaluate are done: 50%.
	p.observe(common.ContentEvent{Progress: &client.PromptProgress{Total: 2000, Cache: 500, Processed: 1250, TimeMS: 150}}, t0.Add(150*time.Millisecond))
	if got := chipText(&p, t0.Add(200*time.Millisecond)); got != "prompt 50% · 5.0k t/s" {
		t.Errorf("chip = %q", got)
	}

	// The first token arrives, with the server's live speed.
	p.observe(common.ContentEvent{Content: "He", Timings: client.Timings{PredictedPerSecond: 42.34}}, t0.Add(800*time.Millisecond))
	if got := chipText(&p, t0.Add(900*time.Millisecond)); got != "42.3 t/s · ttft 800ms" {
		t.Errorf("chip while generating = %q", got)
	}
}

func TestPerfFinishUsesServerSpeedWhenGiven(t *testing.T) {
	var p perfState
	p.begin(t0)
	p.observe(common.ContentEvent{Content: "a", Timings: client.Timings{PredictedPerSecond: 61.5}}, t0.Add(time.Second))
	p.observe(common.ContentEvent{Content: "ab", Timings: client.Timings{PredictedPerSecond: 61.5}}, t0.Add(2*time.Second))
	p.observe(common.ContentEvent{Completed: true, Timings: client.Timings{PredictedPerSecond: 58.2}, Usage: client.Usage{CompletionTokens: 100}}, t0.Add(2*time.Second))

	if !p.final || p.finalTPS != 58.2 {
		t.Errorf("final=%v tps=%v, want the server's final figure", p.final, p.finalTPS)
	}
	if got := chipText(&p, t0.Add(3*time.Second)); got != "58.2 t/s · ttft 1.0s" {
		t.Errorf("chip after the call = %q", got)
	}
}

func TestPerfFinishComputesSpeedForBackendsWithoutTimings(t *testing.T) {
	var p perfState
	p.begin(t0)
	p.observe(common.ContentEvent{Content: "a"}, t0.Add(time.Second))
	p.observe(common.ContentEvent{Content: "ab"}, t0.Add(3*time.Second))
	p.observe(common.ContentEvent{Completed: true, Usage: client.Usage{CompletionTokens: 101}}, t0.Add(3*time.Second))

	// 101 tokens span 100 intervals over the 2s between first and last token.
	if p.finalTPS < 49.9 || p.finalTPS > 50.1 {
		t.Errorf("finalTPS = %v, want ~50", p.finalTPS)
	}

	// While generating there was no live number to show, only the latency.
	var live perfState
	live.begin(t0)
	live.observe(common.ContentEvent{Content: "a"}, t0.Add(time.Second))
	if got := chipText(&live, t0.Add(2*time.Second)); got != "ttft 1.0s" {
		t.Errorf("live chip without server timings = %q, want only the ttft", got)
	}
}

func TestPerfNoSpeedWhenNothingToMeasure(t *testing.T) {
	var p perfState
	p.begin(t0)
	p.observe(common.ContentEvent{Content: "a"}, t0.Add(time.Second))
	// Everything arrived in one burst: no meaningful window.
	p.observe(common.ContentEvent{Content: "ab", Completed: true, Usage: client.Usage{CompletionTokens: 50}}, t0.Add(time.Second+time.Millisecond))
	if p.finalTPS != 0 {
		t.Errorf("finalTPS = %v, want 0 for a sub-50ms window", p.finalTPS)
	}
}

func TestPerfCompletionEventDoesNotMoveLastToken(t *testing.T) {
	var p perfState
	p.begin(t0)
	p.observe(common.ContentEvent{Content: "abc"}, t0.Add(time.Second))
	p.observe(common.ContentEvent{Content: "abc"}, t0.Add(9*time.Second)) // same size: not a new token
	if !p.lastToken.Equal(t0.Add(time.Second)) {
		t.Errorf("lastToken = %v, an event without new content must not count", p.lastToken)
	}
}

func TestPerfAbortedAndStaleCallsShowNothing(t *testing.T) {
	var p perfState
	p.begin(t0)
	p.abort() // errored before any token
	if got := chipText(&p, t0.Add(time.Second)); got != "" {
		t.Errorf("aborted call chip = %q", got)
	}

	var done perfState
	done.begin(t0)
	done.observe(common.ContentEvent{Content: "x", Completed: true}, t0.Add(time.Second))
	if chipText(&done, t0.Add(time.Minute)) == "" {
		t.Error("a recently finished call keeps its summary")
	}
	if got := chipText(&done, t0.Add(10*time.Minute)); got != "" {
		t.Errorf("a long-finished call should drop its summary, got %q", got)
	}
	if perfChip(nil, t0) != "" || perfChip(&perfState{}, t0) != "" {
		t.Error("no data, no chip")
	}
}

func TestPerfRecoveryExcludesRetryBackoffFromTTFT(t *testing.T) {
	var p perfState
	p.begin(t0)
	p.recovered(t0.Add(20 * time.Second)) // connection came back after backoff
	p.observe(common.ContentEvent{Content: "a"}, t0.Add(20*time.Second+500*time.Millisecond))
	if d, ok := p.ttft(); !ok || d != 500*time.Millisecond {
		t.Errorf("ttft = %v (ok=%v), want 500ms", d, ok)
	}
}

func TestIsPrefillOnly(t *testing.T) {
	prog := &client.PromptProgress{Total: 10}
	cases := []struct {
		name string
		ev   common.ContentEvent
		want bool
	}{
		{"progress only", common.ContentEvent{Progress: prog}, true},
		{"with content", common.ContentEvent{Progress: prog, Content: "x"}, false},
		{"completed", common.ContentEvent{Progress: prog, Completed: true}, false},
		{"no progress", common.ContentEvent{}, false},
	}
	for _, c := range cases {
		if got := isPrefillOnly(c.ev); got != c.want {
			t.Errorf("%s: isPrefillOnly = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFormatting(t *testing.T) {
	for v, want := range map[float64]string{7.26: "7.3", 42.34: "42.3", 118.6: "119", 1250: "1.2k"} {
		if got := fmtRate(v); got != want {
			t.Errorf("fmtRate(%v) = %q, want %q", v, got, want)
		}
	}
	for d, want := range map[time.Duration]string{640 * time.Millisecond: "640ms", 1500 * time.Millisecond: "1.5s", 42 * time.Second: "42s"} {
		if got := fmtDuration(d); got != want {
			t.Errorf("fmtDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// --- through the model ---

func orchEvent(ev common.Event) OrchestratorEventMsg { return OrchestratorEventMsg{Event: ev} }

func TestPrefillProgressKeepsAgentThinkingAndShowsInStatusBar(t *testing.T) {
	m := newStatusBarModel(t, &mockOrchestrator{})
	id := m.Focused.ID()

	res, _ := m.Update(orchEvent(common.StatusEvent{ID: id, Status: "thinking"}))
	m = res.(Model)
	res, _ = m.Update(orchEvent(common.ContentEvent{ID: id, Progress: &client.PromptProgress{Total: 1000, Processed: 400, TimeMS: 400}}))
	m = res.(Model)

	if st := m.GetAgentState(id).State; st != StateThinking {
		t.Errorf("state = %v, prompt progress must not look like streaming", st)
	}
	if bar := ansi.Strip(m.statusBarView()); !strings.Contains(bar, "prompt 40%") {
		t.Errorf("status bar = %q, want the prefill progress", bar)
	}

	res, _ = m.Update(orchEvent(common.ContentEvent{ID: id, Content: "Hi", Timings: client.Timings{PredictedPerSecond: 33.3}}))
	m = res.(Model)
	if st := m.GetAgentState(id).State; st != StateStreaming {
		t.Errorf("state = %v after the first token, want streaming", st)
	}
	if bar := ansi.Strip(m.statusBarView()); !strings.Contains(bar, "33.3 t/s") {
		t.Errorf("status bar = %q, want the generation speed", bar)
	}
}

func TestStatusBarDropsPerfChipOnNarrowTerminals(t *testing.T) {
	m := newStatusBarModel(t, &mockOrchestrator{})
	s := m.GetAgentState(m.Focused.ID())
	s.Perf.begin(time.Now().Add(-5 * time.Second))
	s.Perf.observe(common.ContentEvent{Content: "x", Timings: client.Timings{PredictedPerSecond: 20}}, time.Now())

	if bar := ansi.Strip(m.statusBarView()); !strings.Contains(bar, "t/s") {
		t.Fatalf("a wide bar should carry the chip: %q", bar)
	}
	m.SetSize(perfMinWidth-1, 30)
	if bar := ansi.Strip(m.statusBarView()); strings.Contains(bar, "t/s") {
		t.Errorf("a narrow bar must stay uncluttered: %q", bar)
	}
}
