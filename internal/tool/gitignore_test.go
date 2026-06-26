package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ──────────────────────────────────────────────
// GitIgnore pattern parsing and matching
// ──────────────────────────────────────────────

func TestGitIgnore_SimplePattern(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("*.log\n")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"build.log", false, true},
		{"sub/build.log", false, true},
		{"build.go", false, false},
		{"log", false, false},
	}
	for _, tt := range tests {
		got := gi.Matches(tt.path, tt.isDir)
		if got != tt.want {
			t.Errorf("Matches(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestGitIgnore_DirectoryOnly(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("build/\n")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"build", true, true},          // dir named "build"
		{"build", false, false},        // file named "build" — dirOnly, no match
		{"build/foo.go", false, false}, // file inside matched dir — pattern matches dir component
		{"src/build", true, true},      // dir named "build" at any depth
		{"src/build/foo.go", false, false},
	}
	for _, tt := range tests {
		got := gi.Matches(tt.path, tt.isDir)
		if got != tt.want {
			t.Errorf("Matches(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestGitIgnore_Negation(t *testing.T) {
	// Ignore all .log files, but not important.log
	gi, err := LoadGitIgnoreFromString("*.log\n!important.log\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("debug.log", false) {
		t.Error("debug.log should be ignored")
	}
	if gi.Matches("important.log", false) {
		t.Error("important.log should NOT be ignored (negated)")
	}
}

func TestGitIgnore_NegationOrder(t *testing.T) {
	// "Last matching pattern wins" — re-ignore after negation
	gi, err := LoadGitIgnoreFromString("*.log\n!important.log\nimportant.log\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("important.log", false) {
		t.Error("important.log should be ignored — re-ignored by third pattern")
	}
}

func TestGitIgnore_AnchoredPattern(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("/build\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("build", true) {
		t.Error("/build should match 'build' at root")
	}
	if gi.Matches("src/build", true) {
		t.Error("/build should NOT match nested 'src/build' (anchored)")
	}
}

func TestGitIgnore_StarStar(t *testing.T) {
	// **/foo matches foo at any depth
	gi1, err := LoadGitIgnoreFromString("**/foo\n")
	if err != nil {
		t.Fatal(err)
	}
	if !gi1.Matches("foo", false) {
		t.Error("**/foo should match 'foo'")
	}
	if !gi1.Matches("a/b/foo", false) {
		t.Error("**/foo should match 'a/b/foo'")
	}

	// a/**/b matches a/b, a/x/b, a/x/y/b
	gi2, err := LoadGitIgnoreFromString("a/**/b\n")
	if err != nil {
		t.Fatal(err)
	}
	if !gi2.Matches("a/b", false) {
		t.Error("a/**/b should match 'a/b'")
	}
	if !gi2.Matches("a/x/b", false) {
		t.Error("a/**/b should match 'a/x/b'")
	}
	if !gi2.Matches("a/x/y/b", false) {
		t.Error("a/**/b should match 'a/x/y/b'")
	}
}

func TestGitIgnore_Star(t *testing.T) {
	// *.test matches .test files
	gi, err := LoadGitIgnoreFromString("*.test\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("foo.test", false) {
		t.Error("*.test should match 'foo.test'")
	}
	if gi.Matches("foo.test.txt", false) {
		t.Error("*.test should NOT match 'foo.test.txt'")
	}
	// Make sure * doesn't cross directory boundaries
	if !gi.Matches("sub/foo.test", false) {
		t.Error("*.test should match 'sub/foo.test' (unanchored)")
	}
}

func TestGitIgnore_QuestionMark(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("file.?xt\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("file.txt", false) {
		t.Error("file.?xt should match 'file.txt'")
	}
	if gi.Matches("file.text", false) {
		t.Error("file.?xt should NOT match 'file.text' (two chars)")
	}
}

func TestGitIgnore_CharacterClass(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("file[0-9].txt\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("file1.txt", false) {
		t.Error("file[0-9].txt should match 'file1.txt'")
	}
	if gi.Matches("filea.txt", false) {
		t.Error("file[0-9].txt should NOT match 'filea.txt'")
	}
}

func TestGitIgnore_CharacterClassNegation(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("file[!a].txt\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches("file1.txt", false) {
		t.Error("file[!a].txt should match 'file1.txt'")
	}
	if gi.Matches("filea.txt", false) {
		t.Error("file[!a].txt should NOT match 'filea.txt'")
	}
}

func TestGitIgnore_HiddenFiles(t *testing.T) {
	gi, err := LoadGitIgnoreFromString(".*\n")
	if err != nil {
		t.Fatal(err)
	}

	if !gi.Matches(".env", false) {
		t.Error(".* should match '.env'")
	}
	if !gi.Matches(".gitignore", false) {
		t.Error(".* should match '.gitignore'")
	}
}

func TestGitIgnore_CommentAndBlankLines(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("# this is a comment\n\n*.log\n")
	if err != nil {
		t.Fatal(err)
	}
	if !gi.Matches("debug.log", false) {
		t.Error("should match after comment/blank lines")
	}
	if gi.Matches("debug.txt", false) {
		t.Error("should not match non-log file")
	}
}

func TestGitIgnore_EmptyGitIgnore(t *testing.T) {
	gi, err := LoadGitIgnoreFromString("")
	if err != nil {
		t.Fatal(err)
	}
	if gi != nil {
		t.Error("empty .gitignore should return nil")
	}
}

func TestGitIgnore_MatchesNil(t *testing.T) {
	var gi *GitIgnore
	if gi.Matches("anything", false) {
		t.Error("nil GitIgnore should never match")
	}
}

// ──────────────────────────────────────────────
// LoadGitIgnore from file
// ──────────────────────────────────────────────

func TestLoadGitIgnore_FileNotFound(t *testing.T) {
	gi, err := LoadGitIgnore("/nonexistent/.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	if gi != nil {
		t.Error("should return nil for missing file")
	}
}

func TestLoadGitIgnore_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	os.WriteFile(path, []byte("*.log\nbuild/\n"), 0644)

	gi, err := LoadGitIgnore(path)
	if err != nil {
		t.Fatal(err)
	}
	if gi == nil {
		t.Fatal("expected non-nil GitIgnore")
	}
	if !gi.Matches("debug.log", false) {
		t.Error("should match .log files")
	}
	if !gi.Matches("build", true) {
		t.Error("should match build dirs")
	}
}

// ──────────────────────────────────────────────
// FindRepoRoot
// ──────────────────────────────────────────────

func TestFindRepoRoot(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	subDir := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(subDir, 0755)

	root := FindRepoRoot(subDir)
	if root != dir {
		t.Errorf("FindRepoRoot(%q) = %q, want %q", subDir, root, dir)
	}

	// Root itself
	root = FindRepoRoot(dir)
	if root != dir {
		t.Errorf("FindRepoRoot(%q) = %q, want %q", dir, root, dir)
	}
}

func TestFindRepoRoot_NoGit(t *testing.T) {
	dir := t.TempDir()
	root := FindRepoRoot(dir)
	if root != "" {
		t.Errorf("FindRepoRoot without .git should return empty, got %q", root)
	}
}

// ──────────────────────────────────────────────
// Integration: SearchTool respects .gitignore
// ──────────────────────────────────────────────

func TestSearchTool_GitIgnoreRespected(t *testing.T) {
	ResetGitIgnoreCache()

	dir := t.TempDir()

	// Create a fake repo root with .gitignore
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n_private/\n"), 0644)

	// Create test files
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "debug.log"), []byte("some debug output\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "_private"), 0755)
	os.WriteFile(filepath.Join(dir, "_private", "secret.go"), []byte("package secret\n"), 0644)

	tool := &SearchTool{}

	// Search for "package" — should find main.go but NOT debug.log or _private/secret.go
	args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "main.go") {
		t.Errorf("expected main.go in results, got: %q", result)
	}
	if strings.Contains(result, "debug.log") {
		t.Errorf("debug.log should be gitignored, got: %q", result)
	}
}

func TestSearchTool_GitIgnoreDirectorySkipped(t *testing.T) {
	ResetGitIgnoreCache()

	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("_private/\n"), 0644)

	os.MkdirAll(filepath.Join(dir, "_private", "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "_private", "sub", "deep.go"), []byte("package deep\n"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte("package vis\n"), 0644)

	tool := &SearchTool{}

	args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "visible.go") {
		t.Errorf("expected visible.go in results, got: %q", result)
	}
	if strings.Contains(result, "deep.go") {
		t.Errorf("deep.go inside _private/ should be gitignored, got: %q", result)
	}
}

func TestSearchTool_NoGitIgnoreDir(t *testing.T) {
	ResetGitIgnoreCache()

	// Searching outside a git repo should work normally
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	tool := &SearchTool{}

	args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "main.go") {
		t.Errorf("expected main.go in results, got: %q", result)
	}
}

func TestSearchTool_GitIgnoreWithAnchoredPattern(t *testing.T) {
	ResetGitIgnoreCache()

	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/dist\n"), 0644)

	os.MkdirAll(filepath.Join(dir, "dist"), 0755)
	os.WriteFile(filepath.Join(dir, "dist", "bundle.js"), []byte("console.log('bundled')\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "src", "dist"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "dist", "helper.js"), []byte("function help() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "app.js"), []byte("function app() {}\n"), 0644)

	tool := &SearchTool{}

	args := json.RawMessage(`{"pattern": "function", "path": "` + dir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(result, "bundle.js") {
		t.Errorf("dist/bundle.js should be gitignored (anchored /dist), got: %q", result)
	}
	if !strings.Contains(result, "helper.js") {
		t.Errorf("src/dist/helper.js should NOT be gitignored (anchored /dist only matches root), got: %q", result)
	}
	if !strings.Contains(result, "app.js") {
		t.Errorf("src/app.js should be in results, got: %q", result)
	}
}

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

// LoadGitIgnoreFromString parses a .gitignore from a string (for testing).
func LoadGitIgnoreFromString(content string) (*GitIgnore, error) {
	return parseGitIgnoreLines(content)
}

// parseGitIgnoreLines parses newline-separated patterns from a string.
func parseGitIgnoreLines(content string) (*GitIgnore, error) {
	gi := &GitIgnore{}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		p, err := parseGitIgnorePattern(trimmed)
		if err != nil {
			return nil, err
		}
		gi.patterns = append(gi.patterns, p)
	}
	if len(gi.patterns) == 0 {
		return nil, nil
	}
	return gi, nil
}
