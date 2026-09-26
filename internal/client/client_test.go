package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSupportsVisionOverride(t *testing.T) {
	// Case 1: Config without EnableImages, and supportsVision is false
	c1 := NewClient(Config{
		BaseURL:      "http://localhost:8080",
		EnableImages: false,
	})
	if c1.SupportsVision() {
		t.Errorf("expected SupportsVision() to be false when EnableImages is false and backend support is unknown/false")
	}

	// Case 2: Config with EnableImages = true
	c2 := NewClient(Config{
		BaseURL:      "http://localhost:8080",
		EnableImages: true,
	})
	if !c2.SupportsVision() {
		t.Errorf("expected SupportsVision() to be true when EnableImages is true")
	}

	// Case 3: Config without EnableImages, but supportsVision is true
	c3 := NewClient(Config{
		BaseURL: "http://localhost:8080",
	})
	c3.supportsVision = true
	if !c3.SupportsVision() {
		t.Errorf("expected SupportsVision() to be true when c.supportsVision is true")
	}
}

func TestClient_Headers(t *testing.T) {
	t.Run("generic endpoint sends User-Agent without OpenRouter headers", func(t *testing.T) {
		var receivedHeaders http.Header
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}))
		defer server.Close()

		c := NewClient(Config{BaseURL: server.URL})
		_, err := c.ChatCompletion(context.Background(), defaultRequest())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedUA := "late-cli/dev (+https://github.com/mlhher/late-cli)"
		if got := receivedHeaders.Get("User-Agent"); got != expectedUA {
			t.Errorf("User-Agent = %q, want %q", got, expectedUA)
		}
		if got := receivedHeaders.Get("HTTP-Referer"); got != "" {
			t.Errorf("HTTP-Referer should not be set for generic endpoint, got %q", got)
		}
		if got := receivedHeaders.Get("X-OpenRouter-Title"); got != "" {
			t.Errorf("X-OpenRouter-Title should not be set for generic endpoint, got %q", got)
		}
		if got := receivedHeaders.Get("X-OpenRouter-Categories"); got != "" {
			t.Errorf("X-OpenRouter-Categories should not be set for generic endpoint, got %q", got)
		}
	})

	t.Run("openrouter endpoint sends attribution headers and uses AppVersion", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}))
		defer server.Close()

		c := NewClient(Config{
			BaseURL:    "https://openrouter.ai/api/v1",
			AppVersion: "1.2.3",
		})
		req, err := http.NewRequestWithContext(context.Background(), "POST", server.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		c.applyHeaders(req)

		expectedUA := "late-cli/1.2.3 (+https://github.com/mlhher/late-cli)"
		if got := req.Header.Get("User-Agent"); got != expectedUA {
			t.Errorf("User-Agent = %q, want %q", got, expectedUA)
		}
		if got := req.Header.Get("HTTP-Referer"); got != "https://github.com/mlhher/late-cli" {
			t.Errorf("HTTP-Referer = %q, want https://github.com/mlhher/late-cli", got)
		}
		if got := req.Header.Get("X-OpenRouter-Title"); got != "Late-CLI" {
			t.Errorf("X-OpenRouter-Title = %q, want Late-CLI", got)
		}
		if got := req.Header.Get("X-OpenRouter-Categories"); got != "cli-agent" {
			t.Errorf("X-OpenRouter-Categories = %q, want cli-agent", got)
		}
	})

	t.Run("custom UserAgent override", func(t *testing.T) {
		customUA := "custom-agent/1.0"
		c := NewClient(Config{
			BaseURL:   "http://localhost:8080",
			UserAgent: customUA,
		})
		req, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost:8080", nil)
		c.applyHeaders(req)

		if got := req.Header.Get("User-Agent"); got != customUA {
			t.Errorf("User-Agent = %q, want %q", got, customUA)
		}
	})
}

// Sample SSE chunk JSON strings shared across stream tests.
const (
	sampleChunkHello = `{"id":"c1","choices":[{"delta":{"content":"Hello"}}]}`
	sampleChunkWorld = `{"id":"c1","choices":[{"delta":{"content":" world"}}]}`
	sampleChunkStop  = `{"id":"c1","choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`
)

// defaultRequest returns a minimal ChatCompletionRequest used by most stream tests.
func defaultRequest() ChatCompletionRequest {
	return ChatCompletionRequest{
		Model:    "test-model",
		Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
	}
}

// streamTest is a shared test fixture for ChatCompletionStream tests.
type streamTest struct {
	server *httptest.Server
	client *Client
}

// newStreamTest creates a test server and client pair. The caller should call
// st.Handle() to set the response handler before making requests.
func newStreamTest(t *testing.T) *streamTest {
	t.Helper()
	st := &streamTest{
		server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
		})),
	}
	st.client = NewClient(Config{BaseURL: st.server.URL})
	return st
}

// Handle replaces the server's response handler.
func (st *streamTest) Handle(handler http.HandlerFunc) {
	st.server.Config.Handler = handler
}

// Close shuts down the test server. Call via defer immediately after creation.
func (st *streamTest) Close() {
	st.server.Close()
}

// collectStream drains the output/error channels from ChatCompletionStream
// into a slice of ChatCompletionChunk and an optional error.
func collectStream(t *testing.T, ctx context.Context, c *Client, req ChatCompletionRequest) ([]ChatCompletionChunk, error) {
	t.Helper()
	outCh, errCh := c.ChatCompletionStream(ctx, req)

	var chunks []ChatCompletionChunk
	var streamErr error

	// Collect in a goroutine to avoid deadlocks if channels don't close.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range outCh {
			chunks = append(chunks, chunk)
		}
		select {
		case err, ok := <-errCh:
			if ok && err != nil {
				streamErr = err
			}
		default:
		}
	}()

	// Wait with a timeout to catch hangs.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream collection timed out — channels were not closed")
	}

	return chunks, streamErr
}

// sseChunks builds an SSE-formatted response body from JSON strings.
func sseChunks(jsons ...string) string {
	var b strings.Builder
	for _, j := range jsons {
		fmt.Fprintf(&b, "data: %s\n", j)
	}
	return b.String()
}

// accumulateContent concatenates the delta content strings from all choices across chunks.
func accumulateContent(chunks []ChatCompletionChunk) string {
	var b strings.Builder
	for _, ch := range chunks {
		if len(ch.Choices) > 0 {
			b.WriteString(ch.Choices[0].Delta.Content.String())
		}
	}
	return b.String()
}

func TestChatCompletionStream_Termination(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantChunks int
		wantErr    bool
	}{
		{
			name:       "DONE_sentinel",
			body:       sseChunks(sampleChunkHello, "[DONE]"),
			wantChunks: 1,
			wantErr:    false,
		},
		{
			name:       "connection_close_no_sentinel",
			body:       sseChunks(sampleChunkHello),
			wantChunks: 1,
			wantErr:    false,
		},
		{
			name:       "empty_data_line",
			body:       sseChunks(sampleChunkHello, ""),
			wantChunks: 1,
			wantErr:    false,
		},
		{
			name:       "only_empty_data_line",
			body:       "data: \n",
			wantChunks: 0,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStreamTest(t)
			defer st.Close()

			st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tt.body)
			}))

			chunks, err := collectStream(t, context.Background(), st.client, defaultRequest())

			if got := len(chunks); got != tt.wantChunks {
				t.Errorf("got %d chunks, want %d", got, tt.wantChunks)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestChatCompletionStream_ContentAccumulation(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	chunkPayloads := []string{
		sampleChunkHello,
		sampleChunkWorld,
		sampleChunkStop,
	}

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunks(chunkPayloads...), "\ndata: [DONE]\n\n")
	}))

	resultChunks, err := collectStream(t, context.Background(), st.client, defaultRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify chunk count (3 data lines, none are [DONE])
	if got := len(resultChunks); got != 3 {
		t.Errorf("got %d chunks, want 3", got)
	}

	// Verify content accumulation
	if got := accumulateContent(resultChunks); got != "Hello world" {
		t.Errorf("accumulated content = %q, want %q", got, "Hello world")
	}

	// Verify finish reason on last chunk
	if resultChunks[2].Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", resultChunks[2].Choices[0].FinishReason, "stop")
	}
}

func TestChatCompletionStream_Non200Status(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error"}}`)
	}))

	_, err := collectStream(t, context.Background(), st.client, defaultRequest())
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestChatCompletionStream_InvalidJSON(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunks("{invalid json}", `{"id":"c1","choices":[{"delta":{"content":"valid"}}]}`, "[DONE]"))
	}))

	chunks, err := collectStream(t, context.Background(), st.client, defaultRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have 1 valid chunk (the invalid JSON was skipped).
	if got := len(chunks); got != 1 {
		t.Errorf("got %d chunks, want 1 (invalid JSON should be skipped)", got)
	}
}

func TestChatCompletionStream_OversizedSSELine(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	// Build a single SSE data line whose JSON payload is ~600 KB: a chunk
	// with a delta.content string of 600,000 chars. Marshal a
	// ChatCompletionChunk so the JSON is guaranteed to be valid.
	const bigLen = 600000
	bigContent := strings.Repeat("a", bigLen)
	bigChunk := ChatCompletionChunk{
		ID: "c1",
		Choices: []ChatCompletionChunkChoice{
			{Delta: ChatMessage{Content: TextContent(bigContent)}},
		},
	}
	payload, err := json.Marshal(bigChunk)
	if err != nil {
		t.Fatalf("failed to marshal oversized chunk: %v", err)
	}

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n", payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	chunks, err := collectStream(t, context.Background(), st.client, defaultRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exactly one chunk should be delivered — the oversized line must not
	// trip bufio.ErrTooLong and abort the stream.
	if got := len(chunks); got != 1 {
		t.Fatalf("got %d chunks, want 1", got)
	}
	if len(chunks[0].Choices) == 0 {
		t.Fatal("chunk has no choices")
	}
	if got := chunks[0].Choices[0].Delta.Content.String(); len(got) != bigLen {
		t.Errorf("chunk content length = %d, want %d", len(got), bigLen)
	}
}

// TestChatCompletionStream_LineOverScannerCap mirrors
// TestChatCompletionStream_OversizedSSELine but pushes the single data line
// past the client's 1 MB scanner cap (~1.5 MB). The scanner must abort with
// bufio.ErrTooLong and the stream must surface a *StreamInterruptedError on
// the error channel with no chunks delivered.
func TestChatCompletionStream_LineOverScannerCap(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	// Build a single SSE data line over the 1 MB scanner cap: a chunk with a
	// delta.content string of 1,500,000 chars. Marshal a ChatCompletionChunk
	// so the JSON is guaranteed to be valid.
	const bigLen = 1500000
	bigContent := strings.Repeat("a", bigLen)
	bigChunk := ChatCompletionChunk{
		ID: "c1",
		Choices: []ChatCompletionChunkChoice{
			{Delta: ChatMessage{Content: TextContent(bigContent)}},
		},
	}
	payload, err := json.Marshal(bigChunk)
	if err != nil {
		t.Fatalf("failed to marshal over-cap chunk: %v", err)
	}
	if got := len("data: ") + len(payload); got <= 1<<20 {
		t.Fatalf("test fixture line length = %d, want > %d (scanner cap)", got, 1<<20)
	}

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n", payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	chunks, err := collectStream(t, context.Background(), st.client, defaultRequest())

	// The scanner aborts before emitting anything: zero chunks must arrive.
	if got := len(chunks); got != 0 {
		t.Errorf("got %d chunks, want 0 (the over-cap line must abort the stream)", got)
	}

	// The scanner failure must surface as a *StreamInterruptedError wrapping
	// bufio.ErrTooLong.
	if err == nil {
		t.Fatal("expected an error from the error channel, got nil")
	}
	var sie *StreamInterruptedError
	if !errors.As(err, &sie) {
		t.Fatalf("error = %v (%T), want errors.As to match *StreamInterruptedError", err, err)
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("error = %v, want it to wrap bufio.ErrTooLong", err)
	}
	// The rendered message must stay byte-identical to the previous
	// fmt.Errorf("stream interrupted: %w", err).
	if got, want := err.Error(), "stream interrupted: bufio.Scanner: token too long"; got != want {
		t.Errorf("error message = %q, want %q", got, want)
	}
}

func TestChatCompletionStream_ContextCancellation(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	// Separate channel to block the server handler. Using ctx.Done() here
	// would create a race — we need to distinguish "client exited due to
	// context cancel" from "client exited because server closed connection."
	cancelled := make(chan struct{})
	defer close(cancelled)

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DiscoverBackend probes /props before the actual request. Return
		// 404 for non-completions paths so those probes don't hang on <-cancelled.
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		flusher := w.(http.Flusher) // httptest.Server always implements Flusher

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunks(sampleChunkHello))
		flusher.Flush()

		// Block until test teardown closes the cancelled channel.
		<-cancelled
	}))

	ctx, cancel := context.WithCancel(context.Background())

	outCh, errCh := st.client.ChatCompletionStream(ctx, defaultRequest())

	// Collect the first chunk — should arrive promptly before cancellation.
	select {
	case chunk := <-outCh:
		if got := chunk.Choices[0].Delta.Content.String(); got != "Hello" {
			t.Errorf("first chunk content = %q, want %q", got, "Hello")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	// Cancel the context to trigger cleanup.
	cancel()

	// Verify output channel closes after cancellation.
	select {
	case _, ok := <-outCh:
		if ok {
			t.Error("output channel should be closed after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("output channel not closed after context cancellation")
	}

	// Context cancellation interrupts the HTTP read, causing scanner.Err() to
	// send a "stream interrupted" error on errCh before the deferred close fires.
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error on errCh after context cancellation, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("error channel did not produce a value after context cancellation")
	}

	// Verify errCh is now closed.
	select {
	case _, ok := <-errCh:
		if ok {
			t.Error("error channel should be closed, but produced another value")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("error channel not closed after context cancellation")
	}
}

// TestChatCompletionStream_IdleWatchdog verifies that a provider which accepts
// the request, sends the SSE headers, and then streams nothing is aborted by
// the stream idle watchdog instead of blocking scanner.Scan() for the OS TCP
// lifetime. This is the half-open-connection / stalled-backend failure mode
// that used to hang the agent (subagents included).
func TestChatCompletionStream_IdleWatchdog(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	held := make(chan struct{})
	defer close(held) // runs (LIFO) before st.Close so the handler can unblock

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DiscoverBackend probes /props before the actual request; 404 those
		// so the probes don't hang on <-held.
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Send the response HEADERS, flush, then never write another byte and
		// never close the connection: the stream is completely silent.
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-held
	}))

	old := defaultStreamIdleTimeout
	SetStreamIdleTimeout(300 * time.Millisecond)
	t.Cleanup(func() { SetStreamIdleTimeout(old) })

	start := time.Now()
	outCh, errCh := st.client.ChatCompletionStream(context.Background(), defaultRequest())

	// The chunk channel must close once the watchdog aborts the stalled body
	// read. The watchdog ticks every 5s, so with a 300ms idle timeout the
	// abort lands on the first tick (~5s); the bound below proves a fast fail
	// (not the pre-fix unbounded hang) without over-pinning the tick schedule.
	chunkClosed := false
	for !chunkClosed {
		select {
		case _, ok := <-outCh:
			if !ok {
				chunkClosed = true
			}
		case <-time.After(20 * time.Second):
			t.Fatal("chunk channel never closed — idle watchdog did not abort the silent stream")
		}
	}
	elapsed := time.Since(start)

	// Sanity guard against a vacuous pass: only a real watchdog abort (or a
	// broken test setup) could return this fast; the abort itself cannot fire
	// before the 300ms idle timeout elapses.
	if elapsed < 200*time.Millisecond {
		t.Errorf("stream returned after %s; too fast for an idle-watchdog abort (idle timeout is 300ms) — test setup likely broken", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Errorf("stream aborted after %s; expected the idle watchdog to fail fast (~5s), not hang", elapsed)
	}
	t.Logf("idle watchdog aborted the silent stream after %s", elapsed)

	// The abort must surface as a regular stream error through the existing
	// scanner.Err() path (don't over-pin the message: it may be the
	// "stream interrupted" wrapper or a context/body-read error).
	select {
	case err, ok := <-errCh:
		if ok && err == nil {
			t.Error("error channel delivered a nil error")
		}
	default:
		t.Error("expected a stream error on the error channel after the idle watchdog aborted, got none")
	}
}

// TestChatCompletionStream_NormalStreamNotKilled verifies that a stream which
// keeps making real progress is never aborted, even across a watchdog tick
// (~5s): lines arrive every 300ms, well inside the 1s idle window, so lastRead
// keeps advancing and the watchdog never fires.
func TestChatCompletionStream_NormalStreamNotKilled(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	// 20 lines x 300ms = ~6s, deliberately crossing the first 5s watchdog tick
	// so the tick check actually runs against a live (slow) stream.
	const lines = 20

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < lines; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"chunk-%d\"}}]}\n", i)
			flusher.Flush()
			time.Sleep(300 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n")
		flusher.Flush()
	}))

	old := defaultStreamIdleTimeout
	SetStreamIdleTimeout(1 * time.Second)
	t.Cleanup(func() { SetStreamIdleTimeout(old) })

	outCh, errCh := st.client.ChatCompletionStream(context.Background(), defaultRequest())

	var chunks []ChatCompletionChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range outCh {
			chunks = append(chunks, chunk)
		}
	}()

	start := time.Now()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("stream did not finish — the idle watchdog likely killed a stream that was making progress")
	}
	t.Logf("slow-but-active stream of %d lines completed in %s without being killed", lines, time.Since(start))

	// outCh closes only after errCh was closed (LIFO defers), so any error is
	// already buffered by now.
	select {
	case err, ok := <-errCh:
		if ok && err != nil {
			t.Fatalf("watchdog killed a stream that was making progress every 300ms: %v", err)
		}
	default:
	}

	if got := len(chunks); got != lines {
		t.Errorf("got %d chunks, want %d (watchdog must not drop chunks on an active stream)", got, lines)
	}
}

// TestChatCompletion_RequestTimeout verifies that a server which accepts the
// connection and never responds cannot block the non-streaming ChatCompletion
// past defaultRequestTimeout (overridden here to 200ms).
func TestChatCompletion_RequestTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DiscoverBackend probes /props before the actual request; 404 those
		// so the probes don't hang on <-release.
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		<-release // never respond until test teardown
	}))
	// LIFO: close(release) runs before server.Close, so the blocked handler
	// is unblocked before the server waits for outstanding requests.
	defer server.Close()
	defer close(release)

	c := NewClient(Config{BaseURL: server.URL})

	old := defaultRequestTimeout
	SetRequestTimeout(200 * time.Millisecond)
	t.Cleanup(func() { SetRequestTimeout(old) })

	start := time.Now()
	_, err := c.ChatCompletion(context.Background(), defaultRequest())
	elapsed := time.Since(start)
	t.Logf("non-stream request against a silent server returned after %s", elapsed)

	if err == nil {
		t.Fatal("expected a timeout error from a server that never responds, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("request returned after %s; expected the 200ms request timeout to fire much sooner", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v; want it to wrap context.DeadlineExceeded", err)
	}
}

func TestStreamInterruptedError(t *testing.T) {
	sie := &StreamInterruptedError{Err: errors.New("boom")}
	if got := sie.Error(); got != "stream interrupted: boom" {
		t.Errorf("Error() = %q, want %q", got, "stream interrupted: boom")
	}

	// Unwrap keeps errors.Is working through the chain.
	outer := fmt.Errorf("outer: %w", &StreamInterruptedError{Err: io.ErrUnexpectedEOF})
	if !errors.Is(outer, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(%v, io.ErrUnexpectedEOF) = false, want true", outer)
	}

	// errors.As finds the type through a double wrap.
	double := fmt.Errorf("level1: %w", fmt.Errorf("level2: %w", &StreamInterruptedError{Err: io.ErrUnexpectedEOF}))
	var found *StreamInterruptedError
	if !errors.As(double, &found) {
		t.Fatalf("errors.As(%v, *StreamInterruptedError) = false, want true", double)
	}
	if !errors.Is(found.Err, io.ErrUnexpectedEOF) {
		t.Errorf("found.Err = %v, want io.ErrUnexpectedEOF", found.Err)
	}
}

// TestChatCompletionStream_MidBodyDisconnectErrorType proves the typed error
// is produced end-to-end: a 200 whose body is truncated mid-stream must reach
// the error channel as a *StreamInterruptedError wrapping the transport cause.
// It mirrors the hijack pattern of the executor's
// TestRunLoopMidBodyDisconnectRetries.
func TestChatCompletionStream_MidBodyDisconnectErrorType(t *testing.T) {
	st := newStreamTest(t)
	defer st.Close()

	st.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DiscoverBackend probes /props and /v1/models before the POST; keep
		// those on the plain handler so only the stream request is hijacked.
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server ResponseWriter does not support Hijack")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return
		}
		defer conn.Close()

		// A valid 200 whose declared Content-Length exceeds the bytes sent:
		// one complete SSE data line, then a partial line with no newline
		// terminator, then FIN. The unterminated line forces the scanner to
		// read again, where net/http surfaces the short body as
		// io.ErrUnexpectedEOF (verified against this Go version; an RST-style
		// close would surface *net.OpError instead and is deliberately
		// avoided). The trailing empty line of a normal SSE frame is
		// deliberately omitted: the loop treats an empty data line as a clean
		// end-of-stream and would miss the transport error entirely.
		head := "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1000\r\n\r\n"
		complete := "data: " + sampleChunkHello + "\n"
		partial := "data: " + sampleChunkWorld // no trailing newline
		if _, err := conn.Write([]byte(head)); err != nil {
			return
		}
		if _, err := conn.Write([]byte(complete)); err != nil {
			return
		}
		if _, err := conn.Write([]byte(partial)); err != nil {
			return
		}
		// FIN: the client hits EOF before the declared Content-Length.
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}))

	outCh, errCh := st.client.ChatCompletionStream(context.Background(), defaultRequest())

	// Drain chunks concurrently. ScanLines emits an unterminated tail as a
	// final token at EOF, so the partial line surfaces as one more chunk and
	// the stream goroutine would block forever on the unbuffered out channel
	// if the test read chunks synchronously.
	var chunks []ChatCompletionChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range outCh {
			chunks = append(chunks, chunk)
		}
	}()

	// The mid-body transport failure must surface on errCh.
	var streamErr error
	select {
	case streamErr = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the stream error on errCh")
	}

	// outCh closes right after the error is sent.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("chunk channel did not close after the stream error")
	}

	// The complete data line must have been delivered before the disconnect.
	if len(chunks) == 0 {
		t.Fatal("no chunks delivered before the disconnect, want at least the complete data line")
	}
	if got := chunks[0].Choices[0].Delta.Content.String(); got != "Hello" {
		t.Errorf("first chunk content = %q, want %q", got, "Hello")
	}

	var sie *StreamInterruptedError
	if !errors.As(streamErr, &sie) {
		t.Fatalf("error = %v (%T), want errors.As to match *StreamInterruptedError", streamErr, streamErr)
	}
	if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want it to wrap io.ErrUnexpectedEOF", streamErr)
	}
	// The rendered message must stay byte-identical to the previous
	// fmt.Errorf("stream interrupted: %w", err).
	if got, want := streamErr.Error(), "stream interrupted: unexpected EOF"; got != want {
		t.Errorf("error message = %q, want %q", got, want)
	}
}
