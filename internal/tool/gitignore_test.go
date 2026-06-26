package tool

import (
	"context"
	"encoding/json"
	"fmt"
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
// CWD-keyed cache integration tests
// ──────────────────────────────────────────────

// TestGetRepoRoot_CWDKeyedCache verifies that getRepoRoot() correctly detects
// a process CWD change and recomputes the cached repo root and .gitignore.
// Uses os.Chdir to simulate an IDE workspace switch.
func TestGetRepoRoot_CWDKeyedCache(t *testing.T) {
	origCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origCWD)
	}()

	// Create two separate git repos with different .gitignore rules
	repo1 := t.TempDir()
	os.MkdirAll(filepath.Join(repo1, ".git"), 0755)
	os.WriteFile(filepath.Join(repo1, ".gitignore"), []byte("*.log\n"), 0644)

	repo2 := t.TempDir()
	os.MkdirAll(filepath.Join(repo2, ".git"), 0755)
	os.WriteFile(filepath.Join(repo2, ".gitignore"), []byte("*.tmp\n"), 0644)

	// ---- Switch to repo1 ----
	if err := os.Chdir(repo1); err != nil {
		t.Fatal(err)
	}
	ResetGitIgnoreCache()

	root1, gi1 := getRepoRoot()
	if root1 != repo1 {
		t.Errorf("repo1: expected root %q, got %q", repo1, root1)
	}
	if !gi1.Matches("debug.log", false) {
		t.Error("repo1: debug.log should be ignored by *.log rule")
	}
	if gi1.Matches("debug.tmp", false) {
		t.Error("repo1: debug.tmp should NOT be ignored (no *.tmp rule)")
	}

	// ---- Switch to repo2 WITHOUT calling ResetGitIgnoreCache ----
	// The CWD change must trigger an automatic cache refresh.
	if err := os.Chdir(repo2); err != nil {
		t.Fatal(err)
	}

	root2, gi2 := getRepoRoot()
	if root2 != repo2 {
		t.Errorf("repo2: expected root %q, got %q (stale cache?)", repo2, root2)
	}
	if gi2.Matches("debug.log", false) {
		t.Errorf("repo2: debug.log should NOT be ignored (stale cache from repo1 still active)")
	}
	if !gi2.Matches("debug.tmp", false) {
		t.Errorf("repo2: debug.tmp should be ignored by repo2's *.tmp rule (stale cache from repo1?)")
	}
}

// TestSearchTool_CWDKeyedCache verifies that switching the process CWD between
// two repos and calling the full search tool works without panicking and
// produces correct results. Uses absolute paths (like all existing tests in
// this file) to avoid a pre-existing relative-path bug in matchesGitIgnore.
func TestSearchTool_CWDKeyedCache(t *testing.T) {
	origCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origCWD)
	}()

	repo1 := t.TempDir()
	os.MkdirAll(filepath.Join(repo1, ".git"), 0755)
	os.WriteFile(filepath.Join(repo1, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(repo1, "work.go"), []byte("package repo1\n"), 0644)
	os.WriteFile(filepath.Join(repo1, "debug.log"), []byte("package debug.log\n"), 0644)
	os.WriteFile(filepath.Join(repo1, "debug.tmp"), []byte("package debug.tmp\n"), 0644)

	repo2 := t.TempDir()
	os.MkdirAll(filepath.Join(repo2, ".git"), 0755)
	os.WriteFile(filepath.Join(repo2, ".gitignore"), []byte("*.tmp\n"), 0644)
	os.WriteFile(filepath.Join(repo2, "work.go"), []byte("package repo2\n"), 0644)
	os.WriteFile(filepath.Join(repo2, "debug.log"), []byte("package debug.log\n"), 0644)
	os.WriteFile(filepath.Join(repo2, "debug.tmp"), []byte("package debug.tmp\n"), 0644)

	tool := &SearchTool{}

	// ---- Switch to repo1 ----
	if err := os.Chdir(repo1); err != nil {
		t.Fatal(err)
	}
	ResetGitIgnoreCache()

	r1, err := tool.Execute(context.Background(), json.RawMessage(
		`{"pattern": "package", "path": "`+repo1+`", "output_mode": "files_with_matches"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r1, "work.go") {
		t.Errorf("repo1: expected work.go, got: %s", r1)
	}
	if strings.Contains(r1, "debug.log") {
		t.Errorf("repo1: debug.log should be ignored by repo1's *.log rule, got: %s", r1)
	}
	if !strings.Contains(r1, "debug.tmp") {
		t.Errorf("repo1: debug.tmp should NOT be ignored (no *.tmp rule in repo1), got: %s", r1)
	}

	// ---- Switch to repo2 WITHOUT calling ResetGitIgnoreCache ----
	if err := os.Chdir(repo2); err != nil {
		t.Fatal(err)
	}

	r2, err := tool.Execute(context.Background(), json.RawMessage(
		`{"pattern": "package", "path": "`+repo2+`", "output_mode": "files_with_matches"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r2, "work.go") {
		t.Errorf("repo2: expected work.go, got: %s", r2)
	}
	if !strings.Contains(r2, "debug.log") {
		t.Errorf("repo2: debug.log should NOT be ignored (no *.log rule in repo2), got: %s", r2)
	}
	if strings.Contains(r2, "debug.tmp") {
		t.Errorf("repo2: debug.tmp should be ignored by repo2's *.tmp rule — stale cache from repo1 still active, got: %s", r2)
	}
}

// TestGetGitIgnoreForPath_NestedGitIgnore verifies that searching a subdirectory
// with its own .gitignore returns that nested file instead of the root one.
func TestGetGitIgnoreForPath_NestedGitIgnore(t *testing.T) {
	ResetGitIgnoreCache()

	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)

	// Root .gitignore ignores *.log
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.log\n"), 0644)

	// Nested sub-project with its own .gitignore ignoring *.tmp
	subDir := filepath.Join(repo, "services", "foo")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, ".gitignore"), []byte("*.tmp\n"), 0644)

	// Searching the nested directory should return the nested .gitignore
	gi, root := getGitIgnoreForPath(subDir)
	if gi == nil {
		t.Fatal("expected non-nil GitIgnore for nested sub-directory")
	}
	if root != subDir {
		t.Errorf("expected root %q, got %q", subDir, root)
	}
	if !gi.Matches("handler.tmp", false) {
		t.Error("nested .gitignore should match *.tmp files")
	}
	// The nested .gitignore only has *.tmp; root's *.log should NOT be in it
	if gi.Matches("debug.log", false) {
		t.Error("nested .gitignore should NOT match *.log (that's in root's gitignore)")
	}

	// Searching the root directory should return the root .gitignore
	gi, root = getGitIgnoreForPath(repo)
	if gi == nil {
		t.Fatal("expected non-nil GitIgnore for root")
	}
	if root != repo {
		t.Errorf("expected root %q, got %q", repo, root)
	}
	if !gi.Matches("debug.log", false) {
		t.Error("root .gitignore should match *.log files")
	}
}

// TestGetRepoRoot_ConcurrentSafety verifies that getRepoRoot can be called
// concurrently without panicking or corrupting the cache state.
func TestGetRepoRoot_ConcurrentSafety(t *testing.T) {
	ResetGitIgnoreCache()

	// Prime the cache from a single goroutine first so the initial CWD
	// computation doesn't race during the parallel phase.
	getRepoRoot()

	// Run 50 concurrent calls — this exercises the RLock fast path
	// and the double-checked locking slow path.
	n := 50
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errs <- fmt.Errorf("panic: %v", r)
				}
			}()
			root, gi := getRepoRoot()
			_ = root
			_ = gi
			errs <- nil
		}()
	}

	for i := 0; i < n; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
}

// ──────────────────────────────────────────────
// .llmignore integration tests
// ──────────────────────────────────────────────

// TestLoadMergedIgnore_BothExist verifies that loadMergedIgnore merges patterns
// from .gitignore and .llmignore into a single GitIgnore, with .llmignore
// patterns appended after .gitignore for correct "last matching wins" semantics.
func TestLoadMergedIgnore_BothExist(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(dir, ".llmignore"), []byte("*.tmp\n"), 0644)

	gi, err := loadMergedIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gi == nil {
		t.Fatal("expected non-nil merged GitIgnore")
	}

	// .gitignore pattern
	if !gi.Matches("debug.log", false) {
		t.Error("merged should match *.log from .gitignore")
	}
	// .llmignore pattern
	if !gi.Matches("debug.tmp", false) {
		t.Error("merged should match *.tmp from .llmignore")
	}
}

// TestLoadMergedIgnore_LlmIgnoreNegation verifies that negation patterns in
// .llmignore take precedence over .gitignore patterns when merged.
func TestLoadMergedIgnore_LlmIgnoreNegation(t *testing.T) {
	dir := t.TempDir()
	// .gitignore ignores all .log files
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	// .llmignore un-ignores important.log
	os.WriteFile(filepath.Join(dir, ".llmignore"), []byte("!important.log\n"), 0644)

	gi, err := loadMergedIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gi == nil {
		t.Fatal("expected non-nil merged GitIgnore")
	}

	// Normal .log files should still be ignored (from .gitignore)
	if !gi.Matches("debug.log", false) {
		t.Error("debug.log should be ignored by .gitignore's *.log rule")
	}
	// .llmignore's negation should take final precedence
	if gi.Matches("important.log", false) {
		t.Error("important.log should NOT be ignored — .llmignore's negation takes precedence")
	}
}

// TestLoadMergedIgnore_GitIgnoreOnly verifies that loadMergedIgnore works when
// only .gitignore exists (backward compatible).
func TestLoadMergedIgnore_GitIgnoreOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)

	gi, err := loadMergedIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gi == nil {
		t.Fatal("expected non-nil GitIgnore")
	}
	if !gi.Matches("debug.log", false) {
		t.Error("should match *.log from .gitignore")
	}
}

// TestLoadMergedIgnore_LlmIgnoreOnly verifies that loadMergedIgnore works when
// only .llmignore exists (no .gitignore at all).
func TestLoadMergedIgnore_LlmIgnoreOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".llmignore"), []byte("*.tmp\n"), 0644)

	gi, err := loadMergedIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gi == nil {
		t.Fatal("expected non-nil GitIgnore for .llmignore-only dir")
	}
	if !gi.Matches("debug.tmp", false) {
		t.Error("should match *.tmp from .llmignore")
	}
	if gi.Matches("debug.log", false) {
		t.Error("should NOT match *.log (no pattern for it)")
	}
}

// TestLoadMergedIgnore_Neither verifies that loadMergedIgnore returns nil, nil
// when neither .gitignore nor .llmignore exists.
func TestLoadMergedIgnore_Neither(t *testing.T) {
	dir := t.TempDir()
	gi, err := loadMergedIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gi != nil {
		t.Error("expected nil when neither ignore file exists")
	}
}

// TestSearchTool_LlmIgnoreFilters verifies that the full search tool respects
// patterns from .llmignore when no .gitignore exists.
func TestSearchTool_LlmIgnoreFilters(t *testing.T) {
	ResetGitIgnoreCache()

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	// Only .llmignore, no .gitignore — ignore .tmp files
	os.WriteFile(filepath.Join(dir, ".llmignore"), []byte("*.tmp\n"), 0644)
	os.WriteFile(filepath.Join(dir, "work.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "temp.tmp"), []byte("package temp\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "work.go") {
		t.Errorf("expected work.go in results, got: %q", result)
	}
	if strings.Contains(result, "temp.tmp") {
		t.Errorf("temp.tmp should be filtered by .llmignore's *.tmp rule, got: %q", result)
	}
}

// TestSearchTool_GitIgnoreAndLlmIgnore verifies the full search tool with
// both .gitignore and .llmignore in the same directory, where .llmignore
// adds additional filters beyond .gitignore.
func TestSearchTool_GitIgnoreAndLlmIgnore(t *testing.T) {
	ResetGitIgnoreCache()

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	// .gitignore ignores *.log
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	// .llmignore additionally ignores *.json (e.g., large fixtures)
	os.WriteFile(filepath.Join(dir, ".llmignore"), []byte("*.json\n"), 0644)

	os.WriteFile(filepath.Join(dir, "work.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "debug.log"), []byte("package log\n"), 0644)
	os.WriteFile(filepath.Join(dir, "fixture.json"), []byte("{\"package\": \"json\"}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "work.go") {
		t.Errorf("expected work.go in results, got: %q", result)
	}
	if strings.Contains(result, "debug.log") {
		t.Errorf("debug.log should be filtered by .gitignore's *.log rule, got: %q", result)
	}
	if strings.Contains(result, "fixture.json") {
		t.Errorf("fixture.json should be filtered by .llmignore's *.json rule, got: %q", result)
	}
}

// TestSearchTool_LlmIgnoreNested verifies that the nested directory walk in
// getGitIgnoreForPath finds .llmignore in a subdirectory when no .gitignore
// exists there.
func TestSearchTool_LlmIgnoreNested(t *testing.T) {
	ResetGitIgnoreCache()

	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)

	// Root has .gitignore only
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(repo, "root.go"), []byte("package root\n"), 0644)

	// Nested sub-dir has only .llmignore (no .gitignore)
	subDir := filepath.Join(repo, "services", "foo")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, ".llmignore"), []byte("*.tmp\n"), 0644)
	os.WriteFile(filepath.Join(subDir, "handler.go"), []byte("package foo\n"), 0644)
	os.WriteFile(filepath.Join(subDir, "handler.tmp"), []byte("package tmp\n"), 0644)

	tool := &SearchTool{}

	// Search the nested sub-directory specifically
	args := json.RawMessage(`{"pattern": "package", "path": "` + subDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "handler.go") {
		t.Errorf("expected handler.go in nested search results, got: %q", result)
	}
	if strings.Contains(result, "handler.tmp") {
		t.Errorf("handler.tmp should be filtered by nested .llmignore's *.tmp rule, got: %q", result)
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
