package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestResolveRenderTheme_EmptyReturnsBase: with no overrides, the merged
// output is byte-for-byte equal to the bundled LateTheme (no wasted
// copy, no schema-pollution markers).
func TestResolveRenderTheme_EmptyReturnsBase(t *testing.T) {
	got, err := ResolveRenderTheme("", nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != string(LateTheme) {
		t.Fatalf("expected byte-equal base theme when overrides are nil")
	}
}

// TestResolveRenderTheme_MergesGlamourKeys: a top-level glamour override
// is woven into the merged result AND the theme-name marker is present
// downstream consumers (the TUI/helpers) can read.
func TestResolveRenderTheme_MergesGlamourKeys(t *testing.T) {
	mod := map[string]any{
		"document": map[string]any{
			"color": "#FF0000",
		},
	}
	got, err := ResolveRenderTheme("p:red", mod, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "#FF0000") {
		t.Fatal("expected merged colour in output")
	}
	if !strings.Contains(s, `"_late_theme_name":"p:red"`) &&
		!strings.Contains(s, `_late_theme_name`) {
		t.Fatal("expected theme name marker in output")
	}

	// Sanity: roundtrip through json.Unmarshal to confirm valid JSON, so
	// we don't accidentally feed glamour invalid bytes.
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("merged theme is not valid json: %v", err)
	}
}

// TestResolveRenderTheme_PaletteAttached: a palette is staged under the
// specialized `_late_palette` key so it doesn't fight glamour's schema
// but downstream consumers can introspect it.
func TestResolveRenderTheme_PaletteAttached(t *testing.T) {
	palette := map[string]string{
		"bg":     "#000000",
		"accent": "#E5A85C",
	}
	got, err := ResolveRenderTheme("plugin:ocean", nil, palette)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "_late_palette") {
		t.Fatal("expected _late_palette marker")
	}
	if !strings.Contains(s, "E5A85C") {
		t.Fatal("expected palette colour in output")
	}
}
