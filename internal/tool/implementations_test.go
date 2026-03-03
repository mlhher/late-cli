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

func TestReadFileTool_PartialRead(t *testing.T) {
	// constant setup
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool()

	// Test case: Read lines 2-4
	args := json.RawMessage(`{"path": "` + filePath + `", "start_line": 2, "end_line": 4}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	expected := "2 | line2\n3 | line3\n4 | line4\n"
	if result != expected {
		t.Errorf("Expected:\n%q\nGot:\n%q", expected, result)
	}

	// Test case: Invalid range
	args = json.RawMessage(`{"path": "` + filePath + `", "start_line": 4, "end_line": 2}`)
	result, err = tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Invalid range") {
		t.Errorf("Expected invalid range error, got: %q", result)
	}
}

func TestReadFileTool_NoCaching(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	content := "unchanged content"
	os.WriteFile(filePath, []byte(content), 0644)

	tool := NewReadFileTool()
	args := json.RawMessage(`{"path": "` + filePath + `"}`)

	// First read
	res1, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res1, "unchanged content") {
		t.Error("First read failed")
	}

	// Second read (should RETURN CONTENT now, not unchanged message)
	res2, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	// It should contain the content again
	if !strings.Contains(res2, "unchanged content") {
		t.Errorf("Expected content to be returned again, got: %q", res2)
	}
	if strings.Contains(res2, "File has not changed") {
		t.Error("Should not return unchanged message")
	}

	// Modify file
	os.WriteFile(filePath, []byte("new content"), 0644)

	// Third read (should return new content)
	res3, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res3, "new content") {
		t.Errorf("Expected new content, got: %q", res3)
	}
}

func TestBashTool_Execute(t *testing.T) {
	tests := []struct {
		name    string
		params  json.RawMessage
		wantErr bool
		wantOut string
	}{
		{
			name:    "whitelisted command echo hello",
			params:  json.RawMessage(`{"command": "echo", "args": ["hello"]}`),
			wantErr: false,
			wantOut: "hello",
		},
		{
			name:    "non-whitelisted command rm",
			params:  json.RawMessage(`{"command": "rm", "args": ["-rf", "/"]}`),
			wantErr: true,
			wantOut: "",
		},
		{
			name:    "whitelisted command pwd",
			params:  json.RawMessage(`{"command": "pwd"}`),
			wantErr: false,
			wantOut: "tool", // pwd returns path containing "tool" (the package directory)
		},
		{
			name:    "whitelisted command with multiple args",
			params:  json.RawMessage(`{"command": "echo", "args": ["hello", "world", "test"]}`),
			wantErr: false,
			wantOut: "hello world test",
		},
		{
			name:    "full command string in command field (fallback parsing)",
			params:  json.RawMessage(`{"command": "echo hello world"}`),
			wantErr: true,
			wantOut: "",
		},
		{
			name:    "full command string with non-whitelisted base command",
			params:  json.RawMessage(`{"command": "rm -rf /"}`),
			wantErr: true,
			wantOut: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := BashTool{}
			out, err := tool.Execute(context.Background(), tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				if out != "" {
					t.Errorf("Execute() expected error, got output: %q", out)
				}
			} else {
				if !strings.Contains(out, tt.wantOut) {
					t.Errorf("Execute() output = %q, want to contain %q", out, tt.wantOut)
				}
			}
		})
	}
}

func TestBashTool_CWDParameter(t *testing.T) {
	// Create a subdirectory within the current working directory
	// Use a subdirectory of the package directory to ensure it's within allowed paths
	tmpDir := filepath.Join("internal", "tool", "test_cwd")
	err := os.MkdirAll(tmpDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tool := BashTool{}

	// Test with custom cwd
	params := json.RawMessage(fmt.Sprintf(`{"command": "pwd", "cwd": "%s"}`, tmpDir))
	out, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, tmpDir) {
		t.Errorf("Execute() output = %q, want to contain %q", out, tmpDir)
	}
}

func TestBashTool_MultipleArgs(t *testing.T) {
	tool := BashTool{}

	// Test with multiple arguments
	params := json.RawMessage(`{"command": "echo", "args": ["arg1", "arg2", "arg3"]}`)
	out, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "arg1 arg2 arg3"
	// Trim trailing newline
	out = strings.TrimSpace(out)
	if out != expected {
		t.Errorf("Execute() output = %q, want %q", out, expected)
	}
}

func TestBashTool_OutputTruncation(t *testing.T) {
	tool := BashTool{}

	// Create a command that outputs more than 1024 lines
	// Using seq to generate numbers 1-2000
	params := json.RawMessage(`{"command": "seq", "args": ["1", "2000"]}`)
	out, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Count lines in output
	lines := strings.Split(out, "\n")
	if len(lines) > 1025 { // 1024 lines + truncation message
		t.Errorf("Output has %d lines, expected max 1025", len(lines))
	}

	// Check that truncation message is present
	if !strings.Contains(out, "... (output truncated)") {
		t.Error("Expected output to contain truncation message")
	}
}

func TestBashTool_UnsafeCWD(t *testing.T) {
	tool := BashTool{}

	// Try to use an unsafe cwd (outside CWD)
	// This should fail if we're not running from root
	params := json.RawMessage(`{"command": "pwd", "cwd": "/tmp"}`)
	out, err := tool.Execute(context.Background(), params)

	// The test depends on where we're running from
	// If /tmp is within CWD, this should succeed
	// If /tmp is outside CWD, this should fail
	cwd, _ := os.Getwd()
	absTmp, _ := filepath.Abs("/tmp")

	if !strings.HasPrefix(absTmp, cwd) {
		// /tmp is outside CWD, should return error
		if err == nil {
			t.Errorf("Execute() expected error for unsafe cwd, got output: %q", out)
		}
	} else {
		// /tmp is within CWD, should succeed
		if err != nil {
			t.Errorf("Execute() unexpected error for safe cwd: %v", err)
		}
	}
}

func TestBashTool_DefaultCWD(t *testing.T) {
	tool := BashTool{}

	// Execute without cwd parameter - should use current directory
	params := json.RawMessage(`{"command": "pwd"}`)
	out, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should return the current working directory
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}

	if !strings.Contains(out, currentDir) {
		t.Errorf("Execute() output = %q, want to contain %q", out, currentDir)
	}
}

func TestBashTool_CallString(t *testing.T) {
	tests := []struct {
		name     string
		params   json.RawMessage
		expected string
	}{
		{
			name:     "simple command",
			params:   json.RawMessage(`{"command": "echo", "args": ["hello"]}`),
			expected: "Executing: echo hello",
		},
		{
			name:     "command with cwd",
			params:   json.RawMessage(`{"command": "pwd", "cwd": "/tmp"}`),
			expected: "Executing: pwd in dir: /tmp",
		},
		{
			name:     "command with args and cwd",
			params:   json.RawMessage(`{"command": "echo", "args": ["a", "b", "c"], "cwd": "/tmp"}`),
			expected: "Executing: echo a b c in dir: /tmp",
		},
		{
			name:     "command only",
			params:   json.RawMessage(`{"command": "pwd"}`),
			expected: "Executing: pwd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := BashTool{}
			result := tool.CallString(tt.params)
			if result != tt.expected {
				t.Errorf("CallString() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBashTool_RequiresConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		params   json.RawMessage
		expected bool
	}{
		{
			name:     "whitelisted command grep",
			params:   json.RawMessage(`{"command": "grep", "args": ["-r", "pattern", "."]}`),
			expected: false,
		},
		{
			name:     "whitelisted command find",
			params:   json.RawMessage(`{"command": "find", "args": [".", "-name", "*.go"]}`),
			expected: false,
		},
		{
			name:     "whitelisted command ls",
			params:   json.RawMessage(`{"command": "ls"}`),
			expected: false,
		},
		{
			name:     "non-whitelisted command rm",
			params:   json.RawMessage(`{"command": "rm", "args": ["-rf", "/"]}`),
			expected: true,
		},
		{
			name:     "non-whitelisted command curl",
			params:   json.RawMessage(`{"command": "curl"}`),
			expected: true,
		},
		{
			name:     "full command string with whitelisted base",
			params:   json.RawMessage(`{"command": "find /var/home -type f -name *.go"}`),
			expected: false,
		},
		{
			name:     "full command string with non-whitelisted base",
			params:   json.RawMessage(`{"command": "rm -rf /"}`),
			expected: true,
		},
		{
			name:     "invalid JSON",
			params:   json.RawMessage(`{invalid}`),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := BashTool{}
			result := tool.RequiresConfirmation(tt.params)
			if result != tt.expected {
				t.Errorf("RequiresConfirmation() = %v, want %v", result, tt.expected)
			}
		})
	}
}
