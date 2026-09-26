package compaction

import (
	"fmt"
	"strings"
	"testing"
)

// assertSegmentsInvariant checks the Segment contract: original[s.StartByte:
// s.EndByte] == s.Text for every segment, plus tokens, ID numbering, and the
// 1-based line span recomputed from the byte offsets.
func assertSegmentsInvariant(t *testing.T, original string, segs []Segment) {
	t.Helper()
	for i, s := range segs {
		if got := original[s.StartByte:s.EndByte]; got != s.Text {
			t.Errorf("segment %d (%s): original[%d:%d] = %q, want Text %q", i, s.ID, s.StartByte, s.EndByte, got, s.Text)
		}
		if s.EndByte < s.StartByte {
			t.Errorf("segment %d (%s): EndByte %d < StartByte %d", i, s.ID, s.EndByte, s.StartByte)
		}
		if s.Tokens <= 0 {
			t.Errorf("segment %d (%s): Tokens = %d, want > 0 for non-empty text", i, s.ID, s.Tokens)
		}
		if want := fmt.Sprintf("seg-%d", i+1); s.ID != want {
			t.Errorf("segment %d ID = %q, want %q", i, s.ID, want)
		}
		if want := 1 + strings.Count(original[:s.StartByte], "\n"); s.LineStart != want {
			t.Errorf("segment %d (%s): LineStart = %d, want %d (recomputed from StartByte)", i, s.ID, s.LineStart, want)
		}
		wantEnd := 1 + strings.Count(original[:s.EndByte], "\n")
		if s.EndByte > 0 && original[s.EndByte-1] == '\n' {
			wantEnd--
		}
		if s.LineEnd != wantEnd {
			t.Errorf("segment %d (%s): LineEnd = %d, want %d (recomputed from EndByte)", i, s.ID, s.LineEnd, wantEnd)
		}
		if s.LineStart > s.LineEnd {
			t.Errorf("segment %d (%s): LineStart %d > LineEnd %d", i, s.ID, s.LineStart, s.LineEnd)
		}
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].StartByte < segs[i-1].EndByte {
			t.Errorf("segment %d starts at %d before segment %d ends at %d (overlapping spans)",
				i, segs[i].StartByte, i-1, segs[i-1].EndByte)
		}
	}
}

func TestSegmentSegments_ParagraphSplitting(t *testing.T) {
	out := "first paragraph\n\nsecond paragraph\n\nthird paragraph"
	segs := SegmentSegments(out, 0)
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3: %+v", len(segs), segs)
	}
	for i, want := range []string{"first paragraph\n\n", "second paragraph\n\n", "third paragraph"} {
		if segs[i].Text != want {
			t.Errorf("segment %d Text = %q, want %q (trailing separator included)", i, segs[i].Text, want)
		}
	}
	assertSegmentsInvariant(t, out, segs)

	// The spans tile the input from the first content byte: concatenating
	// every segment reproduces the output.
	var joined strings.Builder
	for _, s := range segs {
		joined.WriteString(s.Text)
	}
	if joined.String() != out {
		t.Errorf("concatenated segments = %q, want original %q", joined.String(), out)
	}
}

// TestSegmentSegments_LineSpans pins the 1-based line spans the pointer
// format needs (the reference's line_span): each segment covers the lines
// its span occupies, blank-line separators included.
func TestSegmentSegments_LineSpans(t *testing.T) {
	out := "first paragraph\n\nsecond paragraph\n\nthird paragraph"
	segs := SegmentSegments(out, 0)
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3", len(segs))
	}
	for i, want := range [][2]int{{1, 2}, {3, 4}, {5, 5}} {
		if got := [2]int{segs[i].LineStart, segs[i].LineEnd}; got != want {
			t.Errorf("segment %d line span = %v, want %v", i, got, want)
		}
	}
	assertSegmentsInvariant(t, out, segs)

	// A hard rune-safe cut mid-line (no newline inside the cap window) still
	// lands both pieces on the same line.
	long := strings.Repeat("a", 1500)
	segs = SegmentSegments(long, 0)
	if len(segs) < 2 {
		t.Fatalf("got %d segments, want the oversized split", len(segs))
	}
	for i, s := range segs {
		if got := [2]int{s.LineStart, s.LineEnd}; got != [2]int{1, 1} {
			t.Errorf("piece %d line span = %v, want [1 1] (single-line output)", i, got)
		}
	}
	assertSegmentsInvariant(t, long, segs)
}

func TestSegmentSegments_TinyParagraphsMerged(t *testing.T) {
	// A tiny paragraph between two larger ones is absorbed forward.
	out := strings.Repeat("x", 100) + "\n\ntiny\n\n" + strings.Repeat("y", 100)
	segs := SegmentSegments(out, 0)
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1 (tiny paragraph merged forward)", len(segs))
	}

	// A trailing tiny paragraph is folded backward into the previous one.
	out = strings.Repeat("x", 100) + "\n\ntiny"
	segs = SegmentSegments(out, 0)
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1 (trailing tiny paragraph folded back)", len(segs))
	}
	if segs[0].Text != out {
		t.Errorf("merged Text = %q, want the full output %q", segs[0].Text, out)
	}
	assertSegmentsInvariant(t, out, segs)
}

func TestSegmentSegments_TinyParagraphNotMergedWhenTooBig(t *testing.T) {
	// Merging would blow the cap, so the paragraphs stay separate: 1195
	// content bytes + the 2-byte separator + the 6-byte tiny paragraph
	// exceeds the 1200-byte cap.
	out := strings.Repeat("x", 1195) + "\n\ntiny"
	segs := SegmentSegments(out, 0)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2 (merge would exceed the cap)", len(segs))
	}
	assertSegmentsInvariant(t, out, segs)
}

func TestSegmentSegments_SizeCapSplitsLargeParagraphs(t *testing.T) {
	// One giant paragraph with newlines inside: splits at newlines within
	// the 1200-byte window.
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, strings.Repeat(fmt.Sprint(i%10), 100))
	}
	out := strings.Join(lines, "\n") // 40*100 + 39 = 4039 bytes
	segs := SegmentSegments(out, 0)
	if len(segs) < 3 {
		t.Fatalf("got %d segments, want ≥3 for a %d-byte paragraph under the %d-byte cap",
			len(segs), len(out), DefaultMaxSegChars)
	}
	for i, s := range segs {
		if len(s.Text) > DefaultMaxSegChars {
			t.Errorf("segment %d is %d bytes, want ≤%d", i, len(s.Text), DefaultMaxSegChars)
		}
	}
	assertSegmentsInvariant(t, out, segs)

	// Rune-safe hard splitting: a paragraph with no newlines at all.
	out = strings.Repeat("é", 3000) // 6000 bytes of 2-byte runes
	segs = SegmentSegments(out, 0)
	if len(segs) != 5 { // 6000 bytes / 1200 = 5 exactly (rune-aligned cap)
		t.Fatalf("got %d segments, want 5", len(segs))
	}
	var joined strings.Builder
	for _, s := range segs {
		if len(s.Text) > DefaultMaxSegChars {
			t.Errorf("segment %s is %d bytes, want ≤%d", s.ID, len(s.Text), DefaultMaxSegChars)
		}
		joined.WriteString(s.Text)
	}
	if joined.String() != out {
		t.Error("hard-split pieces do not reproduce the original output")
	}
	assertSegmentsInvariant(t, out, segs)
}

func TestSegmentSegments_ExplicitMaxSegChars(t *testing.T) {
	out := strings.Repeat("a", 500)
	if segs := SegmentSegments(out, 0); len(segs) != 1 {
		t.Fatalf("got %d segments, want 1 under the default cap", len(segs))
	}
	if segs := SegmentSegments(out, 200); len(segs) != 3 {
		t.Fatalf("got %d segments, want 3 under a 200-byte cap", len(segs))
	}
}

func TestSegmentSegments_DefaultCapMatchesConstant(t *testing.T) {
	out := strings.Repeat("a", 3000)
	a := SegmentSegments(out, 0)
	b := SegmentSegments(out, DefaultMaxSegChars)
	if len(a) != len(b) {
		t.Fatalf("maxSegChars 0 gave %d segments, explicit %d gave %d — 0 must mean the default",
			len(a), DefaultMaxSegChars, len(b))
	}
	for i := range a {
		if a[i].Text != b[i].Text {
			t.Errorf("segment %d differs between 0 and the default cap", i)
		}
	}
}

func TestSegmentSegments_EmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "\n\n\n", "   \n\t\n  \n"} {
		if segs := SegmentSegments(in, 0); segs != nil {
			t.Errorf("SegmentSegments(%q) = %+v, want nil", in, segs)
		}
	}
}

func TestSegmentSegments_LeadingBlanksAndCRLF(t *testing.T) {
	// Leading blank lines belong to no segment; CRLF blank lines still
	// separate paragraphs.
	out := "\n\n\r\nfirst\r\n\n\r\nsecond\n"
	segs := SegmentSegments(out, 0)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(segs), segs)
	}
	if segs[0].Text != "first\r\n\n\r\n" {
		t.Errorf("segment 0 Text = %q, want %q", segs[0].Text, "first\r\n\n\r\n")
	}
	if segs[1].Text != "second\n" {
		t.Errorf("segment 1 Text = %q, want %q", segs[1].Text, "second\n")
	}
	assertSegmentsInvariant(t, out, segs)
}

func TestSegmentSegments_WhitespaceOnlyLinesDoNotSplitParagraphs(t *testing.T) {
	// A line holding only spaces is blank (a separator), not content.
	out := "alpha\n   \nbeta"
	segs := SegmentSegments(out, 0)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2 (whitespace-only line is a separator)", len(segs))
	}
	assertSegmentsInvariant(t, out, segs)
}

func TestSegmentSegments_MultiByteOffsets(t *testing.T) {
	// Byte offsets must stay byte-based (not rune-based) with multibyte
	// content before the split point.
	out := "日本語のテキスト\n\nsecond paragraph with ascii"
	segs := SegmentSegments(out, 0)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	assertSegmentsInvariant(t, out, segs)
	if segs[0].StartByte != 0 || segs[0].EndByte != len("日本語のテキスト\n\n") {
		t.Errorf("segment 0 span = [%d,%d), want [0,%d)", segs[0].StartByte, segs[0].EndByte, len("日本語のテキスト\n\n"))
	}
}

// TestClassifyKind pins the kind heuristics, most specific first. Each case
// is one deterministic classification of a whole segment's text.
func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name string
		text string
		want SegmentKind
	}{
		{name: "empty is text", text: "", want: KindText},
		{name: "prose is text", text: "The parser accepts three input formats and normalizes them before scoring.", want: KindText},
		{
			name: "fenced code block is code",
			text: "```go\nfmt.Println(\"hi\")\n```",
			want: KindCode,
		},
		{
			name: "fence beats diff markers inside it",
			text: "```diff\n+++ b/x.go\n@@ -1 +1 @@\n```",
			want: KindCode,
		},
		{
			name: "diff header is diff",
			text: "diff --git a/x.go b/x.go\nindex 1234..5678 100644\n--- a/x.go\n+++ b/x.go",
			want: KindDiff,
		},
		{
			name: "plus-plus prefix is diff",
			text: "+++ b/internal/x.go\n@@ -1 +1 @@",
			want: KindDiff,
		},
		{
			name: "hunk header is diff",
			text: "@@ -12,7 +12,9 @@ func main() {",
			want: KindDiff,
		},
		{
			name: "go panic header is stacktrace",
			text: "goroutine 1 [running]:\nmain.main()\n\t/home/dev/app/main.go:42 +0x1a4",
			want: KindStacktrace,
		},
		{
			name: "python traceback is stacktrace",
			text: "Traceback (most recent call last):\n  File \"x.py\", line 3, in <module>",
			want: KindStacktrace,
		},
		{
			name: "java frame with leading tab is stacktrace",
			text: "\tat com.example.Foo.bar(Foo.java:99)\nat com.example.Foo.baz(Foo.java:10)",
			want: KindStacktrace,
		},
		{
			name: "exception name is stacktrace",
			text: "java.lang.NullPointerException: cannot invoke method on null",
			want: KindStacktrace,
		},
		{
			name: "json object is json",
			text: `{"ok": true, "items": [1, 2, 3]}`,
			want: KindJSON,
		},
		{
			name: "json array is json",
			text: `[{"id": 1}, {"id": 2}]`,
			want: KindJSON,
		},
		{
			name: "bracketed prose is not json",
			text: "[TODO] fix the parser before the next release",
			want: KindText,
		},
		{
			name: "broken json is not json",
			text: `{"ok": true, "trailing":`,
			want: KindText,
		},
		{
			name: "iso timestamps are log",
			text: "2024-01-02T15:04:05Z INFO boot\n2024-01-02T15:04:06Z ERROR fail\n2024-01-02T15:04:07Z INFO ready",
			want: KindLog,
		},
		{
			name: "space-separated timestamps are log",
			text: "2024-01-02 15:04:05 starting import\n2024-01-02 15:04:06 import done",
			want: KindLog,
		},
		{
			name: "bracketed times are log",
			text: "[12:00:00] boot ok\n[12:00:01] ready\n[12:00:02] done",
			want: KindLog,
		},
		{
			name: "severity prefixes are log",
			text: "WARN disk nearly full\nERROR write failed\nINFO retrying",
			want: KindLog,
		},
		{
			name: "one timestamp mention in prose is not a log",
			text: "The deploy finished at 2024-01-02 15:04:05 sharp, and everyone celebrated.",
			want: KindText,
		},
		{
			name: "pipe rows with consistent columns are table",
			text: "name | count\nalpha | 2\nbeta | 3",
			want: KindTable,
		},
		{
			name: "inconsistent columns are not a table",
			text: "name | count\nalpha | 2 | extra\nbeta | 3",
			want: KindText,
		},
		{
			name: "a single pipe line is not a table",
			text: "alpha | beta",
			want: KindText,
		},
		{
			name: "prose mentioning a pipe is not a table",
			text: "Use a | sparingly.\nOr not at all.\nReally, don't.",
			want: KindText,
		},
		{
			name: "diff beats exception-looking content",
			text: "+++ b/x.py\n@@ -1 +1 @@\n-raise Exception('x')",
			want: KindDiff,
		},
		{
			name: "stacktrace beats table",
			text: "goroutine 1 [running]:\nmain.a()\nmain.b() | frame",
			want: KindStacktrace,
		},
		{
			name: "log beats table",
			text: "2024-01-02T15:04:05Z INFO a | b\n2024-01-02T15:04:06Z INFO c | d",
			want: KindLog,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyKind(tc.text); got != tc.want {
				t.Errorf("classifyKind(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestSegmentSegments_KindClassification checks that segmentation stamps
// every segment with its kind while leaving the byte-offset contract
// untouched.
func TestSegmentSegments_KindClassification(t *testing.T) {
	prose := strings.Repeat("plain prose. ", 8) // 104 chars: over the tiny floor
	var tableRows []string
	for i := 0; i < 6; i++ {
		tableRows = append(tableRows, fmt.Sprintf("item-%02d | count-%02d | note", i, i))
	}
	table := strings.Join(tableRows, "\n")
	stack := "goroutine 1 [running]:\nmain.main()\n\t/home/dev/app/main.go:42 +0x1a4\nexit status 2"
	// Over the 80-char tiny-paragraph floor so it stays its own segment.
	diff := "diff --git a/main.go b/main.go\nindex 1234..5678 100644\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@"
	// Both over the 80-char tiny-paragraph floor so each stays its own segment.
	js := `{"ok": true, "items": [1, 2, 3], "note": "padding padding padding padding padding"}`

	out := strings.Join([]string{prose, table, stack, diff, js}, "\n\n")
	segs := SegmentSegments(out, 0)
	if len(segs) != 5 {
		t.Fatalf("got %d segments, want 5: %+v", len(segs), segs)
	}
	wantKinds := []SegmentKind{KindText, KindTable, KindStacktrace, KindDiff, KindJSON}
	for i, want := range wantKinds {
		if segs[i].Kind != want {
			t.Errorf("segment %d Kind = %q, want %q (text %q)", i, segs[i].Kind, want, segs[i].Text)
		}
	}
	assertSegmentsInvariant(t, out, segs)
}
