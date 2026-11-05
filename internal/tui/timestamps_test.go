package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"late/internal/client"
	"late/internal/config"
)

// Stamped transcript blocks carry a quiet HH:MM:SS footer from the message's
// recorded receive time when ShowTimestamps is on: inside the prompt card for
// user messages, right-aligned under the reply for assistant messages.
// Messages without a timestamp (legacy history entries) never render one, and
// the stamp never adds a row to a user card.
func TestTranscriptTimestampFooter(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("TIMESTAMPEDPROMPT"), Timestamp: "2025-01-02T03:04:05Z"},
		{Role: "assistant", Content: client.TextContent("TIMESTAMPEDREPLY"), Timestamp: "2025-01-02T03:04:06Z"},
		{Role: "user", Content: client.TextContent("LEGACYPROMPT")},
	}
	m, s := newViewportBenchmarkModel(history)

	off := ansi.Strip(testTranscriptContent(m))
	if strings.Contains(off, "03:04:05") || strings.Contains(off, "03:04:06") {
		t.Fatalf("timestamps rendered while ShowTimestamps is off:\n%s", off)
	}
	offUserHeight := blockHeight(t, s, 0)

	m.ShowTimestamps = true
	on := ansi.Strip(testTranscriptContent(m))
	for _, want := range []string{"03:04:05", "03:04:06"} {
		if !strings.Contains(on, want) {
			t.Fatalf("missing %s stamp:\n%s", want, on)
		}
	}
	if strings.Contains(on, "[03:") {
		t.Fatalf("stamps must not be bracketed prefixes:\n%s", on)
	}
	if got := blockHeight(t, s, 0); got != offUserHeight {
		t.Fatalf("user card height changed with timestamps: %d -> %d", offUserHeight, got)
	}
	// The user stamp sits on the card's last row, right-aligned.
	userRows := blockRows(t, s, 0)
	last := ""
	for _, row := range userRows {
		if r := strings.TrimRight(ansi.Strip(row), " "); r != "" {
			last = r
		}
	}
	if !strings.HasSuffix(last, "03:04:05") {
		t.Fatalf("user stamp not on the card's last row, got %q", last)
	}
	// The reply stamp is its own right-aligned row after the text.
	replyRows := blockRows(t, s, 1)
	replyLast := strings.TrimRight(ansi.Strip(replyRows[len(replyRows)-1]), " ")
	if !strings.HasSuffix(replyLast, "03:04:06") || strings.Contains(replyLast, "TIMESTAMPEDREPLY") {
		t.Fatalf("reply stamp row malformed: %q", replyLast)
	}
	for _, row := range blockRows(t, s, 2) {
		if strings.Contains(ansi.Strip(row), ":") && strings.Contains(ansi.Strip(row), "03:") {
			t.Fatalf("legacy block unexpectedly carries a stamp: %q", ansi.Strip(row))
		}
	}
}

func blockRows(t *testing.T, s *AppState, index int) []string {
	t.Helper()
	block := renderBlockForIndex(s, index)
	if block == nil {
		t.Fatalf("no rendered block for history index %d", index)
	}
	return s.Transcript.rows[block.StartLine : block.EndLine+1]
}

func blockHeight(t *testing.T, s *AppState, index int) int {
	t.Helper()
	return len(blockRows(t, s, index))
}

func renderBlockForIndex(s *AppState, index int) *RenderBlock {
	for i := range s.RenderBlocks {
		if s.RenderBlocks[i].MessageIndex == index {
			return &s.RenderBlocks[i]
		}
	}
	return nil
}

// TestTimestampsTogglePersistsToConfig mirrors the theme-apply persistence
// pattern: the slash command flips the view flag, the in-memory config, and
// the persisted config.json value.
func TestTimestampsTogglePersistsToConfig(t *testing.T) {
	// The /timestamps toggle persists via config.SaveConfig, so isolate the
	// config.json write into a temp dir, mirroring the config package's own
	// test helpers, and drive the slash command with a synthetic Enter
	// keypress. These stanzas are inlined rather than extracted into
	// package-level helpers to keep the shared test-helper namespace free
	// for the other single-feature PRs.
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("APPDATA", configRoot)
	if runtime.GOOS != "windows" {
		t.Setenv("HOME", configRoot)
	}
	// SaveConfig refuses to write into a missing late config dir (the
	// permission tightening fails), so create it up front before the
	// toggle persists anything.
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(userConfigDir, "late"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.SetSize(120, 30)
	if m.ShowTimestamps {
		t.Fatal("ShowTimestamps should default to false when unset in config")
	}

	m.Input.SetValue("/timestamps")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	m = next
	if !m.ShowTimestamps {
		t.Fatal("expected ShowTimestamps to be true after /timestamps")
	}
	if m.ToastMessage != "timestamps on" {
		t.Fatalf("toast = %q, want %q", m.ToastMessage, "timestamps on")
	}
	if !cfg.ShowTimestamps {
		t.Fatal("expected cfg.ShowTimestamps to be true after /timestamps")
	}
	loaded, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !loaded.ShowTimestamps {
		t.Fatal("expected show-timestamps to be persisted as true")
	}

	// Toggle off again.
	m.Input.SetValue("/timestamps")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	m = next
	if m.ShowTimestamps {
		t.Fatal("expected ShowTimestamps to be false after second /timestamps")
	}
	if m.ToastMessage != "timestamps off" {
		t.Fatalf("toast = %q, want %q", m.ToastMessage, "timestamps off")
	}
	if cfg.ShowTimestamps {
		t.Fatal("expected cfg.ShowTimestamps to be false after second /timestamps")
	}
	loaded, err = config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if loaded.ShowTimestamps {
		t.Fatal("expected show-timestamps to be persisted as false")
	}
}

func TestTimestampsCommandListed(t *testing.T) {
	found := false
	for _, cmd := range AvailableCommands {
		if cmd.Name == "/timestamps" {
			found = true
			if cmd.Description == "" {
				t.Fatal("/timestamps should carry a description for the help view")
			}
		}
	}
	if !found {
		t.Fatal("/timestamps missing from AvailableCommands")
	}
}
