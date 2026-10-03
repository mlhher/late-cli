package compaction

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedDecisionRequest mirrors the wire request so tests can assert the
// reference protocol shape (model, state with a {ref,text} items array,
// per-item noul questions with instructions and criteria).
type capturedDecisionRequest struct {
	Model string `json:"model"`
	State struct {
		Task  string      `json:"task"`
		Items []stateItem `json:"items"`
	} `json:"state"`
	Questions map[string]decisionQuestion `json:"questions"`
}

// capturedRequest is one decoded request plus its auth headers.
type capturedRequest struct {
	Req         capturedDecisionRequest
	Auth        string
	Title       string
	ContentType string
	Path        string
}

// decisionsServer is a scripted decisions endpoint. Each attempt calls
// handler with the 1-based attempt number and the decoded request and serves
// the returned status and body. retryAfter, when non-empty, is set on every
// non-200 response.
type decisionsServer struct {
	srv        *httptest.Server
	retryAfter string
	mu         sync.Mutex
	got        []capturedRequest

	handler func(attempt int, req capturedRequest) (int, string)
}

func newDecisionsServer(t *testing.T, handler func(attempt int, req capturedRequest) (int, string)) *decisionsServer {
	t.Helper()
	d := &decisionsServer{handler: handler}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxDecisionResponseBytes))
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var cr capturedRequest
		if err := json.Unmarshal(body, &cr.Req); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cr.Auth = r.Header.Get("Authorization")
		cr.Title = r.Header.Get("X-Title")
		cr.ContentType = r.Header.Get("Content-Type")
		cr.Path = r.URL.Path

		d.mu.Lock()
		d.got = append(d.got, cr)
		attempt := len(d.got)
		d.mu.Unlock()

		status, respBody := handler(attempt, cr)
		if status != http.StatusOK && d.retryAfter != "" {
			w.Header().Set("Retry-After", d.retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *decisionsServer) requests() []capturedRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]capturedRequest(nil), d.got...)
}

// answersBody builds a decisions response serving each ref its score as the
// protocol's noul answer object ({"type":"noul","noul":<score>}).
func answersBody(scores map[string]float64) string {
	answers := make(map[string]any, len(scores))
	for ref, s := range scores {
		answers[ref] = map[string]any{"type": "noul", "noul": s}
	}
	b, err := json.Marshal(map[string]any{"answers": answers, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// echoHandler answers every request with a score derived from the item id
// (ids "seg-N" score 0.01*N, kept inside [0,1] so clamping never kicks in),
// so tests can verify round-tripping.
func echoHandler(_ int, req capturedRequest) (int, string) {
	scores := make(map[string]float64, len(req.Req.Questions))
	for ref := range req.Req.Questions {
		var n int
		if _, err := fmt.Sscanf(ref, "seg-%d", &n); err == nil {
			scores[ref] = float64(n) * 0.01
		} else {
			scores[ref] = 0.42
		}
	}
	return http.StatusOK, answersBody(scores)
}

// fastClient builds a DecisionClient against the given URL with the retry
// curve shrunk for tests, keeping the production attempt count unless
// overridden (attempts <= 0 keeps the default of 4).
func fastClient(url string, attempts int) *DecisionClient {
	c := NewDecisionClient(ResolvedBackend{Backend: Backend{Name: "test", URL: url, Model: "jev-latest"}}, "test-key")
	c.baseBackoff = time.Millisecond
	c.maxBackoff = 2 * time.Millisecond
	if attempts > 0 {
		c.maxAttempts = attempts
	}
	return c
}

// TestScoreBatch_BatchesAboveProtocolCeiling: 33 items must split into two
// requests of ≤32 questions each, with every item scored exactly once.
func TestScoreBatch_BatchesAboveProtocolCeiling(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := fastClient(d.srv.URL+"/v1/systemone", 0)

	items := make(map[string]Item, 33)
	for i := 1; i <= 33; i++ {
		items[fmt.Sprintf("seg-%d", i)] = Item{Text: fmt.Sprintf("paragraph %d", i), Tokens: 10}
	}
	scores, err := c.ScoreBatch(context.Background(), "Ship the release", items)
	if err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}

	reqs := d.requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2 (32-item ceiling)", len(reqs))
	}
	seen := map[string]bool{}
	for i, r := range reqs {
		if n := len(r.Req.Questions); n > MaxItemsPerRequest {
			t.Errorf("request %d carried %d questions, want ≤%d", i+1, n, MaxItemsPerRequest)
		}
		if r.Req.Model != "jev-latest" {
			t.Errorf("request %d model = %q, want jev-latest", i+1, r.Req.Model)
		}
		if r.Req.State.Task != "Ship the release" {
			t.Errorf("request %d state.task = %q, want Ship the release", i+1, r.Req.State.Task)
		}
		for ref := range r.Req.Questions {
			if seen[ref] {
				t.Errorf("item %s sent in more than one request", ref)
			}
			seen[ref] = true
			q := r.Req.Questions[ref]
			if q.Type != "noul" {
				t.Errorf("item %s question type = %q, want noul", ref, q.Type)
			}
			wantInstructions := "Considering item " + ref + " only: " + AdmitQuestionInstructions
			if q.Instructions != wantInstructions {
				t.Errorf("item %s question instructions = %q, want %q", ref, q.Instructions, wantInstructions)
			}
			if q.Criteria.True != AdmitQuestionTrue || q.Criteria.False != AdmitQuestionFalse {
				t.Errorf("item %s question criteria = %+v, want the admit true/false texts", ref, q.Criteria)
			}
			// state.items is an array of {ref,text}: the asked ref must
			// appear exactly once, carrying its own text.
			var item *stateItem
			for j := range r.Req.State.Items {
				if r.Req.State.Items[j].Ref == ref {
					if item != nil {
						t.Errorf("item %s appears more than once in state.items", ref)
					}
					item = &r.Req.State.Items[j]
				}
			}
			if item == nil {
				t.Errorf("item %s has a question but no state.items entry", ref)
				continue
			}
			var n int
			if _, err := fmt.Sscanf(ref, "seg-%d", &n); err == nil {
				if want := fmt.Sprintf("paragraph %d", n); item.Text != want {
					t.Errorf("item %s state text = %q, want %q", ref, item.Text, want)
				}
			}
		}
	}
	if len(seen) != 33 {
		t.Errorf("total distinct questions = %d, want 33", len(seen))
	}
	if len(scores) != 33 {
		t.Fatalf("got %d scores, want 33", len(scores))
	}
	for i := 1; i <= 33; i++ {
		ref := fmt.Sprintf("seg-%d", i)
		if want := float64(i) * 0.01; scores[ref] != want {
			t.Errorf("scores[%s] = %v, want %v", ref, scores[ref], want)
		}
	}
}

// TestScoreBatch_ReferenceWireShape is the golden test: the exact bytes of a
// two-item decisions request must match the reference protocol (scorer.py
// build_state + _ref_question, types.py Noul.to_payload, openrouter.py ask)
// — state.items is an ARRAY of {ref,text} pairs, each question carries
// instructions ("Considering item <ref> only: …") plus true/false criteria,
// the task travels only in state.task, and the answer comes back as a noul
// object. The question texts are interpolated from the AdmitQuestion*
// constants, so their wording is pinned verbatim against pipeline.py's
// ADMIT_QUESTION below.
func TestScoreBatch_ReferenceWireShape(t *testing.T) {
	rawCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		rawCh <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers": {"seg-1": {"type": "noul", "noul": 0.75}, "seg-2": {"type": "noul", "noul": 0.25}}}`)
	}))
	t.Cleanup(srv.Close)

	c := fastClient(srv.URL, 1)
	if _, err := c.ScoreBatch(context.Background(), "Ship the release", map[string]Item{
		"seg-1": {Text: "alpha text", Tokens: 5},
		"seg-2": {Text: "beta text", Tokens: 5},
	}); err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	raw := <-rawCh

	want := `{"model":"jev-latest","state":{"task":"Ship the release","items":[` +
		`{"ref":"seg-1","text":"alpha text"},{"ref":"seg-2","text":"beta text"}]},` +
		`"questions":{` +
		`"seg-1":{"type":"noul","instructions":"Considering item seg-1 only: ` + AdmitQuestionInstructions + `","criteria":{"true":"` + AdmitQuestionTrue + `","false":"` + AdmitQuestionFalse + `"}},` +
		`"seg-2":{"type":"noul","instructions":"Considering item seg-2 only: ` + AdmitQuestionInstructions + `","criteria":{"true":"` + AdmitQuestionTrue + `","false":"` + AdmitQuestionFalse + `"}}}}`
	if string(raw) != want {
		t.Errorf("request body does not match the reference wire shape:\n got: %s\nwant: %s", raw, want)
	}

	// The admit wording must stay pinned — the golden request above
	// interpolates the constants, so the text itself is pinned here: the
	// sharpened boundary (noise = progress output/confirmations/boilerplate/
	// re-derivable dumps; essential = concrete, non-re-derivable facts;
	// verbose intermediate logs are noise, their final results essential).
	if AdmitQuestionInstructions != "Will this item still be needed later in the task described in `task`? "+
		"Answer false if it is NOISE: progress output, success or progress confirmations, repeated "+
		"boilerplate, or a large repetitive dump (verbose intermediate logs, build or test output, "+
		"file listings) whose key facts — file paths, commands, error messages, final results — are "+
		"retained in the surrounding kept content or can be re-derived by rerunning the step. "+
		"Verbose intermediate logs are noise even when they mention relevant words; the final result "+
		"or summary of such a log is essential. "+
		"Answer true only if it is ESSENTIAL: it contains concrete facts a later step may have to "+
		"refer back to — file paths, commands and their outcomes, error messages, decisions, user "+
		"preferences, todo state, numbers or results, or the key fields of an API response — that "+
		"are not retained elsewhere and cannot be re-derived." {
		t.Errorf("AdmitQuestionInstructions drifted from the pinned admit wording: %q", AdmitQuestionInstructions)
	}
	if AdmitQuestionTrue != "The item carries concrete facts a later step may need — paths, commands, "+
		"errors, decisions, results, or key response fields — that are not retained elsewhere and "+
		"cannot be re-derived." {
		t.Errorf("AdmitQuestionTrue drifted from the pinned wording: %q", AdmitQuestionTrue)
	}
	if AdmitQuestionFalse != "The item is progress noise, a confirmation, repeated boilerplate, or a "+
		"verbose dump whose useful facts are retained nearby or re-derivable — eliding it loses "+
		"nothing a later step cannot recover." {
		t.Errorf("AdmitQuestionFalse drifted from the pinned wording: %q", AdmitQuestionFalse)
	}
}

// TestScoreBatch_PacksByTokenBudget: items whose combined tokens exceed the
// 64k state+questions ceiling split across requests even under 32 items.
func TestScoreBatch_PacksByTokenBudget(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := fastClient(d.srv.URL, 0)

	// Three 30k-token items: two fit together under 64k, the third must
	// start a new request.
	items := map[string]Item{
		"seg-1": {Text: "one", Tokens: 30_000},
		"seg-2": {Text: "two", Tokens: 30_000},
		"seg-3": {Text: "three", Tokens: 30_000},
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	if got := len(d.requests()); got != 2 {
		t.Fatalf("got %d requests, want 2 (token-budget packing)", got)
	}
	if len(scores) != 3 {
		t.Errorf("got %d scores, want 3", len(scores))
	}
}

// TestScoreBatch_OversizedItemSkipped: an item that cannot fit in any
// request is skipped with an error and fail-opens to 1.0; the rest still
// score and the oversized text never reaches the wire.
func TestScoreBatch_OversizedItemSkipped(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := fastClient(d.srv.URL, 0)

	items := map[string]Item{
		"seg-1":    {Text: "small", Tokens: 100},
		"seg-huge": {Text: "huge", Tokens: MaxStateTokens}, // alone busts the budget once overhead is added
		"seg-2":    {Text: "small too", Tokens: 100},
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want an oversized-item error")
	}
	var ise *ItemScoreError
	if !errors.As(err, &ise) || ise.ItemID != "seg-huge" {
		t.Fatalf("error = %v, want *ItemScoreError for seg-huge", err)
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %q should say the item is too large", err.Error())
	}
	if scores["seg-huge"] != keepScore {
		t.Errorf("oversized item score = %v, want %v (fail-open)", scores["seg-huge"], keepScore)
	}
	if scores["seg-1"] != 0.01 || scores["seg-2"] != 0.02 {
		t.Errorf("small items = %v/%v, want the echoHandler scores 0.01/0.02, not fail-open %v", scores["seg-1"], scores["seg-2"], keepScore)
	}
	for i, r := range d.requests() {
		found := false
		for _, it := range r.Req.State.Items {
			if it.Ref == "seg-huge" {
				found = true
			}
		}
		if found {
			t.Errorf("request %d carried the oversized item", i+1)
		}
	}
}

// TestScoreBatch_RetryOn429HonorsRetryAfter: a 429 with Retry-After delays
// the retry by at least the requested amount, and the retry succeeds.
func TestScoreBatch_RetryOn429HonorsRetryAfter(t *testing.T) {
	d := newDecisionsServer(t, func(attempt int, req capturedRequest) (int, string) {
		if attempt == 1 {
			return http.StatusTooManyRequests, `{"error": {"message": "slow down"}}`
		}
		return echoHandler(attempt, req)
	})
	// Retry-After is honored as a floor; the local backoff is shrunk to ~0,
	// so the observed elapsed time proves the header was honored.
	c := fastClient(d.srv.URL, 0)
	c.baseBackoff = time.Millisecond
	c.maxBackoff = time.Millisecond
	d.retryAfter = "1"

	start := time.Now()
	scores, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	if scores["seg-1"] != 0.01 {
		t.Errorf("scores[seg-1] = %v, want the echoHandler score 0.01 after the successful retry", scores["seg-1"])
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("retry happened after %v, want ≥ ~1s (Retry-After floor)", elapsed)
	}
	if got := len(d.requests()); got != 2 {
		t.Errorf("got %d requests, want 2 (429 then success)", got)
	}
}

// TestScoreBatch_NonRetryableStatusFailsFast: 401 must not be retried; all
// items fail-open to 1.0 with one recorded error each.
func TestScoreBatch_NonRetryableStatusFailsFast(t *testing.T) {
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
		t.Fatal("ScoreBatch() error = nil, want a recorded failure")
	}
	if got := len(d.requests()); got != 1 {
		t.Errorf("got %d requests, want 1 (401 is non-retryable)", got)
	}
	for ref := range items {
		if scores[ref] != keepScore {
			t.Errorf("scores[%s] = %v, want %v (fail-open)", ref, scores[ref], keepScore)
		}
	}
	var itemErrs int
	for _, e := range strings.Split(err.Error(), "\n") {
		if strings.Contains(e, "score item \"seg-") {
			itemErrs++
		}
	}
	if itemErrs != 2 {
		t.Errorf("error reports %d item failures, want 2 (one per item): %v", itemErrs, err)
	}
}

// TestScoreBatch_ProviderOutageFailsOpen: a hard outage retries the full
// budget and then keeps everything, with one recorded error per item.
func TestScoreBatch_ProviderOutageFailsOpen(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusServiceUnavailable, `{"error": {"message": "overloaded"}}`
	})
	c := fastClient(d.srv.URL, 4)

	items := map[string]Item{
		"seg-1": {Text: "a", Tokens: 5},
		"seg-2": {Text: "b", Tokens: 5},
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want a recorded failure")
	}
	if got := len(d.requests()); got != 4 {
		t.Errorf("got %d requests, want 4 (full attempt budget)", got)
	}
	for ref := range items {
		if scores[ref] != keepScore {
			t.Errorf("scores[%s] = %v, want %v (fail-open keeps everything on outage)", ref, scores[ref], keepScore)
		}
	}
	for _, ref := range []string{"seg-1", "seg-2"} {
		if !strings.Contains(err.Error(), fmt.Sprintf("score item %q", ref)) {
			t.Errorf("error %v missing the failure for %q", err, ref)
		}
	}
}

// TestScoreBatch_AuthAndTitleHeaders asserts Bearer auth and the
// jev-compaction X-Title marker on every request.
func TestScoreBatch_AuthAndTitleHeaders(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := fastClient(d.srv.URL, 0)

	if _, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}}); err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	for i, r := range d.requests() {
		if r.Auth != "Bearer test-key" {
			t.Errorf("request %d Authorization = %q, want %q", i+1, r.Auth, "Bearer test-key")
		}
		if r.Title != "jev-compaction" {
			t.Errorf("request %d X-Title = %q, want %q", i+1, r.Title, "jev-compaction")
		}
		if r.ContentType != "application/json" {
			t.Errorf("request %d Content-Type = %q, want application/json", i+1, r.ContentType)
		}
	}
}

// TestScoreBatch_NoKeySkipsAuthHeader: an empty key must not produce a bare
// "Bearer " header.
func TestScoreBatch_NoKeySkipsAuthHeader(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := NewDecisionClient(ResolvedBackend{Backend: Backend{URL: d.srv.URL, Model: "jev-latest"}}, "")
	c.baseBackoff, c.maxBackoff = time.Millisecond, time.Millisecond

	if _, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}}); err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	if r := d.requests()[0]; r.Auth != "" {
		t.Errorf("Authorization = %q, want no auth header without a key", r.Auth)
	}
}

// TestScoreBatch_PerBackendURL asserts each provider's endpoint is hit —
// typesafe, openrouter, and gateway URL shapes over one test server.
func TestScoreBatch_PerBackendURL(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	paths := []string{
		"/v1/systemone",        // typesafe-shaped
		"/api/alpha/decisions", // openrouter-shaped
		"/gateway/decisions",   // gateway-shaped
	}
	for _, p := range paths {
		c := fastClient(d.srv.URL+p, 0)
		if _, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 5}}); err != nil {
			t.Fatalf("ScoreBatch(%s) error = %v", p, err)
		}
	}
	got := d.requests()
	if len(got) != len(paths) {
		t.Fatalf("got %d requests, want %d", len(got), len(paths))
	}
	for i, p := range paths {
		if got[i].Path != p {
			t.Errorf("request %d hit %q, want %q", i+1, got[i].Path, p)
		}
	}
}

// TestScoreBatch_AnswerParsingLenient: the protocol's noul answer object is
// the primary answer form; bare numbers and string numbers (local gateways,
// test stubs) stay tolerated; everything parses clamped into [0,1]; junk
// answers (non-numeric strings, wrong-type objects, noul objects without a
// number) fail that item only.
func TestScoreBatch_AnswerParsingLenient(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusOK, `{"answers": {
			"seg-num": 0.25,
			"seg-str": "0.5",
			"seg-high": 1.7,
			"seg-low": -0.2,
			"seg-obj": {"type": "noul", "noul": 0.75},
			"seg-obj-high": {"type": "noul", "noul": 2.0},
			"seg-junk": "banana",
			"seg-wrongtype": {"type": "choice", "choice": "a"},
			"seg-nonoul": {"type": "noul"}
		}}`
	})
	c := fastClient(d.srv.URL, 0)

	items := map[string]Item{
		"seg-num":       {Text: "a", Tokens: 1},
		"seg-str":       {Text: "b", Tokens: 1},
		"seg-high":      {Text: "c", Tokens: 1},
		"seg-low":       {Text: "d", Tokens: 1},
		"seg-obj":       {Text: "e", Tokens: 1},
		"seg-obj-high":  {Text: "f", Tokens: 1},
		"seg-junk":      {Text: "g", Tokens: 1},
		"seg-wrongtype": {Text: "h", Tokens: 1},
		"seg-nonoul":    {Text: "i", Tokens: 1},
	}
	scores, err := c.ScoreBatch(context.Background(), "task", items)
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want the unusable answers recorded as errors")
	}
	want := map[string]float64{
		"seg-num":       0.25,
		"seg-str":       0.5,
		"seg-high":      1, // clamped
		"seg-low":       0, // clamped
		"seg-obj":       0.75,
		"seg-obj-high":  1, // clamped
		"seg-junk":      1, // fail-open
		"seg-wrongtype": 1, // fail-open
		"seg-nonoul":    1, // fail-open (noul object without a number must not decode as a silent 0)
	}
	for ref, w := range want {
		if scores[ref] != w {
			t.Errorf("scores[%s] = %v, want %v", ref, scores[ref], w)
		}
	}
	for _, id := range []string{"seg-junk", "seg-wrongtype", "seg-nonoul"} {
		found := false
		for _, e := range strings.Split(err.Error(), "\n") {
			if strings.Contains(e, fmt.Sprintf("score item %q", id)) {
				found = true
			}
		}
		if !found {
			t.Errorf("error %v missing the failure for %q", err, id)
		}
	}
}

// TestScoreBatch_MissingAnswerFailsOpenItemOnly: a response that omits one
// ref keeps that ref at 1.0 without touching the others.
func TestScoreBatch_MissingAnswerFailsOpenItemOnly(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusOK, `{"answers": {"seg-1": {"type": "noul", "noul": 0.3}}}`
	})
	c := fastClient(d.srv.URL, 0)

	scores, err := c.ScoreBatch(context.Background(), "task", map[string]Item{
		"seg-1": {Text: "a", Tokens: 1},
		"seg-2": {Text: "b", Tokens: 1},
	})
	if err == nil {
		t.Fatal("ScoreBatch() error = nil, want the missing answer recorded")
	}
	if scores["seg-1"] != 0.3 {
		t.Errorf("scores[seg-1] = %v, want 0.3", scores["seg-1"])
	}
	if scores["seg-2"] != keepScore {
		t.Errorf("scores[seg-2] = %v, want %v (fail-open)", scores["seg-2"], keepScore)
	}
}

// TestScoreBatch_EmptyItems is a no-op.
func TestScoreBatch_EmptyItems(t *testing.T) {
	c := fastClient("http://127.0.0.1:1/x", 0)
	scores, err := c.ScoreBatch(context.Background(), "task", nil)
	if err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	if len(scores) != 0 {
		t.Errorf("scores = %v, want empty", scores)
	}
}

// TestScoreBatch_EmptyTaskGetsDefault: an empty task still produces a valid
// question.
func TestScoreBatch_EmptyTaskGetsDefault(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	c := fastClient(d.srv.URL, 0)
	if _, err := c.ScoreBatch(context.Background(), "  ", map[string]Item{"seg-1": {Text: "x", Tokens: 1}}); err != nil {
		t.Fatalf("ScoreBatch() error = %v", err)
	}
	if got := d.requests()[0].Req.State.Task; got != defaultTask {
		t.Errorf("state.task = %q, want %q", got, defaultTask)
	}
}

// TestIsRetryableDecisionError covers the status/transport matrix: the
// protocol's retryable set {408,429,500,502,503,504,529} plus transport
// failures, versus the non-retryable set {400,401,402,403,404,405,422} plus
// TLS errors and cancellation.
func TestIsRetryableDecisionError(t *testing.T) {
	statusErr := func(code int) *DecisionStatusError {
		return &DecisionStatusError{StatusCode: code, Status: fmt.Sprintf("%d x", code)}
	}
	retryable := []error{
		statusErr(408), statusErr(429), statusErr(500), statusErr(502),
		statusErr(503), statusErr(504), statusErr(529), statusErr(501),
		&url.Error{Op: "Post", Err: errors.New("connection refused")},
		errors.New("decode decisions response: unexpected EOF"),
	}
	nonRetryable := []error{
		statusErr(400), statusErr(401), statusErr(402), statusErr(403),
		statusErr(404), statusErr(405), statusErr(422), statusErr(409),
		statusErr(301),
		&url.Error{Op: "Post", Err: x509.UnknownAuthorityError{}},
		&url.Error{Op: "Post", Err: fmt.Errorf("tls: handshake failure")},
		&url.Error{Op: "Post", Err: errors.New(`unsupported protocol scheme "ftp"`)},
		context.Canceled,
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
	}
	for _, err := range retryable {
		if !isRetryableDecisionError(err) {
			t.Errorf("isRetryableDecisionError(%v) = false, want true", err)
		}
	}
	for _, err := range nonRetryable {
		if isRetryableDecisionError(err) {
			t.Errorf("isRetryableDecisionError(%v) = true, want false", err)
		}
	}
	if isRetryableDecisionError(nil) {
		t.Error("isRetryableDecisionError(nil) = true, want false")
	}
}

// TestDecisionClient_ScoringLimiterShared proves the compaction client goes
// through the scoring-slot wrapper: under a cap of 1, two concurrent
// ScoreBatch calls must serialize inside the server.
func TestDecisionClient_ScoringLimiterShared(t *testing.T) {
	// First, the wrapper itself: unlimited → nil, capped → non-nil.
	setScoringConcurrency(0)
	if rel := acquireScoringSlot(context.Background()); rel != nil {
		t.Error("acquireScoringSlot with unlimited limiter returned non-nil release")
	}
	setScoringConcurrency(1)
	defer setScoringConcurrency(0)
	rel := acquireScoringSlot(context.Background())
	if rel == nil {
		t.Fatal("acquireScoringSlot with cap 1 returned nil release")
	}
	rel()

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	d := newDecisionsServer(t, func(_ int, req capturedRequest) (int, string) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		time.Sleep(50 * time.Millisecond)
		return echoHandler(1, req)
	})
	c := fastClient(d.srv.URL, 0)
	c.limiter = nil // isolate the scoring slot's effect from the token bucket

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ScoreBatch(context.Background(), "task", map[string]Item{"seg-1": {Text: "x", Tokens: 1}}); err != nil {
				t.Errorf("ScoreBatch() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Errorf("peak concurrent handler runs = %d, want 1 — scoring calls must share the scoring bound", peak)
	}
}

// TestParseRetryAfter covers both header forms and the invalid ones.
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"-3", 0},
		{"junk", 0},
		{"2", 2 * time.Second},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
