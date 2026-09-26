package compaction

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"late/internal/common"
)

// DefaultMaxSegChars caps one segment's size in bytes. It mirrors the
// jev-compaction default: large enough to keep a paragraph's context together
// for the scorer, small enough that one giant tool dump does not become one
// giant undifferentiated segment.
const DefaultMaxSegChars = 1200

// defaultMinSegChars is the size under which a paragraph is "tiny": a tiny
// paragraph is folded into an adjacent larger paragraph instead of being
// scored on its own, so a stray one-liner next to substantial output does
// not become its own segment. Runs of small paragraphs stay separate.
const defaultMinSegChars = 80

// Segment is one scored piece of a tool output.
//
// StartByte/EndByte are byte offsets into the ORIGINAL string (EndByte
// exclusive) with the invariant original[StartByte:EndByte] == Text. Each
// span includes the paragraph's trailing blank-line separator when one
// follows it, so the spans tile the input: concatenating the kept segments'
// Text reproduces the original output byte-for-byte minus the elided spans.
// (Blank bytes before the first paragraph belong to no segment.)
type Segment struct {
	ID        string
	Text      string
	StartByte int
	EndByte   int
	Tokens    int
	// Kind is the segment's content classification (classifyKind), computed
	// at segmentation time. The gate consults it for protected-kind floors:
	// stacktrace and diff segments are only elided below their own, much
	// lower, floor.
	Kind SegmentKind
	// LineStart/LineEnd are the 1-based [first, last] line numbers the
	// segment's span occupies in the segmented string (the reference's
	// line_span) — the numbers a pointer for this segment carries. Computed
	// at segmentation time from the byte offsets; 0/0 never occurs for
	// segments from SegmentSegments.
	LineStart int
	LineEnd   int
	// Group is the 1-based identity of the merged paragraph span this
	// segment was cut from: when splitOversized cuts one oversized
	// paragraph into several pieces, every piece carries the same Group, so
	// the elide decision can keep the paragraph ATOMIC — pieces of one
	// paragraph must share a single elide decision (AtomicElideDecisions /
	// AtomicDecisionScores), because eliding one piece of a cut paragraph
	// corrupts the whole (a JSON blob split mid-structure and partially
	// elided is unparseable). Ids are assigned per SegmentSegments call and
	// every decision is computed over one call's segments (one tool output,
	// one history message), so groups from different calls never mix. 0
	// means ungrouped — segments built outside SegmentSegments (hand-built
	// test fixtures) carry no grouping and behave exactly as before this
	// field existed: each stands alone.
	Group int
}

// SegmentKind classifies a segment's content so the gate can treat kinds
// differently (the reference pipeline's protected_kinds). The zero value is
// meaningless — SegmentSegments always sets one of the Kind* constants.
type SegmentKind string

const (
	// KindText is prose: anything that is none of the more specific kinds.
	KindText SegmentKind = "text"
	// KindJSON is a parseable JSON document (object or array).
	KindJSON SegmentKind = "json"
	// KindLog is timestamped/severity-prefixed log output.
	KindLog SegmentKind = "log"
	// KindStacktrace is a panic or exception trace. Protected: traces are
	// dense in signal and cheap in tokens, so they survive almost any score.
	KindStacktrace SegmentKind = "stacktrace"
	// KindTable is pipe-delimited tabular data.
	KindTable SegmentKind = "table"
	// KindCode is a fenced code block.
	KindCode SegmentKind = "code"
	// KindDiff is unified-diff output. Protected: a dropped hunk silently
	// corrupts everything built on top of it.
	KindDiff SegmentKind = "diff"
)

// Classification patterns. Compiled once; all anchored per trimmed line so
// leading indentation (Java's "\tat com...") and CRLF endings never hide a
// match.
var (
	// stackFrameLineRe matches V8/Java-style frames: "at pkg.File(Tool.go:42)"
	// and "at com.example.Foo.bar(Foo.java:99)".
	stackFrameLineRe = regexp.MustCompile(`^at .+\(.+:\d+\)`)
	// goroutineHeaderRe matches Go panic headers: "goroutine 1 [running]:"
	// and "goroutine 17 [signal SIGSEGV: ...]".
	goroutineHeaderRe = regexp.MustCompile(`^goroutine \d+ \[`)
	// iso8601LogLineRe matches a leading ISO-8601-ish timestamp, with a "T"
	// or space separator: "2024-01-02T15:04:05Z", "2024-01-02 15:04:05,123".
	iso8601LogLineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)
	// bracketedTimeLogLineRe matches syslog-style prefixes: "[12:34:56]".
	bracketedTimeLogLineRe = regexp.MustCompile(`^\[\d{1,2}:\d{2}:\d{2}\]`)
	// logLevelLineRe matches a leading severity word: "ERROR ", "WARN:",
	// "INFO", "DEBUG ...".
	logLevelLineRe = regexp.MustCompile(`^(WARN|ERROR|INFO|DEBUG)\b`)
)

// classifyKind guesses a segment's content kind with cheap, deterministic
// heuristics, most specific first (ported from the reference pipeline):
//
//  1. a fenced ``` code block → code (the fence wins over the fenced
//     content's own appearance, which may look like logs or diffs),
//  2. `diff --git` / `+++ ` / `@@ -` lines → diff,
//  3. "Traceback (most recent call last)", exception names, "at f(x.go:1)"
//     frames, or "goroutine N [...]" headers → stacktrace,
//  4. trimmed text opening with { or [ that parses as JSON → json (the
//     validity check keeps "[TODO] fix the parser" prose out),
//  5. a majority of lines with timestamp or severity prefixes → log,
//  6. a majority of lines pipe rows with one shared column count → table,
//  7. anything else → text.
func classifyKind(text string) SegmentKind {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return KindText
	}
	lines := strings.Split(trimmed, "\n")

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			return KindCode
		}
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "diff --git") || strings.HasPrefix(t, "+++ ") || strings.HasPrefix(t, "@@ -") {
			return KindDiff
		}
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.Contains(t, "Traceback (most recent call last)") ||
			strings.Contains(t, "Exception") ||
			stackFrameLineRe.MatchString(t) ||
			goroutineHeaderRe.MatchString(t) {
			return KindStacktrace
		}
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if json.Valid([]byte(trimmed)) {
			return KindJSON
		}
	}
	if lineMajority(lines, looksLikeLogLine) {
		return KindLog
	}
	if looksLikeTable(lines) {
		return KindTable
	}
	return KindText
}

// looksLikeLogLine reports whether one line carries a log timestamp or
// severity prefix.
func looksLikeLogLine(line string) bool {
	t := strings.TrimSpace(line)
	return iso8601LogLineRe.MatchString(t) ||
		bracketedTimeLogLineRe.MatchString(t) ||
		logLevelLineRe.MatchString(t)
}

// lineMajority reports whether strictly more than half of the non-blank
// lines satisfy match. A majority (not a single hit) keeps one timestamp
// mention inside prose from reclassifying the paragraph around it.
func lineMajority(lines []string, match func(string) bool) bool {
	total, hits := 0, 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		total++
		if match(t) {
			hits++
		}
	}
	return total > 0 && hits*2 > total
}

// looksLikeTable reports whether a majority of the non-blank lines are table
// rows — lines containing "|" with one shared column count — and at least two
// such rows exist (a single line mentioning a pipe is prose, not a table).
func looksLikeTable(lines []string) bool {
	total, rows, columns := 0, 0, -1
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		total++
		if !strings.Contains(t, "|") {
			continue
		}
		cols := strings.Count(t, "|")
		if columns >= 0 && cols != columns {
			return false // inconsistent columns: not a table
		}
		columns = cols
		rows++
	}
	return rows >= 2 && rows*2 > total
}

// SegmentSegments splits toolOutput into segments at paragraph boundaries
// (blank lines), merging tiny paragraphs and capping each segment at
// maxSegChars bytes (DefaultMaxSegChars when maxSegChars <= 0). It returns
// nil for empty or whitespace-only output.
func SegmentSegments(toolOutput string, maxSegChars int) []Segment {
	if maxSegChars <= 0 {
		maxSegChars = DefaultMaxSegChars
	}
	paras := splitParagraphs(toolOutput)
	if len(paras) == 0 {
		return nil
	}

	var segs []Segment
	n := 0
	group := 0
	// newlines counts the '\n' bytes in toolOutput[:cursor]; pieces arrive
	// in offset order, so line numbers come from one linear pass. (The
	// reference carries the same information as line_span.)
	cursor, newlines := 0, 0
	for _, sp := range mergeParagraphs(paras, toolOutput, maxSegChars) {
		// One group per merged paragraph span: every piece splitOversized
		// cuts from this span shares the id, so the elide decision can treat
		// the paragraph as one atomic unit.
		group++
		for _, piece := range splitOversized(toolOutput, sp, maxSegChars) {
			n++
			text := toolOutput[piece.start:piece.end]
			newlines += strings.Count(toolOutput[cursor:piece.start], "\n")
			lineStart := newlines + 1
			newlines += strings.Count(toolOutput[piece.start:piece.end], "\n")
			// The last byte of the span terminates its line when it is a
			// newline; either way the span ends on that line.
			lineEnd := newlines + 1
			if toolOutput[piece.end-1] == '\n' {
				lineEnd--
			}
			cursor = piece.end
			segs = append(segs, Segment{
				ID:        fmt.Sprintf("seg-%d", n),
				Text:      text,
				StartByte: piece.start,
				EndByte:   piece.end,
				Tokens:    common.EstimateTokenCount(text),
				Kind:      classifyKind(text),
				LineStart: lineStart,
				LineEnd:   lineEnd,
				Group:     group,
			})
		}
	}
	return segs
}

// span is a half-open byte range [start, end) into the segmented string.
type span struct {
	start int
	end   int
}

// splitParagraphs cuts s into paragraphs: maximal runs of non-blank lines.
// A blank line is a line that is empty or whitespace-only (\r\n tolerant).
// The span of a paragraph starts at its first content byte and ends after
// the blank-line separator that follows it (or at EOF for trailing
// whitespace), so consecutive spans tile the string from the first content
// byte onward.
func splitParagraphs(s string) []span {
	var out []span
	i, n := 0, len(s)
	for i < n {
		// Skip blank lines (this also skips leading blanks, which belong to
		// no paragraph).
		i = skipBlankLines(s, i)
		if i >= n {
			break
		}
		start := i
		// Consume content lines until a blank line or EOF.
		for i < n {
			lineEnd, next := lineBounds(s, i)
			if strings.TrimSpace(s[i:lineEnd]) == "" {
				break
			}
			i = next
		}
		// Absorb the trailing blank-line separator into this paragraph's
		// span so the spans tile the input.
		end := i
		for i < n {
			lineEnd, next := lineBounds(s, i)
			if strings.TrimSpace(s[i:lineEnd]) != "" {
				break
			}
			i = next
			end = i
		}
		out = append(out, span{start, end})
	}
	return out
}

// lineBounds returns the end (exclusive, before the '\n') and the start of
// the following line for the line beginning at i.
func lineBounds(s string, i int) (lineEnd, next int) {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j, i + j + 1
	}
	return len(s), len(s)
}

// skipBlankLines advances i past blank lines and returns the new offset.
func skipBlankLines(s string, i int) int {
	for i < len(s) {
		lineEnd, next := lineBounds(s, i)
		if strings.TrimSpace(s[i:lineEnd]) != "" {
			return i
		}
		i = next
	}
	return i
}

// mergeParagraphs folds tiny paragraphs into their large neighbors: adjacent
// paragraphs merge when exactly one of them is tiny (smaller than
// defaultMinSegChars), so a stray one-liner next to substantial content does
// not become its own scored segment, while runs of small paragraphs — and
// runs of large ones — keep their separate identities. Merges chain greedily
// from each paragraph and are only taken while the combined span still fits
// maxSegChars. Lengths are measured in bytes including the absorbed
// separators.
func mergeParagraphs(paras []span, s string, maxSegChars int) []span {
	if len(paras) <= 1 {
		return paras
	}
	tiny := func(sp span) bool { return sp.end-sp.start < defaultMinSegChars }
	merged := make([]span, 0, len(paras))
	i := 0
	for i < len(paras) {
		// Grow the group starting at i across adjacent paragraphs: each step
		// straddles a tiny/large boundary and must keep the combined span
		// within the cap.
		j := i + 1
		for j < len(paras) &&
			tiny(paras[j-1]) != tiny(paras[j]) &&
			len(s[paras[i].start:paras[j].end]) <= maxSegChars {
			j++
		}
		merged = append(merged, span{paras[i].start, paras[j-1].end})
		i = j
	}
	return merged
}

// splitOversized cuts a merged span longer than maxSegChars into pieces of at
// most maxSegChars bytes, cutting at the last newline inside the window when
// one exists and otherwise at a rune-safe hard boundary.
func splitOversized(s string, sp span, maxSegChars int) []span {
	if sp.end-sp.start <= maxSegChars {
		return []span{sp}
	}
	var out []span
	start := sp.start
	for start < sp.end {
		if sp.end-start <= maxSegChars {
			out = append(out, span{start, sp.end})
			break
		}
		window := start + maxSegChars
		cut := window
		if idx := strings.LastIndexByte(s[start:window], '\n'); idx >= 0 {
			// Keep the newline with the left piece.
			cut = start + idx + 1
		} else {
			// Hard cut that never splits a multi-byte rune.
			for cut > start && !utf8.RuneStart(s[cut]) {
				cut--
			}
		}
		if cut <= start {
			cut = start + maxSegChars // paranoia; cannot happen
		}
		out = append(out, span{start, cut})
		start = cut
	}
	return out
}

// AtomicElideDecisions computes per-segment elide decisions with paragraph
// atomicity: pieces cut from the same oversized paragraph (equal nonzero
// Group — the pieces splitOversized produced) share ONE decision, made on
// the paragraph's MINIMUM sibling score against the MINIMUM sibling floor —
// unless a sibling sits at the unelidable score ceiling, which keeps the
// whole paragraph (see AtomicDecisionScores). See AtomicDecisionScores for
// the exact decision inputs; this returns just the flags. A mismatched
// input triple yields nil (a caller bug).
func AtomicElideDecisions(segs []Segment, scores, floors []float64) []bool {
	decide, decFloors := AtomicDecisionScores(segs, scores, floors)
	if decide == nil {
		return nil
	}
	elide := make([]bool, len(segs))
	for i := range segs {
		elide[i] = decide[i] < decFloors[i]
	}
	return elide
}

// AtomicDecisionScores returns, per segment, the score and floor its elide
// decision turns on, with paragraph atomicity: segments cut from the same
// oversized paragraph (equal nonzero Group — the pieces splitOversized
// produced) decide as ONE unit, on the paragraph's MINIMUM sibling score
// against the MINIMUM sibling floor.
//
// Why: oversized single paragraphs are hard-cut by splitOversized into
// multiple segment pieces; eliding ONE piece of a cut paragraph corrupts the
// whole — a JSON blob split mid-structure and partially elided is
// unparseable, and a prose paragraph loses its middle. So a cut paragraph is
// decided as a unit: one low sibling (the minimum sibling score is the
// binding one) elides the WHOLE paragraph — the run's original is stored
// whole and Reconstruct restores it byte for byte — while siblings above
// their floor keep the paragraph fully.
//
// scores[i] is segment i's score (the caller applies any origin protection
// first), floors[i] its elide floor (the gate's protected-kind floor when
// the kind has one, else the keep threshold; a flat threshold for callers
// without a gate). The returned slices are fresh; ungrouped segments
// (Group 0) decide on their own score and floor. For groups whose floors are
// all equal — the overwhelmingly common case, pieces of one paragraph
// sharing their content kind — the decision reduces exactly to
// min-score < floor. The decision scores returned here are also what the
// shadow log records, so score-vs-threshold replay reproduces the recorded
// decisions. A mismatched input triple yields (nil, nil) — a caller bug.
//
// Unelidable pin: protection overrides the minimum. A sibling whose score
// sits at unelidableScore (1.0) — a protected origin's clamped score
// (protectedScore), or the scorer's fail-open keep score — can never be
// elided at any gate setting, and the paragraph decides as one unit, so a
// group holding such a sibling is PINNED to keep: every piece's decision
// score becomes unelidableScore (its floor stays the group minimum). The
// naive alternative — letting the min-floor rule carry protection — would
// invert it: min(1.0, 0.35) = 0.35 drops the protection floor and the
// protected piece would elide with its low-scoring sibling. Pinning the
// decision score (not the floor) is what keeps the shadow log and every
// replay read consistent: 1.0 is never strictly below a floor in [0, 1], so
// recorded decisions, Stats, FalseNegativeRate, and ReplayTable all
// reproduce the keep a mutating run would make.
func AtomicDecisionScores(segs []Segment, scores, floors []float64) (decide, decFloors []float64) {
	if len(segs) != len(scores) || len(segs) != len(floors) {
		return nil, nil // defensive: a mismatched triple is a caller bug
	}
	decide = append([]float64(nil), scores...)
	decFloors = append([]float64(nil), floors...)

	// Group reduce: minimum score and minimum floor per paragraph, plus the
	// unelidable pin — any sibling at the unelidable score ceiling (a
	// protected origin's clamped score, or the fail-open keep score) pins
	// the whole group to keep.
	type minima struct{ score, floor float64 }
	groups := make(map[int]*minima)
	pinned := make(map[int]bool)
	for i, seg := range segs {
		if seg.Group <= 0 {
			continue
		}
		if scores[i] >= unelidableScore {
			pinned[seg.Group] = true
		}
		m, ok := groups[seg.Group]
		if !ok {
			m = &minima{score: decide[i], floor: decFloors[i]}
			groups[seg.Group] = m
			continue
		}
		if decide[i] < m.score {
			m.score = decide[i]
		}
		if decFloors[i] < m.floor {
			m.floor = decFloors[i]
		}
	}
	for i, seg := range segs {
		if seg.Group <= 0 {
			continue
		}
		m := groups[seg.Group]
		decide[i] = m.score
		decFloors[i] = m.floor
		if pinned[seg.Group] {
			// Protection is absolute and the paragraph decides as one
			// unit, so the unit keeps. Recording the ceiling as the
			// decision score — never the raw group minimum — keeps every
			// score-vs-floor read (the recorded decision, Stats,
			// FalseNegativeRate, ReplayTable) reproducing the keep a
			// mutating run would make.
			decide[i] = unelidableScore
		}
	}
	return decide, decFloors
}
