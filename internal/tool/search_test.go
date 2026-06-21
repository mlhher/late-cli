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

// ---------------------------------------------------------------------------
// Unit tests: Tool interface methods
// ---------------------------------------------------------------------------

func TestSearchTool_Name(t *testing.T) {
	tool := &SearchTool{}
	if got := tool.Name(); got != "search_tool" {
		t.Errorf("Name() = %q, want %q", got, "search_tool")
	}
}

func TestSearchTool_Description(t *testing.T) {
	tool := &SearchTool{}
	desc := tool.Description()
	if desc == "" {
		t.Fatal("Description() should not be empty")
	}
	if !strings.Contains(desc, "search_tool") && !strings.Contains(desc, "search") && !strings.Contains(desc, "Search") {
		t.Errorf("Description() should mention searching, got: %q", desc)
	}
}

func TestSearchTool_Parameters_IsValidJSON(t *testing.T) {
	tool := &SearchTool{}
	raw := tool.Parameters()
	if !json.Valid(raw) {
		t.Fatalf("Parameters() returned invalid JSON: %s", string(raw))
	}
}

func TestSearchTool_Parameters_HasRequiredFields(t *testing.T) {
	tool := &SearchTool{}
	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("failed to parse Parameters schema: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("Parameters schema type = %q, want %q", schema.Type, "object")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "pattern" {
		t.Errorf("Required = %v, want [\"pattern\"]", schema.Required)
	}
}

func TestSearchTool_RequiresConfirmation(t *testing.T) {
	tests := []struct {
		name string
		args json.RawMessage
		want bool
	}{
		{"empty args", json.RawMessage(`{}`), false},
		{"with pattern", json.RawMessage(`{"pattern": "test"}`), false},
		{"full params", json.RawMessage(`{"pattern": "foo", "path": ".", "output_mode": "content"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &SearchTool{}
			if got := tool.RequiresConfirmation(tt.args); got != tt.want {
				t.Errorf("RequiresConfirmation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSearchTool_CallString(t *testing.T) {
	tests := []struct {
		name string
		args json.RawMessage
		want []string // all substrings must be present
	}{
		{
			name: "with pattern",
			args: json.RawMessage(`{"pattern": "mySearch"}`),
			want: []string{"search_tool", "mySearch"},
		},
		{
			name: "empty args",
			args: json.RawMessage(`{}`),
			want: []string{"search_tool"},
		},
		{
			name: "long pattern truncated",
			args: json.RawMessage(`{"pattern": "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"}`),
			want: []string{"search_tool"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &SearchTool{}
			got := tool.CallString(tt.args)
			for _, s := range tt.want {
				if !strings.Contains(got, s) {
					t.Errorf("CallString() = %q, want it to contain %q", got, s)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: parameter validation (no temp dirs needed)
// ---------------------------------------------------------------------------

func TestSearchTool_EmptyPattern(t *testing.T) {
	tool := &SearchTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": ""}`))
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %v, want 'pattern is required'", err)
	}
}

func TestSearchTool_InvalidOutputMode(t *testing.T) {
	tool := &SearchTool{}
	tests := []struct {
		mode string
		args json.RawMessage
	}{
		{"invalid", json.RawMessage(`{"pattern": "x", "output_mode": "invalid"}`)},
		{"empty string after default", json.RawMessage(`{"pattern": "x", "output_mode": ""}`)}, // should default pass, not error
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), tt.args)
			if tt.mode == "invalid" && err == nil {
				t.Error("expected error for invalid output_mode")
			}
		})
	}
}

func TestSearchTool_InvalidJSON(t *testing.T) {
	tool := &SearchTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSearchTool_MaxResultsCapping(t *testing.T) {
	tool := &SearchTool{}
	// We can't easily inspect the internal capped value, but we can verify
	// that a value > 500 doesn't cause errors when run against a real dir.
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("func A() {}\n"), 0644)

	tests := []struct {
		name  string
		value int
	}{
		{"zero", 0},
		{"negative", -10},
		{"above cap", 1000},
		{"at cap", 500},
		{"normal", 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(
				`{"pattern": "func", "path": "%s", "max_results": %d, "output_mode": "files_with_matches"}`,
				tmpDir, tt.value))
			result, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(result, "a.go") {
				t.Errorf("result should contain a.go, got: %q", result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: Execute output modes (exact content verification)
// ---------------------------------------------------------------------------

func TestSearchTool_NoMatches(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "nonexistent", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No matches found" {
		t.Errorf("got %q, want 'No matches found'", result)
	}
}

func TestSearchTool_FilesWithMatchesMode(t *testing.T) {
	tmpDir := t.TempDir()
	aPath := filepath.Join(tmpDir, "a.go")
	bPath := filepath.Join(tmpDir, "b.go")
	os.WriteFile(aPath, []byte("package a\nfunc Foo() {}\n"), 0644)
	os.WriteFile(bPath, []byte("package b\nfunc Bar() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "a.go") {
		t.Errorf("result missing a.go, got: %q", result)
	}
	if !strings.Contains(result, "b.go") {
		t.Errorf("result missing b.go, got: %q", result)
	}
	// Should NOT have line numbers or content
	if strings.Contains(result, "|") {
		t.Errorf("files_with_matches should not contain '|' line markers, got: %q", result)
	}
}

func TestSearchTool_ContentMode_ExactFormat(t *testing.T) {
	tmpDir := t.TempDir()
	// Two-line file where line 2 has the match
	content := "package a\nfunc Foo() {}\n"
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(content), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "content"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	// Must have file header line (path may be absolute tmp dir)
	if !strings.Contains(result, "a.go") {
		t.Errorf("expected 'a.go' in output, got: %q", result)
	}
	// Must have line number + pipe
	if !strings.Contains(result, "2 | func Foo") {
		t.Errorf("expected '2 | func Foo' in output, got: %q", result)
	}
}

func TestSearchTool_ContentMode_NoMatchesInFile(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\nfunc Foo() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package b\n"), 0644)

	tool := &SearchTool{}
	// Only a.go has 'func'
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "content"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "a.go") {
		t.Errorf("expected a.go in results, got: %q", result)
	}
	if strings.Contains(result, "b.go") {
		t.Errorf("b.go should NOT be in results (no match), got: %q", result)
	}
}

func TestSearchTool_CountMode_ExactCounts(t *testing.T) {
	tmpDir := t.TempDir()
	// a.go has 2 'func' matches, b.go has 1
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\nfunc Foo() {}\nfunc Bar() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package b\nfunc Baz() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "count"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	countA, countB := 0, 0
	for _, line := range lines {
		if strings.Contains(line, "a.go") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				fmt.Sscanf(strings.TrimSpace(parts[len(parts)-1]), "%d", &countA)
			}
		}
		if strings.Contains(line, "b.go") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				fmt.Sscanf(strings.TrimSpace(parts[len(parts)-1]), "%d", &countB)
			}
		}
	}
	if countA != 2 {
		t.Errorf("a.go count = %d, want 2", countA)
	}
	if countB != 1 {
		t.Errorf("b.go count = %d, want 1", countB)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: case sensitivity
// ---------------------------------------------------------------------------

func TestSearchTool_CaseInsensitiveDefault(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("func Foo() {}\n"), 0644)

	tool := &SearchTool{}
	// Default is case-insensitive: "foo" should match "Foo"
	args := json.RawMessage(`{"pattern": "foo", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "a.go") {
		t.Error("default case-insensitive search should match 'Foo' with pattern 'foo'")
	}
}

func TestSearchTool_CaseSensitive(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("func Foo() {}\n"), 0644)

	tool := &SearchTool{}
	// Case-sensitive: "foo" should NOT match "Foo"
	args := json.RawMessage(`{"pattern": "foo", "path": "` + tmpDir + `", "case_sensitive": true, "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No matches found" {
		t.Error("case-sensitive search should not match 'Foo' with pattern 'foo'")
	}

	// But "Foo" should match
	args2 := json.RawMessage(`{"pattern": "Foo", "path": "` + tmpDir + `", "case_sensitive": true, "output_mode": "files_with_matches"}`)
	result2, err := tool.Execute(context.Background(), args2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result2, "a.go") {
		t.Error("case-sensitive search should match 'Foo'")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: fixed_strings mode
// ---------------------------------------------------------------------------

func TestSearchTool_FixedStringsLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	// File containing literal parentheses — "hello(world)" will work for both literal and regex matching
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("hello(world)\n"), 0644)

	tool := &SearchTool{}

	// fixed_strings: literal "(world" should match the file
	args := json.RawMessage(`{"pattern": "(world", "path": "` + tmpDir + `", "fixed_strings": true, "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "a.go") {
		t.Error("fixed_strings should match literal '(world'")
	}

	// Without fixed_strings, a regex pattern should work normally
	// "w.rld" — dot matches any char, should match "world"
	args2 := json.RawMessage(`{"pattern": "w.rld", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result2, err2 := tool.Execute(context.Background(), args2)
	if err2 != nil {
		t.Fatal(err2)
	}
	if !strings.Contains(result2, "a.go") {
		t.Error("regex 'w.rld' should match 'world' via dot matching")
	}

	// fixed_strings with a pattern containing regex meta-chars: "w.rld" as literal should NOT match "world"
	// because the dot is treated as a literal dot, not a wildcard
	args3 := json.RawMessage(`{"pattern": "w.rld", "path": "` + tmpDir + `", "fixed_strings": true, "output_mode": "files_with_matches"}`)
	result3, err3 := tool.Execute(context.Background(), args3)
	if err3 != nil {
		t.Fatal(err3)
	}
	if result3 != "No matches found" {
		t.Errorf("fixed_strings 'w.rld' should NOT match 'world' (literal dot != regex dot), got: %q", result3)
	}
}

func TestSearchTool_FixedStringsCaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("Hello World\n"), 0644)

	tool := &SearchTool{}

	// fixed_strings + default insensitive: "hello" should match "Hello"
	args := json.RawMessage(`{"pattern": "hello", "path": "` + tmpDir + `", "fixed_strings": true, "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "a.go") {
		t.Error("fixed_strings + insensitive should match 'Hello' with pattern 'hello'")
	}

	// fixed_strings + case_sensitive: "hello" should NOT match "Hello"
	args2 := json.RawMessage(`{"pattern": "hello", "path": "` + tmpDir + `", "fixed_strings": true, "case_sensitive": true, "output_mode": "files_with_matches"}`)
	result2, err := tool.Execute(context.Background(), args2)
	if err != nil {
		t.Fatal(err)
	}
	if result2 != "No matches found" {
		t.Error("fixed_strings + sensitive should not match 'Hello' with pattern 'hello'")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: include glob filter
// ---------------------------------------------------------------------------

func TestSearchTool_IncludeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("func A() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a.ts"), []byte("function A() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a.py"), []byte("def A():\n"), 0644)

	tool := &SearchTool{}

	tests := []struct {
		name    string
		include string
		want    []string // files that should appear
		notWant []string // files that should NOT appear
	}{
		{"go files", "*.go", []string{"a.go"}, []string{"a.ts", "a.py"}},
		{"ts files", "*.ts", []string{"a.ts"}, []string{"a.go", "a.py"}},
		{"py files", "*.py", []string{"a.py"}, []string{"a.go", "a.ts"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := json.RawMessage(`{"pattern": "A", "path": "` + tmpDir + `", "include": "` + tt.include + `", "output_mode": "files_with_matches"}`)
			result, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.want {
				if !strings.Contains(result, f) {
					t.Errorf("expected %q in results, got: %q", f, result)
				}
			}
			for _, f := range tt.notWant {
				if strings.Contains(result, f) {
					t.Errorf("%q should not be in results, got: %q", f, result)
				}
			}
		})
	}
}

func TestSearchTool_IncludeFilterNoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("func A() {}\n"), 0644)

	tool := &SearchTool{}
	// Nonexistent glob pattern
	args := json.RawMessage(`{"pattern": "A", "path": "` + tmpDir + `", "include": "*.xyz", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No matches found" {
		t.Errorf("expected 'No matches found' for non-matching glob, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: context lines
// ---------------------------------------------------------------------------

func TestSearchTool_ContextLines(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2\nline3 MATCH\nline4\nline5\n"
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(content), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "MATCH", "path": "` + tmpDir + `", "output_mode": "content", "context_lines": 1}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	// Context before: line2 (with - separator)
	if !strings.Contains(result, "- line2") {
		t.Errorf("expected context line (line2 with - separator), got: %q", result)
	}
	// Match: line3 (with | separator)
	if !strings.Contains(result, "| line3 MATCH") {
		t.Errorf("expected match line (line3 with | separator), got: %q", result)
	}
}

func TestSearchTool_ContextLinesZero(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2 MATCH\nline3\n"
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(content), 0644)

	tool := &SearchTool{}
	// context_lines=0 should NOT include adjacent lines
	args := json.RawMessage(`{"pattern": "MATCH", "path": "` + tmpDir + `", "output_mode": "content", "context_lines": 0}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "| line2 MATCH") {
		t.Errorf("expected match line, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: default path (no path param = CWD)
// ---------------------------------------------------------------------------

func TestSearchTool_DefaultPathIsCWD(t *testing.T) {
	// Search for a pattern we know exists in the tool package source
	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "SearchTool", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "search.go") {
		t.Errorf("expected search.go in default-path results, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: vendor dir and hidden file filtering
// ---------------------------------------------------------------------------

func TestSearchTool_HiddenFilesSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, ".hidden.go"), []byte("func secret() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "visible.go"), []byte("func visible() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "visible.go") {
		t.Error("expected visible.go in results")
	}
	if strings.Contains(result, ".hidden.go") {
		t.Error("hidden file .hidden.go should be skipped")
	}
}

func TestSearchTool_VendorDirSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg", "lib.js"), []byte("function foo() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.js"), []byte("function bar() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "function", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "main.js") {
		t.Error("expected main.js in results")
	}
	if strings.Contains(result, "node_modules") {
		t.Error("node_modules contents should be skipped")
	}
}

func TestSearchTool_GitDirSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".git", "objects", "pack"), []byte("binary data\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("func main() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "main.go") {
		t.Error("expected main.go in results")
	}
	if strings.Contains(result, ".git") {
		t.Error(".git contents should be skipped")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: binary file detection
// ---------------------------------------------------------------------------

func TestSearchTool_BinarySkip(t *testing.T) {
	tmpDir := t.TempDir()
	// File with null byte = binary
	binaryContent := []byte("some text\x00more text\n")
	os.WriteFile(filepath.Join(tmpDir, "binary.bin"), binaryContent, 0644)
	os.WriteFile(filepath.Join(tmpDir, "text.go"), []byte("func text() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "text", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(result, "binary.bin") {
		t.Error("binary file should be skipped")
	}
	if !strings.Contains(result, "text.go") {
		t.Error("text file should still be found")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: truncation
// ---------------------------------------------------------------------------

func TestSearchTool_FileCapTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 150; i++ {
		os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("file%d.go", i)),
			[]byte(fmt.Sprintf("package p%d\nfunc F%d() {}\n", i, i)), 0644)
	}

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "max_results": 50, "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "(output truncated)") {
		t.Errorf("expected truncated message, got %d lines: %q",
			strings.Count(result, "\n"), result)
	}
	// Count how many file paths we got
	lines := strings.Split(strings.TrimSpace(result), "\n")
	// Last line might be the truncation message
	fileLines := 0
	for _, l := range lines {
		if strings.HasSuffix(l, ".go") {
			fileLines++
		}
	}
	if fileLines > 55 { // 50 + margin for non-file lines
		t.Errorf("expected ~50 file results, got %d", fileLines)
	}
}

func TestSearchTool_CharCapTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	// Create many files, each with a match. 500 files × ~70 chars/file = 35K > 32K maxSearchChars.
	for i := 0; i < 500; i++ {
		content := fmt.Sprintf("package p%d\n// line with MATCH keyword number %d\nfunc F%d() {}\n", i, i, i)
		os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("f%d.go", i)), []byte(content), 0644)
	}

	tool := &SearchTool{}
	// files_with_matches mode — each file adds a path line, 500 files should exceed maxSearchChars
	args := json.RawMessage(`{"pattern": "MATCH", "path": "` + tmpDir + `", "output_mode": "files_with_matches", "max_results": 500}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	// Should be truncated — either by char cap or result cap
	if !strings.Contains(result, "(output truncated)") {
		t.Errorf("expected truncated message, got result of length %d:\n%s...", len(result), result[:min(200, len(result))])
	}
}

// ---------------------------------------------------------------------------
// Unit tests: long line truncation
// ---------------------------------------------------------------------------

func TestSearchTool_LongLineTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	longLine := strings.Repeat("x", 2000) + "MATCH\n"
	os.WriteFile(filepath.Join(tmpDir, "long.txt"), []byte(longLine), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "MATCH", "path": "` + tmpDir + `", "output_mode": "content"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	// Line should be truncated — no 1500 consecutive x's
	if strings.Contains(result, strings.Repeat("x", 1500)) {
		t.Error("long line should have been truncated")
	}
	// Truncation indicator should be present
	if !strings.Contains(result, "...") {
		t.Error("truncated line should show ...")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: nonexistent path
// ---------------------------------------------------------------------------

func TestSearchTool_NonexistentPath(t *testing.T) {
	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "test", "path": "/nonexistent/path/that/does/not/exist"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No matches found" {
		t.Errorf("got %q, want 'No matches found'", result)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: context cancellation
// ---------------------------------------------------------------------------

func TestSearchTool_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 100; i++ {
		os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("file%d.go", i)),
			[]byte(fmt.Sprintf("package p%d\nfunc F%d() {}\n", i, i)), 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(ctx, args)
	// Context cancellation should not cause an error — WalkDir returns the ctx error,
	// but we filter it the same way we filter stopErr
	if err != nil {
		t.Fatalf("unexpected error on cancelled context: %v", err)
	}
	_ = result // might be partial or empty
}

// ---------------------------------------------------------------------------
// Unit tests: regex edge cases
// ---------------------------------------------------------------------------

func TestSearchTool_RegexpSpecialChars(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("a.b\n"), 0644)

	tool := &SearchTool{}

	// In regex mode, "a.b" should match "a.b" (dot = any char)
	args := json.RawMessage(`{"pattern": "a.b", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "a.go") {
		t.Error("regex 'a.b' should match 'a.b'")
	}

	// Escaped dot should match literal dot
	args2 := json.RawMessage(`{"pattern": "a\\.b", "path": "` + tmpDir + `", "output_mode": "files_with_matches"}`)
	result2, err := tool.Execute(context.Background(), args2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result2, "a.go") {
		t.Error("regex 'a\\.b' should match 'a.b'")
	}
}

func TestSearchTool_InvalidRegex(t *testing.T) {
	tool := &SearchTool{}
	// Unbalanced bracket — invalid regex
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern": "[invalid"}`))
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex pattern") {
		t.Errorf("error = %v, want 'invalid regex pattern'", err)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: helper function truncateLine
// ---------------------------------------------------------------------------

func TestTruncateLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // exact expected output
	}{
		{
			name:  "short line unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "exactly 1000 chars",
			input: strings.Repeat("a", 1000),
			want:  strings.Repeat("a", 1000),
		},
		{
			name:  "1001 chars truncated",
			input: strings.Repeat("a", 1001),
			want:  strings.Repeat("a", 997) + "...",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "much longer truncated",
			input: strings.Repeat("b", 5000),
			want:  strings.Repeat("b", 997) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateLine(tt.input); got != tt.want {
				t.Errorf("truncateLine(%d) = %d chars, want %d chars",
					len(tt.input), len(got), len(tt.want))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: helper function countMatches
// ---------------------------------------------------------------------------

func TestCountMatches(t *testing.T) {
	tmpDir := t.TempDir()

	// File with known content
	path := filepath.Join(tmpDir, "test.go")
	os.WriteFile(path, []byte("line1\nfunc Foo()\nline3\nfunc Bar()\nline5\n"), 0644)

	matchFunc := func(s string) bool {
		return strings.Contains(s, "func")
	}

	count, err := countMatches(path, matchFunc)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("countMatches = %d, want 2", count)
	}
}

func TestCountMatches_NoMatches(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.go")
	os.WriteFile(path, []byte("package main\n"), 0644)

	matchFunc := func(s string) bool {
		return strings.Contains(s, "nonexistent")
	}

	count, err := countMatches(path, matchFunc)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("countMatches = %d, want 0", count)
	}
}

func TestCountMatches_BinaryFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "binary.bin")
	os.WriteFile(path, []byte("text\x00more\n"), 0644)

	matchFunc := func(s string) bool {
		return true // would match everything, but binary should return 0
	}

	count, err := countMatches(path, matchFunc)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("countMatches on binary = %d, want 0", count)
	}
}

func TestCountMatches_NonexistentFile(t *testing.T) {
	count, err := countMatches("/nonexistent/file.go", func(s string) bool { return true })
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if count != 0 {
		t.Errorf("countMatches = %d, want 0 on error", count)
	}
}

func TestCountMatches_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.go")
	os.WriteFile(path, []byte{}, 0644)

	matchFunc := func(s string) bool { return true }
	count, err := countMatches(path, matchFunc)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("countMatches on empty = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: default output mode
// ---------------------------------------------------------------------------

func TestSearchTool_DefaultOutputMode(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\n"), 0644)

	tool := &SearchTool{}
	// No output_mode specified — should default to files_with_matches
	args := json.RawMessage(`{"pattern": "package", "path": "` + tmpDir + `"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result == "No matches found" {
		t.Fatal("expected matches with default output mode")
	}
	// files_with_matches output should just be file paths, no | markers
	if strings.Contains(result, "|") {
		t.Errorf("default mode should not have | markers, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: multiple files in content mode
// ---------------------------------------------------------------------------

func TestSearchTool_ContentModeMultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\nfunc A() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package b\nfunc B() {}\n"), 0644)

	tool := &SearchTool{}
	args := json.RawMessage(`{"pattern": "func", "path": "` + tmpDir + `", "output_mode": "content"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "a.go") {
		t.Errorf("expected a.go in content results, got: %q", result)
	}
	if !strings.Contains(result, "b.go") {
		t.Errorf("expected b.go in content results, got: %q", result)
	}
	// Both files' match lines should be present
	if !strings.Contains(result, "func A") {
		t.Errorf("expected func A in results, got: %q", result)
	}
	if !strings.Contains(result, "func B") {
		t.Errorf("expected func B in results, got: %q", result)
	}
}
