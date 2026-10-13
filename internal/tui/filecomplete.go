package tui

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"late/internal/tool"
)

// @file completion: typing "@" in the prompt opens a dropdown of project files
// that narrows as the path is typed. Accepting one inserts "@path" — the agent
// reads the file with its normal tools, nothing is attached or expanded.

// autocompleteKind says what the open dropdown is completing.
type autocompleteKind int

const (
	autocompleteCommand autocompleteKind = iota // "/command"
	autocompleteFile                            // "@path/to/file"
)

const (
	// maxIndexedFiles bounds the in-memory file list of very large projects.
	maxIndexedFiles = 20000
	// maxFileSuggestions bounds the ranked list handed to the dropdown.
	maxFileSuggestions = 50
	// fileIndexTTL is how long an index is trusted before the next "@"
	// refreshes it in the background (the stale one keeps serving meanwhile).
	fileIndexTTL = 30 * time.Second
)

// fileEntry is one indexed file, with its lowercase form precomputed so a
// keystroke does not allocate per file.
type fileEntry struct {
	rel       string // slash-separated, relative to the project root
	lower     string // rel, rune-wise lowercased (same rune count as rel)
	baseStart int    // byte offset of the file name inside rel and lower
	depth     int    // number of directory separators
}

// fileIndex is a snapshot of the project's files.
type fileIndex struct {
	root     string
	entries  []fileEntry
	loadedAt time.Time
}

// fileIndexLoadedMsg delivers a finished background scan to the update loop.
type fileIndexLoadedMsg struct {
	root  string
	files []string
}

// loadFileIndexCmd scans root off the update loop.
func loadFileIndexCmd(root string) tea.Cmd {
	return func() tea.Msg {
		// A partial list from a failed walk is still worth offering.
		files, _ := tool.ListProjectFiles(root, maxIndexedFiles)
		return fileIndexLoadedMsg{root: root, files: files}
	}
}

func newFileIndex(root string, files []string) *fileIndex {
	idx := &fileIndex{root: root, loadedAt: time.Now(), entries: make([]fileEntry, 0, len(files))}
	for _, f := range files {
		lower := strings.Map(unicode.ToLower, f)
		idx.entries = append(idx.entries, fileEntry{
			rel:       f,
			lower:     lower,
			baseStart: strings.LastIndexByte(f, '/') + 1,
			depth:     strings.Count(f, "/"),
		})
	}
	return idx
}

// rank returns the entries matching query, best first. Matches are tiered:
// file-name prefix, file-name substring, path substring, then a subsequence of
// the file name and of the whole path; shorter names win within a tier. An
// empty query lists the shallowest files first.
func (idx *fileIndex) rank(query string, limit int) []fileEntry {
	q := strings.Map(unicode.ToLower, strings.TrimPrefix(query, "./"))

	type scored struct {
		e    fileEntry
		tier int
	}
	var hits []scored
	for _, e := range idx.entries {
		tier := 0
		if q != "" {
			base := e.lower[e.baseStart:]
			switch {
			case strings.HasPrefix(base, q):
				tier = 0
			case strings.Contains(base, q):
				tier = 1
			case strings.Contains(e.lower, q):
				tier = 2
			case isSubsequence(q, base):
				tier = 3
			case isSubsequence(q, e.lower):
				tier = 4
			default:
				continue
			}
		}
		hits = append(hits, scored{e: e, tier: tier})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if q == "" {
			if a.e.depth != b.e.depth {
				return a.e.depth < b.e.depth
			}
			return a.e.rel < b.e.rel
		}
		if al, bl := len(a.e.rel)-a.e.baseStart, len(b.e.rel)-b.e.baseStart; al != bl {
			return al < bl
		}
		if len(a.e.rel) != len(b.e.rel) {
			return len(a.e.rel) < len(b.e.rel)
		}
		return a.e.rel < b.e.rel
	})

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]fileEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// isSubsequence reports whether the runes of q appear in s in order.
func isSubsequence(q, s string) bool {
	qr := []rune(q)
	if len(qr) == 0 {
		return true
	}
	i := 0
	for _, r := range s {
		if r == qr[i] {
			i++
			if i == len(qr) {
				return true
			}
		}
	}
	return false
}

// matchPositions returns the rune positions of rel to highlight for query: the
// last contiguous occurrence if there is one (the file name sits at the end),
// otherwise a subsequence matched from the right.
func matchPositions(query, rel string) map[int]bool {
	q := []rune(strings.Map(unicode.ToLower, strings.TrimPrefix(query, "./")))
	if len(q) == 0 {
		return nil
	}
	lower := strings.Map(unicode.ToLower, rel)
	pos := map[int]bool{}
	if at := strings.LastIndex(lower, string(q)); at >= 0 {
		start := utf8.RuneCountInString(lower[:at])
		for i := range q {
			pos[start+i] = true
		}
		return pos
	}
	lr := []rune(lower)
	qi := len(q) - 1
	for i := len(lr) - 1; i >= 0 && qi >= 0; i-- {
		if lr[i] == q[qi] {
			pos[i] = true
			qi--
		}
	}
	if qi >= 0 {
		return nil
	}
	return pos
}

// fileToken returns the "@query" token that ends at the cursor, or ok=false
// when the cursor is not at the end of an @-mention. A mention only counts at
// the start of a word, so an e-mail address never opens the dropdown.
func (m Model) fileToken() (query string, runeLen int, ok bool) {
	lines := strings.Split(m.Input.Value(), "\n")
	row := m.Input.Line()
	if row < 0 || row >= len(lines) {
		return "", 0, false
	}
	line := []rune(lines[row])
	col := min(m.Input.Column(), len(line))
	start := col
	for start > 0 && !unicode.IsSpace(line[start-1]) {
		start--
	}
	token := line[start:col]
	if len(token) == 0 || token[0] != '@' {
		return "", 0, false
	}
	return string(token[1:]), len(token), true
}

// projectRoot is the directory @file completion indexes.
func (m Model) projectRoot() string {
	if m.CWD != "" {
		return m.CWD
	}
	return "."
}

// fileIndexNeeded reports whether a scan should be started because the cursor
// is in an @-mention and there is no fresh index for the project.
func (m Model) fileIndexNeeded() bool {
	if m.fileIdxLoading || m.RunningPluginAction != "" {
		return false
	}
	if _, _, ok := m.fileToken(); !ok {
		return false
	}
	root := m.projectRoot()
	return m.fileIdx == nil || m.fileIdx.root != root || time.Since(m.fileIdx.loadedAt) > fileIndexTTL
}

// updateFileAutocomplete opens, narrows or closes the @file dropdown. It
// reports whether the cursor is in an @-mention at all (so the caller leaves
// the slash-command dropdown alone otherwise).
func (m *Model) updateFileAutocomplete() bool {
	query, _, ok := m.fileToken()
	if !ok {
		return false
	}
	trigger := "@" + query
	if m.autocompleteDismissed == trigger {
		m.closeAutocomplete()
		return true
	}
	m.autocompleteDismissed = ""

	if m.fileIdx == nil {
		// The first scan is still running; the dropdown appears when it lands.
		m.closeAutocomplete()
		return true
	}

	sig := trigger + "\x00" + m.fileIdx.loadedAt.String()
	if m.AutocompleteKind == autocompleteFile && m.autocompleteSig == sig {
		return true // same query, same index: keep selection and scroll
	}

	matches := m.fileIdx.rank(query, maxFileSuggestions)
	if len(matches) == 0 {
		m.closeAutocomplete()
		return true
	}
	items := make([]CommandDef, len(matches))
	for i, e := range matches {
		items[i] = CommandDef{Name: "@" + quoteIfSpaced(e.rel)}
	}
	if m.AutocompleteKind != autocompleteFile || m.autocompleteQuery != query {
		m.AutocompleteIndex = 0
		m.AutocompleteOffset = 0
	}
	m.ShowAutocomplete = true
	m.AutocompleteKind = autocompleteFile
	m.AutocompleteItems = items
	m.autocompleteQuery = query
	m.autocompleteSig = sig
	return true
}

func (m *Model) closeAutocomplete() {
	m.ShowAutocomplete = false
	m.AutocompleteItems = nil
	m.AutocompleteIndex = 0
	m.AutocompleteOffset = 0
	m.autocompleteSig = ""
}

// quoteIfSpaced keeps a path with spaces readable as one mention.
func quoteIfSpaced(rel string) string {
	if strings.ContainsAny(rel, " \t") {
		return `"` + rel + `"`
	}
	return rel
}

// acceptFileCompletion replaces the @-mention before the cursor with the
// selected file and a trailing space.
func (m Model) acceptFileCompletion() Model {
	if m.AutocompleteIndex < 0 || m.AutocompleteIndex >= len(m.AutocompleteItems) {
		m.closeAutocomplete()
		return m
	}
	selected := m.AutocompleteItems[m.AutocompleteIndex].Name
	_, tokenLen, ok := m.fileToken()
	if !ok {
		m.closeAutocomplete()
		return m
	}
	// Completing in the middle of a sentence must not double the space.
	suffix := " "
	if lines := strings.Split(m.Input.Value(), "\n"); m.Input.Line() < len(lines) {
		line := []rune(lines[m.Input.Line()])
		if col := m.Input.Column(); col < len(line) && unicode.IsSpace(line[col]) {
			suffix = ""
		}
	}
	for range tokenLen {
		m.Input, _ = m.Input.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	}
	m.Input.InsertString(selected + suffix)
	m.lastInputLen = len(m.Input.Value())
	m.closeAutocomplete()
	return m
}

// fileSuggestionLine renders one dropdown row: the file name, then its
// directory, with the characters that matched the query picked out.
func fileSuggestionLine(item CommandDef, query string, selected bool, width int) string {
	rel := strings.Trim(strings.TrimPrefix(item.Name, "@"), `"`)
	slash := strings.LastIndexByte(rel, '/') + 1
	dir, base := rel[:slash], rel[slash:]

	marker := "  "
	baseStyle := lipgloss.NewStyle().Foreground(subtextColor).Background(cardBgColor)
	dirStyle := lipgloss.NewStyle().Foreground(mutedTextColor).Background(cardBgColor)
	hitStyle := lipgloss.NewStyle().Foreground(secondaryColor).Background(cardBgColor).Bold(true)
	if selected {
		marker = "▸ "
		baseStyle = baseStyle.Foreground(primaryColor).Bold(true)
		dirStyle = dirStyle.Foreground(subtextColor)
	}

	pos := matchPositions(query, rel)
	paint := func(s string, offset int, normal lipgloss.Style) string {
		var sb strings.Builder
		for i, r := range []rune(s) {
			if pos[offset+i] {
				sb.WriteString(hitStyle.Render(string(r)))
			} else {
				sb.WriteString(normal.Render(string(r)))
			}
		}
		return sb.String()
	}

	left := baseStyle.PaddingLeft(1).Render(marker) + paint(base, utf8.RuneCountInString(dir), baseStyle)
	room := width - lipgloss.Width(left) - 3
	var right string
	if dir != "" && room > 4 {
		shown := dir
		if w := lipgloss.Width(shown); w > room {
			shown = ansi.TruncateLeft(shown, w-room+1, "…")
		}
		offset := utf8.RuneCountInString(dir) - utf8.RuneCountInString(shown)
		if offset < 0 || shown != dir {
			offset = 1 << 20 // truncated: positions no longer line up, skip highlights
		}
		right = paint(shown, offset, dirStyle)
	}
	// The directory sits flush right, one cell from the edge, so file names
	// form a clean column on the left.
	gap := lipgloss.NewStyle().Background(cardBgColor)
	pad := max(0, width-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return left + gap.Render(strings.Repeat(" ", pad)) + right + gap.Render(" ")
}
