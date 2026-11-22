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

// target_editTool tests
func TestTargetEditTool(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "test_target_edit_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	file1 := "test.txt"
	content := "line 1\nline 2\nline 3\n"
	filePath := filepath.Join(tmpDir, file1)
	// Use forward slashes so the path is valid JSON on all platforms (including Windows).
	filePath = filepath.ToSlash(filePath)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := TargetEditTool{}
	ctx := context.Background()

	t.Run("replace text with search/replace", func(t *testing.T) {
		// Replace "line 2" with "updated line 2"
		args := json.RawMessage(`{
			"file": "` + filePath + `",
			"search": "line 2",
			"replace": "updated line 2"
		}`)
		res, err := tool.Execute(ctx, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(res, "Successfully applied") {
			t.Errorf("expected success message, got: %s", res)
		}

		data, _ := os.ReadFile(filePath)
		expected := "line 1\nupdated line 2\nline 3\n"
		if string(data) != expected {
			t.Errorf("expected %q, got %q", expected, string(data))
		}
	})

	t.Run("search block not found", func(t *testing.T) {
		args := json.RawMessage(`{
			"file": "` + filePath + `",
			"search": "nonexistent",
			"replace": "something"
		}`)
		_, err := tool.Execute(ctx, args)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "search block not found") {
			t.Errorf("expected not found error, got: %v", err)
		}
	})

	t.Run("search block not unique", func(t *testing.T) {
		// Reset file with duplicates
		if err := os.WriteFile(filePath, []byte("line 1\nrepeat\nrepeat\n"), 0644); err != nil {
			t.Fatal(err)
		}

		args := json.RawMessage(`{
			"file": "` + filePath + `",
			"search": "repeat",
			"replace": "unique"
		}`)
		_, err := tool.Execute(ctx, args)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "search block found 2 times") {
			t.Errorf("expected duplicate error, got: %v", err)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		args := json.RawMessage(`{
			"file": "/nonexistent/file.txt",
			"search": "test",
			"replace": "replacement"
		}`)
		_, err := tool.Execute(ctx, args)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expected not found error, got: %v", err)
		}
	})

	t.Run("empty search parameter", func(t *testing.T) {
		args := json.RawMessage(`{
			"file": "` + filePath + `",
			"search": "",
			"replace": "val"
		}`)
		_, err := tool.Execute(ctx, args)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "search block cannot be empty") {
			t.Errorf("expected empty parameter error, got: %v", err)
		}
	})

	t.Run("multiline replace", func(t *testing.T) {
		// Reset file
		if err := os.WriteFile(filePath, []byte("line 1\nline 2\nline 3\n"), 0644); err != nil {
			t.Fatal(err)
		}

		args := json.RawMessage(`{
			"file": "` + filePath + `",
			"search": "line 2",
			"replace": "line 2a\nline 2b"
		}`)

		res, err := tool.Execute(ctx, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(res, "Successfully applied") {
			t.Errorf("expected success message, got: %s", res)
		}

		data, _ := os.ReadFile(filePath)
		fileContent := string(data)

		expected := "line 1\nline 2a\nline 2b\nline 3\n"
		if fileContent != expected {
			t.Errorf("multiline replace failed. Expected %q, got %q", expected, fileContent)
		}
	})
}

func TestTargetEditTool_WhitespaceMismatchDiagnostic(t *testing.T) {
	tool := TargetEditTool{}
	ctx := context.Background()

	runEdit := func(t *testing.T, filePath, search, replace string) error {
		t.Helper()
		payload, err := json.Marshal(struct {
			File    string `json:"file"`
			Search  string `json:"search"`
			Replace string `json:"replace"`
		}{
			File:    filePath,
			Search:  search,
			Replace: replace,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = tool.Execute(ctx, payload)
		return err
	}

	t.Run("off-by-1 leading spaces", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "task.ts"))
		orig := "export class Runner {\n  inspectRejected(fn: () => void): void {\n    fn();\n  }\n}\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(
			t,
			filePath,
			"   inspectRejected(fn: () => void): void {\n     fn();\n   }",
			"  inspectRejected(fn: () => void): void {\n    fn();\n  }",
		)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"search block not found in file",
			"lines 2-4",
			"line 2 has 2 spaces, search had 3 spaces",
			"2|  inspectRejected(fn: () => void): void {",
			"3|    fn();",
			"4|  }",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}

		after, _ := os.ReadFile(filePath)
		if string(after) != orig {
			t.Errorf("file should remain unmodified on diagnostic error, got:\n%s", string(after))
		}
	})

	t.Run("tabs vs spaces", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "handler.go"))
		orig := "func f() error {\n\tif bad {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "    return err", "\t\treturn nil")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"line 3",
			"line 3 has 2 tabs, search had 4 spaces",
			"3|\t\treturn err",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
	})

	t.Run("trailing whitespace mismatch", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "trailing.txt"))
		orig := "alpha\n  beta   \ngamma\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "  beta\ngamma", "  BETA\ngamma")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"lines 2-3",
			"line 2 differs in trailing whitespace",
			"2|  beta   ",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
	})

	t.Run("mixed indentation mismatch", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "mixed.txt"))
		orig := "\t  mixedLine()\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "    mixedLine()", "  cleanLine()")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"line 1 has mixed whitespace",
			"search had 4 spaces",
			"1|\t  mixedLine()",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
	})

	t.Run("multiple whitespace-only matches", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "dup.go"))
		orig := "func a() error {\n\treturn nil\n}\nfunc b() error {\n\treturn nil\n}\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "    return nil", "\treturn err")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"matched 2 locations when ignoring leading/trailing whitespace",
			"line 2",
			"line 5",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
	})

	t.Run("search block with trailing newline", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "nl.ts"))
		orig := "  first()\n  second()\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "    first()\n    second()\n", "  first()\n  third()\n")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"lines 1-2",
			"line 1 has 2 spaces, search had 4 spaces",
			"1|  first()",
			"2|  second()",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
	})

	t.Run("whitespace-only search block", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "blanks.txt"))
		orig := "a\n\nb\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "   \n   ", "x")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "search block not found in file" {
			t.Errorf("expected plain not found error, got: %q", err.Error())
		}
	})

	t.Run("truly nonexistent search block", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "other.txt"))
		orig := "  hello world\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "does not exist anywhere", "replacement")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "search block not found in file" {
			t.Errorf("expected plain not found error, got: %q", err.Error())
		}
	})

	t.Run("multi-line non-uniform indents", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "nonuniform.ts"))
		orig := "header\n   a()\n      b()\n         c()\n"
		if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, "  a()\n    b()\n      c()", "   a()\n      b()\n         d()")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"lines 2-4",
			"line 2 has 3 spaces, search had 2 spaces",
			"line 3 has 6 spaces, search had 4 spaces",
			"line 4 has 9 spaces, search had 6 spaces",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
	})

	t.Run("large block truncation", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.ToSlash(filepath.Join(tmpDir, "large.txt"))
		var fileBuilder, searchBuilder strings.Builder
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(&fileBuilder, "  line_%d\n", i)
			fmt.Fprintf(&searchBuilder, "    line_%d\n", i)
		}
		if err := os.WriteFile(filePath, []byte(fileBuilder.String()), 0644); err != nil {
			t.Fatal(err)
		}

		err := runEdit(t, filePath, strings.TrimSuffix(searchBuilder.String(), "\n"), "  replaced")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"lines 1-30",
			"line 1 has 2 spaces, search had 4 spaces",
			"line 5 has 2 spaces, search had 4 spaces",
			"... (+25 more)",
			"25|  line_25",
			"... (5 more lines)",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("expected error to contain %q, got:\n%s", want, msg)
			}
		}
		if strings.Contains(msg, "line 6 has") {
			t.Errorf("did not expect line 6 diff in header when capped at 5 diffs:\n%s", msg)
		}
		if strings.Contains(msg, "26|  line_26") {
			t.Errorf("did not expect line 26 to be printed when capped at 25 lines:\n%s", msg)
		}
	})
}
