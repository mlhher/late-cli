package compaction

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"late/internal/common"
)

// Item is one unit of tool output to score.
type Item struct {
	// Text is the segment text sent to the decision model.
	Text string
	// Tokens is the precomputed token estimate; 0 means estimate from Text.
	// Callers that already counted (the pipeline passes each Segment's
	// Tokens) avoid re-running the BPE.
	Tokens int
}

// keepScore is the fail-open score: when an item cannot be scored it is
// reported as fully essential so the downstream decision keeps it.
const keepScore = 1.0

// defaultTask is used when a caller hands ScoreBatch an empty task.
const defaultTask = "Continue the user's ongoing task."

// Production knobs for the decisions protocol.
const (
	// MaxItemsPerRequest is the protocol ceiling of 32 questions per request.
	MaxItemsPerRequest = 32
	// MaxStateTokens is the protocol ceiling of 64k tokens for
	// state + questions in one request; batches are packed under it and
	// single items exceeding it alone are skipped with an error (fail-open).
	MaxStateTokens = 64_000
	// decisionRatePerSec/Burst pace the process-local request rate. The
	// endpoint tolerates ~1200 RPM fleet-wide; 16 req/s with burst 16 lets a
	// 33-item output fire its two batches back-to-back without stampeding.
	decisionRatePerSec = 16
	decisionRateBurst  = 16
	// perAttemptTimeout bounds one HTTP attempt (request + response body).
	perAttemptTimeout = 30 * time.Second
	// Retry policy: full-jitter backoff, base 500ms capped at 30s, 4
	// attempts (initial + 3 retries) — the same curve as the agent's
	// infrastructure retry tier in internal/executor.
	defaultMaxAttempts = 4
	defaultBaseBackoff = 500 * time.Millisecond
	defaultMaxBackoff  = 30 * time.Second
	// retryAfterCeiling caps a server-requested Retry-After wait (mirrors
	// internal/executor): a hostile or buggy Retry-After must not hang the
	// agent for hours.
	retryAfterCeiling = 5 * time.Minute
	// requestOverheadTokens is a conservative estimate of the fixed JSON
	// skeleton around the state and questions (model, task, the questions
	// object, braces and field names).
	requestOverheadTokens = 48
	// perItemWireTokens is a conservative flat allowance for the JSON keys
	// and braces around one state item and its question ("ref"/"text",
	// "type"/"instructions"/"criteria") that the text and ref estimates do
	// not count.
	perItemWireTokens = 16
	// maxDecisionStatusBodyBytes bounds how much of an error response body
	// is read before parsing.
	maxDecisionStatusBodyBytes = 8192
	// maxDecisionResponseBytes bounds a 200 response body: answers echo only
	// refs and small noul objects, so anything near this bound is hostile.
	maxDecisionResponseBytes = 8 << 20
)

// noulQuestionFor builds the one question the client asks per item, named
// for that item: the reference's ADMIT_QUESTION (pipeline.py) structure —
// instructions plus true/false criteria — carried by the AdmitQuestion*
// constants in retrieve.go (the reference's conservative shape with wording
// sharpened against real-run replay; see the constants), with scorer.py's
// _ref_question "Considering item <ref> only:" prefix — the batch shares one
// state, so the question text is the only thing that tells the model which
// of the batch's items a given answer is about.
func noulQuestionFor(ref string) decisionQuestion {
	return decisionQuestion{
		Type:         "noul",
		Instructions: "Considering item " + ref + " only: " + AdmitQuestionInstructions,
		Criteria: decisionCriteria{
			True:  AdmitQuestionTrue,
			False: AdmitQuestionFalse,
		},
	}
}

// budgetQuestionTokens estimates one per-ref question payload's token cost
// from a representative short ref — the same "i0" stand-in the reference's
// planner uses (scorer.py: estimate_tokens(
// _ref_question(question, "i0").to_payload())). ScoreBatch adds each item's
// own ref on top, so a long id still counts against the budget.
func budgetQuestionTokens() int {
	b, err := json.Marshal(noulQuestionFor("i0"))
	if err != nil {
		// json.Marshal cannot fail on this plain-string struct; the fallback
		// merely keeps the budget estimate conservative.
		return 128
	}
	return common.EstimateTokenCount(string(b))
}

// Wire types for the System One decisions protocol — the reference's
// openrouter.py ask() body, scorer.py build_state, and types.py
// Noul.to_payload:
//
//	POST {"model": …, "state": {"task": …, "items": [{"ref": …, "text": …}]},
//	      "questions": {ref: {"type": "noul", "instructions": …,
//	                          "criteria": {"true": …, "false": …}}}}
//	→    {"answers": {ref: {"type": "noul", "noul": <float>}}, "usage": {…}}
//
// state.items is an ARRAY of {ref, text} pairs (the reference builds it
// positionally), not a map keyed by ref, and the task travels only in
// state.task — the question instructions never embed it.
type decisionRequest struct {
	Model     string                      `json:"model"`
	State     decisionState               `json:"state"`
	Questions map[string]decisionQuestion `json:"questions"`
}

// decisionState is one batch's shared state: the task digest plus the
// batch's items, in request order.
type decisionState struct {
	Task  string      `json:"task"`
	Items []stateItem `json:"items"`
}

// stateItem is one entry of the state's items array: the item's ref (the
// question/answer key) and the text being scored.
type stateItem struct {
	Ref  string `json:"ref"`
	Text string `json:"text"`
}

// decisionQuestion is the wire form of a Noul question: instructions plus
// the true/false criteria the answer's probability weighs.
type decisionQuestion struct {
	Type         string           `json:"type"`
	Instructions string           `json:"instructions"`
	Criteria     decisionCriteria `json:"criteria"`
}

// decisionCriteria carries the two criterion descriptions of a Noul question.
type decisionCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

type decisionResponse struct {
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   decisionUsage              `json:"usage"`
}

type decisionUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// DecisionStatusError is a non-200 response from the decisions endpoint.
// Like internal/client.StatusError it carries a bounded diagnostic body and
// the parsed Retry-After so the retry loop can honor server pacing.
type DecisionStatusError struct {
	StatusCode int
	Status     string
	Body       string
	RetryAfter time.Duration
}

func (e *DecisionStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("decisions API error (%d): %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("decisions API status: %d", e.StatusCode)
}

// ItemScoreError records why one item fell back to the keep score. ItemID is
// the caller's key into the returned score map.
type ItemScoreError struct {
	ItemID string
	Err    error
}

func (e *ItemScoreError) Error() string {
	return fmt.Sprintf("score item %q: %v", e.ItemID, e.Err)
}

func (e *ItemScoreError) Unwrap() error { return e.Err }

// DecisionClient scores items against one System One decisions backend.
//
// It is safe for concurrent use: the configuration is fixed at construction
// and the only mutable state is the rate limiter's, which is mutex-guarded.
type DecisionClient struct {
	backend ResolvedBackend
	apiKey  string
	limiter *tokenBucket
	http    *http.Client

	// unavailable is the poison flag: set the moment the backend rejects
	// auth (401/403), which no retry or re-sent key can fix. Once set,
	// ScoreBatch fails open without touching the network and Unavailable()
	// lets callers stop asking for the rest of the session (no per-message
	// request storms against a backend that can only say 401 again).
	unavailable atomic.Bool

	// Retry knobs. Unexported fields rather than constants solely so
	// same-package tests can shrink the curve; production code must never
	// reassign them (mirrors the executor's throttle-knob convention).
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
}

// Unavailable reports whether the client has been poisoned by an auth
// rejection (401/403): the backend refused the credentials, so scoring is
// disabled for the rest of the session. Callers check this to stop asking
// for scores; ScoreBatch itself keeps failing open (keep scores, typed
// KindAuth errors) without touching the network.
func (c *DecisionClient) Unavailable() bool {
	return c.unavailable.Load()
}

// poison marks the client unavailable after an auth rejection. Idempotent.
func (c *DecisionClient) poison() {
	c.unavailable.Store(true)
}

// NewDecisionClient builds a client for a resolved backend. apiKey overrides
// backend.APIKey when non-empty (the pipeline passes its own key through).
// A nil-or-empty model or URL fails lazily at call time as item errors —
// ResolveBackend produces a ready backend in normal use.
func NewDecisionClient(backend ResolvedBackend, apiKey string) *DecisionClient {
	if apiKey == "" {
		apiKey = backend.APIKey
	}
	return &DecisionClient{
		backend:     backend,
		apiKey:      apiKey,
		limiter:     newTokenBucket(decisionRatePerSec, decisionRateBurst),
		http:        &http.Client{Timeout: perAttemptTimeout},
		maxAttempts: defaultMaxAttempts,
		baseBackoff: defaultBaseBackoff,
		maxBackoff:  defaultMaxBackoff,
	}
}

// packed is one item staged into a request batch with its token estimate.
type packed struct {
	id  string
	it  Item
	tok int
}

// ScoreBatch scores every item for the given task with one "noul" question
// per item ("Considering item <ref> only: <admit instructions>" — the
// reference's ADMIT_QUESTION structure via the AdmitQuestion* constants),
// with
// the task traveling only in state.task, batched under the protocol ceilings:
// at most
// MaxItemsPerRequest items and MaxStateTokens estimated tokens per request
// (batches are formed in lexicographic item-ID order, so the split is
// deterministic).
//
// It never fails hard: any item that cannot be scored comes back as keepScore
// (1.0) plus a recorded error. The returned error is non-nil exactly when at
// least one item failed, as an errors.Join of *ItemScoreError values whose
// Err carries the classified *Error where one exists: budget violations
// (KindBudget, raised before send), and the request-level class (auth,
// validation, unavailable) when the whole batch failed. An auth rejection
// (401/403) additionally poisons the client: Unavailable() turns true and
// every later ScoreBatch fails open with typed auth errors and no requests.
func (c *DecisionClient) ScoreBatch(ctx context.Context, task string, items map[string]Item) (map[string]float64, error) {
	scores := make(map[string]float64, len(items))
	if len(items) == 0 {
		return scores, nil
	}
	if strings.TrimSpace(task) == "" {
		task = defaultTask
	}

	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if c.Unavailable() {
		// Poisoned by an earlier auth rejection: scoring is off for the
		// session. Fail open without touching the network — every id gets
		// the keep score and one typed auth error.
		errs := make([]error, 0, len(ids))
		for _, id := range ids {
			scores[id] = keepScore
			errs = append(errs, &ItemScoreError{
				ItemID: id,
				Err:    authError(0, opScoreBatch, errScoringDisabled),
			})
		}
		return scores, errors.Join(errs...)
	}

	// The per-question token cost is estimated once from a representative
	// payload (the reference's planner stands in the ref "i0"); each item
	// then adds its own ref and a flat wire allowance, so long ids and the
	// per-item JSON keys still count against the budget.
	questionTokens := budgetQuestionTokens()
	itemCost := func(itemTokens int, ref string) int {
		return itemTokens + questionTokens + common.EstimateTokenCount(ref) + perItemWireTokens
	}
	overhead := requestOverheadTokens + common.EstimateTokenCount(task) + questionTokens

	var (
		errs        []error
		batch       []packed
		batchTokens = overhead
	)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		answered, answerErrs, reqErr := c.scoreBatchRequest(ctx, task, batch)
		for _, p := range batch {
			switch {
			case reqErr != nil:
				// The whole request failed: every item in it fail-opens.
				scores[p.id] = keepScore
				errs = append(errs, &ItemScoreError{ItemID: p.id, Err: reqErr})
			case answerErrs[p.id] != nil:
				scores[p.id] = keepScore
				errs = append(errs, &ItemScoreError{ItemID: p.id, Err: answerErrs[p.id]})
			default:
				scores[p.id] = answered[p.id]
			}
		}
		batch = nil
		batchTokens = overhead
	}

	for _, id := range ids {
		it := items[id]
		tok := it.Tokens
		if tok <= 0 {
			tok = common.EstimateTokenCount(it.Text)
		}
		if cost := overhead + itemCost(tok, id); cost > MaxStateTokens {
			// A single item that cannot fit even alone in a request is a
			// budget violation (the reference's JevBudgetError: raised
			// before sending). It is skipped per item — the fail-open keep
			// score stands — while the rest of the batch still scores.
			scores[id] = keepScore
			errs = append(errs, &ItemScoreError{
				ItemID: id,
				Err: budgetError(opScoreBatch, fmt.Errorf("item too large to score: %d estimated tokens exceeds the %d-token state+questions budget",
					tok, MaxStateTokens)),
			})
			continue
		}
		if len(batch) >= MaxItemsPerRequest || batchTokens+itemCost(tok, id) > MaxStateTokens {
			flush()
		}
		batch = append(batch, packed{id: id, it: it, tok: tok})
		batchTokens += itemCost(tok, id)
	}
	flush()

	return scores, errors.Join(errs...)
}

// scoreBatchRequest sends one batch as a single decisions request and returns
// the parsed scores plus any per-item answer errors. A non-nil request error
// means the whole batch failed (after retries — except auth and validation,
// which come back on the first attempt, never retried); per-item errors mark
// individual unusable answers. The batch's shared state carries the task plus
// the items as a {ref, text} array; each ref gets its own question naming it
// (noulQuestionFor, the reference's _ref_question).
func (c *DecisionClient) scoreBatchRequest(ctx context.Context, task string, batch []packed) (map[string]float64, map[string]error, error) {
	if c.Unavailable() {
		// Poisoned mid-call (an earlier batch in this same ScoreBatch hit
		// an auth rejection): fail the remaining batches without touching
		// the network — the caller fail-opens each item.
		return nil, nil, authError(0, opScoreBatch, errScoringDisabled)
	}
	items := make([]stateItem, len(batch))
	questions := make(map[string]decisionQuestion, len(batch))
	for i, p := range batch {
		items[i] = stateItem{Ref: p.id, Text: p.it.Text}
		questions[p.id] = noulQuestionFor(p.id)
	}
	body, err := json.Marshal(decisionRequest{
		Model:     c.backend.Backend.Model,
		State:     decisionState{Task: task, Items: items},
		Questions: questions,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode decisions request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("decisions request canceled: %w", err)
		}
		answered, answerErrs, err := c.attempt(ctx, body)
		if err == nil {
			for _, p := range batch {
				if _, ok := answered[p.id]; !ok && answerErrs[p.id] == nil {
					answerErrs[p.id] = fmt.Errorf("decision response missing answer")
				}
			}
			return answered, answerErrs, nil
		}
		lastErr = err
		if !isRetryableDecisionError(err) {
			break
		}
		if attempt == c.maxAttempts {
			break
		}
		if err := sleepCtx(ctx, c.retryDelay(attempt, retryAfterFrom(err))); err != nil {
			return nil, nil, fmt.Errorf("decisions request canceled during backoff: %w", err)
		}
	}
	return nil, nil, lastErr
}

// attempt performs one HTTP attempt: pacing (process-local token bucket,
// then the fleet-wide LLM slot), the request with Bearer auth and the
// X-Title marker, and the response decode.
func (c *DecisionClient) attempt(ctx context.Context, body []byte) (map[string]float64, map[string]error, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, nil, err
	}
	// Fleet-wide pacing: compaction/scoring calls share the scoring
	// concurrency slot (see acquireScoringSlot in llm_slot.go). No early
	// return sits between the
	// acquire and Do, so the deferred release covers every exit below.
	release := acquireScoringSlot(ctx)
	if release == nil && ctx.Err() != nil {
		return nil, nil, fmt.Errorf("waiting for an LLM concurrency slot: %w", ctx.Err())
	}
	if release != nil {
		defer release()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.backend.Backend.URL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	// jev-compaction marks its decisions traffic with this title.
	req.Header.Set("X-Title", "jev-compaction")

	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failure — classified as Unavailable (the reference's
		// JevUnavailableError: retried, then fail-open). A permanent
		// TLS/scheme mismatch wraps the same class, but the retry decision
		// still refuses it via the transport checks.
		return nil, nil, classifyStatus(opScoreBatch, 0, "", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := newDecisionStatusError(resp)
		classified := classifyStatus(opScoreBatch, statusErr.StatusCode, statusErr.Body, statusErr)
		if classified.Kind == KindAuth {
			// The backend refused the credentials: nothing sent after this
			// can succeed. Poison the client so the rest of the session
			// stops calling (fail-open, no request storms).
			c.poison()
		}
		return nil, nil, classified
	}

	var decoded decisionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDecisionResponseBytes)).Decode(&decoded); err != nil {
		// A 200 that does not speak the protocol is the backend being
		// broken, not the request being wrong: Unavailable, retried like
		// any other transient failure.
		return nil, nil, unavailableError(0, opScoreBatch, fmt.Errorf("decode decisions response: %w", err))
	}
	answered, answerErrs := parseAnswers(decoded.Answers)
	return answered, answerErrs, nil
}

// newDecisionStatusError converts a non-200 response into a
// *DecisionStatusError, reading the error body once and bounded.
func newDecisionStatusError(resp *http.Response) *DecisionStatusError {
	e := &DecisionStatusError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDecisionStatusBodyBytes))
	e.Body = strings.TrimSpace(string(body))
	if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
		e.RetryAfter = ra
	}
	return e
}

// retryableStatuses are the protocol's transient failures.
var retryableStatuses = map[int]bool{
	http.StatusRequestTimeout:      true, // 408
	http.StatusTooManyRequests:     true, // 429
	http.StatusInternalServerError: true, // 500
	http.StatusBadGateway:          true, // 502
	http.StatusServiceUnavailable:  true, // 503
	http.StatusGatewayTimeout:      true, // 504
	529:                            true, // origin-is-overloaded (Cloudflare-style)
}

// nonRetryableStatuses are deterministic client/protocol rejections: burning
// retries cannot change the answer, so they fail fast into the fail-open
// keep score.
var nonRetryableStatuses = map[int]bool{
	http.StatusBadRequest:          true, // 400
	http.StatusUnauthorized:        true, // 401
	http.StatusPaymentRequired:     true, // 402
	http.StatusForbidden:           true, // 403
	http.StatusNotFound:            true, // 404
	http.StatusMethodNotAllowed:    true, // 405
	http.StatusUnprocessableEntity: true, // 422
}

// isRetryableDecisionError classifies a failed attempt: protocol-transient
// statuses, any other 5xx (server-side trouble), and ordinary transport
// failures are retryable; deterministic client errors, TLS/certificate
// failures, and cancellation are not. Typed taxonomy errors decide by class:
// only KindUnavailable can ever be retried — auth (401/403) and validation
// (400/404/422) come back to the caller on the first attempt (the reference
// never retries JevValidationError) — and even an Unavailable classification
// still falls through to the transport checks, so a permanent TLS/scheme
// failure wrapped as Unavailable is not retried either.
func isRetryableDecisionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ce *Error
	if errors.As(err, &ce) {
		switch ce.Kind {
		case KindAuth, KindValidation, KindBudget:
			// Deterministic: retrying cannot change the answer. Budget is
			// raised before send and resolved by splitting, not retrying.
			return false
		}
		// KindUnavailable falls through to the transport checks below.
	}
	var se *DecisionStatusError
	if errors.As(err, &se) {
		if nonRetryableStatuses[se.StatusCode] {
			return false
		}
		if retryableStatuses[se.StatusCode] {
			return true
		}
		// Unlisted statuses: any other server-side trouble is treated as
		// transient; anything else is not worth a retry.
		return se.StatusCode >= 500 && se.StatusCode <= 599
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return !isPermanentTransportError(ue)
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	// Unknown failures (e.g. a malformed 200 body): the fail-open bias
	// prefers one more attempt over giving up early.
	return true
}

// isPermanentTransportError reports whether a url.Error wraps a cause that
// retrying cannot fix: TLS certificate/trust failures, non-TLS bytes on a
// TLS connection, or an unsupported URL scheme.
func isPermanentTransportError(ue *url.Error) bool {
	var authErr x509.UnknownAuthorityError
	if errors.As(ue.Err, &authErr) {
		return true
	}
	var hostErr x509.HostnameError
	if errors.As(ue.Err, &hostErr) {
		return true
	}
	var certErr x509.CertificateInvalidError
	if errors.As(ue.Err, &certErr) {
		return true
	}
	var recordErr tls.RecordHeaderError
	if errors.As(ue.Err, &recordErr) {
		return true
	}
	msg := ue.Err.Error()
	return strings.HasPrefix(msg, "tls:") ||
		strings.HasPrefix(msg, "unsupported protocol scheme") ||
		strings.HasPrefix(msg, "http: server gave HTTP response to HTTPS client")
}

// retryAfterFrom extracts the server-requested Retry-After from a
// *DecisionStatusError anywhere in the error chain; 0 when absent.
func retryAfterFrom(err error) time.Duration {
	var se *DecisionStatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

// maxRetryAttempt clamps the attempt exponent so the 1<<(attempt-1) shift
// can never overflow before the cap is applied.
const maxRetryAttempt = 40

// retryDelay returns the full-jitter wait before retry attempt (1-based):
// uniform over [0, min(maxBackoff, baseBackoff*2^(attempt-1))], never shorter
// than the server-requested Retry-After (capped at retryAfterCeiling) —
// the same shape as internal/executor's effectiveRetryDelay.
func (c *DecisionClient) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maxRetryAttempt {
		attempt = maxRetryAttempt
	}
	backoff := c.baseBackoff * (1 << (attempt - 1))
	if backoff <= 0 || backoff > c.maxBackoff { // <=0 guards shift overflow
		backoff = c.maxBackoff
	}
	delay := rand.N(backoff)
	if retryAfter > retryAfterCeiling {
		retryAfter = retryAfterCeiling
	}
	if retryAfter > delay {
		return retryAfter
	}
	return delay
}

// parseAnswers converts the raw answer map into scores, tolerating the
// protocol's noul answer objects and, for local gateways, bare numeric and
// string-number payloads, clamped into [0,1]. Unusable answers land in the
// returned per-item error map instead of failing the batch.
func parseAnswers(raw map[string]json.RawMessage) (map[string]float64, map[string]error) {
	answered := make(map[string]float64, len(raw))
	errs := make(map[string]error)
	for id, v := range raw {
		s, err := parseScore(v)
		if err != nil {
			errs[id] = err
			continue
		}
		answered[id] = s
	}
	return answered, errs
}

// parseScore parses one answer payload: the protocol's noul answer object
// ({"type":"noul","noul":<number>} — types.py parse_answer), a bare JSON
// number, or a string holding one (the bare forms stay tolerated for local
// gateways and test stubs), clamped into [0,1]. A noul-typed object without
// a usable "noul" number falls through and is rejected — it must not decode
// as a silent 0 (an elision).
func parseScore(v json.RawMessage) (float64, error) {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return 0, fmt.Errorf("empty answer")
	}
	var noul struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	}
	if err := json.Unmarshal(trimmed, &noul); err == nil && noul.Type == "noul" && noul.Noul != nil {
		return clampScore(*noul.Noul), nil
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err == nil {
		return clampScore(f), nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, fmt.Errorf("non-numeric answer %q", s)
		}
		return clampScore(f), nil
	}
	payload := string(trimmed)
	if len(payload) > 64 {
		payload = payload[:64] + "…"
	}
	return 0, fmt.Errorf("unexpected answer payload %s", payload)
}

// clampScore pins a score into [0,1]; out-of-range model output is clamped
// rather than rejected so one wild answer does not fail an item.
func clampScore(f float64) float64 {
	return math.Max(0, math.Min(1, f))
}

// sleepCtx waits d, returning early with the context's error if ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// parseRetryAfter parses a Retry-After header value in either delta-seconds
// ("2") or HTTP-date form. Empty, invalid, and non-positive values yield 0.
// (Port-local copy of internal/client's helper, which is unexported and
// deliberately not part of the provider layer compaction reuses.)
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if date, err := http.ParseTime(v); err == nil {
		if d := time.Until(date); d > 0 {
			return d
		}
	}
	return 0
}

// tokenBucket is a minimal process-local rate limiter: the decisions endpoint
// is metered per request, and a 33-item tool output would otherwise fire its
// batches back-to-back. It is mutex-guarded and safe for concurrent use.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64 // tokens per second
	last   time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{tokens: burst, burst: burst, rate: rate, last: time.Now()}
}

// wait blocks until one token is available or ctx ends. A nil bucket is
// unlimited (used by tests that bypass construction defaults).
func (b *tokenBucket) wait(ctx context.Context) error {
	if b == nil {
		return nil
	}
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		b.last = now
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		deficit := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()
		if deficit < time.Millisecond {
			deficit = time.Millisecond
		}
		t := time.NewTimer(deficit)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
