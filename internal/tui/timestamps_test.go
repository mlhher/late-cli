package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"late/internal/client"
	"late/internal/config"
)

// Rendered transcript blocks are prefixed with [HH:MM:SS] from the message's
// recorded receive time when ShowTimestamps is on. Messages without a
// timestamp (legacy history entries) always render unprefixed.
func TestTranscriptTimestampPrefix(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("TIMESTAMPEDPROMPT"), Timestamp: "2025-01-02T03:04:05Z"},
		{Role: "assistant", Content: client.TextContent("TIMESTAMPEDREPLY"), Timestamp: "2025-01-02T03:04:06Z"},
		{Role: "user", Content: client.TextContent("LEGACYPROMPT")},
	}
	m, s := newViewportBenchmarkModel(history)

	// Off (default): no prefixes anywhere.
	if content := testTranscriptContent(m); strings.Contains(ansi.Strip(content), "[03:04:05]") || strings.Contains(ansi.Strip(content), "[03:04:06]") {
		t.Fatalf("timestamps rendered while ShowTimestamps is off:\n%s", ansi.Strip(content))
	}

	// On: the stamped user and assistant blocks carry their prefixes; the
	// legacy block stays unprefixed.
	m.ShowTimestamps = true
	content := ansi.Strip(testTranscriptContent(m))
	if !strings.Contains(content, "[03:04:05]") {
		t.Fatalf("missing [03:04:05] user prefix:\n%s", content)
	}
	if !strings.Contains(content, "[03:04:06]") {
		t.Fatalf("missing [03:04:06] assistant prefix:\n%s", content)
	}
	assertBlockPrefix(t, s, 0, "[03:04:05]")
	assertBlockPrefix(t, s, 1, "[03:04:06]")
	assertBlockWithoutPrefix(t, s, 2)
}

// assertBlockPrefix checks that the first row of the block for the history
// message at index starts with the expected [HH:MM:SS] prefix.
func assertBlockPrefix(t *testing.T, s *AppState, index int, want string) {
	t.Helper()
	block := renderBlockForIndex(s, index)
	if block == nil {
		t.Fatalf("no rendered block for history index %d", index)
	}
	first := strings.TrimSpace(ansi.Strip(s.Transcript.rows[block.StartLine]))
	if !strings.HasPrefix(first, want) {
		t.Fatalf("block %d starts with %q, want prefix %q", index, first, want)
	}
}

// assertBlockWithoutPrefix checks that no row of the block for the history
// message at index carries a timestamp prefix ([HH:MM:SS] is 10 characters).
func assertBlockWithoutPrefix(t *testing.T, s *AppState, index int) {
	t.Helper()
	block := renderBlockForIndex(s, index)
	if block == nil {
		t.Fatalf("no rendered block for history index %d", index)
	}
	for _, row := range s.Transcript.rows[block.StartLine : block.EndLine+1] {
		trimmed := strings.TrimSpace(ansi.Strip(row))
		if len(trimmed) == 10 && strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			t.Fatalf("legacy block %d unexpectedly carries timestamp prefix %q", index, trimmed)
		}
	}
}

func renderBlockForIndex(s *AppState, index int) *RenderBlock {
	for i := range s.RenderBlocks {
		if s.RenderBlocks[i].MessageIndex == index {
			return &s.RenderBlocks[i]
		}
	}
	return nil
}

// TestTimestampsTogglePersistsToConfig mirrors the /infobar toggle test: the
// slash command flips the view flag, the in-memory config, and the persisted
// config.json value.
func TestTimestampsTogglePersistsToConfig(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	// SaveConfig refuses to write into a missing late config dir (the
	// permission tightening fails), so create it up front the way the
	// /infobar persistence test does.
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
	m = pressEnter(t, m)
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
	m = pressEnter(t, m)
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
