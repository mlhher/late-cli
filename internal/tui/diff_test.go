package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func kinds(ops []diffLine) string {
	var b strings.Builder
	for _, op := range ops {
		switch op.kind {
		case diffKeep:
			b.WriteByte('=')
		case diffAdd:
			b.WriteByte('+')
		case diffDel:
			b.WriteByte('-')
		case diffGap:
			b.WriteByte('~')
		}
	}
	return b.String()
}

func TestDiffLinesReplacedLine(t *testing.T) {
	ops := diffLines("a\nb\nc", "a\nB\nc", 10)
	if got := kinds(ops); got != "=-+=" {
		t.Fatalf("kinds = %q, want =-+=", got)
	}
	// Numbering follows each side independently.
	want := []struct{ old, new int }{{10, 10}, {11, 0}, {0, 11}, {12, 12}}
	for i, w := range want {
		if ops[i].oldNo != w.old || ops[i].newNo != w.new {
			t.Errorf("op %d numbers = (%d,%d), want (%d,%d)", i, ops[i].oldNo, ops[i].newNo, w.old, w.new)
		}
	}
}

func TestDiffLinesWithoutPositionCarryNoNumbers(t *testing.T) {
	for _, op := range diffLines("a", "b", 0) {
		if op.oldNo != 0 || op.newNo != 0 {
			t.Fatalf("expected no line numbers, got %+v", op)
		}
	}
}

func TestDiffLinesEmptySides(t *testing.T) {
	if got := kinds(diffLines("", "x\ny\n", 1)); got != "++" {
		t.Errorf("new file kinds = %q, want ++", got)
	}
	if got := kinds(diffLines("x\ny", "", 1)); got != "--" {
		t.Errorf("deleted content kinds = %q, want --", got)
	}
	if got := diffLines("", "", 1); len(got) != 0 {
		t.Errorf("empty diff = %v, want none", got)
	}
}

func TestDiffLinesIgnoreLineEndingAndTrailingNewline(t *testing.T) {
	added, deleted := diffStats(diffLines("a\r\nb\r\n", "a\nb", 1))
	if added != 0 || deleted != 0 {
		t.Fatalf("CRLF/trailing newline should not register as a change, got +%d -%d", added, deleted)
	}
}

func TestDiffLinesInsertionKeepsSurroundingContext(t *testing.T) {
	ops := diffLines("one\ntwo\nthree", "one\ntwo\nnew\nthree", 1)
	if got := kinds(ops); got != "==+=" {
		t.Fatalf("kinds = %q, want ==+=", got)
	}
	if ops[2].text != "new" || ops[2].newNo != 3 {
		t.Errorf("inserted op = %+v", ops[2])
	}
}

func TestDiffLinesTooLargeForLCSStillCorrect(t *testing.T) {
	var a, b []string
	for i := range 1500 {
		a = append(a, fmt.Sprintf("old %d", i))
		b = append(b, fmt.Sprintf("new %d", i))
	}
	added, deleted := diffStats(diffLines(strings.Join(a, "\n"), strings.Join(b, "\n"), 1))
	if added != 1500 || deleted != 1500 {
		t.Fatalf("stats = +%d -%d, want +1500 -1500", added, deleted)
	}
}

func TestCollapseDiff(t *testing.T) {
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	oldText := strings.Join(lines, "\n")
	lines[10] = "changed"
	ops := collapseDiff(diffLines(oldText, strings.Join(lines, "\n"), 1), 2)

	if got := kinds(ops); got != "~==-+==~" {
		t.Fatalf("kinds = %q, want ~==-+==~", got)
	}
	// 21 ops: ten unchanged lines, the replaced line (delete + add), nine more.
	// Two lines of context either side of the change leave 8 and 7 collapsed.
	if ops[0].n != 8 || ops[len(ops)-1].n != 7 {
		t.Errorf("gap sizes = %d and %d, want 8 and 7", ops[0].n, ops[len(ops)-1].n)
	}
}

func TestCollapseDiffKeepsSingleLineGapsAsLines(t *testing.T) {
	// One unchanged line sits between two distant changes: a "1 unchanged line"
	// row would take the same space as the line itself.
	lines := []string{"a", "b", "c", "d", "e", "f", "g"}
	edited := append([]string(nil), lines...)
	edited[0], edited[6] = "A", "G"
	ops := collapseDiff(diffLines(strings.Join(lines, "\n"), strings.Join(edited, "\n"), 1), 2)
	for _, op := range ops {
		if op.kind == diffGap && op.n == 1 {
			t.Fatalf("single-line gap not expanded: %s", kinds(ops))
		}
	}
}

func TestCollapseDiffIdenticalIsOneGap(t *testing.T) {
	ops := collapseDiff(diffLines("a\nb\nc\nd", "a\nb\nc\nd", 1), 2)
	if got := kinds(ops); got != "~" || ops[0].n != 4 {
		t.Fatalf("got %q (%+v), want a single 4-line gap", got, ops)
	}
}

func TestRenderDiffRowsFillWidthAndWrapLongLines(t *testing.T) {
	const width = 40
	long := strings.Repeat("0123456789", 8) // 80 cells
	ops := diffLines("short\n\tindented", "short\n"+long, 5)

	rows, hidden := renderDiffRows(ops, width, 100)
	if hidden != 0 {
		t.Fatalf("hidden = %d, want 0", hidden)
	}
	if len(rows) < 4 {
		t.Fatalf("expected the long line to wrap onto several rows, got %d rows", len(rows))
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != width {
			t.Errorf("row %d width = %d, want %d: %q", i, w, width, ansi.Strip(row))
		}
	}
	joined := ansi.Strip(strings.Join(rows, ""))
	if !strings.Contains(strings.ReplaceAll(joined, " ", ""), long) {
		t.Errorf("wrapped text lost characters:\n%s", ansi.Strip(strings.Join(rows, "\n")))
	}
}

func TestRenderDiffRowsMaxRowsReportsHidden(t *testing.T) {
	ops := diffLines("", "1\n2\n3\n4\n5\n6", 1)
	rows, hidden := renderDiffRows(ops, 30, 4)
	if len(rows) != 4 || hidden != 2 {
		t.Fatalf("rows = %d, hidden = %d, want 4 and 2", len(rows), hidden)
	}
}

func TestRenderDiffRowsSignsAndGutter(t *testing.T) {
	rows, _ := renderDiffRows(diffLines("keep\nold", "keep\nnew", 98), 30, 10)
	plain := make([]string, len(rows))
	for i, r := range rows {
		plain[i] = strings.TrimRight(ansi.Strip(r), " ")
	}
	want := []string{"98   keep", "99 - old", "99 + new"}
	for i := range want {
		if plain[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, plain[i], want[i])
		}
	}
}
