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
