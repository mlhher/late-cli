package tool

import (
	"context"
	"encoding/json"
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
