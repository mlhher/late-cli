package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseBodyHandler serves a static SSE body on the chat completions path and
// 404s the discovery probes (/props, /v1/models) so streaming tests are not
// coupled to backend identification.
func sseBodyHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}
}

// jsonBodyHandler serves a static JSON body on the chat completions path and
// 404s the discovery probes.
func jsonBodyHandler(body string, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	}
}

// TestChatCompletionStream_DataLineVariants pins the SSE data-line shapes the
// scanner must accept. OpenAI emits "data: {json}" (colon+space), but the SSE
// spec makes the single space optional, and some providers and proxies emit
// "data:{json}"; a UTF-8 BOM glued to the first line is equally tolerated.
// The regression being pinned: any of these shapes that fails the prefix
// match is silently dropped — no error is raised, so no retry tier ever sees
// the loss and the turn commits an arbitrarily truncated response.
func TestChatCompletionStream_DataLineVariants(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantChunks int
	}{
		{
			name:       "colon_space (OpenAI shape)",
			body:       sseChunks(sampleChunkHello, "[DONE]"),
			wantChunks: 1,
		},
		{
			name:       "colon_no_space (spec-legal)",
			body:       "data:" + sampleChunkHello + "\ndata: [DONE]\n",
			wantChunks: 1,
		},
		{
			name:       "mixed shapes in one stream",
			body:       "data: " + sampleChunkHello + "\ndata:" + sampleChunkWorld + "\ndata: [DONE]\n",
			wantChunks: 2,
		},
		{
			name:       "utf8_bom_on_first_line",
			body:       "\ufeff" + sseChunks(sampleChunkHello, "[DONE]"),
			wantChunks: 1,
		},
		{
			name:       "bom before no-space data line",
			body:       "\ufeffdata:" + sampleChunkHello + "\ndata: [DONE]\n",
			wantChunks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStreamTest(t)
			defer st.Close()
			st.Handle(sseBodyHandler(tt.body))

			chunks, err := collectStream(t, context.Background(), st.client, defaultRequest())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(chunks); got != tt.wantChunks {
				t.Fatalf("got %d chunks, want %d", got, tt.wantChunks)
			}
			if got := chunks[0].Choices[0].Delta.Content.String(); got != "Hello" {
				t.Errorf("first chunk content = %q, want %q", got, "Hello")
			}
		})
	}
}

// TestChatCompletionStream_EmptyDataKeepaliveDoesNotEndStream pins the
// keepalive fix: a bare "data:" line (nothing after the colon) is an SSE
// comment/keepalive, not end-of-stream. Treating it as the end ended the turn
// early on servers that interleave keepalives with real chunks — a truncated
// response with no error for any retry tier to see. The keepalive here sits
// between two real chunks: the stream must continue and complete.
func TestChatCompletionStream_EmptyDataKeepaliveDoesNotEndStream(t *testing.T) {
	body := "data: " + sampleChunkHello + "\n\n" +
		"data: \n\n" + // keepalive: bare "data:" with nothing after the colon
		"data: " + sampleChunkWorld + "\n\n" +
		"data: [DONE]\n"

	st := newStreamTest(t)
	defer st.Close()
	st.Handle(sseBodyHandler(body))

	chunks, err := collectStream(t, context.Background(), st.client, defaultRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 — the keepalive must not end the stream", len(chunks))
	}
	if got := accumulateContent(chunks); got != "Hello world" {
		t.Errorf("content after keepalive = %q, want %q", got, "Hello world")
	}
}

// TestChatCompletion_BOMBodyDecodes pins BOM tolerance on the non-streaming
// path: encoding/json rejects a UTF-8 BOM outright, so an unstripped BOM turns
// an otherwise healthy completion into a confusing parse error.
func TestChatCompletion_BOMBodyDecodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(jsonBodyHandler(
		"\ufeff"+`{"id":"1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`,
		"application/json")))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	resp, err := c.ChatCompletion(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("ChatCompletion with BOM body failed: %v", err)
	}
	if resp.ID != "1" {
		t.Errorf("resp.ID = %q, want %q", resp.ID, "1")
	}
}

// TestChatCompletion_OversizedBodyIsBounded pins the success-path body cap: a
// broken or hostile server streaming an unbounded 200 body must fail the
// bounded decode instead of being slurped into memory without limit (the
// error path is already bounded by maxErrorBodyBytes). The failure is the
// typed BodyTooLargeError — not a silent-truncation decode error.
func TestChatCompletion_OversizedBodyIsBounded(t *testing.T) {
	// 9 MiB of JSON string content: beyond maxCompletionBodyBytes, so the
	// bounded read detects the overrun and reports it as size.
	huge := `{"id":"` + strings.Repeat("a", 9<<20) + `"}`

	server := httptest.NewServer(http.HandlerFunc(jsonBodyHandler(huge, "application/json")))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	_, err := c.ChatCompletion(context.Background(), defaultRequest())
	var tooLarge *BodyTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *BodyTooLargeError for an oversized body, got %v", err)
	}
	if tooLarge.Limit != maxCompletionBodyBytes {
		t.Errorf("Limit = %d, want %d", tooLarge.Limit, maxCompletionBodyBytes)
	}
	if want := "response body too large"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// TestChatCompletion_OversizedBodyByOneByte pins the boundary: a body of
// exactly maxCompletionBodyBytes+1 bytes is the smallest possible violation,
// and it must still produce the clear typed error rather than the
// "unexpected end of JSON input" the silently truncated read used to yield.
func TestChatCompletion_OversizedBodyByOneByte(t *testing.T) {
	// Valid JSON prefix {"id":"aaa...a} trimmed so the raw body length is
	// exactly maxCompletionBodyBytes+1: decode would fail anyway, but the
	// size check must fire first with the actionable error.
	over := maxCompletionBodyBytes + 1
	prefix := `{"id":"`
	body := prefix + strings.Repeat("a", over-len(prefix))
	if len(body) != over {
		t.Fatalf("test body is %d bytes, want exactly %d", len(body), over)
	}

	server := httptest.NewServer(http.HandlerFunc(jsonBodyHandler(body, "application/json")))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	_, err := c.ChatCompletion(context.Background(), defaultRequest())
	var tooLarge *BodyTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *BodyTooLargeError for a (max+1)-byte body, got %v", err)
	}
	if tooLarge.Limit != maxCompletionBodyBytes {
		t.Errorf("Limit = %d, want %d", tooLarge.Limit, maxCompletionBodyBytes)
	}
}

// TestChatCompletion_BodyAtLimitStillDecodes pins the other side of the
// boundary: a body of exactly maxCompletionBodyBytes bytes is legal and must
// decode normally — the +1 probe exists so at-limit responses are not
// rejected.
func TestChatCompletion_BodyAtLimitStillDecodes(t *testing.T) {
	// {"id":"aaa...a"} sized to exactly maxCompletionBodyBytes.
	prefix, suffix := `{"id":"`, `"}`
	body := prefix + strings.Repeat("a", maxCompletionBodyBytes-len(prefix)-len(suffix)) + suffix
	if len(body) != maxCompletionBodyBytes {
		t.Fatalf("test body is %d bytes, want exactly %d", len(body), maxCompletionBodyBytes)
	}

	server := httptest.NewServer(http.HandlerFunc(jsonBodyHandler(body, "application/json")))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	resp, err := c.ChatCompletion(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("at-limit body must still decode, got error: %v", err)
	}
	if resp.ID == "" {
		t.Error("at-limit body decoded to an empty ID")
	}
}

// TestChatCompletion_NormalBodyStillDecodes pins that a normal-sized 200 body
// still decodes after the bounded-read change (guard against regressions in
// the shared non-stream decode path).
func TestChatCompletion_NormalBodyStillDecodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(jsonBodyHandler(
		`{"id":"2","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		"application/json")))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	resp, err := c.ChatCompletion(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Errorf("resp.Usage.TotalTokens = %d, want 7", resp.Usage.TotalTokens)
	}
}

// TestParseRetryAfterAt_ExtremeValues pins the Retry-After parser against the
// hostile values a misbehaving server can send: negative, zero, float-looking,
// and overflowing delta-seconds all yield 0 ("no requested delay") instead of
// a panic or a nonsensical wait; the executor caps any surviving value at its
// retryAfterCeiling.
func TestParseRetryAfterAt_ExtremeValues(t *testing.T) {
	// Fixed clock: the past/future HTTP-date cases must not depend on the
	// live wall clock (which can jump under NTP or virtualized time).
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		v    string
	}{
		{"negative", "-5"},
		{"zero", "0"},
		{"float", "1.5"},
		{"nan", "NaN"},
		{"overflowing seconds", "99999999999999999999"},
		{"garbage", "soon-ish"},
		{"past http-date", now.Add(-time.Hour).Format(http.TimeFormat)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfterAt(tt.v, now); got != 0 {
				t.Errorf("parseRetryAfterAt(%q) = %v, want 0", tt.v, got)
			}
		})
	}
}

// TestParseRetryAfterAt_HugeButValidSecondsDoesNotPanic is a companion to the
// executor's ceiling test: the parser itself may return a very large duration
// for a valid (absurd) delta-seconds value; the guarantee is that it neither
// panics nor returns a non-positive delay for a positive request.
func TestParseRetryAfterAt_HugeButValidSecondsDoesNotPanic(t *testing.T) {
	if d := parseRetryAfterAt("999999999", time.Now()); d <= 0 {
		t.Fatalf("parseRetryAfterAt(999999999) = %v, want > 0", d)
	}
}
