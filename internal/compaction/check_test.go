package compaction

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// allScoresHandler answers every request with the given score for every
// asked ref — the uniform-scorer stand-in for gate-stage edge cases.
func allScoresHandler(score float64) func(int, capturedRequest) (int, string) {
	return func(_ int, req capturedRequest) (int, string) {
		scores := make(map[string]float64, len(req.Req.Questions))
		for ref := range req.Req.Questions {
			scores[ref] = score
		}
		return http.StatusOK, answersBody(scores)
	}
}

// statusHandler answers every request with the given status and body: the
// typed-failure stand-in (401 auth, 422 validation).
func statusHandler(status int, body string) func(int, capturedRequest) (int, string) {
	return func(_ int, _ capturedRequest) (int, string) {
		return status, body
	}
}

// checkBackend wraps a decisions-server URL in a ResolvedBackend shaped like
// the providers layer's output.
func checkBackend(url string) ResolvedBackend {
	return ResolvedBackend{
		Backend:   Backend{Name: "test", URL: url, Model: "jev-latest"},
		APIKey:    "test-key",
		KeySource: KeySourceEnv,
	}
}

// TestRunPreflightAllPass: a working backend passes all three real stages —
// questions parse, the gate relocates something from real output into the
// store, and the pointer expands back byte for byte — and the report carries
// the backend stage 0 first.
func TestRunPreflightAllPass(t *testing.T) {
	d := newDecisionsServer(t, echoHandler) // probe ids score 0.42, seg ids 0.01*N
	results, ok := RunPreflight(context.Background(), checkBackend(d.srv.URL), "", nil)
	if !ok {
		t.Fatalf("RunPreflight() ok = false, want all stages to pass:\n%s", FormatCheckReport(results, ok))
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4 (backend, questions, gate, expand)", len(results))
	}
	for i, want := range []string{CheckStageBackend, CheckStageQuestions, CheckStageGate, CheckStageExpand} {
		if results[i].Stage != want {
			t.Errorf("results[%d].Stage = %q, want %q", i, results[i].Stage, want)
		}
		if !results[i].OK {
			t.Errorf("results[%d] (%s) = FAIL, want ok: %s", i, results[i].Stage, results[i].Detail)
		}
		if results[i].Latency < 0 {
			t.Errorf("results[%d].Latency = %v, want non-negative", i, results[i].Latency)
		}
	}
	// The gate stage really relocated: its detail says so, and the expand
	// stage names the byte-for-byte round trip.
	if !strings.Contains(results[2].Detail, "relocated") || !strings.Contains(results[2].Detail, "1 store record") {
		t.Errorf("gate detail = %q, want the relocation summary", results[2].Detail)
	}
	if !strings.Contains(results[3].Detail, "byte for byte") {
		t.Errorf("expand detail = %q, want the round-trip summary", results[3].Detail)
	}
	// Stage 0 names the backend, endpoint, model, and key source.
	for _, want := range []string{`"test"`, d.srv.URL, `"jev-latest"`, "env"} {
		if !strings.Contains(results[0].Detail, want) {
			t.Errorf("backend detail = %q, want it to contain %q", results[0].Detail, want)
		}
	}
}

// TestRunPreflightAuthFailure: a 401 backend fails stage 1 with the auth
// class named, and the later stages never run.
func TestRunPreflightAuthFailure(t *testing.T) {
	d := newDecisionsServer(t, statusHandler(http.StatusUnauthorized, `{"error": {"message": "bad key"}}`))
	results, ok := RunPreflight(context.Background(), checkBackend(d.srv.URL), "", nil)
	if ok {
		t.Fatal("RunPreflight() ok = true, want the auth failure to fail the run")
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (backend + questions; later stages must not run)", len(results))
	}
	failed := results[1]
	if failed.Stage != CheckStageQuestions {
		t.Fatalf("failing stage = %q, want %q", failed.Stage, CheckStageQuestions)
	}
	if failed.OK {
		t.Error("questions stage reported OK on a 401 backend")
	}
	if !strings.Contains(failed.Detail, "bad or missing API key") {
		t.Errorf("detail = %q, want the auth class named", failed.Detail)
	}
}

// TestRunPreflightValidationFailure: a 422 backend (the too-small local
// model's signature failure) is named as a malformed request, not an outage.
func TestRunPreflightValidationFailure(t *testing.T) {
	d := newDecisionsServer(t, statusHandler(http.StatusUnprocessableEntity, `{"error": {"message": "model too small"}}`))
	results, ok := RunPreflight(context.Background(), checkBackend(d.srv.URL), "", nil)
	if ok {
		t.Fatal("RunPreflight() ok = true, want the validation failure to fail the run")
	}
	if len(results) != 2 || results[1].Stage != CheckStageQuestions {
		t.Fatalf("failing stage = %+v, want questions", results)
	}
	if got := results[1].Detail; !strings.Contains(got, "malformed request (backend rejected it)") {
		t.Errorf("detail = %q, want the validation class named", got)
	}
}

// TestRunPreflightUnreachable: a closed port fails stage 1 with the
// unavailable class named (transport failure, not a malformed request).
func TestRunPreflightUnreachable(t *testing.T) {
	// Grab a port and close it: nothing is listening there anymore.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	results, ok := RunPreflight(context.Background(), checkBackend("http://"+addr), "", nil)
	if ok {
		t.Fatal("RunPreflight() ok = true, want the unreachable backend to fail the run")
	}
	if len(results) != 2 || results[1].Stage != CheckStageQuestions {
		t.Fatalf("failing stage = %+v, want questions", results)
	}
	if got := results[1].Detail; !strings.Contains(got, "unreachable or erroring backend") {
		t.Errorf("detail = %q, want the unavailable class named", got)
	}
}

// TestRunPreflightUniformKeepScoresFailGate: a backend that answers 1.0 for
// everything parses fine (stage 1 passes) but relocates nothing at the 1.0
// keep threshold — the gate stage must say so instead of passing vacuously.
func TestRunPreflightUniformKeepScoresFailGate(t *testing.T) {
	d := newDecisionsServer(t, allScoresHandler(1.0))
	results, ok := RunPreflight(context.Background(), checkBackend(d.srv.URL), "", nil)
	if ok {
		t.Fatal("RunPreflight() ok = true, want the vacuous gate to fail the run")
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 (backend + questions + gate; expand must not run)", len(results))
	}
	failed := results[2]
	if failed.Stage != CheckStageGate {
		t.Fatalf("failing stage = %q, want %q", failed.Stage, CheckStageGate)
	}
	if !strings.Contains(failed.Detail, "relocated nothing") {
		t.Errorf("detail = %q, want the no-elision explanation", failed.Detail)
	}
}

// TestProbeBackend: the light startup probe passes against a working backend
// and returns a typed auth error against a 401 one (the caller's cue to
// disable the session's scoring).
func TestProbeBackend(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	if err := ProbeBackend(context.Background(), checkBackend(d.srv.URL), ""); err != nil {
		t.Fatalf("ProbeBackend() error = %v, want nil", err)
	}

	bad := newDecisionsServer(t, statusHandler(http.StatusForbidden, `{"error": {"message": "forbidden"}}`))
	err := ProbeBackend(context.Background(), checkBackend(bad.srv.URL), "")
	if err == nil {
		t.Fatal("ProbeBackend() error = nil, want the 403 classified")
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Kind != KindAuth {
		t.Fatalf("error = %v, want a typed auth error", err)
	}
}

// TestFailureDetailClasses pins the report's class wordings — the operator
// learns WHICH class broke from the detail's prefix.
func TestFailureDetailClasses(t *testing.T) {
	for _, tc := range []struct {
		kind  ErrorKind
		want  string
		cause string
	}{
		{KindAuth, "bad or missing API key", "compaction: auth (401) during score-batch"},
		{KindValidation, "malformed request (backend rejected it)", "compaction: validation (422) during score-batch"},
		{KindBudget, "output too large for the backend budget", "compaction: budget during score-batch"},
		{KindUnavailable, "unreachable or erroring backend", "compaction: unavailable during score-batch"},
	} {
		err := &Error{Kind: tc.kind, Op: opScoreBatch, Err: errors.New(tc.cause)}
		got := failureDetail(err)
		if !strings.HasPrefix(got, tc.want+": ") {
			t.Errorf("failureDetail(%v) = %q, want the %q prefix", tc.kind, got, tc.want)
		}
	}
	// An unclassified error still renders (cause only).
	if got := failureDetail(errors.New("mystery")); got != "mystery" {
		t.Errorf("failureDetail(unclassified) = %q, want the bare cause", got)
	}
	// Joined item errors (ScoreBatch's failure shape) resolve to their class.
	joined := errors.Join(
		&ItemScoreError{ItemID: "probe-1", Err: &Error{Kind: KindAuth, Op: opScoreBatch}},
		&ItemScoreError{ItemID: "probe-2", Err: &Error{Kind: KindAuth, Op: opScoreBatch}},
	)
	if got := failureDetail(joined); !strings.HasPrefix(got, "bad or missing API key: ") {
		t.Errorf("failureDetail(joined) = %q, want the auth class prefix", got)
	}
}

// TestFormatCheckReportPass: the report names every stage, the verdict, and
// the honest cost line.
func TestFormatCheckReportPass(t *testing.T) {
	results := []CheckResult{
		{Stage: CheckStageBackend, OK: true, Detail: `backend "gateway" url https://gw/decisions model "jev-latest" (api key: env)`},
		{Stage: CheckStageQuestions, OK: true, Latency: 142 * time.Millisecond, Detail: "3/3 questions answered with numeric scores (noul protocol)"},
		{Stage: CheckStageGate, OK: true, Latency: 231 * time.Millisecond, Detail: "gate relocated 1 run(s) — 4 of 4 segments, 512 tokens — into 1 store record(s)"},
		{Stage: CheckStageExpand, OK: true, Latency: 900 * time.Microsecond, Detail: "1 pointer(s) expanded back byte for byte (2016 bytes)"},
	}
	out := FormatCheckReport(results, true)
	for _, want := range []string{
		"late compaction preflight",
		"[ok  ] " + CheckStageBackend,
		"[ok  ] " + CheckStageQuestions,
		"[ok  ] " + CheckStageGate,
		"[ok  ] " + CheckStageExpand,
		"142ms",
		"231ms",
		"0.90ms",
		"result: PASS (4/4 stages ok)",
		"cost: n/a (the decisions client does not track token usage)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestFormatCheckReportFailure: the failing stage's row and the verdict carry
// the class-naming detail, and the report stays line-oriented even when the
// underlying error spans lines (ScoreBatch joins per-item errors with \n).
func TestFormatCheckReportFailure(t *testing.T) {
	results := []CheckResult{
		{Stage: CheckStageBackend, OK: true, Detail: `backend "test" url http://x model "m" (api key: env)`},
		{Stage: CheckStageQuestions, OK: false, Latency: 96 * time.Millisecond,
			Detail: "bad or missing API key: score item \"probe-1\": compaction: auth (401) during score-batch: denied\nscore item \"probe-2\": compaction: auth (401) during score-batch: denied"},
	}
	out := FormatCheckReport(results, false)
	for _, want := range []string{
		"[ok  ] " + CheckStageBackend,
		"[FAIL] " + CheckStageQuestions,
		"bad or missing API key",
		`result: FAIL (stage "questions" failed)`,
		"cost: n/a",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// The multi-line detail was flattened: the report has one row per stage
	// plus header, verdict, and cost lines.
	if lines := strings.Count(out, "\n"); lines != 5 {
		t.Errorf("report has %d lines, want 5 (header + 2 stages + verdict + cost):\n%s", lines, out)
	}
	// The latency column formats seconds too.
	if got := formatLatency(1500 * time.Millisecond); got != "1.50s" {
		t.Errorf("formatLatency(1.5s) = %q, want 1.50s", got)
	}
}

// TestCheckToolOutputShape pins the synthetic gate-stage fixture: ~2KB, four
// segments (the fixture must give the gate several decisions to make).
func TestCheckToolOutputShape(t *testing.T) {
	out := checkToolOutput()
	if len(out) < 1800 || len(out) > 2400 {
		t.Errorf("checkToolOutput() = %d bytes, want ~2KB", len(out))
	}
	segs := SegmentSegments(out, 0)
	if len(segs) != 4 {
		t.Fatalf("got %d segments, want 4", len(segs))
	}
	assertSegmentsInvariant(t, out, segs)
}
