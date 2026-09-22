package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"late/internal/client"
)

// TestStartStream_SanitizesInterruptedToolRun drives StartStream end-to-end
// against a fake OpenAI-compatible server (the client.Config{BaseURL} approach
// used by the client package's own stream tests) and asserts that a history
// interrupted mid-tool-run is repaired in the outgoing request messages while
// s.History itself is left untouched.
func TestStartStream_SanitizesInterruptedToolRun(t *testing.T) {
	// requestBodies carries the decoded-time JSON body of each
	// /chat/completions request the fake server receives.
	requestBodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer non-200 so the client's backend discovery probe stays
		// "unknown" and does not change request behavior.
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		requestBodies <- body

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n")
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	defer server.Close()

	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("run the tools")},
		{Role: "assistant", ToolCalls: []client.ToolCall{
			{ID: "call_1", Type: "function", Function: client.FunctionCall{Name: "read_file", Arguments: "{}"}},
			{ID: "call_2", Type: "function", Function: client.FunctionCall{Name: "list_dir", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: client.TextContent("first result")},
	}
	// Deep snapshot of the saved history, to prove StartStream does not
	// mutate it (the sanitizer must only repair the request copy).
	snapshot, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal history snapshot: %v", err)
	}

	c := client.NewClient(client.Config{BaseURL: server.URL, Model: "test-model"})
	s := New(c, "", history, "", false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	outCh, errCh := s.StartStream(ctx, nil)
	for range outCh {
	}
	if err, ok := <-errCh; ok && err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	var body []byte
	select {
	case body = <-requestBodies:
	case <-time.After(5 * time.Second):
		t.Fatal("fake server never received the chat completion request")
	}

	var payload struct {
		Messages []client.ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode captured request: %v\nbody: %s", err, body)
	}

	// Expect: user, assistant(2 tool_calls), tool(call_1), tool(call_2 placeholder).
	if len(payload.Messages) != 4 {
		t.Fatalf("outgoing messages = %d, want 4:\n%s", len(payload.Messages), body)
	}
	if got := payload.Messages[1].Role; got != "assistant" || len(payload.Messages[1].ToolCalls) != 2 {
		t.Fatalf("outgoing messages[1] = role %q with %d tool_calls, want assistant with 2",
			got, len(payload.Messages[1].ToolCalls))
	}
	wantIDs := [2]string{"call_1", "call_2"}
	for i, wantID := range wantIDs {
		m := payload.Messages[2+i]
		if m.Role != "tool" {
			t.Fatalf("outgoing messages[%d].Role = %q, want %q", 2+i, m.Role, "tool")
		}
		if m.ToolCallID != wantID {
			t.Fatalf("outgoing messages[%d].ToolCallID = %q, want %q", 2+i, m.ToolCallID, wantID)
		}
	}
	if got := payload.Messages[3].Content.String(); got != interruptedToolResultText {
		t.Fatalf("synthesized tool result content = %q, want %q", got, interruptedToolResultText)
	}

	// The saved history must remain untouched.
	after, err := json.Marshal(s.History)
	if err != nil {
		t.Fatalf("marshal history after StartStream: %v", err)
	}
	if !bytes.Equal(snapshot, after) {
		t.Fatalf("s.History was mutated by StartStream:\nbefore: %s\nafter:  %s", snapshot, after)
	}
	if len(s.History) != 3 {
		t.Fatalf("len(s.History) = %d, want 3 (no synthesized tool result persisted)", len(s.History))
	}
}
