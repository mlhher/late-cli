package compaction

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- taxonomy unit tests ------------------------------------------------------

// TestErrorKindString pins the lowercase class names used in messages.
func TestErrorKindString(t *testing.T) {
	cases := map[ErrorKind]string{
		KindAuth:        "auth",
		KindValidation:  "validation",
		KindBudget:      "budget",
		KindUnavailable: "unavailable",
		ErrorKind(99):   "errorkind(99)",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("ErrorKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

// TestClassifyStatus covers the status → class mapping: 401/403 auth;
// 400/404/422 (and unlisted 4xx) validation, never retried; 429/408/5xx and
// status 0 (transport) unavailable.
func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{401, KindAuth},
		{403, KindAuth},
		{400, KindValidation},
		{404, KindValidation},
		{422, KindValidation},
		{402, KindValidation}, // unlisted 4xx: deterministic, never retried
		{405, KindValidation},
		{409, KindValidation},
		{429, KindUnavailable},
		{408, KindUnavailable},
		{500, KindUnavailable},
		{501, KindUnavailable},
		{502, KindUnavailable},
		{503, KindUnavailable},
		{504, KindUnavailable},
		{529, KindUnavailable},
		{302, KindUnavailable},
		// (status 0 with no body and no cause classifies to nil — there is
		// nothing to describe; with a transport cause it is Unavailable,
		// covered in TestClassifyStatusCauseAndBody.)
	}
	for _, tc := range cases {
		e := classifyStatus(opScoreBatch, tc.status, "", nil)
		if e == nil {
			t.Fatalf("classifyStatus(%d) = nil, want an error", tc.status)
		}
		if e.Kind != tc.want {
			t.Errorf("classifyStatus(%d).Kind = %v, want %v", tc.status, e.Kind, tc.want)
		}
		if e.Status != tc.status {
			t.Errorf("classifyStatus(%d).Status = %d, want %d", tc.status, e.Status, tc.status)
		}
	}
}

// TestClassifyStatusCauseAndBody: a non-nil cause is preserved through
// Unwrap (the *DecisionStatusError's Retry-After must survive), and the body
// becomes the cause when no cause is given.
func TestClassifyStatusCauseAndBody(t *testing.T) {
	cause := &DecisionStatusError{StatusCode: 429, RetryAfter: 3 * time.Second}
	e := classifyStatus(opScoreBatch, 429, `{"error":{"message":"slow down"}}`, cause)
	if e.Err != cause {
		t.Errorf("classifyStatus().Err = %v, want the provided cause", e.Err)
	}
	if got := errors.Unwrap(e); got != cause {
		t.Errorf("Unwrap() = %v, want the cause", got)
	}
	var se *DecisionStatusError
	if !errors.As(e, &se) || se.RetryAfter != 3*time.Second {
		t.Errorf("errors.As through the typed error lost the *DecisionStatusError: %v", e)
	}

	bodyOnly := classifyStatus(opScoreBatch, 401, `{"error":{"message":"bad key"}}`, nil)
	if bodyOnly == nil || bodyOnly.Err == nil || !strings.Contains(bodyOnly.Err.Error(), "bad key") {
		t.Errorf("classifyStatus with only a body must wrap the body as the cause, got %+v", bodyOnly)
	}

	plain := classifyStatus(opScoreBatch, 0, "", errors.New("connection refused"))
	if plain == nil || plain.Kind != KindUnavailable || plain.Err == nil {
		t.Errorf("classifyStatus(0, cause) = %+v, want Unavailable carrying the cause", plain)
	}

	if e := classifyStatus(opScoreBatch, 0, "", nil); e != nil {
		t.Errorf("classifyStatus with nothing to describe = %v, want nil", e)
	}
}

// TestErrorMessageFormat pins the message shape: "compaction: auth (401)
// during score-batch: bad or missing API key: <cause>".
func TestErrorMessageFormat(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		want string
	}{
		{
			name: "auth with status and op",
			err:  authError(401, opScoreBatch, errors.New("bad key")),
			want: "compaction: auth (401) during score-batch: bad or missing API key: bad key",
		},
		{
			name: "validation never retried",
			err:  validationError(422, opScoreBatch, nil),
			want: "compaction: validation (422) during score-batch: malformed request (never retried — this is a bug, not a transient failure)",
		},
		{
			name: "budget raised before send",
			err:  budgetError(opScoreBatch, errors.New("item too large to score")),
			want: "compaction: budget during score-batch: request would violate a hard limit (split and retry): item too large to score",
		},
		{
			name: "unavailable with status",
			err:  unavailableError(503, opScoreBatch, nil),
			want: "compaction: unavailable (503) during score-batch: backend unavailable",
		},
		{
			name: "no op",
			err:  authError(403, "", nil),
			want: "compaction: auth (403): bad or missing API key",
		},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestConstructorsSetFields: each class constructor sets Kind plus the
// fields it is given.
func TestConstructorsSetFields(t *testing.T) {
	cause := errors.New("boom")
	cases := []struct {
		got  *Error
		want Error
	}{
		{authError(401, "op", cause), Error{Kind: KindAuth, Status: 401, Op: "op", Err: cause}},
		{validationError(422, "op", cause), Error{Kind: KindValidation, Status: 422, Op: "op", Err: cause}},
		{budgetError("op", cause), Error{Kind: KindBudget, Status: 0, Op: "op", Err: cause}},
		{unavailableError(429, "op", cause), Error{Kind: KindUnavailable, Status: 429, Op: "op", Err: cause}},
	}
	for i, tc := range cases {
		if *tc.got != tc.want {
			t.Errorf("constructor %d = %+v, want %+v", i, *tc.got, tc.want)
		}
	}
}

// TestIsRetryableDecisionError_Typed extends the status/transport matrix
// with the typed taxonomy: auth, validation, and budget are never retried;
// unavailable is (unless a permanent transport failure hides underneath).
func TestIsRetryableDecisionError_Typed(t *testing.T) {
	never := []error{
		authError(401, opScoreBatch, errors.New("bad key")),
		authError(403, opScoreBatch, nil),
		validationError(400, opScoreBatch, nil),
		validationError(422, opScoreBatch, nil),
		budgetError(opScoreBatch, errors.New("too large")),
		// A wrapped cause must not smuggle retryability past the class.
		fmt.Errorf("score item %q: %w", "seg-1", authError(401, opScoreBatch, nil)),
	}
	for _, err := range never {
		if isRetryableDecisionError(err) {
			t.Errorf("isRetryableDecisionError(%v) = true, want false (never retried)", err)
		}
	}

	retryable := []error{
		unavailableError(429, opScoreBatch, nil),
		unavailableError(503, opScoreBatch, &DecisionStatusError{StatusCode: 503}),
		unavailableError(0, opScoreBatch, errors.New("connection refused")),
	}
	for _, err := range retryable {
		if !isRetryableDecisionError(err) {
			t.Errorf("isRetryableDecisionError(%v) = false, want true", err)
		}
	}

	// A permanent TLS failure classified Unavailable still must not be
	// retried: the transport checks see through the typed wrapper (the
	// real shape — http.Client wraps transport failures in a *url.Error).
	permanent := unavailableError(0, opScoreBatch,
		&url.Error{Op: "Post", URL: "https://x", Err: errors.New("tls: handshake failure")})
	if isRetryableDecisionError(permanent) {
		t.Errorf("isRetryableDecisionError(%v) = true, want false (permanent TLS)", permanent)
	}
}

// --- wire behavior (httptest) --------------------------------------------------

// TestScoreBatch_AuthErrorNoRetryPoisonsClient: a 401 is answered on the
// first attempt — no retries — every item fail-opens with a typed auth
// error, and the client is poisoned: Unavailable() flips and a second
// ScoreBatch fails open with zero further requests.
func TestScoreBatch_AuthErrorNoRetryPoisonsClient(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusUnauthorized, `{"error": {"message": "bad key"}}`
	})
	c := fastClient(d.srv.URL, 4)

	items := map[string]Item{
		"seg-1": {Text: "a", Tokens: 5},
		"seg-2": {Text: "b", Tokens: 5},
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want a recorded auth failure")
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("got %d requests, want 1 (401 is never retried)", got)
	}
	for ref := range items {
		if scores[ref] != keepScore {
			t.Errorf("scores[%s] = %v, want %v (fail-open)", ref, scores[ref], keepScore)
		}
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Kind != KindAuth || ae.Status != 401 {
		t.Fatalf("error = %v, want a *Error KindAuth (401) through the item join", err)
	}
	var ise *ItemScoreError
	if !errors.As(err, &ise) || ise.ItemID != "seg-1" {
		t.Errorf("error = %v, want *ItemScoreError entries for the failed items", err)
	}
	if !c.Unavailable() {
		t.Error("an auth rejection must poison the client (Unavailable() = true)")
	}

	// The poisoned client fail-opens without touching the network again.
	scores2, err2 := c.ScoreBatch(context.Background(), "task", items)
	if err2 == nil {
		t.Fatal("second ScoreBatch() error = nil, want the disabled-scoring auth error")
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("poisoned client made %d requests total, want 1 (no network)", got)
	}
	for ref := range items {
		if scores2[ref] != keepScore {
			t.Errorf("second scores[%s] = %v, want %v (fail-open)", ref, scores2[ref], keepScore)
		}
	}
	if !errors.Is(err2, errScoringDisabled) {
		t.Errorf("second error = %v, want it rooted in errScoringDisabled", err2)
	}
	var ae2 *Error
	if !errors.As(err2, &ae2) || ae2.Kind != KindAuth {
		t.Errorf("second error = %v, want a typed auth error", err2)
	}
}

// TestScoreBatch_ValidationErrorNoRetry: 422 — the reference's
// JevValidationError — comes back on the first attempt, never retried, and
// does NOT poison the client (the key is fine; the request or the backend is
// not).
func TestScoreBatch_ValidationErrorNoRetry(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusUnprocessableEntity, `{"error": {"message": "malformed"}}`
	})
	c := fastClient(d.srv.URL, 4)

	_, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}})
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want the typed validation error")
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("got %d requests, want 1 (422 is never retried)", got)
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Kind != KindValidation || ae.Status != 422 {
		t.Fatalf("error = %v, want a *Error KindValidation (422)", err)
	}
	if c.Unavailable() {
		t.Error("a validation failure must not poison the client")
	}
}

// TestScoreBatch_BadRequestNoRetry: the 400 a too-small backend throws at
// every message must not burn retries — one request, typed validation error,
// client still usable (unpoisoned).
func TestScoreBatch_BadRequestNoRetry(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusBadRequest, `{"error": {"message": "model too small"}}`
	})
	c := fastClient(d.srv.URL, 4)

	_, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}})
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want the typed validation error")
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("got %d requests, want 1 (400 must not burn retries)", got)
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Kind != KindValidation || ae.Status != 400 {
		t.Fatalf("error = %v, want a *Error KindValidation (400)", err)
	}
	if c.Unavailable() {
		t.Error("a 400 must not poison the client")
	}
}

// TestScoreBatch_AuthStopsRemainingBatches: a 33-item output splits into two
// requests; the auth rejection on the first must stop the second (poisoned
// mid-call) — exactly one request ever reaches the wire.
func TestScoreBatch_AuthStopsRemainingBatches(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusUnauthorized, `{"error": {"message": "bad key"}}`
	})
	c := fastClient(d.srv.URL, 4)

	items := make(map[string]Item, 33)
	for i := 1; i <= 33; i++ {
		items[fmt.Sprintf("seg-%d", i)] = Item{Text: fmt.Sprintf("paragraph %d", i), Tokens: 10}
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want the typed auth error")
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("got %d requests, want 1 (the second batch must be poisoned off the wire)", got)
	}
	for i := 1; i <= 33; i++ {
		ref := fmt.Sprintf("seg-%d", i)
		if scores[ref] != keepScore {
			t.Errorf("scores[%s] = %v, want %v (fail-open)", ref, scores[ref], keepScore)
		}
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Kind != KindAuth {
		t.Errorf("error = %v, want a typed auth error", err)
	}
}

// TestScoreBatch_UnavailableRetriedThenSuccess: a 500 is transient — the
// retry succeeds, the real scores flow, nothing is poisoned.
func TestScoreBatch_UnavailableRetriedThenSuccess(t *testing.T) {
	d := newDecisionsServer(t, func(attempt int, req capturedRequest) (int, string) {
		if attempt == 1 {
			return http.StatusInternalServerError, `{"error": {"message": "boom"}}`
		}
		return echoHandler(attempt, req)
	})
	c := fastClient(d.srv.URL, 0)

	scores, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}})
	if err != nil {
		t.Fatalf("ScoreBatch() error = %v, want success after the retry", err)
	}
	if got := len(d.requests()); got != 2 {
		t.Errorf("got %d requests, want 2 (500 then success)", got)
	}
	if scores["seg-1"] != 0.01 {
		t.Errorf("scores[seg-1] = %v, want the echoHandler score 0.01", scores["seg-1"])
	}
	if c.Unavailable() {
		t.Error("a recovered outage must not poison the client")
	}
}

// TestScoreBatch_UnavailableAlwaysFailsOpen: a hard outage burns the full
// attempt budget, then keeps everything with a typed unavailable error — and
// leaves the client usable (an outage is not an auth failure).
func TestScoreBatch_UnavailableAlwaysFailsOpen(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusServiceUnavailable, `{"error": {"message": "overloaded"}}`
	})
	c := fastClient(d.srv.URL, 4)

	scores, err := c.ScoreBatch(context.Background(), "task", map[string]Item{
		"seg-1": {Text: "a", Tokens: 5},
		"seg-2": {Text: "b", Tokens: 5},
	})
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want a recorded failure")
	}
	if got := len(d.requests()); got != 4 {
		t.Errorf("got %d requests, want 4 (full attempt budget)", got)
	}
	for ref := range map[string]bool{"seg-1": true, "seg-2": true} {
		if scores[ref] != keepScore {
			t.Errorf("scores[%s] = %v, want %v (fail-open)", ref, scores[ref], keepScore)
		}
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Kind != KindUnavailable || ae.Status != 503 {
		t.Errorf("error = %v, want a *Error KindUnavailable (503)", err)
	}
	if c.Unavailable() {
		t.Error("an outage must not poison the client (only auth does)")
	}
}

// TestScoreBatch_BudgetErrorIsTyped: the oversized-item skip keeps its
// per-item fail-open contract, and its cause is now a typed budget error
// (raised before send).
func TestScoreBatch_BudgetErrorIsTyped(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := fastClient(d.srv.URL, 0)

	items := map[string]Item{
		"seg-1":    {Text: "small", Tokens: 100},
		"seg-huge": {Text: "huge", Tokens: MaxStateTokens},
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want the oversized-item error")
	}
	var ise *ItemScoreError
	if !errors.As(err, &ise) || ise.ItemID != "seg-huge" {
		t.Fatalf("error = %v, want *ItemScoreError for seg-huge", err)
	}
	var be *Error
	if !errors.As(ise.Err, &be) || be.Kind != KindBudget {
		t.Errorf("item error = %v, want a typed budget cause", ise.Err)
	}
	if scores["seg-huge"] != keepScore {
		t.Errorf("oversized item score = %v, want %v (per-item fail-open)", scores["seg-huge"], keepScore)
	}
	if scores["seg-1"] != 0.01 {
		t.Errorf("scores[seg-1] = %v, want 0.01 (the rest of the batch still scores)", scores["seg-1"])
	}
}

// --- pipeline auth-poison behavior ---------------------------------------------

// TestPipeline_AuthErrorDisablesScoringForSession: the first auth rejection
// fail-opens with the typed error and logs exactly one warning; every later
// CompactToolOutput returns the output untouched with Disabled set and makes
// no further backend requests.
func TestPipeline_AuthErrorDisablesScoringForSession(t *testing.T) {
	output, _, _, _ := relocationOutput()
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusUnauthorized, `{"error": {"message": "bad key"}}`
	})
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	// The relocation fixtures are a few dozen tokens — far below the
	// reference 400-token min-gate — so disable the gate or the scoring
	// round trip (and with it the auth failure) never happens.
	applyTestGate(p, 0.35, 0.7)
	var warnings strings.Builder
	p.warnTo = &warnings

	first, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err == nil {
		t.Fatal("first CompactToolOutput() error = nil, want the typed auth error")
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Kind != KindAuth {
		t.Fatalf("error = %v, want a typed auth error", err)
	}
	if first.CompactText != output {
		t.Errorf("fail-open must return the original text unchanged, got:\n%s", first.CompactText)
	}
	if first.Disabled == "" {
		t.Error("first result must report Disabled with the reason")
	}
	if got := warnings.String(); !strings.Contains(got, "compaction scoring disabled") {
		t.Errorf("warnings = %q, want exactly one clear disable note", got)
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("got %d requests, want 1", got)
	}
	if store.Len() != 0 {
		t.Error("store must stay empty when scoring failed")
	}

	// Every later call short-circuits: no request, original text, Disabled
	// still reported, and NO additional warning (the sync.Once-style guard).
	second, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("second CompactToolOutput() error = %v, want nil (disabled, not failed)", err)
	}
	if second.CompactText != output || second.Disabled == "" {
		t.Errorf("second result = %+v, want the original text with Disabled set", second)
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("poisoned pipeline made %d requests total, want 1 (no request storms)", got)
	}
	if got := warnings.String(); strings.Count(got, "\n") != 1 {
		t.Errorf("warnings = %q, want exactly one line no matter how many calls fail", got)
	}
	if store.Len() != 0 {
		t.Error("a disabled pipeline must not store anything")
	}
}
