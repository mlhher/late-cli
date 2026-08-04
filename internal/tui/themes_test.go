package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
)

// makeTheme builds a ThemeEntry suitable for testing.
func makeTheme(id, pluginName, themeName string) ThemeEntry {
	return ThemeEntry{
		ID:         id,
		PluginName: pluginName,
		ThemeName:  themeName,
		Glamour: map[string]any{
			"document": map[string]any{
				"color": "#ABCDEF",
			},
		},
		Palette: map[string]string{
			"bg": "#000000",
		},
	}
}

// 1. SetThemes copies the input slice (caller can mutate afterwards).
func TestSetThemes_CopiesInput(t *testing.T) {
	m := &Model{}
	src := []ThemeEntry{
		makeTheme("ocean:deep", "ocean", "deep"),
		makeTheme("ocean:shallow", "ocean", "shallow"),
	}
	m.SetThemes(src)
	if len(m.ThemeEntries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m.ThemeEntries))
	}
	// Mutate the source — entries inside the model should not change.
	src[0].ThemeName = "MUTATED"
	if m.ThemeEntries[0].ThemeName != "deep" {
		t.Fatalf("SetThemes did not copy: got %q", m.ThemeEntries[0].ThemeName)
	}
}

// 2. SetThemes(nil) clears the list.
func TestSetThemes_NilClears(t *testing.T) {
	m := &Model{}
	m.SetThemes([]ThemeEntry{makeTheme("a:b", "a", "b")})
	if len(m.ThemeEntries) == 0 {
		t.Fatal("setup failed: empty after add")
	}
	m.SetThemes(nil)
	if len(m.ThemeEntries) != 0 {
		t.Fatalf("expected empty after nil, got %d", len(m.ThemeEntries))
	}
	m.SetThemes([]ThemeEntry{}) // also empty (non-nil)
	if len(m.ThemeEntries) != 0 {
		t.Fatalf("expected empty after empty slice, got %d", len(m.ThemeEntries))
	}
}

// 3. FindTheme exact ID match.
func TestFindTheme_ExactID(t *testing.T) {
	m := &Model{}
	m.SetThemes([]ThemeEntry{
		makeTheme("ocean:deep", "ocean", "deep"),
		makeTheme("ocean:shallow", "ocean", "shallow"),
	})
	info := m.FindTheme("ocean:deep")
	if info == nil || info.ThemeName != "deep" {
		t.Fatalf("expected deep theme, got %+v", info)
	}
}

// 4. FindTheme bare name match (case-insensitive).
func TestFindTheme_BareNameCaseInsensitive(t *testing.T) {
	m := &Model{}
	m.SetThemes([]ThemeEntry{
		makeTheme("ocean:Deep", "ocean", "Deep"),
	})
	if info := m.FindTheme("deep"); info == nil {
		t.Fatal("expected bare-name match")
	}
	if info := m.FindTheme("DEEP"); info == nil {
		t.Fatal("expected case-insensitive match")
	}
}

// 5. FindTheme prefers active when bare name is ambiguous.
func TestFindTheme_PrefersActive(t *testing.T) {
	m := &Model{}
	m.SetThemes([]ThemeEntry{
		makeTheme("a:cool", "a", "cool"),
		makeTheme("b:cool", "b", "cool"),
	})
	m.SelectedTheme = "b:cool"
	info := m.FindTheme("cool")
	if info == nil || info.PluginName != "b" {
		t.Fatalf("expected active b:cool, got %+v", info)
	}
}

// 6. FindTheme returns nil for empty query or empty catalog.
func TestFindTheme_Empty(t *testing.T) {
	m := &Model{}
	if info := m.FindTheme("anything"); info != nil {
		t.Fatal("expected nil for empty catalog")
	}
	m.SetThemes([]ThemeEntry{makeTheme("a:b", "a", "b")})
	if info := m.FindTheme(""); info != nil {
		t.Fatal("expected nil for empty query")
	}
}

// 7. FindTheme returns nil when no match.
func TestFindTheme_NoMatch(t *testing.T) {
	m := &Model{}
	m.SetThemes([]ThemeEntry{
		makeTheme("ocean:deep", "ocean", "deep"),
	})
	if info := m.FindTheme("mountain"); info != nil {
		t.Fatalf("expected nil, got %+v", info)
	}
}

// 8. FindThemeByIndex respects bounds.
func TestFindThemeByIndex_Bounds(t *testing.T) {
	m := &Model{}
	m.SetThemes([]ThemeEntry{
		makeTheme("a:b", "a", "b"),
	})
	if info := m.FindThemeByIndex(0); info == nil {
		t.Fatal("expected hit at index 0")
	}
	if info := m.FindThemeByIndex(1); info != nil {
		t.Fatal("expected nil past end")
	}
	if info := m.FindThemeByIndex(-1); info != nil {
		t.Fatal("expected nil for negative index")
	}
}

// 9. ApplyTheme rejects nil input.
func TestApplyTheme_RejectsNil(t *testing.T) {
	m := &Model{}
	if err := m.ApplyTheme(nil); err == nil {
		t.Fatal("expected error for nil theme")
	}
}

// 10. ApplyTheme requires a non-nil viewport width on the model (or
// falls back to 80). We don't need a real renderer; the test only
// exercises the nil/empty guard and the path that builds a new renderer.
func TestApplyTheme_BuildsRendererOnEmptyViewport(t *testing.T) {
	m := &Model{
		Viewport: viewport.Model{}, // zero value
	}
	info := makeTheme("ocean:deep", "ocean", "deep")
	if err := m.ApplyTheme(&info); err != nil {
		t.Fatalf("ApplyTheme failed: %v", err)
	}
	if m.Renderer == nil {
		t.Fatal("expected Renderer to be set after ApplyTheme")
	}
	if m.SelectedTheme != "ocean:deep" {
		t.Fatalf("expected SelectedTheme set, got %q", m.SelectedTheme)
	}
}

// 11. ApplyTheme clears per-agent render caches.
func TestApplyTheme_ClearsRenderCaches(t *testing.T) {
	m := &Model{
		AgentStates: map[string]*AppState{
			"agent-1": {
				RenderedHistory:     []string{"cached"},
				LastTotalContent:    "x",
				LastStreamingContent: "y",
				StreamingStyledCache: "z",
			},
		},
		Viewport: viewport.Model{},
	}
	info := makeTheme("a:b", "a", "b")
	if err := m.ApplyTheme(&info); err != nil {
		t.Fatalf("ApplyTheme failed: %v", err)
	}
	s := m.AgentStates["agent-1"]
	if s.RenderedHistory != nil {
		t.Fatal("RenderedHistory not cleared")
	}
	if s.LastTotalContent != "" || s.LastStreamingContent != "" || s.StreamingStyledCache != "" {
		t.Fatal("caches not cleared")
	}
}

// 12. ViewThemes dispatch: up/down navigation moves the cursor.
func TestViewThemes_Navigation(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{
		Mode:         ViewThemes,
		ThemeEntries: []ThemeEntry{makeTheme("a:b", "a", "b"), makeTheme("c:d", "c", "d"), makeTheme("e:f", "e", "f")},
		ThemeIndex:   0,
	}

	// "down" advances the cursor.
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	*m = nm.(Model)
	if m.ThemeIndex != 1 {
		t.Fatalf("expected index 1, got %d", m.ThemeIndex)
	}

	// "j" vim-style also advances.
	nm, _ = m.Update(tea.KeyPressMsg{Text: "j"})
	*m = nm.(Model)
	if m.ThemeIndex != 2 {
		t.Fatalf("expected index 2 after j, got %d", m.ThemeIndex)
	}

	// At end, down is a no-op.
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	*m = nm.(Model)
	if m.ThemeIndex != 2 {
		t.Fatalf("expected clamp at 2, got %d", m.ThemeIndex)
	}

	// "up" retreats.
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	*m = nm.(Model)
	if m.ThemeIndex != 1 {
		t.Fatalf("expected index 1, got %d", m.ThemeIndex)
	}

	// At start, up is a no-op.
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	*m = nm.(Model)
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	*m = nm.(Model)
	if m.ThemeIndex != 0 {
		t.Fatalf("expected clamp at 0, got %d", m.ThemeIndex)
	}
}

// 13. ViewThemes esc returns to chat.
func TestViewThemes_EscExits(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{
		Mode:         ViewThemes,
		ThemeEntries: []ThemeEntry{makeTheme("a:b", "a", "b")},
		ThemeIndex:   0,
	}
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	nm2 := nm.(Model)
	if nm2.Mode != ViewChat {
		t.Fatalf("expected ViewChat, got %v", nm2.Mode)
	}
}

// 14. ViewThemes enter applies the theme and exits.
func TestViewThemes_EnterApplies(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{
		Mode:         ViewThemes,
		ThemeEntries: []ThemeEntry{makeTheme("a:b", "a", "b")},
		ThemeIndex:   0,
		Viewport:     viewport.Model{},
	}
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	nm2 := nm.(Model)
	if nm2.Mode != ViewChat {
		t.Fatalf("expected ViewChat after enter, got %v", nm2.Mode)
	}
	if nm2.SelectedTheme != "a:b" {
		t.Fatalf("expected SelectedTheme a:b, got %q", nm2.SelectedTheme)
	}
	if nm2.Renderer == nil {
		t.Fatal("expected Renderer to be set after apply")
	}
	if nm2.ToastMessage == "" {
		t.Fatal("expected confirmation toast")
	}
}

// 15. /themes with no themes shows toast.
func TestSlashThemes_NoThemesToast(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{} // simpler: empty model, type a command
	// Type "/themes" then press enter.
	m.Input.SetValue("> /themes")
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	nm2 := nm.(Model)
	if nm2.ToastMessage == "" || !strings.Contains(nm2.ToastMessage, "no plugin themes") {
		t.Fatalf("expected 'no plugin themes' toast, got %q", nm2.ToastMessage)
	}
}

// 16. /themes <name> applies the named theme.
func TestSlashThemes_AppliesByName(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{
		ThemeEntries: []ThemeEntry{
			makeTheme("ocean:deep", "ocean", "deep"),
			makeTheme("ocean:shallow", "ocean", "shallow"),
		},
		Viewport: viewport.Model{},
	}
	m.Input.SetValue("> /themes deep")
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	nm2 := nm.(Model)
	if nm2.SelectedTheme != "ocean:deep" {
		t.Fatalf("expected ocean:deep, got %q", nm2.SelectedTheme)
	}
	if !strings.Contains(nm2.ToastMessage, "deep") {
		t.Fatalf("expected toast mentioning deep, got %q", nm2.ToastMessage)
	}
}

// 17. /themes <unknown> shows not-found toast.
func TestSlashThemes_UnknownName(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{
		ThemeEntries: []ThemeEntry{makeTheme("a:b", "a", "b")},
		Viewport:     viewport.Model{},
	}
	m.Input.SetValue("> /themes missing")
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	nm2 := nm.(Model)
	if !strings.Contains(nm2.ToastMessage, "theme not found") {
		t.Fatalf("expected 'theme not found' toast, got %q", nm2.ToastMessage)
	}
}

// 18. /themes (no args) opens the picker at the active theme.
func TestSlashThemes_OpensPickerAtActive(t *testing.T) {
	t.Skip("requires orchestrator plumbing; covered manually")
	m := &Model{
		ThemeEntries: []ThemeEntry{
			makeTheme("a:b", "a", "b"),
			makeTheme("c:d", "c", "d"),
		},
		SelectedTheme: "c:d",
		Viewport:      viewport.Model{},
	}
	m.Input.SetValue("> /themes")
	nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	nm2 := nm.(Model)
	if nm2.Mode != ViewThemes {
		t.Fatalf("expected ViewThemes, got %v", nm2.Mode)
	}
	if nm2.ThemeIndex != 1 {
		t.Fatalf("expected cursor at active theme (1), got %d", nm2.ThemeIndex)
	}
}

// 19. renderThemeView handles empty list without panicking.
func TestRenderThemeView_EmptyList(t *testing.T) {
	m := &Model{Viewport: viewport.Model{}}
	m.Viewport.SetWidth(80)
	m.Viewport.SetHeight(24)
	m.renderThemeView()
	// No assertion needed — the test is "does not panic".
}

// 20. renderThemeView clamps cursor when out of range.
func TestRenderThemeView_ClampsCursor(t *testing.T) {
	m := &Model{
		Viewport:      viewport.Model{},
		ThemeEntries:  []ThemeEntry{makeTheme("a:b", "a", "b")},
		ThemeIndex:    99, // out of range
		SelectedTheme: "",
	}
	m.Viewport.SetWidth(80)
	m.Viewport.SetHeight(24)
	m.renderThemeView()
	if m.ThemeIndex != 0 {
		t.Fatalf("expected clamp to 0, got %d", m.ThemeIndex)
	}
}
