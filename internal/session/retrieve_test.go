package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/compaction"
)

// newRetrieveTestSession builds a session over a fake OpenAI-compatible
// stream server (the session_startstream_test.go pattern) plus a compaction
// pipeline over a scripted decisions server, and returns everything the
// retrieval tests need: the captured chat request bodies channel and the
// pipeline.
func newRetrieveTestSession(t *testing.T, history []client.ChatMessage, historyPath string, answers map[string]float64, answerStatus int) (*Session, *compaction.Pipeline, *compaction.Store, <-chan []byte) {
	t.Helper()

	requestBodies := make(chan []byte, 1)
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(chatServer.Close)

	decisionsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answerStatus)
		// The decisions protocol answers with noul objects, not bare numbers.
		wire := make(map[string]any, len(answers))
		for ref, score := range answers {
			wire[ref] = map[string]any{"type": "noul", "noul": score}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": wire,
			"usage":   map[string]int{"input_tokens": 1},
		})
	}))
	t.Cleanup(decisionsServer.Close)

	backend := compaction.ResolvedBackend{
		Backend: compaction.Backend{Name: "test", URL: decisionsServer.URL, Model: "jev-latest"},
		APIKey:  "k",
	}
	pipeline := compaction.NewPipeline(backend, "k", nil, compaction.PipelineOptions{})

	store := compaction.NewStore()
	store.PutRecord(compaction.Record{ID: "rec-a", Text: "stored original A", Summary: "a", Tokens: 5})
	store.PutRecord(compaction.Record{ID: "rec-b", Text: "stored original B", Summary: "b", Tokens: 5})

	c := client.NewClient(client.Config{BaseURL: chatServer.URL, Model: "test-model"})
	return New(c, historyPath, history, "", false), pipeline, store, requestBodies
}

// TestInjectRetrievedStagesWorkAreaBlock: the retrieved block is appended
// LAST in the outgoing messages (work area, after everything), the session
// history is untouched by both the injection and the request build, and
// nothing reaches the history file on disk.
func TestInjectRetrievedStagesWorkAreaBlock(t *testing.T) {
	tmp := t.TempDir()
	historyPath := filepath.Join(tmp, "history.json")
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("please fix the login bug")},
	}
	if err := SaveHistory(historyPath, history); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	// rec-a scores 0.9 (injected), rec-b scores 0.3 (skipped at the default
	// threshold 0.5).
	s, pipeline, store, requestBodies := newRetrieveTestSession(t, history, historyPath,
		map[string]float64{"rec-a": 0.9, "rec-b": 0.3}, http.StatusOK)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	n, err := s.InjectRetrieved(ctx, pipeline, store, 0, 0, 0)
	if err != nil {
		t.Fatalf("InjectRetrieved() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("InjectRetrieved() = %d records, want 1 (only rec-a clears the threshold)", n)
	}

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

	// Expect: the user message, then the retrieved block LAST.
	if len(payload.Messages) != 2 {
		t.Fatalf("outgoing messages = %d, want 2:\n%s", len(payload.Messages), body)
	}
	last := payload.Messages[len(payload.Messages)-1]
	if last.Role != retrievedContextRole {
		t.Errorf("last outgoing message role = %q, want %q", last.Role, retrievedContextRole)
	}
	content := last.Content.String()
	if !strings.HasPrefix(content, compaction.RetrievedContextHeader) {
		t.Errorf("last outgoing message = %q, want the retrieved-context header", content)
	}
	if !strings.Contains(content, "stored original A") {
		t.Errorf("block does not carry the injected record text: %q", content)
	}
	if strings.Contains(content, "stored original B") {
		t.Errorf("block carries the below-threshold record: %q", content)
	}

	// The ephemeral guarantee: history in memory AND on disk are untouched.
	after, err := json.Marshal(s.History)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("s.History was mutated by the retrieval path:\nbefore: %s\nafter:  %s", before, after)
	}
	onDisk, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, onDisk) {
		t.Fatalf("the history file was rewritten by the retrieval path:\nbefore: %s\nafter:  %s", saved, onDisk)
	}
}

// TestInjectRetrievedClearsStaleBlock: every call overwrites the staged
// block — an empty selection (empty store) clears it, so no stale block can
// ride into the next request.
func TestInjectRetrievedClearsStaleBlock(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("please fix the login bug")},
	}
	s, pipeline, store, requestBodies := newRetrieveTestSession(t, history, "",
		map[string]float64{"rec-a": 0.9, "rec-b": 0.3}, http.StatusOK)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if n, err := s.InjectRetrieved(ctx, pipeline, store, 0, 0, 0); err != nil || n != 1 {
		t.Fatalf("first InjectRetrieved() = (%d, %v), want (1, nil)", n, err)
	}
	// A second injection against an empty store finds nothing relevant.
	if n, err := s.InjectRetrieved(ctx, pipeline, compaction.NewStore(), 0, 0, 0); err != nil || n != 0 {
		t.Fatalf("second InjectRetrieved() = (%d, %v), want (0, nil)", n, err)
	}

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
	if len(payload.Messages) != 1 {
		t.Fatalf("outgoing messages = %d, want 1 (the cleared block must not ride along):\n%s",
			len(payload.Messages), body)
	}
	if strings.Contains(payload.Messages[0].Content.String(), "stored original") {
		t.Fatalf("the cleared block leaked into the request: %s", body)
	}
}

// TestInjectRetrievedScoringErrorClearsStagedBlock: a scoring failure stages
// nothing (and clears anything staged earlier) — an unreliable ranking must
// not stuff the work area.
func TestInjectRetrievedScoringErrorClearsStagedBlock(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("please fix the login bug")},
	}
	s, pipeline, store, requestBodies := newRetrieveTestSession(t, history, "",
		nil, http.StatusUnauthorized) // 401: never retried, fails fast

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if n, err := s.InjectRetrieved(ctx, pipeline, store, 0, 0, 0); err == nil {
		t.Fatalf("InjectRetrieved() error = nil, want the auth-class failure surfaced")
	} else if n != 0 {
		t.Fatalf("InjectRetrieved() = %d records, want 0 on a scoring failure", n)
	}

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
	for _, m := range payload.Messages {
		if strings.Contains(m.Content.String(), compaction.RetrievedContextHeader) {
			t.Fatalf("a failed retrieval staged no block, yet the request carries one: %s", body)
		}
	}
}
