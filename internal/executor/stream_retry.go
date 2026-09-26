package executor

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"strings"
	"time"

	"late/internal/client"
	"late/internal/common"
)

const (
	// DefaultMaxStreamRetries is the default retry budget for a failing LLM
	// stream call. The interactive default is deliberately small: with full
	// jitter over [0, base*2^(n-1)] (base 500ms, capped at 30s) the expected
	// total backoff across the whole budget is about 75.75s — long enough to
	// ride out transient gateway hiccups, short enough that a hung provider
	// does not keep a user waiting for the ~23.8 minutes that 100 retries
	// would mean. Overridable via -max-stream-retries /
	// LATE_MAX_STREAM_RETRIES; 0 or a negative value disables stream
	// retrying entirely.
	DefaultMaxStreamRetries = 10
	// DefaultMaxBadBodyRetries is the dedicated retry budget for HTTP 400
	// responses. A 400 "read body failed" from strict OpenAI-compatible
	// gateways (e.g. z.ai/GLM) is frequently a transient upstream failure,
	// and a handful of quick retries resolves it; genuinely malformed
	// requests still fail after this small, bounded budget. It is
	// deliberately much smaller than the infrastructure budget
	// (DefaultMaxStreamRetries). Not exposed as a CLI flag.
	DefaultMaxBadBodyRetries = 3
	// streamRetryBaseDelay is the backoff for the first retry.
	streamRetryBaseDelay = 500 * time.Millisecond
	// streamRetryMaxDelay caps a single backoff interval.
	streamRetryMaxDelay = 30 * time.Second
)

// streamRetryClass buckets a failed stream attempt into a retry tier:
// retryClassNone fails fast, retryClassInfra draws from the infrastructure
// budget (transport/5xx/429/408), and retryClassBadBody draws from the
// separate, much smaller bad-body budget (HTTP 400 body-parse rejections,
// frequently transient on strict OpenAI-compatible gateways such as
// z.ai/GLM).
type streamRetryClass int

const (
	// retryClassNone means the error must fail fast: cancellation, permanent
	// client errors, and anything unknown.
	retryClassNone streamRetryClass = iota
	// retryClassInfra covers infrastructure-style failures: network-level
	// errors (timeouts, refused/reset connections, mid-body disconnects) and
	// transient server responses (408/429/5xx).
	retryClassInfra
	// retryClassBadBody covers HTTP 400 body-parse rejections, which strict
	// OpenAI-compatible gateways often emit transiently.
	retryClassBadBody
)

// maxStreamRetryAttempt clamps the attempt exponent so the
// 1<<(attempt-1) shift can never overflow before the cap is applied.
const maxStreamRetryAttempt = 40

// streamRetryDelay returns the exponentially growing, jittered wait before
// retry attempt `attempt` (1-based). The doubling delay is capped at
// streamRetryMaxDelay; full jitter spreads the wait uniformly over [0, cap]
// to avoid synchronized retry storms across agents.
func streamRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maxStreamRetryAttempt {
		attempt = maxStreamRetryAttempt
	}
	backoff := streamRetryBaseDelay * (1 << (attempt - 1))
	if backoff > streamRetryMaxDelay || backoff <= 0 { // <=0 guards shift overflow
		backoff = streamRetryMaxDelay
	}
	return rand.N(backoff)
}

// retryAfterCeiling caps a server-requested Retry-After wait. Honoring the
// header fully could hang an interactive session for hours on a hostile or
// buggy server; the cap keeps the worst case bounded while still never
// retrying before the requested delay for sane values. The wait remains
// cancelable (stop) regardless.
const retryAfterCeiling = 5 * time.Minute

// effectiveRetryDelay combines the local jittered backoff with a
// server-requested Retry-After: the result is never shorter than either —
// in particular never earlier than the server asked. RetryAfter of 0 (absent
// or invalid) leaves the local backoff untouched.
func effectiveRetryDelay(local time.Duration, retryAfter time.Duration) time.Duration {
	if retryAfter <= 0 {
		return local
	}
	if retryAfter > retryAfterCeiling {
		retryAfter = retryAfterCeiling
	}
	if local > retryAfter {
		return local
	}
	return retryAfter
}

// classifyStreamError buckets a failed LLM stream attempt into a retry tier.
// retryClassInfra covers infrastructure-style failures that draw from the
// main retry budget: network-level errors (timeouts, refused/reset
// connections, mid-body disconnects), mid-stream transport failures surfaced
// as *client.StreamInterruptedError (HTTP/2 RST_STREAM, GOAWAY, connection
// resets, truncated bodies — the request was accepted with 200 and the body
// then died), and transient server responses (408/429/5xx).
// A *url.Error is infra-tier UNLESS its underlying cause is permanent —
// TLS certificate/trust failures, non-TLS bytes on a TLS connection, or an
// unsupported URL scheme — in which case retrying cannot help and it maps
// to retryClassNone and fails fast (see isPermanentNetworkError).
// The one exception inside *client.StreamInterruptedError is
// bufio.ErrTooLong (the SSE line exceeds the client's scanner cap): that
// failure is deterministic — retrying cannot shrink the line — so it maps to
// retryClassNone and fails fast instead of burning the whole infra budget.
// retryClassBadBody isolates HTTP 400 body-parse rejections, frequently
// transient on strict OpenAI-compatible gateways, into their own tier.
// Everything else — context cancellation, permanent client errors
// (401/403/404), and unknown errors — maps to retryClassNone and fails fast,
// exactly like the pre-retry behavior.
func classifyStreamError(err error) streamRetryClass {
	if err == nil {
		return retryClassNone
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return retryClassNone
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if isPermanentNetworkError(ue) {
			return retryClassNone
		}
		return retryClassInfra
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return retryClassInfra
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return retryClassInfra
	}
	// Mid-stream transport failures: the server accepted the request (200)
	// and the body died — HTTP/2 RST_STREAM ("stream error: stream ID N;
	// INTERNAL_ERROR"), GOAWAY, connection resets, truncated bodies. These
	// are always infrastructure-tier: the attempt commits nothing and a
	// fresh stream is a clean retry.
	var sie *client.StreamInterruptedError
	if errors.As(err, &sie) {
		if errors.Is(sie.Err, bufio.ErrTooLong) {
			// Deterministic: the SSE line exceeds the client's scanner cap.
			// Retrying cannot shrink the line — fail fast instead of burning
			// the whole infra budget (~25 min) on a guaranteed failure.
			return retryClassNone
		}
		return retryClassInfra
	}
	var se *client.StatusError
	if errors.As(err, &se) {
		// 413 NEVER retries — on any tier: the provider rejected the request
		// body itself (payload too large), so resending the identical body is
		// guaranteed to fail again. The client surfaces it as
		// *client.PayloadTooLargeError (sentinel ErrPayloadTooLarge); the
		// fallthrough below would already fail fast (413 is not
		// 400/429/408/5xx), but the explicit branch pins the invariant where
		// a reader can see it.
		if se.StatusCode == 413 {
			return retryClassNone
		}
		if se.StatusCode == 400 {
			return retryClassBadBody
		}
		if se.StatusCode == 408 || se.StatusCode == 429 || se.StatusCode >= 500 {
			return retryClassInfra
		}
		return retryClassNone
	}
	return retryClassNone
}

// isPermanentNetworkError reports whether a url.Error wraps a cause that
// retrying cannot fix: TLS certificate failures (untrusted authority,
// hostname mismatch, invalid/expired chain), non-TLS bytes on a TLS
// connection, unsupported URL schemes, and plain-HTTP-on-HTTPS. Everything
// else a url.Error can carry (connection refused/reset, DNS temporary
// failures, timeouts) stays transient.
func isPermanentNetworkError(ue *url.Error) bool {
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
	return strings.HasPrefix(msg, "unsupported protocol scheme") ||
		strings.HasPrefix(msg, "http: server gave HTTP response to HTTPS client")
}

// isRetryableStreamError reports whether a failed LLM stream attempt should
// be automatically retried from the infrastructure budget. See
// classifyStreamError for the tiering.
func isRetryableStreamError(err error) bool {
	return classifyStreamError(err) == retryClassInfra
}

// maxStreamRetriesFromContext resolves the retry budget from ctx, falling
// back to DefaultMaxStreamRetries. Negative values mean "disabled".
func maxStreamRetriesFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(common.MaxStreamRetriesKey).(int); ok {
		if v < 0 {
			return 0
		}
		return v
	}
	return DefaultMaxStreamRetries
}

// maxBadBodyRetriesFromContext resolves the dedicated HTTP 400 retry budget
// from ctx, falling back to DefaultMaxBadBodyRetries. Negative values mean
// "disabled".
func maxBadBodyRetriesFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(common.MaxBadBodyRetriesKey).(int); ok {
		if v < 0 {
			return 0
		}
		return v
	}
	return DefaultMaxBadBodyRetries
}
