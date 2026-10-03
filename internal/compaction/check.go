package compaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file is Step 16 of the implementation plan: the preflight check (a
// port of the reference check.py) plus the light startup probe. Both make
// REAL requests against the resolved backend — never a stub — and answer the
// three questions that would have caught the too-small local backend before
// it was integrated into a session:
//
//  1. questions — a minimal ScoreBatch parses and every id comes back with a
//     numeric score (the port's decision protocol is ONE "noul" question per
//     item, so "all question types" here means every item of the batch),
//  2. gate — a Pipeline over a synthetic ~2KB tool output actually relocates
//     something: with the keep threshold pinned at 1.0 every score below 1.0
//     elides, whatever the real scorer answers, proving the gate produces
//     [[elided id=r:…]] pointers and store records from real output,
//  3. expand — Reconstruct expands the pointer back byte for byte.
//
// The first failing stage stops the run (the later stages depend on the
// earlier ones), and its Detail names the failure class (the Step 15 typed
// errors) so the operator learns WHICH thing broke — bad key, malformed
// request, unreachable server — not just that something did. The offline
// variant (RunPreflightOffline, in offline.go) runs the same stages over the
// scripted scorer for the compaction-backend "offline" demo path: its stages
// are local by design, not stubbed backends.

// Stage names in a preflight report, in run order. "backend" is stage 0: the
// report's opening line (and, when the CLI cannot resolve a backend at all,
// the only entry — the three real stages cannot run without one).
const (
	CheckStageBackend   = "backend"
	CheckStageQuestions = "questions"
	CheckStageGate      = "gate"
	CheckStageExpand    = "expand"
)

// checkToolName is the tool name the gate stage compacts for. It salts the
// content id of the relocated run, exactly as a real tool name would.
const checkToolName = "preflight"

// probeTask is the task the probe batches score against; probeTexts are the
// item texts. Small on purpose: the probe (and the questions stage) measures
// "can this backend speak the decisions protocol", not "how fast is it on a
// real workload".
const probeTask = "Preflight probe: score how essential each segment is for the ongoing task."

// probeTexts returns count item texts for the probe batch (capped at the
// three the questions stage uses).
func probeTexts(count int) map[string]Item {
	texts := []string{
		"Preflight probe segment 1: the build log shows three failing tests in package compaction, all timing out on the same fixture.",
		"Preflight probe segment 2: a benchmark table timing the scoring endpoint across batch sizes of 1, 8, and 32 questions.",
		"Preflight probe segment 3: a stack trace from an unrelated crash that was already fixed last week.",
	}
	if count > len(texts) {
		count = len(texts)
	}
	items := make(map[string]Item, count)
	for i := 0; i < count; i++ {
		items[fmt.Sprintf("probe-%d", i+1)] = Item{Text: texts[i]}
	}
	return items
}

// CheckResult is one preflight stage's outcome: its Stage name, whether it
// passed, how long the real work took, and a Detail that on failure names the
// failure class (bad or missing API key, malformed request, unreachable
// server, oversized output) plus the underlying error.
type CheckResult struct {
	Stage   string
	OK      bool
	Latency time.Duration
	Detail  string
}

// RunPreflight runs the three-stage compaction preflight against the REAL
// resolved backend and reports one CheckResult per stage, stage 0 ("backend")
// first. apiKey overrides backend.APIKey when non-empty (the same rule as
// NewDecisionClient); hc, when non-nil, replaces the check clients' transport
// (tests inject fast/recorded ones). The first failing stage stops the run —
// the gate stage needs a scoring backend and the expand stage needs the gate
// stage's pointers — so a failed report is shorter than a passed one. The
// boolean is the overall verdict: every stage OK.
//
// The stages run over ONE throwaway client (never a production client: the
// check must not share rate-limit state or an auth poison flag with a live
// pipeline) that sends ONE attempt per request: a preflight names what broke,
// it does not ride out an outage with retries. The offline preflight
// (RunPreflightOffline) runs the same stages over the scripted scorer.
func RunPreflight(ctx context.Context, backend ResolvedBackend, apiKey string, hc *http.Client) ([]CheckResult, bool) {
	return runPreflightStages(ctx, newCheckClient(backend, apiKey, hc), backendDetail(backend, apiKey))
}

// runPreflightStages is the three-stage body shared by the backend preflight
// (RunPreflight) and the offline one (RunPreflightOffline): scorer scores
// every stage, backendLine is stage 0's opening line. Stage order and the
// stop-at-first-failure contract are documented on RunPreflight.
func runPreflightStages(ctx context.Context, scorer Scorer, backendLine string) ([]CheckResult, bool) {
	results := make([]CheckResult, 0, 4)

	// Stage 0 — backend: the report's opening line (name, endpoint, model,
	// key provenance). Reaching here at all means resolution succeeded; the
	// CLI turns a resolution failure into this same stage with OK=false.
	results = append(results, CheckResult{
		Stage:  CheckStageBackend,
		OK:     true,
		Detail: backendLine,
	})

	// Stage 1 — questions: a minimal batch must parse and every id must
	// come back with a numeric score and no failure.
	start := time.Now()
	qErr := probeScoreBatch(ctx, scorer, len(probeTexts(3)))
	qr := CheckResult{Stage: CheckStageQuestions, Latency: time.Since(start)}
	if qErr != nil {
		qr.Detail = failureDetail(qErr)
		return append(results, qr), false
	}
	qr.OK = true
	qr.Detail = "3/3 questions answered with numeric scores (noul protocol)"
	results = append(results, qr)

	// Stage 2 — gate: a throwaway pipeline (in-memory store, no shadow log)
	// compacts a synthetic ~2KB tool output with the keep threshold pinned
	// at 1.0, so every score below 1.0 elides whatever the scorer answers.
	original := checkToolOutput()
	store := NewStore()
	start = time.Now()
	compacted, gErr := checkGate(ctx, scorer, store, original)
	gr := CheckResult{Stage: CheckStageGate, Latency: time.Since(start)}
	if gErr != nil {
		gr.Detail = failureDetail(gErr)
		return append(results, gr), false
	}
	if !strings.Contains(compacted.CompactText, "[[elided id=r:") {
		gr.Detail = "gate relocated nothing: no [[elided id=r: pointer in the compacted output (no segment scored below the keep threshold 1.0)"
		return append(results, gr), false
	}
	if store.Len() < 1 {
		gr.Detail = "gate produced pointers but the store gained no record: expanding them would fail"
		return append(results, gr), false
	}
	gr.OK = true
	gr.Detail = fmt.Sprintf("gate relocated %d run(s) — %d of %d segments, %d tokens — into %d store record(s)",
		len(compacted.Elided), countElidedSegments(compacted.Elided), len(SegmentSegments(original, 0)),
		countElidedTokens(compacted.Elided), store.Len())
	results = append(results, gr)

	// Stage 3 — expand: the pointer(s) must reconstruct the original output
	// byte for byte.
	start = time.Now()
	expanded := Reconstruct(compacted.CompactText, store)
	er := CheckResult{Stage: CheckStageExpand, Latency: time.Since(start)}
	if expanded != original {
		er.Detail = fmt.Sprintf("reconstructed text differs from the original (%d vs %d bytes)", len(expanded), len(original))
		return append(results, er), false
	}
	er.OK = true
	er.Detail = fmt.Sprintf("%d pointer(s) expanded back byte for byte (%d bytes)", len(compacted.Elided), len(original))
	return append(results, er), true
}

// ProbeBackend is the light startup probe (Step 16): one ScoreBatch with a
// single small item against a throwaway client. Startup calls it in a
// goroutine AFTER the TUI is up, so it never blocks the first paint; on a
// typed auth rejection the caller disables the session's scoring the same way
// a live 401 would (the probe's client is throwaway, so the live pipeline
// would otherwise only learn on its first real call), and any other failure
// just warns — scoring is fail-open by contract. The returned error is the
// client's classified failure (a *Error for protocol failures).
func ProbeBackend(ctx context.Context, backend ResolvedBackend, apiKey string) error {
	return probeScoreBatch(ctx, newCheckClient(backend, apiKey, nil), 1)
}

// probeScoreBatch is the questions-stage logic shared by RunPreflight (three
// items) and ProbeBackend (one): one ScoreBatch whose every id must come back
// with a score and without a failure. ScoreBatch fail-opens rather than
// dropping ids, so a missing score is defensive; the real verdict is the
// joined-error return, non-nil exactly when at least one item failed. The
// scorer is the Scorer interface: the offline preflight passes its scripted
// scorer, which by construction always answers every id.
func probeScoreBatch(ctx context.Context, c Scorer, count int) error {
	items := probeTexts(count)
	scores, err := c.ScoreBatch(ctx, probeTask, items)
	if err != nil {
		return err
	}
	for id := range items {
		if _, ok := scores[id]; !ok {
			return fmt.Errorf("decision response missing a score for %q", id)
		}
	}
	return nil
}

// newCheckClient builds a throwaway decisions client for the preflight and
// the startup probe: a fresh client (never a production one), hc swapped in
// when non-nil, and ONE attempt per request — a preflight names what broke,
// it does not ride out an outage with retries (retrying would only stretch
// the report or the startup path past the caller's timeout).
func newCheckClient(backend ResolvedBackend, apiKey string, hc *http.Client) *DecisionClient {
	c := NewDecisionClient(backend, apiKey)
	if hc != nil {
		c.http = hc
	}
	c.maxAttempts = 1
	c.baseBackoff, c.maxBackoff = time.Millisecond, time.Millisecond
	return c
}

// checkGate runs the gate stage: a throwaway pipeline (in-memory store, no
// shadow log — the check writes nothing to disk) compacts the synthetic tool
// output with the keep threshold pinned at 1.0 and every gate guard neutral
// (no protected kinds, no tripwire — elided tokens can never exceed 100% of
// the total — and the token min-gate off). Whatever the scorer answers, any
// score below 1.0 elides: the stage proves the gate relocates real output
// into the store and leaves pointers behind. The backend preflight passes a
// check client shrunk to one attempt (newCheckClient); the offline preflight
// passes its scripted scorer.
func checkGate(ctx context.Context, scorer Scorer, store *Store, output string) (CompactResult, error) {
	p := newPipelineWithScorer(scorer, nil, 0)
	p.warnTo = io.Discard // the check's own report is the warning surface
	p.EnableRelocation(store, 1.0)
	p.ApplyGateConfig(GateConfig{
		KeepThreshold:    1.0,
		MinGateTokens:    0,
		MaxElideFraction: 1.0,
		ProtectedKinds:   map[SegmentKind]float64{},
	})
	return p.CompactToolOutput(ctx, checkToolName, output)
}

// checkToolOutput builds the synthetic ~2KB tool output the gate stage
// compacts: four ~500-byte paragraphs (above the 80-byte tiny-paragraph
// floor, under the 1200-byte segment cap) separated by blank lines, so the
// output segments into four pieces and the gate has several independent
// elision decisions to make.
func checkToolOutput() string {
	var b strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&b, "preflight segment %d: %s\n\n", i, strings.Repeat(fmt.Sprintf("word%d ", i), 84))
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}

// countElidedSegments sums the Elided runs' segment counts.
func countElidedSegments(runs []ElidedSegment) int {
	n := 0
	for _, r := range runs {
		n += r.Segments
	}
	return n
}

// countElidedTokens sums the Elided runs' token counts.
func countElidedTokens(runs []ElidedSegment) int {
	n := 0
	for _, r := range runs {
		n += r.Tokens
	}
	return n
}

// backendDetail renders the report's opening line: which backend, which
// endpoint, which model, and where the API key came from.
func backendDetail(backend ResolvedBackend, apiKey string) string {
	key := "none"
	if backend.APIKey != "" || apiKey != "" {
		key = string(backend.KeySource)
		if key == "" {
			key = "provided"
		}
	}
	return fmt.Sprintf("backend %q url %s model %q (api key: %s)",
		backend.Backend.Name, backend.Backend.URL, backend.Backend.Model, key)
}

// failureDetail renders a stage failure: the taxonomy class first — the
// report must name WHICH thing broke — then the underlying error with its
// newlines flattened so the report stays line-oriented. The class wordings
// are the check's own operator-facing phrasing; the Step 15 typed errors
// carry their policy-oriented detail() strings underneath.
func failureDetail(err error) string {
	class := ""
	var ce *Error
	if errors.As(err, &ce) {
		switch ce.Kind {
		case KindAuth:
			class = "bad or missing API key"
		case KindValidation:
			class = "malformed request (backend rejected it)"
		case KindBudget:
			class = "output too large for the backend budget"
		case KindUnavailable:
			class = "unreachable or erroring backend"
		}
	}
	msg := strings.ReplaceAll(err.Error(), "\n", "; ")
	if class == "" {
		return msg
	}
	return class + ": " + msg
}

// FormatCheckReport renders the human-readable preflight report: one status
// line per stage (status, latency, detail), a verdict naming the first
// failing stage on failure, and the cost line. The decisions client does not
// track token usage (the wire usage object is decoded and discarded), so the
// report says so instead of inventing a cost.
func FormatCheckReport(results []CheckResult, ok bool) string {
	var b strings.Builder
	b.WriteString("late compaction preflight\n")
	for _, r := range results {
		status := "ok  "
		if !r.OK {
			status = "FAIL"
		}
		detail := strings.ReplaceAll(r.Detail, "\n", "; ")
		if detail == "" {
			detail = "-"
		}
		fmt.Fprintf(&b, "  [%s] %-9s %8s  %s\n", status, r.Stage, formatLatency(r.Latency), detail)
	}
	if ok {
		fmt.Fprintf(&b, "result: PASS (%d/%d stages ok)\n", len(results), len(results))
	} else {
		stage := ""
		for _, r := range results {
			if !r.OK {
				stage = r.Stage
				break
			}
		}
		if stage == "" {
			b.WriteString("result: FAIL\n")
		} else {
			fmt.Fprintf(&b, "result: FAIL (stage %q failed)\n", stage)
		}
	}
	b.WriteString("cost: n/a (the decisions client does not track token usage)\n")
	return b.String()
}

// formatLatency renders one stage's latency for the report column.
func formatLatency(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
}
