package session

import (
	"testing"
	"time"

	"late/internal/client"
)

// Add* helpers stamp the message receive time (RFC3339) in one shared path
// (appendMessage) so the TUI transcript can prefix blocks with [HH:MM:SS].
func TestAddUserMessageStampsRFC3339Timestamp(t *testing.T) {
	s := New(nil, "", nil, "", false) // no history path: nothing is persisted
	if err := s.AddUserMessage("Hello"); err != nil {
		t.Fatalf("AddUserMessage() error = %v", err)
	}
	ts := s.History[0].Timestamp
	if ts == "" {
		t.Fatal("expected AddUserMessage to stamp a non-empty Timestamp")
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("Timestamp %q is not RFC3339: %v", ts, err)
	}
	if elapsed := time.Since(parsed); elapsed < 0 || elapsed > time.Minute {
		t.Fatalf("Timestamp %q is not a recent receive time (elapsed %v)", ts, elapsed)
	}
}

func TestAddMessageStampsEmptyAndPreservesCallerTimestamp(t *testing.T) {
	s := New(nil, "", nil, "", false)

	// An unstamped message added via AddMessage is stamped like any other.
	if err := s.AddMessage(client.ChatMessage{Role: "tool", Content: client.TextContent("stamped")}); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if s.History[0].Timestamp == "" {
		t.Fatal("expected AddMessage to stamp an empty Timestamp")
	}

	// A caller supplied timestamp survives the round trip.
	fixed := "2024-05-06T07:08:09Z"
	if err := s.AddMessage(client.ChatMessage{Role: "assistant", Content: client.TextContent("preserved"), Timestamp: fixed}); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if got := s.History[1].Timestamp; got != fixed {
		t.Fatalf("AddMessage overwrote caller timestamp: got %q, want %q", got, fixed)
	}
}
