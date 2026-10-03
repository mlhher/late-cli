package executor

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
)

// timeoutError is a minimal net.Error implementation that always reports a
// timeout, mirroring what net/http surfaces for stalled connections.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestStreamRetryDelay(t *testing.T) {
	t.Run("attempt 1 is positive and within base delay", func(t *testing.T) {
		for i := 0; i < 500; i++ {
			d := streamRetryDelay(1)
			if d <= 0 {
				t.Fatalf("streamRetryDelay(1) = %v, want > 0", d)
			}
			if d > streamRetryBaseDelay {
				t.Fatalf("streamRetryDelay(1) = %v, want <= %v", d, streamRetryBaseDelay)
			}
		}
	})

	t.Run("large attempts are capped", func(t *testing.T) {
		for _, attempt := range []int{40, 100000} {
			for i := 0; i < 100; i++ {
				d := streamRetryDelay(attempt)
				if d < 0 {
					t.Fatalf("streamRetryDelay(%d) = %v, want >= 0", attempt, d)
				}
				if d > streamRetryMaxDelay {
					t.Fatalf("streamRetryDelay(%d) = %v, want <= %v", attempt, d, streamRetryMaxDelay)
				}
			}
		}
	})

	t.Run("backoff grows with attempt number", func(t *testing.T) {
		const draws = 500

		// attempt 1 draws uniformly from [0, 500ms], attempt 5 from
		// [0, 8s]; the observed maxima must reflect that growth.
		maxFirst := time.Duration(0)
		for i := 0; i < draws; i++ {
			if d := streamRetryDelay(1); d > maxFirst {
				maxFirst = d
			}
		}
		maxFifth := time.Duration(0)
		for i := 0; i < draws; i++ {
			if d := streamRetryDelay(5); d > maxFifth {
				maxFifth = d
			}
		}
		if maxFifth <= maxFirst {
			t.Fatalf("expected growth between attempts: max over %d draws was %v for attempt 1, %v for attempt 5", draws, maxFirst, maxFifth)
		}
	})
}

func TestEffectiveRetryDelay(t *testing.T) {
	tests := []struct {
		name       string
		local      time.Duration
		retryAfter time.Duration
		want       time.Duration
	}{
		{
			// Absent (or client-rejected-invalid) Retry-After: the local
			// jittered backoff is used untouched.
			name:       "Retry-After 0 leaves the local backoff untouched",
			local:      500 * time.Millisecond,
			retryAfter: 0,
			want:       500 * time.Millisecond,
		},
		{
			// Core owner requirement: never retry before the server asked.
			name:       "server-requested wait longer than local wins",
			local:      500 * time.Millisecond,
			retryAfter: 2 * time.Second,
			want:       2 * time.Second,
		},
		{
			// The local backoff still applies when it is the larger of the two.
			name:       "local backoff longer than the server request wins",
			local:      5 * time.Second,
			retryAfter: 2 * time.Second,
			want:       5 * time.Second,
		},
		{
			// Hostile/buggy server asking for an absurd wait: capped at the
			// ceiling instead of hanging the interactive session.
			name:       "huge Retry-After is capped at the ceiling",
			local:      500 * time.Millisecond,
			retryAfter: 10 * time.Minute,
			want:       retryAfterCeiling,
		},
		{
			// Defensive: the client maps invalid headers to 0; anything
			// non-positive must degrade to the local backoff.
			name:       "negative Retry-After is treated as absent",
			local:      500 * time.Millisecond,
			retryAfter: -3 * time.Second,
			want:       500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveRetryDelay(tt.local, tt.retryAfter); got != tt.want {
				t.Errorf("effectiveRetryDelay(%v, %v) = %v, want %v", tt.local, tt.retryAfter, got, tt.want)
			}
		})
	}
}

func TestIsRetryableStreamError(t *testing.T) {
	urlTimeoutErr := &url.Error{
		Op:  "Post",
		URL: "https://api/v1/chat/completions",
		Err: errors.New("read tcp 1.2.3.4:5->6:7: operation timed out"),
	}
	urlResetErr := &url.Error{
		Op:  "Post",
		URL: "https://api/v1/chat/completions",
		Err: errors.New("read tcp 1.2.3.4:5->6:7: connection reset by peer"),
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		// Retryable: network-level failures.
		{
			name: "raw url.Error is retryable",
			err:  urlTimeoutErr,
			want: true,
		},
		{
			name: "executor-wrapped url.Error is retryable",
			err:  fmt.Errorf("stream error: %w", urlTimeoutErr),
			want: true,
		},
		{
			name: "double-wrapped url.Error is retryable",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", urlResetErr)),
			want: true,
		},
		{
			name: "net.Error timeout wrapped is retryable",
			err:  fmt.Errorf("stream interrupted: %w", timeoutError{}),
			want: true,
		},
		{
			name: "double-wrapped net.Error timeout is retryable",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", timeoutError{})),
			want: true,
		},
		{
			name: "real DNS timeout wrapped is retryable",
			err:  fmt.Errorf("stream error: %w", &net.DNSError{Err: "i/o timeout", Name: "api.example.com", IsTimeout: true}),
			want: true,
		},
		{
			name: "io.EOF wrapped is retryable",
			err:  fmt.Errorf("stream interrupted: %w", io.EOF),
			want: true,
		},
		{
			name: "io.ErrUnexpectedEOF double-wrapped is retryable",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", io.ErrUnexpectedEOF)),
			want: true,
		},

		// Retryable: transient server responses.
		{
			name: "raw 500 is retryable",
			err:  &client.StatusError{StatusCode: 500, Status: "500 Internal Server Error", Body: "boom"},
			want: true,
		},
		{
			name: "wrapped 500 is retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 500, Status: "500 Internal Server Error", Body: "boom"}),
			want: true,
		},
		{
			name: "double-wrapped 502 is retryable",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", &client.StatusError{StatusCode: 502, Status: "502 Bad Gateway", Body: ""})),
			want: true,
		},
		{
			name: "wrapped 429 is retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 429, Status: "429 Too Many Requests"}),
			want: true,
		},
		{
			name: "wrapped 408 is retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 408, Status: "408 Request Timeout"}),
			want: true,
		},

		// Not retryable: cancellation fails fast even inside the wraps.
		{
			name: "context.Canceled wrapped is not retryable",
			err:  fmt.Errorf("stream error: %w", context.Canceled),
			want: false,
		},
		{
			name: "context.DeadlineExceeded wrapped is not retryable",
			err:  fmt.Errorf("stream error: %w", context.DeadlineExceeded),
			want: false,
		},
		{
			name: "context.Canceled double-wrapped is not retryable",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", context.Canceled)),
			want: false,
		},
		{
			name: "url.Error carrying a canceled context is not retryable",
			err:  fmt.Errorf("stream error: %w", &url.Error{Op: "Post", URL: "https://api/v1/chat/completions", Err: context.Canceled}),
			want: false,
		},

		// Not retryable: permanent server responses.
		{
			name: "wrapped 400 is not retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 400, Status: "400 Bad Request", Body: "invalid model"}),
			want: false,
		},
		{
			name: "wrapped 401 is not retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 401, Status: "401 Unauthorized"}),
			want: false,
		},
		{
			name: "wrapped 403 is not retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 403, Status: "403 Forbidden"}),
			want: false,
		},
		{
			name: "wrapped 404 is not retryable",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 404, Status: "404 Not Found"}),
			want: false,
		},
		{
			// 413 NEVER retries — any tier: the provider rejected the request
			// BODY, so resending the identical body is guaranteed to fail.
			// The typed client error carries the ErrPayloadTooLarge sentinel.
			name: "wrapped 413 payload-too-large is not retryable",
			err:  fmt.Errorf("stream error: %w", &client.PayloadTooLargeError{Status: &client.StatusError{StatusCode: 413, Status: "413 Payload Too Large", Body: "Request body too large"}}),
			want: false,
		},

		// Not retryable: anything unknown fails fast, like pre-retry behavior.
		{
			name: "plain parser error is not retryable",
			err:  errors.New("some parser error"),
			want: false,
		},
		{
			name: "nil is not retryable",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableStreamError(tt.err); got != tt.want {
				t.Errorf("isRetryableStreamError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestMaxStreamRetriesFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want int
	}{
		{
			// Pins the interactive default (owner item 7): 10 retries carry
			// about 75.75s of expected total backoff. A deliberate change to
			// DefaultMaxStreamRetries must update this literal and its docs.
			name: "absent key falls back to the pinned default (10)",
			ctx:  context.Background(),
			want: 10,
		},
		{
			name: "positive override is honored",
			ctx:  context.WithValue(context.Background(), common.MaxStreamRetriesKey, 7),
			want: 7,
		},
		{
			name: "zero disables retries",
			ctx:  context.WithValue(context.Background(), common.MaxStreamRetriesKey, 0),
			want: 0,
		},
		{
			name: "negative value means disabled",
			ctx:  context.WithValue(context.Background(), common.MaxStreamRetriesKey, -3),
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxStreamRetriesFromContext(tt.ctx); got != tt.want {
				t.Errorf("maxStreamRetriesFromContext() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClassifyStreamError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want streamRetryClass
	}{
		// Bad body: 400 gets its own tier.
		{
			name: "wrapped 400 is bad body",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 400, Status: "400 Bad Request", Body: "invalid model"}),
			want: retryClassBadBody,
		},

		// Infrastructure: transient server responses.
		{
			name: "wrapped 408 is infra",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 408, Status: "408 Request Timeout"}),
			want: retryClassInfra,
		},
		{
			name: "wrapped 429 is infra",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 429, Status: "429 Too Many Requests"}),
			want: retryClassInfra,
		},
		{
			name: "wrapped 500 is infra",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 500, Status: "500 Internal Server Error", Body: "boom"}),
			want: retryClassInfra,
		},
		{
			name: "wrapped 503 is infra",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 503, Status: "503 Service Unavailable"}),
			want: retryClassInfra,
		},

		// None: permanent client responses.
		{
			name: "wrapped 401 is none",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 401, Status: "401 Unauthorized"}),
			want: retryClassNone,
		},
		{
			name: "wrapped 403 is none",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 403, Status: "403 Forbidden"}),
			want: retryClassNone,
		},
		{
			name: "wrapped 404 is none",
			err:  fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 404, Status: "404 Not Found"}),
			want: retryClassNone,
		},
		{
			// 413 fails fast on every tier: the request body exceeded the
			// provider's limit, so retrying cannot help (pinned here and by
			// TestRunLoopDoesNotRetry413 end-to-end).
			name: "wrapped 413 payload-too-large is none",
			err:  fmt.Errorf("stream error: %w", &client.PayloadTooLargeError{Status: &client.StatusError{StatusCode: 413, Status: "413 Payload Too Large", Body: "Request body too large"}}),
			want: retryClassNone,
		},

		// None: cancellation fails fast even inside the wraps.
		{
			name: "plain context.Canceled is none",
			err:  context.Canceled,
			want: retryClassNone,
		},
		{
			name: "wrapped context.Canceled is none",
			err:  fmt.Errorf("stream error: %w", context.Canceled),
			want: retryClassNone,
		},
		{
			name: "double-wrapped context.Canceled is none",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", context.Canceled)),
			want: retryClassNone,
		},
		{
			name: "plain context.DeadlineExceeded is none",
			err:  context.DeadlineExceeded,
			want: retryClassNone,
		},
		{
			name: "double-wrapped context.DeadlineExceeded is none",
			err:  fmt.Errorf("stream error: %w", fmt.Errorf("stream interrupted: %w", context.DeadlineExceeded)),
			want: retryClassNone,
		},
		{
			name: "url.Error carrying a canceled context is none",
			err:  fmt.Errorf("stream error: %w", &url.Error{Op: "Post", URL: "https://api/v1/chat/completions", Err: context.Canceled}),
			want: retryClassNone,
		},

		// Infrastructure: network-level failures.
		{
			name: "plain url.Error is infra",
			err:  &url.Error{Op: "Post", URL: "https://api/v1/chat/completions", Err: errors.New("boom")},
			want: retryClassInfra,
		},
		{
			name: "wrapped io.EOF is infra",
			err:  fmt.Errorf("stream error: %w", io.EOF),
			want: retryClassInfra,
		},
		{
			name: "wrapped io.ErrUnexpectedEOF is infra",
			err:  fmt.Errorf("stream error: %w", io.ErrUnexpectedEOF),
			want: retryClassInfra,
		},

		// url.Error: classified by CAUSE, not by the wrapper. Permanent
		// causes (TLS trust/hostname, unsupported scheme, HTTP-on-HTTPS)
		// fail fast; transient causes (refused, DNS, timeouts) stay infra.
		{
			// Certificate trust failure is permanent.
			name: "url.Error carrying x509.UnknownAuthorityError is none",
			err:  fmt.Errorf("stream error: %w", &url.Error{Op: "Post", URL: "https://host", Err: x509.UnknownAuthorityError{}}),
			want: retryClassNone,
		},
		{
			name: "url.Error carrying x509.HostnameError is none",
			err:  &url.Error{Op: "Post", URL: "https://host", Err: x509.HostnameError{Host: "host", Certificate: &x509.Certificate{}}},
			want: retryClassNone,
		},
		{
			name: "url.Error carrying unsupported protocol scheme is none",
			err:  &url.Error{Op: "Post", URL: "ftp://host", Err: errors.New(`unsupported protocol scheme "ftp"`)},
			want: retryClassNone,
		},
		{
			// Still transient — pins the non-overreach of the cause
			// classification: plain dial failures must stay infra.
			name: "url.Error carrying dial connection refused is infra",
			err:  &url.Error{Op: "Post", URL: "https://host", Err: &net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}},
			want: retryClassInfra,
		},
		{
			name: "url.Error carrying temporary DNS failure is infra",
			err:  &url.Error{Op: "Post", URL: "https://host", Err: errors.New("dial tcp: lookup host: temporary failure in name resolution")},
			want: retryClassInfra,
		},

		// Infrastructure: mid-stream transport failures after a 200 — the
		// client surfaces these as *client.StreamInterruptedError.
		{
			name: "wrapped mid-stream http2 RST_STREAM is infra-retryable",
			err:  fmt.Errorf("stream error: %w", &client.StreamInterruptedError{Err: errors.New("stream error: stream ID 1; INTERNAL_ERROR; received from peer")}),
			want: retryClassInfra,
		},
		{
			name: "mid-stream wrapper carrying cancellation is not retryable",
			err:  fmt.Errorf("stream error: %w", &client.StreamInterruptedError{Err: context.Canceled}),
			want: retryClassNone,
		},
		{
			name: "wrapped mid-stream truncated body is infra-retryable",
			err:  fmt.Errorf("stream error: %w", &client.StreamInterruptedError{Err: io.ErrUnexpectedEOF}),
			want: retryClassInfra,
		},
		{
			name: "wrapped oversized SSE line (bufio.ErrTooLong) fails fast",
			err:  fmt.Errorf("stream error: %w", &client.StreamInterruptedError{Err: bufio.ErrTooLong}),
			want: retryClassNone,
		},
		{
			// Owner item 2 pin: a non-timeout net.OpError wrapping ECONNRESET
			// mid-body is infra-retryable, not unknown/non-retryable.
			name: "mid-body ECONNRESET via net.OpError is infra-retryable",
			err:  fmt.Errorf("stream error: %w", &client.StreamInterruptedError{Err: &net.OpError{Op: "read", Err: errors.New("read: connection reset by peer")}}),
			want: retryClassInfra,
		},

		// None: anything unknown fails fast, like pre-retry behavior.
		{
			name: "nil is none",
			err:  nil,
			want: retryClassNone,
		},
		{
			name: "generic error is none",
			err:  fmt.Errorf("stream error: %w", errors.New("x")),
			want: retryClassNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyStreamError(tt.err); got != tt.want {
				t.Errorf("classifyStreamError(%v) = %v, want %v", tt.err, got, tt.want)
			}
			// isRetryableStreamError is the infrastructure-budget view: it
			// must be true exactly for retryClassInfra rows, i.e. false for
			// both retryClassNone and retryClassBadBody rows.
			if got := isRetryableStreamError(tt.err); got != (tt.want == retryClassInfra) {
				t.Errorf("isRetryableStreamError(%v) = %v, want %v (class %v)", tt.err, got, tt.want == retryClassInfra, tt.want)
			}
		})
	}
}

func TestMaxBadBodyRetriesFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want int
	}{
		{
			name: "absent key falls back to default",
			ctx:  context.Background(),
			want: DefaultMaxBadBodyRetries,
		},
		{
			name: "positive override is honored",
			ctx:  context.WithValue(context.Background(), common.MaxBadBodyRetriesKey, 7),
			want: 7,
		},
		{
			name: "zero disables retries",
			ctx:  context.WithValue(context.Background(), common.MaxBadBodyRetriesKey, 0),
			want: 0,
		},
		{
			name: "negative value means disabled",
			ctx:  context.WithValue(context.Background(), common.MaxBadBodyRetriesKey, -3),
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxBadBodyRetriesFromContext(tt.ctx); got != tt.want {
				t.Errorf("maxBadBodyRetriesFromContext() = %d, want %d", got, tt.want)
			}
		})
	}
}
