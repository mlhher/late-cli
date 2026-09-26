package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Row caches belong to the active response and are replaced, not mutated, by
// each worker. Cache values already include styling, clipping and padding.
// Reparse the document on each update: even an appended reference definition
// or delimiter can change the meaning of text much earlier in the response.
type transcriptRowRenderer struct {
	width      int
	background color.Color
	answer     lipgloss.Style
	thought    lipgloss.Style
	markdown   func(string) string
	plainTheme bool
	old, next  map[string][]string
}

func (r *transcriptRowRenderer) normalize(s string) []string {
	rows := strings.Split(s, "\n")
	padding := lipgloss.NewStyle().Background(r.background)
	for i, row := range rows {
		w := ansi.StringWidth(row)
		if w > r.width {
			row = ansi.Truncate(row, r.width, "")
			w = ansi.StringWidth(row)
		}
		if w < r.width {
			row += padding.Render(strings.Repeat(" ", r.width-w))
		}
		rows[i] = row
	}
	return rows
}

func (r *transcriptRowRenderer) cached(kind, source string, render func() string) []string {
	key := kind + source
	rows, ok := r.next[key]
	if !ok {
		rows, ok = r.old[key]
		if !ok {
			rows = r.normalize(render())
		}
		r.next[key] = rows
	}
	return rows
}

func (r *transcriptRowRenderer) prose(source string) []string {
	// Custom themes may add paragraph/document margins or prefixes; keep the
	// full renderer authoritative for those and for all structured Markdown.
	// At a one-column content width, wide graphemes overflow and Glamour's
	// nested wrapping passes affect line count. Keep that edge case exact.
	if r.plainTheme && assistantReplyContentWidth(r.width) > 1 {
		if paragraphs, ok := plainTranscriptParagraphs(source); ok {
			var rows []string
			for i, paragraph := range paragraphs {
				if i > 0 {
					rows = append(rows, r.cached("plain:", "", func() string { return r.answer.Render("") })...)
				}
				for line := range strings.SplitSeq(ansi.Wrap(paragraph, assistantReplyContentWidth(r.width), ""), "\n") {
					rows = append(rows, r.cached("plain:", line, func() string {
						return r.answer.Foreground(lipgloss.Color("#E6EDF3")).Render(line)
					})...)
				}
			}
			return rows
		}
	}
	return r.cached("markdown:", source, func() string {
		return r.answer.Render(strings.Trim(r.markdown(source), "\r\n"))
	})
}

func (r *transcriptRowRenderer) reasoning(source string) []string {
	// ANSI state can span lines, so leave escaped content to Lip Gloss as a
	// whole. Ordinary reasoning has a uniform style and can reuse wrapped rows.
	if strings.ContainsAny(source, "\x1b\r\t") {
		return r.cached("reasoning:", source, func() string { return r.thought.Render(source) })
	}
	width := r.thought.GetWidth() - r.thought.GetHorizontalPadding() - r.thought.GetHorizontalBorderSize()
	if width <= 0 {
		return r.cached("reasoning:", source, func() string { return r.thought.Render(source) })
	}
	var rows []string
	for line := range strings.SplitSeq(ansi.Wrap(source, width, ""), "\n") {
		rows = append(rows, r.cached("thought:", line, func() string { return r.thought.Render(line) })...)
	}
	return rows
}

// Use the same Markdown extensions as Glamour. A fast path based on punctuation
// heuristics would misclassify autolinks, lists, setext headings and references.
func plainTranscriptParagraphs(source string) ([]string, bool) {
	if strings.ContainsAny(source, "&\\\x1b\r\t") {
		return nil, false
	}
	data := []byte(source)
	document := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList)).Parser().Parse(text.NewReader(data))
	var paragraphs []string
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		if node.Kind() != ast.KindParagraph {
			return nil, false
		}
		var paragraph strings.Builder
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			t, ok := child.(*ast.Text)
			if !ok || t.IsRaw() {
				return nil, false
			}
			paragraph.Write(t.Value(data))
			if t.SoftLineBreak() || t.HardLineBreak() {
				paragraph.WriteByte('\n')
			}
		}
		paragraphs = append(paragraphs, paragraph.String())
	}
	return paragraphs, len(paragraphs) > 0
}
