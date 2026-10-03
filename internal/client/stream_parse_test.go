package client

import (
	"context"
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
// error path is already bounded by maxErrorBodyBytes).
func TestChatCompletion_OversizedBodyIsBounded(t *testing.T) {
	// 9 MiB of JSON string content: beyond maxCompletionBodyBytes, so the
	// bounded read truncates it and the decode fails with unexpected EOF.
	huge := `{"id":"` + strings.Repeat("a", 9<<20) + `"}`

	server := httptest.NewServer(http.HandlerFunc(jsonBodyHandler(huge, "application/json")))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	if _, err := c.ChatCompletion(context.Background(), defaultRequest()); err == nil {
		t.Fatal("expected the bounded decode to fail on an oversized body, got nil error")
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
