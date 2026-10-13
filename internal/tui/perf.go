package tui

import (
	"fmt"
	"time"

	"charm.land/lipgloss/v2"

	"late/internal/client"
	"late/internal/common"
)

// Inference stats. Each agent tracks how fast its current (or last) model call
// ran so the status bar can show it:
//
//	prompt 64% · 5.2k t/s      while the server evaluates the prompt (llama.cpp)
//	waiting 2.3s               a slow start without progress information
//	42.3 t/s · ttft 0.8s       while generating
//	42.3 t/s · ttft 0.8s       after the call, dimmed, until the next one starts
//
// Prompt progress and live speed come from llama.cpp's own numbers, so they only
// exist there. Any other backend still gets time-to-first-token (measured here)
// and, once the call is complete, an average speed worked out from the token
// count the server reports at the end.

const (
	// perfWaitingAfter is how long a call may run without any sign of life
	// before the chip starts saying "waiting".
	perfWaitingAfter = 1500 * time.Millisecond
	// perfStaleAfter hides the summary of a call long since finished.
	perfStaleAfter = 5 * time.Minute
	// perfMinSpeedWindow is the shortest generation span a speed is computed
	// from when the server did not report one.
	perfMinSpeedWindow = 50 * time.Millisecond
	// perfMinWidth is the narrowest terminal that gets the chip.
	perfMinWidth = 90
)

// perfState is one agent's inference stats for its current or last model call.
type perfState struct {
	started    time.Time // the call was sent (or its connection recovered)
	firstToken time.Time // the first content, reasoning or tool-call token arrived
	lastToken  time.Time // the most recent token arrived
	tokenBytes int       // size of everything generated so far, to see growth

	progress *client.PromptProgress // latest prompt-evaluation progress
	timings  client.Timings         // latest server-reported timings

	final    bool    // the call has ended
	finalTPS float64 // average generation speed once final, 0 when unknown
}

// begin starts tracking a new model call.
func (p *perfState) begin(now time.Time) {
	*p = perfState{started: now}
}

// observe folds one streaming event of the call into the stats.
func (p *perfState) observe(ev common.ContentEvent, now time.Time) {
	if p.started.IsZero() {
		p.started = now
	}
	if ev.Progress != nil {
		prog := *ev.Progress
		p.progress = &prog
	}
	if ev.Timings != (client.Timings{}) {
		p.timings = ev.Timings
	}

	size := len(ev.Content) + len(ev.ReasoningContent)
	for _, tc := range ev.ToolCalls {
		size += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	if size > p.tokenBytes {
		if p.firstToken.IsZero() {
			p.firstToken = now
		}
		p.lastToken = now
		p.tokenBytes = size
	}

	if ev.Completed {
		p.finish(ev.Usage)
	}
}

// finish closes the call and settles its average generation speed: the
// server's own figure when it gave one, otherwise tokens over generation time.
func (p *perfState) finish(usage client.Usage) {
	p.final = true
	switch {
	case p.timings.PredictedPerSecond > 0:
		p.finalTPS = p.timings.PredictedPerSecond
	case usage.CompletionTokens > 1 && !p.firstToken.IsZero():
		if span := p.lastToken.Sub(p.firstToken); span >= perfMinSpeedWindow {
			// N tokens span N-1 intervals between the first and the last.
			p.finalTPS = float64(usage.CompletionTokens-1) / span.Seconds()
		}
	}
}

// abort closes a call that ended without a normal completion (error, stop).
func (p *perfState) abort() {
	if !p.started.IsZero() {
		p.final = true
	}
}

// recovered restarts the clock after a retried connection came back, so the
// time-to-first-token does not include the retry backoff.
func (p *perfState) recovered(now time.Time) {
	if !p.started.IsZero() && p.firstToken.IsZero() {
		p.started = now
	}
}

func (p *perfState) ttft() (time.Duration, bool) {
	if p.started.IsZero() || p.firstToken.IsZero() {
		return 0, false
	}
	return p.firstToken.Sub(p.started), true
}

// liveTPS is the generation speed to show right now, 0 when not known.
func (p *perfState) liveTPS() float64 {
	if p.final {
		return p.finalTPS
	}
	return p.timings.PredictedPerSecond
}

// prefill returns how far along prompt evaluation is, as a fraction and in
// tokens per second, ok=false without usable progress data.
func (p *perfState) prefill() (frac float64, tps float64, ok bool) {
	pr := p.progress
	if pr == nil {
		return 0, 0, false
	}
	total, done := pr.Total-pr.Cache, pr.Processed-pr.Cache
	if total <= 0 || done < 0 {
		return 0, 0, false
	}
	frac = min(1, float64(done)/float64(total))
	if pr.TimeMS > 100 && done > 0 {
		tps = float64(done) / (pr.TimeMS / 1000)
	}
	return frac, tps, true
}

// isPrefillOnly reports whether an event is just prompt-evaluation progress:
// nothing has been generated yet, so it must not flip the agent to "streaming".
func isPrefillOnly(ev common.ContentEvent) bool {
	return ev.Progress != nil && !ev.Completed &&
		ev.Content == "" && ev.ReasoningContent == "" && len(ev.ToolCalls) == 0 &&
		ev.Usage.TotalTokens == 0
}

// perfChip renders the stats chip for the status bar, "" when there is nothing
// worth showing.
func perfChip(p *perfState, now time.Time) string {
	if p == nil || p.started.IsZero() {
		return ""
	}
	label := lipgloss.NewStyle().Foreground(mutedTextColor).Background(appBgColor)
	sep := label.Render(" · ")
	value := lipgloss.NewStyle().Foreground(secondaryColor).Background(appBgColor)

	if p.final {
		if now.Sub(p.started) > perfStaleAfter && now.Sub(p.lastToken) > perfStaleAfter {
			return ""
		}
		value = lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
	}

	// Before the first token: prompt evaluation or plain waiting.
	if p.firstToken.IsZero() {
		if p.final {
			return ""
		}
		if frac, tps, ok := p.prefill(); ok {
			out := label.Render("prompt ") + value.Render(fmt.Sprintf("%d%%", int(frac*100)))
			if tps > 0 {
				out += sep + value.Render(fmtRate(tps)) + label.Render(" t/s")
			}
			return out
		}
		if elapsed := now.Sub(p.started); elapsed >= perfWaitingAfter {
			return label.Render("waiting ") + value.Render(fmtDuration(elapsed))
		}
		return ""
	}

	var parts []string
	if tps := p.liveTPS(); tps > 0 {
		parts = append(parts, value.Render(fmtRate(tps))+label.Render(" t/s"))
	}
	if d, ok := p.ttft(); ok {
		parts = append(parts, label.Render("ttft ")+value.Render(fmtDuration(d)))
	}
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, part := range parts[1:] {
		out += sep + part
	}
	return out
}

// fmtRate formats a tokens-per-second figure: one decimal below 100, none above.
func fmtRate(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fk", v/1000)
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// fmtDuration formats a latency: milliseconds below a second, then seconds.
func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
}
