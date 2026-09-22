package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatusError_ErrorFormat(t *testing.T) {
	tests := []struct {
		name string
		se   *StatusError
		want string
	}{
		{
			name: "with body renders legacy API error format",
			se:   &StatusError{StatusCode: 500, Status: "500 Internal Server Error", Body: "internal error"},
			want: "API error (500): internal error",
		},
		{
			name: "without body renders legacy status format",
			se:   &StatusError{StatusCode: 429, Status: "429 Too Many Requests"},
			want: "status: 429",
		},
		{
			name: "empty body renders legacy status format",
			se:   &StatusError{StatusCode: 408, Status: "408 Request Timeout", Body: ""},
			want: "status: 408",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.se.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusError_ErrorsAsThroughWrapChain(t *testing.T) {
	se := &StatusError{StatusCode: 503, Status: "503 Service Unavailable", Body: "overloaded"}

	// Mirrors the real chains: executor.go wraps with "stream error: %w" and
	// client.go with "stream interrupted: %w".
	wrapped := fmt.Errorf("a: %w", fmt.Errorf("stream error: %w", se))

	var got *StatusError
	if !errors.As(wrapped, &got) {
		t.Fatal("errors.As failed to recover *StatusError through wrap chain")
	}
	if got.StatusCode != 503 {
		t.Errorf("StatusCode = %d, want 503", got.StatusCode)
	}
	if got != se {
		t.Error("errors.As recovered a different *StatusError instance")
	}

	// The legacy message text must survive wrapping unchanged.
	want := "a: stream error: API error (503): overloaded"
	if msg := wrapped.Error(); msg != want {
		t.Errorf("wrapped message = %q, want %q", msg, want)
	}
}

func TestFormatError_ReturnsTypedStatusError(t *testing.T) {
	t.Run("API error body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"boom","type":"invalid_request_error","code":"invalid_api_key"}}`)
		}))
		defer server.Close()

		c := NewClient(Config{BaseURL: server.URL})
		_, err := c.ChatCompletion(context.Background(), ChatCompletionRequest{
			Model:    "test-model",
			Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
		})
		if err == nil {
			t.Fatal("expected error for 500 response, got nil")
		}

		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("error %T (%v) is not a *StatusError", err, err)
		}
		if se.StatusCode != http.StatusInternalServerError {
			t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusInternalServerError)
		}
		if want := "API error (500): boom"; se.Error() != want {
			t.Errorf("Error() = %q, want %q", se.Error(), want)
		}
		if se.Type != "invalid_request_error" {
			t.Errorf("Type = %q, want %q", se.Type, "invalid_request_error")
		}
		if se.Code != "invalid_api_key" {
			t.Errorf("Code = %v, want %q", se.Code, "invalid_api_key")
		}
		// A short message must be stored verbatim, not truncated.
		if want := "boom"; se.Body != want {
			t.Errorf("Body = %q, want %q (short message must not be truncated)", se.Body, want)
		}
		if se.RetryAfter != 0 {
			t.Errorf("RetryAfter = %v, want 0 when no Retry-After header is sent", se.RetryAfter)
		}
	})

	t.Run("body without type or code fields", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
		}))
		defer server.Close()

		c := NewClient(Config{BaseURL: server.URL})
		_, err := c.ChatCompletion(context.Background(), ChatCompletionRequest{
			Model:    "test-model",
			Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
		})
		if err == nil {
			t.Fatal("expected error for 429 response, got nil")
		}

		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("error %T (%v) is not a *StatusError", err, err)
		}
		if se.StatusCode != http.StatusTooManyRequests {
			t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusTooManyRequests)
		}
		if se.Type != "" {
			t.Errorf("Type = %q, want empty", se.Type)
		}
		if se.Code != nil {
			t.Errorf("Code = %v, want nil", se.Code)
		}
		if want := "rate limited"; se.Body != want {
			t.Errorf("Body = %q, want %q", se.Body, want)
		}
		if want := "API error (429): rate limited"; se.Error() != want {
			t.Errorf("Error() = %q, want %q", se.Error(), want)
		}
	})

	t.Run("empty body renders legacy status format", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		c := NewClient(Config{BaseURL: server.URL})
		_, err := c.ChatCompletion(context.Background(), ChatCompletionRequest{
			Model:    "test-model",
			Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
		})
		if err == nil {
			t.Fatal("expected error for 502 response, got nil")
		}

		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("error %T (%v) is not a *StatusError", err, err)
		}
		if se.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusBadGateway)
		}
		if se.Body != "" {
			t.Errorf("Body = %q, want empty", se.Body)
		}
		if want := "status: 502"; se.Error() != want {
			t.Errorf("Error() = %q, want %q", se.Error(), want)
		}
	})

	t.Run("plain-text body falls back to sanitized text", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "not json")
		}))
		defer server.Close()

		c := NewClient(Config{BaseURL: server.URL})
		_, err := c.ChatCompletion(context.Background(), ChatCompletionRequest{
			Model:    "test-model",
			Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
		})
		if err == nil {
			t.Fatal("expected error for 502 response, got nil")
		}

		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("error %T (%v) is not a *StatusError", err, err)
		}
		if want := "not json"; se.Body != want {
			t.Errorf("Body = %q, want sanitized fallback %q", se.Body, want)
		}
		if want := "API error (502): not json"; se.Error() != want {
			t.Errorf("Error() = %q, want %q", se.Error(), want)
		}
	})
}

// errorResp builds a bare *http.Response for direct formatError tests.
func errorResp(status int, header map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range header {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// formatErrorStatusError runs formatError on resp and returns the recovered
// *StatusError, failing t if the error is not one.
func formatErrorStatusError(t *testing.T, resp *http.Response) *StatusError {
	t.Helper()
	c := NewClient(Config{BaseURL: "http://localhost"})
	err := c.formatError(resp)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("formatError returned %T (%v), want *StatusError", err, err)
	}
	return se
}

func TestFormatError_PlainTextBodyIsSanitized(t *testing.T) {
	se := formatErrorStatusError(t, errorResp(http.StatusBadGateway, nil, "502 Bad Gateway\nupstream   error"))

	if want := "502 Bad Gateway upstream error"; se.Body != want {
		t.Errorf("Body = %q, want collapsed %q", se.Body, want)
	}
	if want := "API error (502): 502 Bad Gateway upstream error"; se.Error() != want {
		t.Errorf("Error() = %q, want %q", se.Error(), want)
	}
}

func TestFormatError_HTMLBodyStripsControlChars(t *testing.T) {
	raw := "<html>\r\n<title>502</title>\x00  Bad  \t Gateway\r\n</html>"
	se := formatErrorStatusError(t, errorResp(http.StatusBadGateway, nil, raw))

	if strings.ContainsAny(se.Body, "\r\x00\n") {
		t.Errorf("Body = %q, want no control characters", se.Body)
	}
	if want := "<html> <title>502</title> Bad Gateway </html>"; se.Body != want {
		t.Errorf("Body = %q, want sanitized %q", se.Body, want)
	}
}

func TestFormatError_WhitespaceOnlyBodyKeepsLegacyStatusFormat(t *testing.T) {
	se := formatErrorStatusError(t, errorResp(http.StatusBadGateway, nil, " \r\n\t "))

	if se.Body != "" {
		t.Errorf("Body = %q, want empty", se.Body)
	}
	if want := "status: 502"; se.Error() != want {
		t.Errorf("Error() = %q, want %q", se.Error(), want)
	}
}

func TestFormatError_LongJSONMessageTruncatedRuneSafe(t *testing.T) {
	t.Run("ascii message truncated to limit", func(t *testing.T) {
		msg := strings.Repeat("x", 1500)
		se := formatErrorStatusError(t, errorResp(http.StatusInternalServerError, nil,
			fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":42}}`, msg)))

		if got := len([]rune(se.Body)); got != maxErrorMessageBytes {
			t.Errorf("rune count of Body = %d, want %d", got, maxErrorMessageBytes)
		}
		if got := len(se.Body); got != maxErrorMessageBytes {
			t.Errorf("byte length of Body = %d, want %d", got, maxErrorMessageBytes)
		}
		if want := msg[:maxErrorMessageBytes]; se.Body != want {
			t.Errorf("Body prefix mismatch: got %q... want %q...", se.Body[:32], want[:32])
		}
		// json.Unmarshal decodes JSON numbers into any as float64.
		if se.Type != "server_error" || se.Code != float64(42) {
			t.Errorf("Type/Code = %q/%v, want server_error/42 (must survive truncation)", se.Type, se.Code)
		}
	})

	t.Run("multibyte message never splits a rune", func(t *testing.T) {
		// é is 2 bytes, so the 1024-byte limit would land exactly on a rune
		// boundary; € is 3 bytes, so the limit falls mid-rune and the cut must
		// walk back to the start of that rune (341 whole runes = 1023 bytes).
		msg := strings.Repeat("€", 400) // 1200 bytes
		se := formatErrorStatusError(t, errorResp(http.StatusInternalServerError, nil,
			fmt.Sprintf(`{"error":{"message":%q}}`, msg)))

		if want := strings.Repeat("€", 341); se.Body != want {
			t.Errorf("Body = %d runes / %d bytes, want 341 intact € runes (1023 bytes)", len([]rune(se.Body)), len(se.Body))
		}
	})
}

func TestFormatError_JSONBodyWithoutMessageFallsBackToRawText(t *testing.T) {
	body := `{"error":{"type":"server_error"}}`
	se := formatErrorStatusError(t, errorResp(http.StatusInternalServerError, nil, body))

	// No structured message exists, so the sanitized raw body is surfaced
	// instead of collapsing to "status: 500"; the type is still preserved.
	if want := "API error (500): " + body; se.Error() != want {
		t.Errorf("Error() = %q, want %q", se.Error(), want)
	}
	if se.Type != "server_error" {
		t.Errorf("Type = %q, want %q", se.Type, "server_error")
	}
}

func TestFormatError_OversizedBodyReadIsBounded(t *testing.T) {
	// countingReader serves an endless supply of 'a' bytes and records how
	// many were consumed, so the read cap can be asserted directly.
	cr := &countingReader{}
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     http.Header{},
		Body:       io.NopCloser(cr),
	}
	se := formatErrorStatusError(t, resp)

	if cr.served > maxErrorBodyBytes {
		t.Errorf("read %d bytes from the error body, want at most %d", cr.served, maxErrorBodyBytes)
	}
	if want := strings.Repeat("a", maxErrorMessageBytes); se.Body != want {
		t.Errorf("Body = %d bytes, want the first %d bytes of the body", len(se.Body), maxErrorMessageBytes)
	}
}

type countingReader struct {
	served int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n := len(p)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	r.served += n
	return n, nil
}

func TestFormatError_CapturesRetryAfterHeader(t *testing.T) {
	t.Run("delta-seconds form", func(t *testing.T) {
		se := formatErrorStatusError(t, errorResp(http.StatusTooManyRequests,
			map[string]string{"Retry-After": "2"}, "rate limited"))
		if se.RetryAfter != 2*time.Second {
			t.Errorf("RetryAfter = %v, want %v", se.RetryAfter, 2*time.Second)
		}
	})

	t.Run("http-date form", func(t *testing.T) {
		// A far-future date must yield a positive delay; the exact value is
		// clock-dependent and covered deterministically by TestParseRetryAfterAt.
		se := formatErrorStatusError(t, errorResp(http.StatusServiceUnavailable,
			map[string]string{"Retry-After": "Wed, 01 Jan 2100 00:00:00 GMT"}, ""))
		if se.RetryAfter <= 0 {
			t.Errorf("RetryAfter = %v, want a positive delay for a future HTTP-date", se.RetryAfter)
		}
	})

	t.Run("invalid and absent values are dropped", func(t *testing.T) {
		for _, v := range []string{"", "0", "-5", "soon", "2.5"} {
			se := formatErrorStatusError(t, errorResp(http.StatusTooManyRequests,
				map[string]string{"Retry-After": v}, ""))
			if se.RetryAfter != 0 {
				t.Errorf("Retry-After %q: RetryAfter = %v, want 0", v, se.RetryAfter)
			}
		}
		se := formatErrorStatusError(t, errorResp(http.StatusTooManyRequests, nil, ""))
		if se.RetryAfter != 0 {
			t.Errorf("RetryAfter = %v with no Retry-After header, want 0", se.RetryAfter)
		}
	})
}

func TestParseRetryAfterAt(t *testing.T) {
	now := time.Date(2015, 10, 21, 7, 28, 0, 0, time.UTC)
	tests := []struct {
		name string
		val  string
		want time.Duration
	}{
		{"empty", "", 0},
		{"delta seconds", "2", 2 * time.Second},
		{"delta seconds with surrounding whitespace", "  3  ", 3 * time.Second},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"float is invalid", "2.5", 0},
		{"garbage", "soon", 0},
		{"out of range integer", "99999999999999999999", 0},
		{"future http date", "Wed, 21 Oct 2015 07:28:30 GMT", 30 * time.Second},
		{"http date at now", "Wed, 21 Oct 2015 07:28:00 GMT", 0},
		{"past http date", "Wed, 21 Oct 2015 07:27:00 GMT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfterAt(tt.val, now); got != tt.want {
				t.Errorf("parseRetryAfterAt(%q, now) = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}

func TestParseRetryAfter_WrapsClock(t *testing.T) {
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Errorf("parseRetryAfter(\"2\") = %v, want %v", got, 2*time.Second)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(\"\") = %v, want 0", got)
	}
	future := time.Now().UTC().Add(45 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 45*time.Second {
		t.Errorf("parseRetryAfter(future date) = %v, want a positive delay of at most 45s", got)
	}
}
