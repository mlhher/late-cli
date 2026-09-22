package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Unit tests: search_content (SearchContentTool)
// =============================================================================

func TestSearchContentTool_Metadata(t *testing.T) {
	tool := &SearchContentTool{}

	if got := tool.Name(); got != "search_content" {
		t.Errorf("Name() = %q, want %q", got, "search_content")
	}

	desc := tool.Description()
	if desc == "" || !strings.Contains(desc, "grep") {
		t.Errorf("Description() should mention grep/content, got: %q", desc)
	}

	raw := tool.Parameters()
	if !json.Valid(raw) {
		t.Fatalf("Parameters() returned invalid JSON: %s", string(raw))
	}

	var schema struct {
		Type     string   `json:"type"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}
	if schema.Type != "object" || len(schema.Required) != 1 || schema.Required[0] != "pattern" {
		t.Errorf("unexpected schema requirements: %v", schema.Required)
	}
}

func TestSearchContentTool_RegexSearch(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sample.go")
	content := "package main\n\nfunc LoginUser() {\n\tprintln(\"logging in\")\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &SearchContentTool{}
	args := json.RawMessage(`{"pattern": "func.*User", "path": "` + tmpDir + `"}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(res, "sample.go:3: func LoginUser() {") {
		t.Errorf("expected match on line 3, got:\n%s", res)
	}
}

func TestSearchContentTool_LiteralSearch(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sample.txt")
	content := "foo\n[bracket.text]\nbar\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &SearchContentTool{}
	args := json.RawMessage(`{"pattern": "[bracket.text]", "path": "` + tmpDir + `", "literal": true}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(res, "sample.txt:2: [bracket.text]") {
		t.Errorf("expected literal match, got:\n%s", res)
	}
}

func TestSearchContentTool_CaseSensitivity(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "case.txt")
	content := "Hello World\nhello world\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &SearchContentTool{}

	// Default: case-insensitive
	argsInsensitive := json.RawMessage(`{"pattern": "HELLO", "path": "` + tmpDir + `", "literal": true}`)
	res, err := tool.Execute(context.Background(), argsInsensitive)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "case.txt:1:") || !strings.Contains(res, "case.txt:2:") {
		t.Errorf("expected 2 matches in case-insensitive mode, got:\n%s", res)
	}

	// Case-sensitive
	argsSensitive := json.RawMessage(`{"pattern": "HELLO", "path": "` + tmpDir + `", "literal": true, "case_sensitive": true}`)
	resSens, err := tool.Execute(context.Background(), argsSensitive)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resSens, "No matches found") {
		t.Errorf("expected no matches for HELLO in case-sensitive mode, got:\n%s", resSens)
	}
}

func TestSearchContentTool_ContextLines(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "lines.txt")
	content := "line 1\nline 2\nline 3 TARGET\nline 4\nline 5\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &SearchContentTool{}
	args := json.RawMessage(`{"pattern": "TARGET", "path": "` + tmpDir + `", "literal": true, "context_lines": 1}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(res, "lines.txt:2- line 2") {
		t.Errorf("expected before-context line 2, got:\n%s", res)
	}
	if !strings.Contains(res, "lines.txt:3: line 3 TARGET") {
		t.Errorf("expected target line 3, got:\n%s", res)
	}
	if !strings.Contains(res, "lines.txt:4- line 4") {
		t.Errorf("expected after-context line 4, got:\n%s", res)
	}
}

func TestSearchContentTool_IncludeExclude(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("MATCH"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a.min.js"), []byte("MATCH"), 0644)

	tool := &SearchContentTool{}

	// Exclude *.min.js
	args := json.RawMessage(`{"pattern": "MATCH", "path": "` + tmpDir + `", "literal": true, "exclude": "*.min.js"}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "a.go") || strings.Contains(res, "a.min.js") {
		t.Errorf("expected only a.go, got:\n%s", res)
	}
}

func TestSearchContentTool_MaxResults(t *testing.T) {
	tmpDir := t.TempDir()
	content := "match 1\nmatch 2\nmatch 3\nmatch 4\n"
	os.WriteFile(filepath.Join(tmpDir, "multi.txt"), []byte(content), 0644)

	tool := &SearchContentTool{}
	args := json.RawMessage(`{"pattern": "match", "path": "` + tmpDir + `", "literal": true, "max_results": 2}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(res, "multi.txt:1:") || !strings.Contains(res, "multi.txt:2:") {
		t.Errorf("expected first 2 matches, got:\n%s", res)
	}
	if strings.Contains(res, "multi.txt:3:") {
		t.Errorf("should not contain match 3 due to cap, got:\n%s", res)
	}
	if !strings.Contains(res, "(output truncated)") {
		t.Errorf("expected truncation note, got:\n%s", res)
	}
}

// =============================================================================
// Unit tests: find_files (FindFilesTool)
// =============================================================================

func TestFindFilesTool_Metadata(t *testing.T) {
	tool := &FindFilesTool{}

	if got := tool.Name(); got != "find_files" {
		t.Errorf("Name() = %q, want %q", got, "find_files")
	}

	desc := tool.Description()
	if desc == "" || !strings.Contains(desc, "find") {
		t.Errorf("Description() should mention find, got: %q", desc)
	}
}

func TestFindFilesTool_FindByNameAndSubstring(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".github", "workflows"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".github", "workflows", "ci.yml"), []byte("name: CI"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Title"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)

	tool := &FindFilesTool{}

	// 1. Substring search for README
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "README", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "README.md") {
		t.Errorf("expected README.md in results, got:\n%s", res)
	}

	// 2. Substring search for workflows folder
	resWf, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "workflows", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resWf, "workflows/") {
		t.Errorf("expected workflows/ directory in results, got:\n%s", resWf)
	}

	// 3. Glob search for *.yml
	resGlob, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "*.yml", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resGlob, "ci.yml") {
		t.Errorf("expected ci.yml in results, got:\n%s", resGlob)
	}

	// 4. Relative path query
	resRel, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": ".github/workflows", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resRel, ".github/workflows/") {
		t.Errorf("expected .github/workflows/ in results, got:\n%s", resRel)
	}
}

func TestFindFilesTool_TypeFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "docs"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "docs", "guide.md"), []byte("guide"), 0644)

	tool := &FindFilesTool{}

	// Directory only
	resDir, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "docs", "path": "`+tmpDir+`", "type": "directory"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resDir, "docs/") || strings.Contains(resDir, "guide.md") {
		t.Errorf("expected only docs/ directory, got:\n%s", resDir)
	}

	// File only
	resFile, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "guide", "path": "`+tmpDir+`", "type": "file"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resFile, "guide.md") || strings.Contains(resFile, "docs/\n") {
		t.Errorf("expected only guide.md file, got:\n%s", resFile)
	}
}

func TestFindFilesTool_GitIgnoreRespected(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("ignored_dir/\n*.secret\n"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "ignored_dir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "ignored_dir", "a.txt"), []byte("secret"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("public"), 0644)

	tool := &FindFilesTool{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "*", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(res, "ignored_dir") {
		t.Errorf("ignored_dir should be excluded by gitignore, got:\n%s", res)
	}
	if !strings.Contains(res, "visible.txt") {
		t.Errorf("expected visible.txt, got:\n%s", res)
	}
}

func TestSearchContentTool_SlashFilterHint(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package main\n"), 0644)

	tool := &SearchContentTool{}
	args := json.RawMessage(`{"pattern": "package", "path": "` + tmpDir + `", "include": "src/*.go"}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(res, "include/exclude filters match file/directory names, not path segments") {
		t.Errorf("expected slash filter hint in no-match output, got:\n%s", res)
	}
}

func TestFindFilesTool_GlobstarHint(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package main\n"), 0644)

	tool := &FindFilesTool{}
	args := json.RawMessage(`{"pattern": "**/*.nonexistent", "path": "` + tmpDir + `"}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(res, "find_files is already recursive by default from 'path'") {
		t.Errorf("expected globstar hint in no-match output, got:\n%s", res)
	}
}

func TestFindFilesTool_BracketEscaping(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "routes", "[id]"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "routes", "[id]", "page.svelte"), []byte("<h1>Item</h1>"), 0644)

	tool := &FindFilesTool{}
	// Escaped bracket search: \[id\]
	args := json.RawMessage(`{"pattern": "\\[id\\]", "path": "` + tmpDir + `"}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(res, "[id]") {
		t.Errorf("expected escaped bracket pattern to match [id], got:\n%s", res)
	}
}

func TestMatchGlobstar_Unit(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "pkg/tool/search.go", true},
		{"**/*.go", "a/b/c/d/e.go", true},
		{"**/*.go", "main.ts", false},
		{"src/**/*.svelte", "src/Button.svelte", true},
		{"src/**/*.svelte", "src/routes/about/page.svelte", true},
		{"src/**/*.svelte", "other/Button.svelte", false},
		{"**/workflows", ".github/workflows", true},
		{"**/workflows", "workflows", true},
		{"**/workflows", "a/b/workflows", true},
		{"**", "any/path/at/all", true},
		{"**/*.zzz", "any/file.go", false},
	}

	for _, tt := range tests {
		got := matchGlobstar(tt.pattern, tt.path)
		if got != tt.want {
			t.Errorf("matchGlobstar(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestFindFilesTool_GlobstarIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "src", "routes", "issues"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, ".github", "workflows"), 0755)

	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "routes", "issues", "page.svelte"), []byte("svelte"), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".github", "workflows", "ci.yml"), []byte("ci"), 0644)

	tool := &FindFilesTool{}

	// 1. **/*.go should match all go files at all depths
	resGo, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "**/*.go", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resGo, "main.go") || !strings.Contains(resGo, "app.go") {
		t.Errorf("expected both main.go and app.go in **/*.go results, got:\n%s", resGo)
	}

	// 2. src/**/*.svelte should match nested svelte file
	resSvelte, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "src/**/*.svelte", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resSvelte, "page.svelte") {
		t.Errorf("expected page.svelte in src/**/*.svelte results, got:\n%s", resSvelte)
	}

	// 3. **/workflows should match .github/workflows directory
	resWf, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "**/workflows", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resWf, "workflows/") {
		t.Errorf("expected workflows/ directory in **/workflows results, got:\n%s", resWf)
	}

	// 4. **/*.zzz should return no matches with hint
	resZzz, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "**/*.zzz", "path": "`+tmpDir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resZzz, "No files or directories matching") || !strings.Contains(resZzz, "Note: find_files is already recursive") {
		t.Errorf("expected no-match with recursive hint, got:\n%s", resZzz)
	}
}
