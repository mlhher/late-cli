package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestTruncateWithEllipsisPlainStringCompat pins the pre-existing plain-text
// contract the status bar and info bar rely on: visible width is capped, the
// ellipsis is appended, and short strings pass through untouched.
func TestTruncateWithEllipsisPlainStringCompat(t *testing.T) {
	m := Model{}

	if got := m.truncateWithEllipsis("short", 40); got != "short" {
		t.Fatalf("short string mangled: %q", got)
	}
	long := strings.Repeat("x", 200)
	got := m.truncateWithEllipsis(long, 40)
	if ansi.StringWidth(got) > 40 {
		t.Fatalf("width = %d, want <= 40", ansi.StringWidth(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated string %q must end with the ellipsis", got)
	}
	// The visible prefix is exactly the width-3 leading characters.
	if got != strings.Repeat("x", 37)+"..." {
		t.Fatalf("unexpected truncation result: %q", got)
	}
}

// TestTruncateWithEllipsisANSISafe pins the escape-sequence fix: the old
// implementation walked raw runes, so a lipgloss-styled string (which the
// status bar truncates AFTER styling) had its escape bytes counted as content
// and could be cut INSIDE a CSI sequence, emitting garbage like "ESC[3..."
// into the rendered bar. ansi.Truncate keeps sequences intact and measures
// the same visible width lipgloss.Width does.
func TestTruncateWithEllipsisANSISafe(t *testing.T) {
	m := Model{}

	styled := "\x1b[31m" + strings.Repeat("x", 50) + "\x1b[0m"
	got := m.truncateWithEllipsis(styled, 10)

	if w := ansi.StringWidth(got); w > 10 {
		t.Fatalf("visible width = %d, want <= 10", w)
	}
	plain := ansi.Strip(got)
	if !strings.HasSuffix(plain, "...") {
		t.Fatalf("truncated text %q must end with the ellipsis", plain)
	}
	// No CSI fragments may survive in the visible text: a cut inside an
	// escape sequence leaves pieces like "[3" behind.
	if strings.ContainsAny(plain, "\x1b[") {
		t.Fatalf("escape sequence cut mid-stream, visible text = %q", plain)
	}
	// Exactly width-3 visible characters before the ellipsis.
	if plain != strings.Repeat("x", 7)+"..." {
		t.Fatalf("visible content = %q, want %q", plain, strings.Repeat("x", 7)+"...")
	}
}

// TestTruncateWithEllipsisANSIUnderFlow ensures a styled string that fits is
// returned untouched (styling preserved, no ellipsis injected).
func TestTruncateWithEllipsisANSIUnderFlow(t *testing.T) {
	m := Model{}

	styled := "\x1b[31mred\x1b[0m"
	if got := m.truncateWithEllipsis(styled, 40); got != styled {
		t.Fatalf("styled under-limit string mangled: %q", got)
	}
}
