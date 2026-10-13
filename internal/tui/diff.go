package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type diffKind int

const (
	diffKeep diffKind = iota
	diffAdd
	diffDel
	// diffGap stands for a run of unchanged lines that was collapsed away; n
	// holds how many.
	diffGap
)

// diffLine is one row of a line diff. oldNo/newNo are 1-based line numbers on
// the respective side, or 0 when the side does not apply (or is unknown).
type diffLine struct {
	kind         diffKind
	text         string
	oldNo, newNo int
	n            int
}

// maxDiffCells bounds the LCS table (len(old)*len(new) after common prefix and
// suffix are trimmed). Past it the middle is shown as a plain delete+add block,
// which is still correct, just less minimal.
const maxDiffCells = 1 << 20

// splitDiffLines splits text on newlines, normalising CRLF and dropping the
// empty element a trailing newline would produce.
func splitDiffLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffLines computes a line diff of oldText against newText. firstLine is the
// 1-based line number the first line of both sides sits at; pass 0 when the
// position is unknown and the result carries no line numbers.
func diffLines(oldText, newText string, firstLine int) []diffLine {
	a, b := splitDiffLines(oldText), splitDiffLines(newText)

	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	ops := make([]diffLine, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		ops = append(ops, diffLine{kind: diffKeep, text: l})
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]
	ops = append(ops, diffMiddle(am, bm)...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffLine{kind: diffKeep, text: l})
	}

	if firstLine > 0 {
		oldNo, newNo := firstLine, firstLine
		for i := range ops {
			switch ops[i].kind {
			case diffKeep:
				ops[i].oldNo, ops[i].newNo = oldNo, newNo
				oldNo++
				newNo++
			case diffDel:
				ops[i].oldNo = oldNo
				oldNo++
			case diffAdd:
				ops[i].newNo = newNo
				newNo++
			}
		}
	}
	return ops
}

func diffMiddle(a, b []string) []diffLine {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	if len(a)*len(b) > maxDiffCells || len(a) == 0 || len(b) == 0 {
		ops := make([]diffLine, 0, len(a)+len(b))
		for _, l := range a {
			ops = append(ops, diffLine{kind: diffDel, text: l})
		}
		for _, l := range b {
			ops = append(ops, diffLine{kind: diffAdd, text: l})
		}
		return ops
	}

	// t[i*(m+1)+j] is the LCS length of a[i:] and b[j:].
	n, m := len(a), len(b)
	t := make([]int32, (n+1)*(m+1))
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i*(m+1)+j] = t[(i+1)*(m+1)+j+1] + 1
			} else {
				t[i*(m+1)+j] = max(t[(i+1)*(m+1)+j], t[i*(m+1)+j+1])
			}
		}
	}
	ops := make([]diffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffLine{kind: diffKeep, text: a[i]})
			i++
			j++
		case t[(i+1)*(m+1)+j] >= t[i*(m+1)+j+1]:
			ops = append(ops, diffLine{kind: diffDel, text: a[i]})
			i++
		default:
			ops = append(ops, diffLine{kind: diffAdd, text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffLine{kind: diffDel, text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffLine{kind: diffAdd, text: b[j]})
	}
	return ops
}

// diffStats counts added and deleted lines.
func diffStats(ops []diffLine) (added, deleted int) {
	for _, op := range ops {
		switch op.kind {
		case diffAdd:
			added++
		case diffDel:
			deleted++
		}
	}
	return added, deleted
}

// collapseDiff keeps every changed line plus ctx lines of context around it and
// replaces longer unchanged runs with a single diffGap row. A run of exactly
// one line is kept as-is: a gap row would take the same space and say less.
func collapseDiff(ops []diffLine, ctx int) []diffLine {
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == diffKeep {
			continue
		}
		for j := max(0, i-ctx); j <= min(len(ops)-1, i+ctx); j++ {
			keep[j] = true
		}
	}
	out := make([]diffLine, 0, len(ops))
	run := 0
	flush := func(end int) {
		switch run {
		case 0:
		case 1:
			out = append(out, ops[end-1])
		default:
			out = append(out, diffLine{kind: diffGap, n: run})
		}
		run = 0
	}
	for i, op := range ops {
		if keep[i] {
			flush(i)
			out = append(out, op)
			continue
		}
		run++
	}
	flush(len(ops))
	return out
}

func decimalWidth(n int) int {
	w := 1
	for n >= 10 {
		n /= 10
		w++
	}
	return w
}

// renderDiffRows draws ops as full-width rows: a line-number gutter, a +/-
// sign and the text, with added and removed lines tinted across the whole row.
// Rows longer than width are wrapped, not cut: an approval preview that hides
// the end of a line defeats its purpose. At most maxRows rows are produced;
// hidden reports how many diff lines (not rows) did not make it.
func renderDiffRows(ops []diffLine, width, maxRows int) (rows []string, hidden int) {
	width = max(width, 12)

	maxNo := 0
	for _, op := range ops {
		maxNo = max(maxNo, op.oldNo, op.newNo)
	}
	gutter := 0
	if maxNo > 0 {
		gutter = decimalWidth(maxNo) + 1
	}
	textW := max(1, width-gutter-2)

	pad := func(s string, bg lipgloss.Style) string {
		if w := ansi.StringWidth(s); w < width {
			s += bg.Render(strings.Repeat(" ", width-w))
		}
		return s
	}

	for idx, op := range ops {
		if len(rows) >= maxRows {
			return rows, len(ops) - idx
		}
		base := lipgloss.NewStyle().Background(appBgColor)
		if op.kind == diffGap {
			label := fmt.Sprintf("⋯ %d unchanged lines", op.n)
			rows = append(rows, pad(strings.Repeat(" ", gutter)+base.Foreground(mutedTextColor).Render(label), base))
			continue
		}

		var sign string
		var signStyle, textStyle lipgloss.Style
		switch op.kind {
		case diffAdd:
			base = base.Background(diffAddBgColor)
			signStyle = base.Foreground(accentEmerald).Bold(true)
			textStyle = base.Foreground(textColor)
			sign = "+"
		case diffDel:
			base = base.Background(diffDelBgColor)
			signStyle = base.Foreground(accentCoral).Bold(true)
			textStyle = base.Foreground(subtextColor)
			sign = "-"
		default:
			signStyle = base.Foreground(mutedTextColor)
			textStyle = base.Foreground(subtextColor)
			sign = " "
		}
		no := op.newNo
		if op.kind == diffDel {
			no = op.oldNo
		}

		text := strings.ReplaceAll(op.text, "\t", "    ")
		segments := strings.Split(ansi.Hardwrap(text, textW, true), "\n")
		for si, seg := range segments {
			var lead string
			if gutter > 0 {
				if si == 0 && no > 0 {
					lead = base.Foreground(mutedTextColor).Render(fmt.Sprintf("%*d ", gutter-1, no))
				} else {
					lead = base.Render(strings.Repeat(" ", gutter))
				}
			}
			mark := base.Render(" ")
			if si == 0 {
				mark = signStyle.Render(sign)
			}
			rows = append(rows, pad(lead+mark+base.Render(" ")+textStyle.Render(seg), base))
		}
	}
	return rows, 0
}
