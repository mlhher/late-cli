package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var sampleFiles = []string{
	"README.md",
	"cmd/late/main.go",
	"internal/tui/model.go",
	"internal/tui/update.go",
	"internal/tui/update_test.go",
	"internal/tool/targetEdit.go",
	"docs/architecture.md",
}

func TestFileIndexRanking(t *testing.T) {
	idx := newFileIndex(".", sampleFiles)
	cases := []struct {
		query string
		first string
	}{
		{"", "README.md"},                        // shallowest first
		{"main", "cmd/late/main.go"},             // file-name match
		{"mod", "internal/tui/model.go"},         // file-name prefix
		{"update", "internal/tui/update.go"},     // shorter name beats update_test.go
		{"tui/up", "internal/tui/update.go"},     // path substring
		{"arch", "docs/architecture.md"},         // prefix
		{"tgted", "internal/tool/targetEdit.go"}, // subsequence of the name
		{"TUIMO", "internal/tui/model.go"},       // case-insensitive subsequence across the path
	}
	for _, c := range cases {
		got := idx.rank(c.query, 10)
		if len(got) == 0 || got[0].rel != c.first {
			var rels []string
			for _, e := range got {
				rels = append(rels, e.rel)
			}
			t.Errorf("rank(%q) first = %v, want %q", c.query, rels, c.first)
		}
	}
	if got := idx.rank("zzzz", 10); len(got) != 0 {
		t.Errorf("rank(zzzz) = %v, want no matches", got)
	}
	if got := idx.rank("", 3); len(got) != 3 {
		t.Errorf("limit not applied: %d", len(got))
	}
}

func TestMatchPositions(t *testing.T) {
	pos := matchPositions("upd", "internal/tui/update.go")
	for _, i := range []int{13, 14, 15} { // "upd" in "update.go"
		if !pos[i] {
			t.Errorf("position %d should be highlighted: %v", i, pos)
		}
	}
	if len(pos) != 3 {
		t.Errorf("want exactly the contiguous match, got %v", pos)
	}
	if got := matchPositions("tgted", "internal/tool/targetEdit.go"); len(got) != 5 {
		t.Errorf("subsequence should highlight every query rune, got %v", got)
	}
	if matchPositions("", "a/b") != nil || matchPositions("zz", "a/b") != nil {
		t.Error("no match must highlight nothing")
	}
}

// typeText feeds text to the model one key at a time, like a user would.
func typeText(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		res, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		m = res.(Model)
	}
	return m
}

func press(t *testing.T, m Model, key tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	res, cmd := m.Update(key)
	return res.(Model), cmd
}

func newCompletionModel(t *testing.T) (Model, *mockOrchestrator) {
	t.Helper()
	orch := &mockOrchestrator{}
	m := NewModel(orch, nil, nil)
	m.CWD = t.TempDir()
	m.SetSize(100, 30)
	return m, orch
}

func loadIndex(t *testing.T, m Model, files ...string) Model {
	t.Helper()
	res, _ := m.Update(fileIndexLoadedMsg{root: m.projectRoot(), files: files})
	return res.(Model)
}

func TestAtMentionOpensDropdownAfterIndexLoads(t *testing.T) {
	m, _ := newCompletionModel(t)

	m = typeText(t, m, "look at @int")
	if !m.fileIdxLoading {
		t.Fatal("typing an @-mention should start the background file scan")
	}
	if m.ShowAutocomplete {
		t.Error("nothing to show before the scan finishes")
	}

	m = loadIndex(t, m, sampleFiles...)
	if !m.ShowAutocomplete || m.AutocompleteKind != autocompleteFile {
		t.Fatalf("dropdown should open once the index lands: show=%v kind=%v", m.ShowAutocomplete, m.AutocompleteKind)
	}
	if m.fileIdxLoading {
		t.Error("loading flag must clear")
	}
	if len(m.AutocompleteItems) == 0 || !strings.HasPrefix(m.AutocompleteItems[0].Name, "@internal/") {
		t.Errorf("items = %v", m.AutocompleteItems)
	}
	if got := m.autocompleteHeight(); got != min(len(m.AutocompleteItems), maxAutocompleteVisible)+1 {
		t.Errorf("autocompleteHeight = %d", got)
	}
}

func TestTabCompletesTheMentionWithoutSubmitting(t *testing.T) {
	m, orch := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "see @upd")

	m, _ = press(t, m, mockKey{code: '\t', text: "tab"})
	if got := m.Input.Value(); got != "see @internal/tui/update.go " {
		t.Errorf("input = %q", got)
	}
	if m.ShowAutocomplete {
		t.Error("dropdown should close after completing")
	}
	if orch.submitCount != 0 {
		t.Error("tab must not submit")
	}
}

func TestEnterCompletesIncompleteMentionThenSendsCompleteOne(t *testing.T) {
	m, orch := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "@main")

	m, _ = press(t, m, mockKey{code: '\r', text: "enter"})
	if orch.submitCount != 0 {
		t.Fatal("first enter should complete the path, not send")
	}
	if got := m.Input.Value(); got != "@cmd/late/main.go " {
		t.Fatalf("input = %q", got)
	}

	// With the path complete there is no dropdown any more: enter sends.
	_, _ = press(t, m, mockKey{code: '\r', text: "enter"})
	if orch.submitCount != 1 || orch.submittedText != "@cmd/late/main.go " {
		t.Errorf("submit count=%d text=%q", orch.submitCount, orch.submittedText)
	}
}

func TestEnterOnTypedFullPathSends(t *testing.T) {
	m, orch := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "@README.md")

	_, _ = press(t, m, mockKey{code: '\r', text: "enter"})
	if orch.submitCount != 1 || orch.submittedText != "@README.md" {
		t.Errorf("a fully typed path must send as is: count=%d text=%q", orch.submitCount, orch.submittedText)
	}
}

func TestCompletingInTheMiddleKeepsSurroundingText(t *testing.T) {
	m, _ := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "fix @upd now")
	// Move the cursor back to the end of the mention (4 chars: " now").
	for range 4 {
		res, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
		m = res.(Model)
	}
	if !m.ShowAutocomplete {
		t.Fatal("cursor at the end of the mention should show the dropdown")
	}

	m, _ = press(t, m, mockKey{code: '\t', text: "tab"})
	if got := m.Input.Value(); got != "fix @internal/tui/update.go now" {
		t.Errorf("input = %q", got)
	}
}

func TestEscKeepsDropdownClosedUntilTheMentionChanges(t *testing.T) {
	m, _ := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "@mo")
	if !m.ShowAutocomplete {
		t.Fatal("dropdown should be open")
	}

	m, _ = press(t, m, mockKey{code: tea.KeyEscape, text: "esc"})
	if m.ShowAutocomplete {
		t.Error("esc should close the dropdown")
	}
	if m.EscConfirmPending {
		t.Error("esc on the dropdown must not open the quit dialog")
	}

	m = typeText(t, m, "d")
	if !m.ShowAutocomplete {
		t.Error("typing more should bring the dropdown back")
	}
}

func TestEscKeepsCommandDropdownClosedToo(t *testing.T) {
	m, _ := newCompletionModel(t)
	m = typeText(t, m, "/he")
	if !m.ShowAutocomplete || m.AutocompleteKind != autocompleteCommand {
		t.Fatal("command dropdown should be open")
	}
	m, _ = press(t, m, mockKey{code: tea.KeyEscape, text: "esc"})
	if m.ShowAutocomplete {
		t.Error("esc should keep the command dropdown closed")
	}
	m = typeText(t, m, "l")
	if !m.ShowAutocomplete {
		t.Error("editing the command should reopen the dropdown")
	}
}

func TestAddressesAndPlainTextDoNotOpenTheDropdown(t *testing.T) {
	m, _ := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "mail me@example.org")
	if m.ShowAutocomplete {
		t.Error("an e-mail address is not a file mention")
	}
	if m.fileIdxLoading {
		t.Error("and must not trigger a scan")
	}
}

func TestNoMatchesClosesTheDropdown(t *testing.T) {
	m, _ := newCompletionModel(t)
	m = loadIndex(t, m, sampleFiles...)
	m = typeText(t, m, "@zzzzzz")
	if m.ShowAutocomplete {
		t.Error("no file matches: no dropdown")
	}
}

func TestFileSuggestionLineFitsWidthAndKeepsFileName(t *testing.T) {
	item := CommandDef{Name: "@internal/some/very/deeply/nested/directory/structure/update.go"}
	for _, width := range []int{30, 60, 100} {
		for _, selected := range []bool{false, true} {
			line := fileSuggestionLine(item, "upd", selected, width)
			if w := lipgloss.Width(line); w != width {
				t.Errorf("width=%d selected=%v: line is %d cells wide", width, selected, w)
			}
			if !strings.Contains(ansi.Strip(line), "update.go") {
				t.Errorf("width=%d: file name lost in %q", width, ansi.Strip(line))
			}
		}
	}
}
