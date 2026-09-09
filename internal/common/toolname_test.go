package common

import (
	"strings"
	"testing"
)

func TestSanitizeToolName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"with-dash_under", "with-dash_under"},
		{"graph-rag:list_files", "graph-rag_list_files"},
		{"plugin:tool", "plugin_tool"},
		{"@scope/pkg:tool", "_scope_pkg_tool"},
		{"spaces and dots.txt", "spaces_and_dots_txt"},
	}
	for _, c := range cases {
		if got := SanitizeToolName(c.in); got != c.want {
			t.Errorf("SanitizeToolName(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Length limit: a very long name must be capped at MaxToolNameLen.
	long := SanitizeToolName(strings.Repeat("a", 200) + ":tool")
	if len(long) > MaxToolNameLen {
		t.Errorf("SanitizeToolName did not cap length: got %d chars", len(long))
	}
	// Cap must never cut a multi-byte rune; every rune here is ASCII so
	// just verify the result is pure [A-Za-z0-9_-].
	for _, r := range long {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			t.Errorf("SanitizeToolName left invalid rune %q in %q", r, long)
		}
	}
}

func TestNamespaceToolName(t *testing.T) {
	if got := NamespaceToolName("alpha", "summarize"); got != "alpha__summarize" {
		t.Errorf("NamespaceToolName = %q, want %q", got, "alpha__summarize")
	}
	// Sanitization happens per part, and the colon separator never leaks.
	if got := NamespaceToolName("my:plugin", "do:thing"); got != "my_plugin__do_thing" {
		t.Errorf("NamespaceToolName(colon parts) = %q", got)
	}
	if got := NamespaceToolName("@scope/pkg", "x"); got != "_scope_pkg__x" {
		t.Errorf("NamespaceToolName(scoped) = %q", got)
	}
}

func TestBareToolName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alpha__summarize", "summarize"},
		{"alpha__beta__tool", "tool"},
		{"legacy:tool", "tool"},
		{"plain", "plain"},
	}
	for _, c := range cases {
		if got := BareToolName(c.in); got != c.want {
			t.Errorf("BareToolName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDeduplicateToolNames(t *testing.T) {
	// Same name twice → second gets a deterministic hash suffix.
	bases := []string{"a__b", "a__b"}
	out := DeduplicateToolNames(bases, nil)
	if out[0] != "a__b" {
		t.Errorf("first occurrence should keep its name, got %q", out[0])
	}
	if out[1] == out[0] || !strings.HasPrefix(out[1], "a__b-") {
		t.Errorf("dup should get a hash suffix, got %q", out[1])
	}
	// Deterministic across calls.
	out2 := DeduplicateToolNames(bases, nil)
	if out2[1] != out[1] {
		t.Errorf("hash suffix must be deterministic: %q vs %q", out[1], out2[1])
	}
	// Pre-seeded names count as taken.
	used := map[string]bool{"a__b": true}
	out3 := DeduplicateToolNames([]string{"a__b"}, used)
	if out3[0] == "a__b" {
		t.Errorf("expected collision with pre-seeded name to be resolved, got %q", out3[0])
	}
	// Distinct names pass through unchanged.
	uniq := DeduplicateToolNames([]string{"x", "y", "z"}, nil)
	if uniq[0] != "x" || uniq[1] != "y" || uniq[2] != "z" {
		t.Errorf("distinct names must pass through, got %v", uniq)
	}
}

func TestDeduplicateToolNames_MaxLenBase(t *testing.T) {
	// A duplicate base at exactly the length limit must not lose its suffix
	// to truncation (which would keep it equal to the taken name forever).
	long := strings.Repeat("a", MaxToolNameLen)
	out := DeduplicateToolNames([]string{long, long}, nil)
	if out[0] != long {
		t.Errorf("first occurrence should keep its name, got %q", out[0])
	}
	if len(out[1]) > MaxToolNameLen {
		t.Errorf("dedup name exceeds limit: %d chars", len(out[1]))
	}
	if out[1] == out[0] {
		t.Errorf("64-char duplicate must still get a distinct name, got %q", out[1])
	}
	// The suffix must contain the hash marker even after truncation.
	if !strings.Contains(out[1], "-") {
		t.Errorf("expected hash suffix in truncated dedup name, got %q", out[1])
	}
	// Deterministic.
	out2 := DeduplicateToolNames([]string{long, long}, nil)
	if out2[1] != out[1] {
		t.Errorf("truncated dedup must stay deterministic: %q vs %q", out[1], out2[1])
	}
}

func TestDeduplicateToolNamesWithIdentities_DistinctHashes(t *testing.T) {
	// Two distinct source identities that sanitize to the same base
	bases := []string{"pkg_a__tool", "pkg_a__tool"}
	identities := []string{"pkg-a:tool", "pkg_a:tool"}

	out := DeduplicateToolNamesWithIdentities(bases, identities, nil)
	if out[0] != "pkg_a__tool" {
		t.Errorf("first tool keeps base name, got %q", out[0])
	}
	if out[1] == out[0] {
		t.Errorf("second tool must be disambiguated, got %q", out[1])
	}

	// Now if the first identity is already taken, second gets its own distinct hash
	used := map[string]bool{"pkg_a__tool": true}
	out1 := DeduplicateToolNamesWithIdentities([]string{"pkg_a__tool"}, []string{"pkg-a:tool"}, used)

	used2 := map[string]bool{"pkg_a__tool": true}
	out2 := DeduplicateToolNamesWithIdentities([]string{"pkg_a__tool"}, []string{"pkg_a:tool"}, used2)

	if out1[0] == out2[0] {
		t.Errorf("different source identities should produce distinct hash suffixes: %q vs %q", out1[0], out2[0])
	}
}

func TestDeduplicateToolNames_MultipleCollisionsEndpointSafe(t *testing.T) {
	// Simulate 5 identical colliding bases
	bases := []string{"collision_tool", "collision_tool", "collision_tool", "collision_tool", "collision_tool"}
	out := DeduplicateToolNames(bases, nil)

	seen := make(map[string]bool)
	for i, name := range out {
		if seen[name] {
			t.Errorf("duplicate name at index %d: %q", i, name)
		}
		seen[name] = true

		if len(name) > MaxToolNameLen {
			t.Errorf("name at index %d exceeds max length %d: %q (len=%d)", i, MaxToolNameLen, name, len(name))
		}

		// Ensure strictly [A-Za-z0-9_-], specifically NO dot '.'
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				t.Errorf("name at index %d contains illegal character %q: %q", i, r, name)
			}
		}
	}
}
