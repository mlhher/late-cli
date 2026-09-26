package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"late/internal/client"
	"late/internal/session"
)

// stubTranscriptSource stands in for *orchestrator.BaseOrchestrator in the
// transcript tests, so no real client or run loop is needed.
type stubTranscriptSource struct {
	id      string
	history []client.ChatMessage
	sess    *session.Session
}

var _ subagentTranscriptSource = (*stubTranscriptSource)(nil)

func (s *stubTranscriptSource) ID() string                    { return s.id }
func (s *stubTranscriptSource) History() []client.ChatMessage { return s.history }
func (s *stubTranscriptSource) Session() *session.Session     { return s.sess }

// TestRenderTranscript covers the pruning rules of renderTranscript: image
// and attachment placeholders, per-role size caps, reasoning suppression, the
// 64 KB total cap with head/tail windows, header fields, and rune-safety.
func TestRenderTranscript(t *testing.T) {
	tests := []struct {
		name    string
		msgs    []client.ChatMessage
		goal    string
		cause   string
		childID string
		check   func(t *testing.T, out string)
	}{
		{
			name: "image and attachment parts are replaced, no base64 leaks",
			msgs: []client.ChatMessage{
				{
					Role: "user",
					Content: client.MessageContent{Parts: []client.ContentPart{
						{Type: client.ContentPartText, Text: "describe this"},
						{Type: client.ContentPartImageURL, ImageURL: &client.ImageURL{URL: "data:image/png;base64,QUJDREVG"}},
						{Type: client.ContentPartType("audio_url")},
					}},
				},
			},
			goal:    "look at the picture",
			cause:   "crashed: boom",
			childID: "coder-subagent-0",
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, "describe this") {
					t.Error("text part should be rendered verbatim")
				}
				if !strings.Contains(out, "[image omitted]") {
					t.Error("image part should collapse to [image omitted]")
				}
				if !strings.Contains(out, "[attachment omitted]") {
					t.Error("non-text non-image part should collapse to [attachment omitted]")
				}
				if strings.Contains(out, "base64") || strings.Contains(out, "data:") || strings.Contains(out, "QUJDREVG") {
					t.Error("no base64 payload or data: URL may appear anywhere in the transcript")
				}
			},
		},
		{
			name: "long tool result is clipped to 2000 bytes with marker",
			msgs: []client.ChatMessage{
				{Role: "tool", ToolCallID: "call-1", Content: client.TextContent(strings.Repeat("a", 2500) + "TAIL")},
			},
			goal:    "run things",
			cause:   "crashed: boom",
			childID: "coder-subagent-1",
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, "tool result for call-1:") {
					t.Error("tool result heading with the ToolCallID is missing")
				}
				if !strings.Contains(out, strings.Repeat("a", 2000)) {
					t.Error("the first 2000 bytes of the tool result should be kept")
				}
				if strings.Contains(out, strings.Repeat("a", 2001)) || strings.Contains(out, "TAIL") {
					t.Error("tool result content beyond 2000 bytes must be dropped")
				}
				if !strings.Contains(out, truncationMarker) {
					t.Error("a truncation marker should be appended")
				}
			},
		},
		{
			name: "long tool call arguments are clipped to 500 bytes",
			msgs: []client.ChatMessage{
				{
					Role: "assistant",
					ToolCalls: []client.ToolCall{{
						Function: client.FunctionCall{Name: "run_query", Arguments: strings.Repeat("b", 800)},
					}},
				},
			},
			goal:    "query things",
			cause:   "crashed: boom",
			childID: "coder-subagent-2",
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, "→ tool run_query(") {
					t.Error("tool call should render as '→ tool name(args)'")
				}
				if !strings.Contains(out, strings.Repeat("b", 500)) {
					t.Error("the first 500 bytes of the arguments should be kept")
				}
				if strings.Contains(out, strings.Repeat("b", 501)) {
					t.Error("tool call arguments beyond 500 bytes must be dropped")
				}
			},
		},
		{
			name: "reasoning content is dropped",
			msgs: []client.ChatMessage{
				{
					Role:             "assistant",
					Content:          client.TextContent("FINAL ANSWER"),
					ReasoningContent: "SECRET-REASONING-CHAIN",
				},
			},
			goal:    "think less",
			cause:   "crashed: boom",
			childID: "coder-subagent-3",
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, "FINAL ANSWER") {
					t.Error("assistant content should be kept")
				}
				if strings.Contains(out, "SECRET-REASONING-CHAIN") {
					t.Error("ReasoningContent must never be rendered")
				}
			},
		},
		{
			name: "total cap keeps header plus first 2 and last 6 messages",
			msgs: func() []client.ChatMessage {
				msgs := make([]client.ChatMessage, 0, 20)
				for i := 0; i < 20; i++ {
					msgs = append(msgs, client.ChatMessage{
						Role:    "assistant",
						Content: client.TextContent(strings.Repeat("x", 4000)),
					})
				}
				return msgs
			}(),
			goal:    "cap test",
			cause:   "cap",
			childID: "cap-child",
			check: func(t *testing.T, out string) {
				for _, want := range []string{"## [1]", "## [2]", "## [15]", "## [20]"} {
					if !strings.Contains(out, want) {
						t.Errorf("expected %q in the pruned transcript (first 2 + last 6 kept)", want)
					}
				}
				for _, gone := range []string{"## [3]", "## [14]"} {
					if strings.Contains(out, gone) {
						t.Errorf("expected %q to be omitted from the pruned transcript", gone)
					}
				}
				if !strings.Contains(out, "[… 12 earlier messages omitted …]") {
					t.Error("expected the omission marker to count the dropped messages")
				}
				if !strings.Contains(out, "- Messages: 20") {
					t.Error("header should still report the original message count")
				}
				if len(out) >= maxTranscriptBytes {
					t.Errorf("pruned transcript should be under the %d byte cap, got %d", maxTranscriptBytes, len(out))
				}
			},
		},
		{
			name: "header carries goal, cause, child id and the pruning notice",
			msgs: []client.ChatMessage{
				{Role: "user", Content: client.TextContent("go")},
				{Role: "assistant", Content: client.TextContent("doing")},
				{Role: "tool", ToolCallID: "call-9", Content: client.TextContent("ok")},
			},
			goal:    "ship the fix",
			cause:   "time budget exhausted (30s)",
			childID: "coder-subagent-7",
			check: func(t *testing.T, out string) {
				for _, want := range []string{
					"# Subagent transcript: coder-subagent-7",
					"- Agent type: coder",
					"- Goal: ship the fix",
					"- Cause: time budget exhausted (30s)",
					"- Messages: 3",
					"Pruned transcript: images, long tool outputs and reasoning are omitted.",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("expected %q in the header block", want)
					}
				}
			},
		},
		{
			name: "multibyte content truncates on rune boundaries",
			msgs: []client.ChatMessage{
				{Role: "user", Content: client.TextContent(strings.Repeat("€", 1000) + "END")},
				{Role: "user", Content: client.TextContent("日本語 status")},
			},
			goal:    "unicode",
			cause:   "crashed: boom",
			childID: "coder-subagent-4",
			check: func(t *testing.T, out string) {
				if !utf8.ValidString(out) {
					t.Fatal("transcript must remain valid UTF-8 after truncation")
				}
				if strings.ContainsRune(out, '\uFFFD') {
					t.Error("truncation must not split a multi-byte rune (no U+FFFD)")
				}
				if !strings.Contains(out, strings.Repeat("€", 666)) {
					t.Error("expected complete € runes up to the 2000 byte user cap (666 × 3 bytes)")
				}
				if strings.Contains(out, strings.Repeat("€", 667)) {
					t.Error("the 667th € rune straddles the cap and must be dropped whole")
				}
				if !strings.Contains(out, "日本語") {
					t.Error("short multibyte content should pass through untouched")
				}
				if !strings.Contains(out, truncationMarker) {
					t.Error("expected a truncation marker after the clipped user message")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderTranscript(tt.msgs, "coder", tt.goal, tt.cause, tt.childID)
			tt.check(t, out)
		})
	}
}

// TestTranscriptDir pins the directory resolution: next to the history file
// (reusing an existing subagents folder instead of nesting one), with the
// user cache directory as the fallback when no session path exists.
func TestTranscriptDir(t *testing.T) {
	tmp := t.TempDir()

	t.Run("plain history path gets a subagents sibling directory", func(t *testing.T) {
		got := transcriptDir(filepath.Join(tmp, "session-1.json"))
		if want := filepath.Join(tmp, "subagents"); got != want {
			t.Errorf("transcriptDir() = %q, want %q", got, want)
		}
	})

	t.Run("history path already inside a subagents dir is used as-is", func(t *testing.T) {
		in := filepath.Join(tmp, "session-1", "subagents", "coder-subagent-0.json")
		got := transcriptDir(in)
		if want := filepath.Join(tmp, "session-1", "subagents"); got != want {
			t.Errorf("transcriptDir() = %q, want %q (no subagents/subagents nesting)", got, want)
		}
	})

	t.Run("empty history path falls back to the cache dir", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))

		got := transcriptDir("")
		if got == "" {
			t.Fatal("transcriptDir(\"\") must never be empty")
		}
		if !strings.HasSuffix(filepath.ToSlash(got), "late/subagent-transcripts") {
			t.Errorf("transcriptDir(\"\") = %q, want a path ending in late/subagent-transcripts", got)
		}
	})
}

// TestWriteSubagentTranscript exercises the writer end-to-end with a stub
// source: location, permissions, and header contents.
func TestWriteSubagentTranscript(t *testing.T) {
	t.Run("places transcript next to the persisted child history", func(t *testing.T) {
		tmp := t.TempDir()
		historyPath := filepath.Join(tmp, "session-1", "subagents", "coder-subagent-0.json")
		src := &stubTranscriptSource{
			id:   "coder-subagent-0",
			sess: session.New(nil, historyPath, nil, "", false),
			history: []client.ChatMessage{
				{Role: "user", Content: client.TextContent("refactor the parser")},
				{Role: "assistant", Content: client.TextContent("crashed mid-edit")},
			},
		}

		path, err := writeSubagentTranscript(src, "coder", "refactor the parser", "crashed: boom")
		if err != nil {
			t.Fatalf("writeSubagentTranscript() error = %v", err)
		}
		wantPath := filepath.Join(tmp, "session-1", "subagents", "coder-subagent-0-transcript.md")
		if path != wantPath {
			t.Errorf("writeSubagentTranscript() = %q, want %q", path, wantPath)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("transcript file not written: %v", err)
		}
		out := string(data)
		for _, want := range []string{"coder-subagent-0", "coder", "refactor the parser", "crashed: boom", "crashed mid-edit"} {
			if !strings.Contains(out, want) {
				t.Errorf("transcript should contain %q", want)
			}
		}

		if fi, err := os.Stat(path); err != nil {
			t.Fatalf("stat transcript: %v", err)
		} else if fi.Mode().Perm() != 0o600 {
			t.Errorf("transcript file mode = %v, want -rw------- (0600)", fi.Mode().Perm())
		}
		if fi, err := os.Stat(filepath.Dir(path)); err != nil {
			t.Fatalf("stat transcript dir: %v", err)
		} else if fi.Mode().Perm() != 0o700 {
			t.Errorf("transcript dir mode = %v, want rwx------ (0700)", fi.Mode().Perm())
		}
	})

	t.Run("falls back to the cache dir without a session path", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))

		src := &stubTranscriptSource{
			id:   "coder-subagent-1",
			sess: session.New(nil, "", nil, "", false),
			history: []client.ChatMessage{
				{Role: "assistant", Content: client.TextContent("was interrupted")},
			},
		}

		path, err := writeSubagentTranscript(src, "coder", "goal", "cancelled or killed by the user")
		if err != nil {
			t.Fatalf("writeSubagentTranscript() error = %v", err)
		}
		if !filepath.IsAbs(path) {
			t.Errorf("writeSubagentTranscript() should return an absolute path, got %q", path)
		}
		if !strings.HasSuffix(filepath.ToSlash(path), "late/subagent-transcripts/coder-subagent-1-transcript.md") {
			t.Errorf("writeSubagentTranscript() = %q, want the cache fallback location", path)
		}
		if data, err := os.ReadFile(path); err != nil {
			t.Fatalf("transcript file not written: %v", err)
		} else if !strings.Contains(string(data), "was interrupted") {
			t.Error("transcript should contain the child history")
		}
	})
}

// TestLastActionPreview pins the parent-facing summary of what the subagent
// was doing when it stopped.
func TestLastActionPreview(t *testing.T) {
	tests := []struct {
		name  string
		msgs  []client.ChatMessage
		limit int
		want  string
	}{
		{
			name:  "empty history",
			msgs:  nil,
			limit: 500,
			want:  "no actions recorded",
		},
		{
			name: "no assistant messages",
			msgs: []client.ChatMessage{
				{Role: "user", Content: client.TextContent("goal")},
				{Role: "tool", ToolCallID: "call-1", Content: client.TextContent("result")},
			},
			limit: 500,
			want:  "no actions recorded",
		},
		{
			name: "prefers the last assistant content",
			msgs: []client.ChatMessage{
				{Role: "assistant", Content: client.TextContent("thinking")},
				{Role: "tool", ToolCallID: "call-1", Content: client.TextContent("42")},
				{Role: "assistant", Content: client.TextContent("final fix applied")},
			},
			limit: 500,
			want:  "final fix applied",
		},
		{
			name: "falls back to the last tool call when content is empty",
			msgs: []client.ChatMessage{
				{Role: "assistant", Content: client.TextContent("earlier words")},
				{
					Role: "assistant",
					ToolCalls: []client.ToolCall{{
						Function: client.FunctionCall{Name: "bash", Arguments: `{"cmd":"ls -la"}`},
					}},
				},
			},
			limit: 500,
			want:  `→ tool bash({"cmd":"ls -la"})`,
		},
		{
			name: "respects the limit",
			msgs: []client.ChatMessage{
				{Role: "assistant", Content: client.TextContent(strings.Repeat("z", 800))},
			},
			limit: 100,
			want:  strings.Repeat("z", 100) + truncationMarker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastActionPreview(tt.msgs, tt.limit); got != tt.want {
				t.Errorf("lastActionPreview() = %q, want %q", got, tt.want)
			}
		})
	}
}
