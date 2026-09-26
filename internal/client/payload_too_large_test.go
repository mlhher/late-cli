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

// TestFormatError_413IsClassifiedPayloadTooLarge pins the 413 contract: the
// error carries the ErrPayloadTooLarge sentinel (errors.Is, through any wrap
// chain), still recovers the *StatusError details (errors.As), and renders
// the actionable PayloadTooLargeGuidance text instead of the generic
// "API error (413): ..." line.
func TestFormatError_413IsClassifiedPayloadTooLarge(t *testing.T) {
	t.Run("json body keeps sentinel, status details, and guidance", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, `{"error":{"message":"Request body too large","type":"invalid_request_error"}}`)
		}))
		defer server.Close()

		c := NewClient(Config{BaseURL: server.URL})
		_, err := c.ChatCompletion(context.Background(), ChatCompletionRequest{
			Model:    "test-model",
			Messages: []ChatMessage{{Role: "user", Content: TextContent("hi")}},
		})
		if err == nil {
			t.Fatal("expected error for 413 response, got nil")
		}
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("error %v (%T) does not carry the ErrPayloadTooLarge sentinel", err, err)
		}
		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("error %T is not (nor wraps) a *StatusError", err)
		}
		if se.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("StatusCode = %d, want 413", se.StatusCode)
		}
		if want := "Request body too large"; se.Body != want {
			t.Errorf("Body = %q, want %q", se.Body, want)
		}
		if se.Type != "invalid_request_error" {
			t.Errorf("Type = %q, want invalid_request_error", se.Type)
		}
		// The rendered text is the actionable guidance, and the provider's
		// body survives as a parenthetical diagnostic.
		if !strings.Contains(err.Error(), PayloadTooLargeGuidance) {
			t.Errorf("Error() = %q, want it to contain the guidance", err.Error())
		}
		if !strings.Contains(err.Error(), "(provider: Request body too large)") {
			t.Errorf("Error() = %q, want it to keep the provider body for diagnostics", err.Error())
		}
		if strings.Contains(err.Error(), "API error (413)") {
			t.Errorf("Error() = %q, want the actionable guidance instead of the legacy status line", err.Error())
		}
	})

	t.Run("empty body renders exactly the guidance", func(t *testing.T) {
		se := formatErrorStatusError(t, errorResp(http.StatusRequestEntityTooLarge, nil, ""))
		err := error(&PayloadTooLargeError{Status: se})
		if got := err.Error(); got != PayloadTooLargeGuidance {
			t.Errorf("Error() = %q, want exactly the guidance %q", got, PayloadTooLargeGuidance)
		}
	})

	t.Run("sentinel and guidance survive the executor wrap chain", func(t *testing.T) {
		// Mirrors executor.go's "stream error: %w" wrapping: both the
		// errors.Is classification and the guidance text must survive.
		wrapped := fmt.Errorf("stream error: %w", &PayloadTooLargeError{Status: &StatusError{
			StatusCode: http.StatusRequestEntityTooLarge,
			Status:     "413 Payload Too Large",
			Body:       "Request body too large",
		}})
		if !errors.Is(wrapped, ErrPayloadTooLarge) {
			t.Fatal("errors.Is(wrapped, ErrPayloadTooLarge) failed through the wrap chain")
		}
		if !strings.Contains(wrapped.Error(), PayloadTooLargeGuidance) {
			t.Errorf("wrapped.Error() = %q, want it to contain the guidance", wrapped.Error())
		}
		var se *StatusError
		if !errors.As(wrapped, &se) || se.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("errors.As through the wrap chain recovered %v, want the 413 *StatusError", wrapped)
		}
	})
}
