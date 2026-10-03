package compaction

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrorKind classifies a compaction scoring failure into the reference's
// error taxonomy (jev-compaction types.py): JevAuthError,
// JevValidationError, JevBudgetError, JevUnavailableError. The class decides
// what the retry loop — and every caller — does with the failure:
//
//   - KindAuth: the backend rejected the credentials (401/403). Never
//     retried; the DecisionClient poisons itself (Unavailable) so callers
//     can stop asking for the rest of the session.
//   - KindValidation: the request was malformed or the backend cannot serve
//     it at all (400/404/422 — e.g. a too-small local model). NEVER retried:
//     it is a bug or a permanent mismatch, not a transient failure (the
//     reference never retries JevValidationError).
//   - KindBudget: the request would violate a hard limit (the 64k
//     state+questions token budget, the 32-item ceiling). Raised BEFORE
//     sending; the caller splits and retries — ScoreBatch's packing does
//     exactly that, and the single item that cannot fit even alone is
//     skipped per item.
//   - KindUnavailable: the backend is transiently broken (429/5xx/timeout/
//     connection failure). Retried with backoff; when the attempt budget is
//     exhausted callers fail open (keep everything).
type ErrorKind int

const (
	KindAuth ErrorKind = iota
	KindValidation
	KindBudget
	KindUnavailable
)

// String returns the taxonomy's lowercase class name.
func (k ErrorKind) String() string {
	switch k {
	case KindAuth:
		return "auth"
	case KindValidation:
		return "validation"
	case KindBudget:
		return "budget"
	case KindUnavailable:
		return "unavailable"
	default:
		return fmt.Sprintf("errorkind(%d)", int(k))
	}
}

// detail is the fixed per-class explanation in Error's message.
func (k ErrorKind) detail() string {
	switch k {
	case KindAuth:
		return "bad or missing API key"
	case KindValidation:
		return "malformed request (never retried — this is a bug, not a transient failure)"
	case KindBudget:
		return "request would violate a hard limit (split and retry)"
	case KindUnavailable:
		return "backend unavailable"
	default:
		return "unclassified failure"
	}
}

// Error is one classified compaction failure. Kind drives the retry loop and
// every caller's policy (fail open vs. give up for the session); Err is the
// underlying cause — often a *DecisionStatusError carrying the bounded
// response body and the server's Retry-After.
type Error struct {
	// Kind is the taxonomy class of the failure.
	Kind ErrorKind
	// Status is the HTTP status that produced the failure, 0 when the
	// failure was not status-shaped (transport failure, or a budget check
	// raised before send).
	Status int
	// Op names the failing operation ("score-batch"), for diagnostics.
	Op string
	// Err is the underlying cause; nil when the class alone says it all.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("compaction: ")
	b.WriteString(e.Kind.String())
	if e.Status > 0 {
		fmt.Fprintf(&b, " (%d)", e.Status)
	}
	if e.Op != "" {
		b.WriteString(" during ")
		b.WriteString(e.Op)
	}
	b.WriteString(": ")
	b.WriteString(e.Kind.detail())
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the underlying cause so errors.Is/errors.As can walk the
// chain — retryAfterFrom pulling the server's Retry-After out of the wrapped
// *DecisionStatusError, callers matching cancellation, tests matching the
// class through an errors.Join of *ItemScoreError values.
func (e *Error) Unwrap() error { return e.Err }

// opScoreBatch names the decisions-protocol operation in typed errors.
const opScoreBatch = "score-batch"

// errScoringDisabled is the cause carried by the typed auth errors a
// poisoned client synthesizes without touching the network.
var errScoringDisabled = errors.New("scoring disabled for the session: the backend rejected the API key")

// The per-class constructors so failure sites read as the class they raise.

func authError(status int, op string, err error) *Error {
	return &Error{Kind: KindAuth, Status: status, Op: op, Err: err}
}

func validationError(status int, op string, err error) *Error {
	return &Error{Kind: KindValidation, Status: status, Op: op, Err: err}
}

// budgetError takes no status: budget violations are raised before sending,
// so no HTTP response exists to classify.
func budgetError(op string, err error) *Error {
	return &Error{Kind: KindBudget, Op: op, Err: err}
}

func unavailableError(status int, op string, err error) *Error {
	return &Error{Kind: KindUnavailable, Status: status, Op: op, Err: err}
}

// classifyStatus maps one failed request onto the taxonomy:
//
//   - 401/403 → KindAuth (bad or missing key — never retried, client poisons)
//   - 400/404/422 (and any other unlisted 4xx) → KindValidation (never
//     retried)
//   - 429/408/5xx → KindUnavailable (retried, then fail-open)
//   - status 0 → KindUnavailable: the failure was transport-shaped
//     (timeout, connection refused, TLS), not an HTTP response at all
//
// body carries the (bounded) response text for diagnostics when cause is
// nil; a non-nil cause wins. The result is nil only when there is nothing
// to describe (no status, no body, no cause).
func classifyStatus(op string, status int, body string, cause error) *Error {
	if status == 0 && cause == nil && body == "" {
		return nil
	}
	err := cause
	if err == nil && body != "" {
		err = errors.New(body)
	}
	return &Error{Kind: kindForStatus(status), Status: status, Op: op, Err: err}
}

// kindForStatus is the status → class mapping behind classifyStatus.
func kindForStatus(status int) ErrorKind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return KindAuth
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return KindValidation
	case 0, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return KindUnavailable
	}
	if status >= 500 && status <= 599 {
		return KindUnavailable
	}
	if status >= 400 && status < 500 {
		// Unlisted 4xx (402, 405, 409, …): deterministic rejections —
		// retrying cannot change the answer. Validation is the closest
		// class: surface it, never retry it.
		return KindValidation
	}
	// 3xx and anything exotic: the backend is not answering the protocol
	// correctly; treat it as availability trouble.
	return KindUnavailable
}
