package compaction

import (
	"strings"
	"testing"
)

// TestContentID_ReferenceVectors pins ContentID to the reference algorithm
// (jev-compaction types.py content_id: sha256(salt+"\x00"+text) hex, first
// 8 chars, prefix+"<8hex>") with vectors computed by the reference
// implementation, plus the determinism contract: same input → same id,
// always; any input change (text or salt) → a different id.
func TestContentID_ReferenceVectors(t *testing.T) {
	if got, want := ContentID("hello world", "", "r"), "r:4eccf346"; got != want {
		t.Errorf("ContentID(hello world) = %q, want %q", got, want)
	}
	if got, want := ContentID("run text", "Bash", "r"), "r:74bf3504"; got != want {
		t.Errorf("ContentID(run text, Bash) = %q, want %q", got, want)
	}

	for _, tc := range []struct {
		name            string
		text, salt, pre string
	}{
		{name: "plain", text: "some text", salt: "", pre: "r"},
		{name: "salted", text: "some text", salt: "Bash", pre: "r"},
		{name: "other prefix", text: "some text", salt: "", pre: "s"},
		{name: "empty text", text: "", salt: "", pre: "r"},
		{name: "unicode", text: "héllo wörld ✓", salt: "s", pre: "r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := ContentID(tc.text, tc.salt, tc.pre)
			if first != ContentID(tc.text, tc.salt, tc.pre) {
				t.Errorf("ContentID is not deterministic: %q vs %q", first, ContentID(tc.text, tc.salt, tc.pre))
			}
			if !strings.HasPrefix(first, tc.pre+":") {
				t.Errorf("ContentID = %q, want the %q: prefix", first, tc.pre)
			}
			hex8 := strings.TrimPrefix(first, tc.pre+":")
			if len(hex8) != 8 {
				t.Errorf("ContentID = %q, want an 8-hex-char digest", first)
			}
			for _, r := range hex8 {
				if !strings.ContainsRune("0123456789abcdef", r) {
					t.Errorf("ContentID = %q, want lowercase hex", first)
				}
			}
			if got := ContentID(tc.text, tc.salt+"x", tc.pre); tc.salt+"x" != tc.salt && got == first {
				t.Errorf("a different salt must mint a different id: %q", got)
			}
			if got := ContentID(tc.text+"x", tc.salt, tc.pre); got == first {
				t.Errorf("different text must mint a different id: %q", got)
			}
		})
	}
}

// TestParsePointer parses the formats that must keep working: the reference
// format with a line range, the legacy "elide-<n>" counter ids, pointers
// with the lines part missing, and summaries with escaped quotes and
// backslashes.
func TestParsePointer(t *testing.T) {
	t.Run("reference format with lines", func(t *testing.T) {
		p, ok := ParsePointer(`[[elided id=r:1a2b3c4d lines=3-9 tokens=364 "he said \"ok\" \\ now"]]`)
		if !ok {
			t.Fatal("ParsePointer() = false, want a parse")
		}
		if p.ID != "r:1a2b3c4d" {
			t.Errorf("ID = %q, want r:1a2b3c4d", p.ID)
		}
		if p.Lines == nil || *p.Lines != [2]int{3, 9} {
			t.Errorf("Lines = %v, want [3 9]", p.Lines)
		}
		if p.Tokens != 364 {
			t.Errorf("Tokens = %d, want 364", p.Tokens)
		}
		if p.Summary != `he said "ok" \ now` {
			t.Errorf("Summary = %q, want the unescaped text", p.Summary)
		}
	})

	t.Run("legacy elide-N id", func(t *testing.T) {
		// Legacy counter ids parse — the id charset allows them. The lines
		// part, though, must use the reference's a-b range shape: the regex
		// is ported exactly, so the pre-reference count form (lines=12)
		// does not match at all (see the non-pointer cases below).
		p, ok := ParsePointer(`[[elided id=elide-3 lines=12-31 tokens=310 "first sixty chars"]]`)
		if !ok {
			t.Fatal("ParsePointer() = false, want a parse")
		}
		if p.ID != "elide-3" {
			t.Errorf("ID = %q, want elide-3", p.ID)
		}
		if p.Lines == nil || *p.Lines != [2]int{12, 31} {
			t.Errorf("Lines = %v, want [12 31]", p.Lines)
		}
	})

	t.Run("missing lines part", func(t *testing.T) {
		p, ok := ParsePointer(`[[elided id=elide-7 tokens=12 "plain summary"]]`)
		if !ok {
			t.Fatal("ParsePointer() = false, want a parse")
		}
		if p.Lines != nil {
			t.Errorf("Lines = %v, want nil", p.Lines)
		}
		if p.Summary != "plain summary" || p.Tokens != 12 {
			t.Errorf("parsed %+v, want the plain summary shape", p)
		}
	})

	t.Run("summary ending in an escaped backslash", func(t *testing.T) {
		p, ok := ParsePointer(`[[elided id=r:abcdef01 lines=1-1 tokens=0 "trailing backslash \\\\"]]`)
		if !ok {
			t.Fatal("ParsePointer() = false, want a parse")
		}
		if p.Summary != `trailing backslash \\` {
			t.Errorf("Summary = %q, want %q", p.Summary, `trailing backslash \\`)
		}
	})

	t.Run("searches inside a longer line", func(t *testing.T) {
		p, ok := ParsePointer(`noise before [[elided id=r:ffeeddcc tokens=5 "s"]] noise after`)
		if !ok || p.ID != "r:ffeeddcc" {
			t.Errorf("ParsePointer() = (%+v, %v), want the embedded pointer", p, ok)
		}
	})

	t.Run("non-pointer lines do not parse", func(t *testing.T) {
		for _, line := range []string{
			"",
			"plain text",
			"[[elided id=r:1a2b3c4d lines=1-2 \"no tokens\"]]",
			`[[elided id=r:1a2b3c4d tokens=5 "unterminated`,
			"[[elided id= tokens=5 \"no id\"]]",
			// The exact regex port: the pre-reference count-form lines part
			// (lines=<count>) never matched the reference pattern either.
			`[[elided id=elide-3 lines=12 tokens=310 "old count-form lines"]]`,
		} {
			if p, ok := ParsePointer(line); ok {
				t.Errorf("ParsePointer(%q) = (%+v, true), want false", line, p)
			}
		}
	})
}

// TestFormatPointerAndRoundTrip: FormatPointer output always parses back to
// the same Pointer — including quotes and backslashes in the summary — and
// is a single line.
func TestFormatPointerAndRoundTrip(t *testing.T) {
	for _, p := range []Pointer{
		{ID: "r:1a2b3c4d", Lines: &[2]int{3, 9}, Tokens: 364, Summary: `he said "ok" \ now`},
		{ID: "elide-7", Tokens: 12, Summary: "plain summary"},
		{ID: "r:abcdef01", Lines: &[2]int{1, 1}, Summary: `trailing backslash \\`},
		{ID: "r:00000000", Lines: &[2]int{1, 400}, Summary: "tabs\tand  spaces flatten elsewhere"},
		{ID: "r:ffffffff", Tokens: 1, Summary: ""},
	} {
		line := FormatPointer(p)
		if strings.Contains(line, "\n") {
			t.Errorf("FormatPointer(%+v) = %q, want a single line", p, line)
		}
		got, ok := ParsePointer(line)
		if !ok {
			t.Fatalf("ParsePointer(%q) = false, want a parse", line)
		}
		if got.ID != p.ID || got.Tokens != p.Tokens || got.Summary != p.Summary {
			t.Errorf("round trip of %+v gave %+v (line %q)", p, got, line)
		}
		if (got.Lines == nil) != (p.Lines == nil) {
			t.Errorf("round trip lines nil-ness changed: %+v → %+v", p, got)
		}
		if p.Lines != nil && *got.Lines != *p.Lines {
			t.Errorf("round trip lines = %v, want %v", *got.Lines, *p.Lines)
		}
	}
}

// TestFindPointers: every pointer in order of appearance, non-pointers
// ignored.
func TestFindPointers(t *testing.T) {
	text := strings.Join([]string{
		"kept prose",
		FormatPointer(Pointer{ID: "r:aaaa0001", Lines: &[2]int{1, 2}, Tokens: 10, Summary: "first"}),
		"more prose",
		FormatPointer(Pointer{ID: "r:aaaa0002", Lines: &[2]int{7, 8}, Tokens: 20, Summary: `second "quoted"`}),
		FormatPointer(Pointer{ID: "elide-9", Tokens: 3, Summary: "legacy"}),
	}, "\n")
	got := FindPointers(text)
	if len(got) != 3 {
		t.Fatalf("FindPointers() = %d pointers, want 3: %+v", len(got), got)
	}
	wantIDs := []string{"r:aaaa0001", "r:aaaa0002", "elide-9"}
	for i, p := range got {
		if p.ID != wantIDs[i] {
			t.Errorf("pointer[%d].ID = %q, want %q", i, p.ID, wantIDs[i])
		}
	}
	if got := FindPointers("no pointers here\nnor here"); len(got) != 0 {
		t.Errorf("FindPointers(pointerless) = %+v, want empty", got)
	}
}

// TestSummarise ports _summarise's contract: the first non-blank line,
// whitespace-flattened, cut at a word boundary within limit runes with a
// "…" suffix.
func TestSummarise(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{
			name:  "short line returned whole",
			text:  "first line\nsecond line",
			limit: 120,
			want:  "first line",
		},
		{
			name:  "leading blank lines skipped",
			text:  "\n \n\t\nthe real first line",
			limit: 120,
			want:  "the real first line",
		},
		{
			// The first RAW line flattens to "spaced out" (the reference's
			// " ".join(line.split()) — " line" lives on the next raw line,
			// so it is not part of the summary).
			name:  "whitespace flattened",
			text:  "  spaced\t\tout \n line\nsecond",
			limit: 120,
			want:  "spaced out",
		},
		{
			name:  "word-boundary cut with ellipsis",
			text:  "aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd",
			limit: 25,
			want:  "aaaaaaaaaa bbbbbbbbbb…",
		},
		{
			name:  "cut lands exactly on a space",
			text:  "aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd",
			limit: 22,
			want:  "aaaaaaaaaa bbbbbbbbbb…",
		},
		{
			name:  "no space in window falls back to the raw cut",
			text:  strings.Repeat("x", 200),
			limit: 120,
			want:  strings.Repeat("x", 120) + "…",
		},
		{
			name:  "all blank text",
			text:  "\n  \n",
			limit: 120,
			want:  "",
		},
		{
			name:  "empty text",
			text:  "",
			limit: 120,
			want:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Summarise(tc.text, tc.limit); got != tc.want {
				t.Errorf("Summarise(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
			}
		})
	}

	// The limit counts runes, not bytes.
	multibyte := strings.Repeat("é", 150) // 300 bytes, 150 runes
	if got := Summarise(multibyte, 120); got != strings.Repeat("é", 120)+"…" {
		t.Errorf("Summarise(multibyte, 120) = %d runes, want 120 runes + ellipsis", len([]rune(got))-1)
	}
	if got := Summarise("one two three", 0); got != "…" {
		t.Errorf("Summarise(_, 0) = %q, want the ellipsis-only cut", got)
	}
}

// TestReconstruct: every pointer whose record exists is substituted by its
// stored text (the pointer line AND its trailing newline are consumed, so
// the text lands exactly where the run was); unknown ids and nil stores
// leave the text untouched byte for byte. The fixture uses the exact shape
// admit renders: pointer lines follow text that carries its own trailing
// blank separator and are followed by exactly one newline.
func TestReconstruct(t *testing.T) {
	store := NewStore()
	store.Put("r:aaaa0001", "original one\n\nwith a trailing separator")
	store.Put("elide-2", "legacy original")

	pOne := FormatPointer(Pointer{ID: "r:aaaa0001", Lines: &[2]int{2, 3}, Tokens: 9, Summary: "one"})
	pTwo := FormatPointer(Pointer{ID: "elide-2", Tokens: 4, Summary: "two"})
	pGone := FormatPointer(Pointer{ID: "r:ffff0000", Lines: &[2]int{9, 9}, Tokens: 1, Summary: "missing"})
	pAgain := FormatPointer(Pointer{ID: "r:aaaa0001", Tokens: 9, Summary: "one again"})

	text := "before block\n\n" + pOne + "\n" +
		"middle block\n\n" + pTwo + "\n" + pGone + "\n" +
		"after block\n" + pAgain

	// Known ids: pointer + trailing newline → record text, landing exactly
	// where the run stood. The unknown id stays as-is (its newline too).
	want := "before block\n\n" + "original one\n\nwith a trailing separator" +
		"middle block\n\n" + "legacy original" + pGone + "\n" +
		"after block\n" + "original one\n\nwith a trailing separator"
	if got := Reconstruct(text, store); got != want {
		t.Errorf("Reconstruct() mismatch:\n got %q\nwant %q", got, want)
	}

	// A pointer at EOF without a trailing newline is still substituted.
	if got, want := Reconstruct("head block\n\n"+pTwo, store), "head block\n\nlegacy original"; got != want {
		t.Errorf("Reconstruct(EOF pointer) = %q, want %q", got, want)
	}

	// Pointerless text is returned byte-for-byte.
	plain := "nothing to see\nmove along"
	if got := Reconstruct(plain, store); got != plain {
		t.Errorf("Reconstruct(pointerless) = %q, want the input unchanged", got)
	}

	// A nil store leaves every pointer in place.
	if got := Reconstruct(text, nil); got != text {
		t.Errorf("Reconstruct(text, nil) must be the identity, got:\n%s", got)
	}
}
