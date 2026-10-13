package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"late/internal/client"
	"late/internal/common"
)

func TestStreamingTranscriptMatchesCompletedMarkdown(t *testing.T) {
	cases := []string{
		"first paragraph\n\nsecond paragraph",
		"first\n\n\n\nsecond\n\n",
		"1. first\n\n2. second\n\n3. third",
		"- first\n\n  continued\n\n- second",
		"See [docs][ref].\n\n[ref]: https://example.com",
		"~~~text\nfirst\n\n**literal**\n~~~",
		"  ```text\nfirst\n\n**literal**\n  ```",
		"````text\n```\n\n**literal**\n````",
		"before\n\n```go\nfoo\n\nbar",
		"> quote\n>\n> next paragraph",
		"header\n---\n\nbody",
		"| a | b |\n| --- | --- |\n| c | d |",
		"Term\n: definition\n\nnext",
		"    indented code\n\n    next line",
		"hello &amp; goodbye \\*literal\\*",
		"See https://example.com and test@example.com.",
		"  leading spaces\nand a soft break  \nand a hard break",
		"hello\tworld\r\nnext",
		"hello 世界 👩🏽‍💻 café é\n\n" + strings.Repeat("界 a ", 30),
		strings.Repeat("plain prose ", 50),
		strings.Repeat("x", 180),
		"first\n\n" + strings.Repeat("word ", 16) + "1. still prose",
	}
	for _, width := range []int{1, 12, 13, 20, 45, 100} {
		for i, content := range cases {
			t.Run(fmt.Sprintf("width_%d/case_%d", width, i), func(t *testing.T) {
				m, s := newViewportBenchmarkModel(nil)
				m.Viewport.SetWidth(width)
				s.State = StateStreaming
				// Exercise changing parse meaning, retained rows and cache hits.
				for _, prefix := range []string{content[:len(content)/2], content, content} {
					s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: prefix}
					got := ansi.Strip(testTranscriptContent(m))
					done, _ := newViewportBenchmarkModel([]client.ChatMessage{{Role: "assistant", Content: client.TextContent(prefix)}})
					done.Viewport.SetWidth(width)
					want := ansi.Strip(testTranscriptContent(done))
					if got != want {
						t.Fatalf("source %q\nstreaming: %q\ncompleted: %q", prefix, got, want)
					}
				}
			})
		}
	}
}

func TestStreamingReasoningMatchesCompleted(t *testing.T) {
	for _, width := range []int{1, 12, 20, 45, 100} {
		for _, content := range []string{
			strings.Repeat("reasoning text ", 100),
			"first\n\nsecond\n",
			"  indented  \ntrailing spaces   ",
			strings.Repeat("世界 👩🏽‍💻 ", 50),
			"\x1b[31mred\nmore red\x1b[0m",
		} {
			m, s := newViewportBenchmarkModel(nil)
			m.Viewport.SetWidth(width)
			s.State = StateStreaming
			s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), ReasoningContent: content, Content: "answer"}
			got := ansi.Strip(testTranscriptContent(m))
			done, _ := newViewportBenchmarkModel([]client.ChatMessage{{Role: "assistant", ReasoningContent: content, Content: client.TextContent("answer")}})
			done.Viewport.SetWidth(width)
			want := ansi.Strip(testTranscriptContent(done))
			if got != want {
				t.Fatalf("source %q\nstreaming: %q\ncompleted: %q", content, got, want)
			}
		}
	}
}

func TestTranscriptThemeChangeInvalidatesRows(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: "first\n\nsecond"}
	renderTestTranscript(m)
	// Paragraph decoration must use the theme's full Markdown rendering.
	theme := []byte(strings.Replace(string(LateTheme), `"paragraph": {`, `"paragraph": {"block_prefix": "PREFIX ",`, 1))
	m.activeThemeStyles = theme
	got := testTranscriptContent(m)
	done, _ := newViewportBenchmarkModel([]client.ChatMessage{{Role: "assistant", Content: client.TextContent(s.StreamingState.Content)}})
	done.activeThemeStyles = theme
	want := testTranscriptContent(done)
	if got != want || !strings.Contains(ansi.Strip(got), "PREFIX") {
		t.Fatalf("theme change mismatch\nstreaming: %q\ncompleted: %q", got, want)
	}
	if _, ok := s.Transcript.fragments["plain:first"]; ok {
		t.Fatal("theme change retained plain rows")
	}
}

func TestTranscriptRowCacheSurvivesRefreshAndExpires(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: "first\n\nsecond"}
	renderTestTranscript(m)
	first := s.Transcript.fragments["plain:first"]
	if len(first) == 0 {
		t.Fatal("missing cached row")
	}
	for _, content := range []string{"first\n\nsecond", "first\n\nsecond more"} {
		s.StreamingState.Content = content
		renderTestTranscript(m)
		cached := s.Transcript.fragments["plain:first"]
		if len(cached) == 0 || &first[0] != &cached[0] {
			t.Fatal("unchanged row was rendered again")
		}
	}
	if _, ok := s.Transcript.fragments["plain:second"]; ok {
		t.Fatal("obsolete tail retained")
	}
	m.Viewport.SetWidth(45)
	renderTestTranscript(m)
	if &first[0] == &s.Transcript.fragments["plain:first"][0] {
		t.Fatal("resize retained stale rows")
	}
	s.State = StateIdle
	s.StreamingState = common.ContentEvent{}
	renderTestTranscript(m)
	if len(s.Transcript.fragments) != 0 {
		t.Fatal("completed stream retained its row cache")
	}
}
