package executor

import (
	"late/internal/client"
	"late/internal/session"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegisterExternalTools_SanitizesNames verifies that config keys
// containing characters OpenAI-compatible endpoints reject (spaces, dots)
// are registered under their sanitized name, and that blank entries are
// skipped.
func TestRegisterExternalTools_SanitizesNames(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	RegisterExternalTools(sess.Registry, map[string]string{
		"my.tool name": "/usr/bin/alpha",
		"My.Tool Name": "/usr/bin/beta",
		"   ":          "/usr/bin/blank-key",
		"":             "/usr/bin/empty-key",
		"empty path":   "",
	})

	// The dotted/spaced key must be exposed under its sanitized name,
	// exactly once.
	count := 0
	for _, tt := range sess.Registry.All() {
		if tt.Name() == "my_tool_name" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected sanitized name 'my_tool_name' exactly once, got %d", count)
	}
	got := sess.Registry.Get("my_tool_name")
	if got == nil {
		t.Fatal("expected tool 'my_tool_name' to be registered")
	}
	if got.Name() != "my_tool_name" {
		t.Errorf("expected Name() 'my_tool_name', got %q", got.Name())
	}
	if !strings.Contains(got.Description(), "/usr/bin/alpha") {
		t.Errorf("expected alpha binary, got %q", got.Description())
	}

	// Sanitization preserves case, so the other key maps to a distinct
	// sanitized name.
	if sess.Registry.Get("My_Tool_Name") == nil {
		t.Error("expected tool 'My_Tool_Name' to be registered")
	}

	// Only the two non-blank keys may have been registered.
	if len(sess.Registry.All()) != 2 {
		t.Errorf("expected exactly 2 tools registered, got %d", len(sess.Registry.All()))
	}

	// Raw keys and blank entries must never reach the registry.
	for _, tt := range sess.Registry.All() {
		switch tt.Name() {
		case "my.tool name", "My.Tool Name", "empty path", "":
			t.Errorf("unexpected raw/blank tool name %q", tt.Name())
		}
	}
}

// TestRegisterExternalTools_DuplicateNameFirstWins verifies that a later
// registration whose key sanitizes to a name already in the registry is
// skipped instead of silently overwriting the first binary.
func TestRegisterExternalTools_DuplicateNameFirstWins(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	RegisterExternalTools(sess.Registry, map[string]string{"my.tool name": "/usr/bin/alpha"})
	// This key sanitizes to the same 'my_tool_name' as above.
	RegisterExternalTools(sess.Registry, map[string]string{"my_tool_name": "/usr/bin/omega"})

	got := sess.Registry.Get("my_tool_name")
	if got == nil {
		t.Fatal("expected tool 'my_tool_name' to be registered")
	}
	if !strings.Contains(got.Description(), "/usr/bin/alpha") {
		t.Errorf("first-registered binary must be kept, got %q", got.Description())
	}
	if strings.Contains(got.Description(), "/usr/bin/omega") {
		t.Errorf("later duplicate must not overwrite the first binary, got %q", got.Description())
	}
}
