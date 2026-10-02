package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contextExceededStatusError builds the StatusError formatError would produce
// for a response, for direct classification tests.
func contextExceededStatusError(status int, body string, code any) *StatusError {
	return &StatusError{StatusCode: status, Status: fmt.Sprintf("%d status", status), Body: body, Code: code}
}

// httptestServeError serves one static non-200 JSON error response; the
// client turns it into whatever formatError classifies it as.
func httptestServeError(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

// TestStatusReportsContextExhaustion is the classification table: statuses ×
// bodies × codes. A body naming context exhaustion classifies only on the
// statuses providers actually use for it (400, 413, 429); a bare status
// without the body never classifies (a 400 says nothing about WHY the request
// was rejected); and the one status-level special case is a 429 whose error
// CODE is OpenAI's "context_length_exceeded" string (throttled status,
// body-level meaning).
func TestStatusReportsContextExhaustion(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   any
		want   bool
	}{
		// Body matches on the qualifying statuses.
		{"400 openai code", 400, `{"error":{"code":"context_length_exceeded"}}`, nil, true},
		{"400 openai message", 400, "This model's maximum context length is 8192 tokens", nil, true},
		{"400 anthropic prompt too long", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, nil, true},
		{"400 gemini token count", 400, "input token count (571785) exceeds the maximum number of tokens allowed (1048576)", nil, true},
		{"400 claude style", 400, "Your input length exceeds the model's window", nil, true},
		{"413 token verdict", 413, "the request exceeds the available context size", nil, true},
		{"429 code verdict", 429, "rate limit exceeded", "context_length_exceeded", true},
		{"429 token-phrase body is a throttle, not exhaustion", 429, "request failed: too many tokens in the prompt", nil, false},
		// Case-insensitivity: the match must survive uppercase providers.
		{"400 uppercase", 400, "CONTEXT WINDOW exceeded", nil, true},
		// Body match on a non-qualifying status: the body alone is not enough
		// (e.g. a 500 whose incident page happens to mention the context).
		{"500 body match does not classify", 500, "maximum context length exceeded", nil, false},
		// Status without a naming body: never classifies.
		{"400 plain body", 400, "invalid tool arguments", nil, false},
		{"400 empty body", 400, "", nil, false},
		{"413 plain body", 413, "Request body too large", nil, false},
		{"429 throttle", 429, "rate limit exceeded", nil, false},
		{"429 unrelated code", 429, "rate limit exceeded", "insufficient_quota", false},
		{"401 body match", 401, "context window", nil, false},
		// False-positive resistance on the throttle status: a 429 naming
		// token phrases is a rate/quota bucket (retryable, Retry-After),
		// not deterministic exhaustion — misclassifying it would skip
		// retries that would have succeeded. The 429 stays recognizable
		// via OpenAI's context_length_exceeded code only.
		{"429 throttle quoting a token statistic", 429, "rate limit exceeded: you sent too many tokens per minute (TPM cap 30000)", nil, false},
		// Accepted narrow 400 surface: a 400 body naming the window is
		// treated as the exhaustion verdict even in principle-unrelated
		// phrasings. Body matching on 400/413 is deliberately substring-
		// level (provider wordings vary too much for anything smarter);
		// the real bad-body transient class (z.ai-style read failures)
		// never mentions the window, so its retries stay intact.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusReportsContextExhaustion(tt.status, tt.body, tt.code); got != tt.want {
				t.Errorf("statusReportsContextExhaustion(%d, %q, %v) = %v, want %v", tt.status, tt.body, tt.code, got, tt.want)
			}
		})
	}
}

// TestEveryPatternClassifies pins the full provider-pattern list: each
// documented signature actually matches, so a future pattern removal or typo
// (e.g. a pattern that no body ever hits) cannot ship silently.
func TestEveryPatternClassifies(t *testing.T) {
	if len(contextExceededPatterns) == 0 {
		t.Fatal("contextExceededPatterns is empty: the classification would never fire")
	}
	for _, pattern := range contextExceededPatterns {
		if !isContextExceededBody("prefix " + pattern + " suffix") {
			t.Errorf("pattern %q does not match itself", pattern)
		}
		if isContextExceededBody("this body mentions nothing relevant") {
			t.Error("an unrelated body must not classify")
		}
	}
}

// TestFormatError_ClassifiesContextExhaustion drives the real formatError
// path: a provider response whose status AND body name the token limit comes
// back as *ContextExceededError carrying the ErrContextExceeded sentinel, the
// actionable guidance text, and the underlying *StatusError for errors.As —
// all preserved through an executor-style "stream error: %w" wrap chain.
func TestFormatError_ClassifiesContextExhaustion(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"400 context_length_exceeded", http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`},
		{"413 token verdict", http.StatusRequestEntityTooLarge, `{"error":{"message":"request body: too many tokens for the configured context size"}}`},
		{"429 code verdict", http.StatusTooManyRequests, `{"error":{"message":"rate limited","code":"context_length_exceeded"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptestServeError(t, tt.status, tt.body)
			defer server.Close()

			c := NewClient(Config{BaseURL: server.URL})
			_, err := c.ChatCompletion(context.Background(), ChatCompletionRequest{
				Model:    "test-model",
				Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrContextExceeded) {
				t.Fatalf("error %v (%T) does not carry the ErrContextExceeded sentinel", err, err)
			}
			var ce *ContextExceededError
			if !errors.As(err, &ce) {
				t.Fatalf("error %T is not a *ContextExceededError", err)
			}
			if ce.Status == nil || ce.Status.StatusCode != tt.status {
				t.Fatalf("Status = %+v, want the %d response", ce.Status, tt.status)
			}
			if !strings.Contains(err.Error(), ContextExceededGuidance) {
				t.Errorf("Error() = %q, want it to contain the guidance", err.Error())
			}
			if !strings.Contains(err.Error(), "start a new session") {
				t.Errorf("Error() = %q, want it to name the manual recovery step", err.Error())
			}
			// The provider body survives as a diagnostic parenthetical.
			if !strings.Contains(err.Error(), "(provider: ") {
				t.Errorf("Error() = %q, want the provider body parenthetical", err.Error())
			}

			// Through the executor's wrap chain the sentinel and guidance
			// survive intact.
			wrapped := fmt.Errorf("stream error: %w", err)
			if !errors.Is(wrapped, ErrContextExceeded) {
				t.Fatal("sentinel lost through the wrap chain")
			}
			if !strings.Contains(wrapped.Error(), ContextExceededGuidance) {
				t.Errorf("wrapped.Error() = %q, want the guidance", wrapped.Error())
			}
		})
	}

	t.Run("413 without the body names tokens stays fail-fast", func(t *testing.T) {
		// Upstream main has no byte-level 413 sentinel (it ships with the
		// compaction PR): a 413 whose body does NOT name the token limit
		// falls through to the generic StatusError — but it is still a
		// fail-fast classification at the executor layer, never retried.
		se := formatErrorStatusError(t, errorResp(http.StatusRequestEntityTooLarge, nil, "Request body too large"))
		if strings.Contains(strings.ToLower(se.Body), "context") || strings.Contains(strings.ToLower(se.Body), "token") {
			t.Fatalf("test body must not name the token limit: %q", se.Body)
		}
	})
}

// TestContextExceededErrorUnwrap pins the unwrap contract: the sentinel for
// errors.Is classification AND the StatusError for errors.As recovery.
func TestContextExceededErrorUnwrap(t *testing.T) {
	se := contextExceededStatusError(400, "maximum context length", nil)
	ce := &ContextExceededError{Status: se, Reason: "http 400"}
	if !errors.Is(ce, ErrContextExceeded) {
		t.Error("errors.Is(ce, ErrContextExceeded) = false, want true")
	}
	var got *StatusError
	if !errors.As(ce, &got) || got.StatusCode != 400 {
		t.Errorf("errors.As recovered %v, want the wrapped 400 StatusError", got)
	}
	// Empty provider body: the guidance IS the text.
	bare := &ContextExceededError{Status: &StatusError{StatusCode: 400}}
	if bare.Error() != ContextExceededGuidance {
		t.Errorf("Error() = %q, want exactly the guidance", bare.Error())
	}
}

// TestSetContextSizeGuardsDiscovery pins the B5 invariant: an explicit
// context-size-tokens declaration survives DiscoverBackend AND
// RefreshContextSize, which would otherwise overwrite it with a probe
// result; a non-positive SetContextSize is ignored entirely.
func TestSetContextSizeGuardsDiscovery(t *testing.T) {
	// The httptest server reports llama.cpp-shaped /props with a DIFFERENT
	// window: if discovery clobbered the explicit value the test fails.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/props") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":2048},"modalities":{"vision":false}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})
	c.SetContextSize(32768)
	if !c.IsContextSizeExplicit() {
		t.Fatal("SetContextSize must mark the value explicit")
	}
	if got := c.ContextSize(); got != 32768 {
		t.Fatalf("ContextSize() = %d, want the explicit 32768", got)
	}
	if backend := c.DiscoverBackend(context.Background()); backend == BackendUnknown {
		t.Fatal("discovery must still identify the backend")
	}
	if got := c.ContextSize(); got != 32768 {
		t.Fatalf("ContextSize() after DiscoverBackend = %d, want the explicit 32768 (discovery must not clobber)", got)
	}
	c.RefreshContextSize(context.Background())
	if got := c.ContextSize(); got != 32768 {
		t.Fatalf("ContextSize() after RefreshContextSize = %d, want the explicit 32768 (refresh must not clobber)", got)
	}

	// A discovery WITHOUT an explicit declaration still takes the probe
	// value (the override must not disable auto-discovery for everyone).
	plain := NewClient(Config{BaseURL: server.URL})
	plain.DiscoverBackend(context.Background())
	if got := plain.ContextSize(); got != 2048 {
		t.Fatalf("ContextSize() = %d, want the discovered 2048 when nothing is declared", got)
	}

	// Non-positive values are ignored: they cannot describe a window, and
	// they must not clobber an existing value either.
	c.SetContextSize(0)
	if !c.IsContextSizeExplicit() || c.ContextSize() != 32768 {
		t.Fatalf("SetContextSize(0) must be a no-op, got explicit=%v size=%d", c.IsContextSizeExplicit(), c.ContextSize())
	}
}
