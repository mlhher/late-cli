package tool

import (
	"testing"
)

// TestIsCdCommand tests the isCdCommand function
func TestIsCdCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantMatch bool
		wantErr   bool
	}{
		// cd commands that should match
		{
			name:      "cd /tmp",
			command:   "cd /tmp",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd ..",
			command:   "cd ..",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd -",
			command:   "cd -",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd ~",
			command:   "cd ~",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd with leading whitespace",
			command:   "  cd /path",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd with tabs",
			command:   "\tcd /path",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd with mixed whitespace",
			command:   "  \t  cd /path",
			wantMatch: true,
			wantErr:   true,
		},
		// NOT cd commands
		{
			name:      "cd_log (not a cd command)",
			command:   "cd_log",
			wantMatch: false,
			wantErr:   false,
		},
		{
			name:      "mkdir (safe command)",
			command:   "mkdir /tmp",
			wantMatch: false,
			wantErr:   false,
		},
		{
			name:      "find (safe command)",
			command:   "find /tmp",
			wantMatch: false,
			wantErr:   false,
		},
		{
			name:      "grep (safe command)",
			command:   "grep \"pattern\" file",
			wantMatch: false,
			wantErr:   false,
		},
		{
			name:      "cd_log with args",
			command:   "cd_log arg1 arg2",
			wantMatch: false,
			wantErr:   false,
		},
		{
			name:      "mkdir with subcommand",
			command:   "mkdir -p /tmp/test",
			wantMatch: false,
			wantErr:   false,
		},
		// Edge cases with comments
		{
			name:      "cd with comment",
			command:   "cd /tmp # this is a comment",
			wantMatch: true,
			wantErr:   true,
		},
		{
			name:      "cd_log with comment",
			command:   "cd_log # not a cd command",
			wantMatch: false,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatch, gotErr := isCdCommand(tt.command)
			if gotMatch != tt.wantMatch {
				t.Errorf("isCdCommand() match = %v, want %v", gotMatch, tt.wantMatch)
			}
			if gotErr != nil && !tt.wantErr {
				t.Errorf("isCdCommand() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if gotErr == nil && tt.wantErr {
				t.Errorf("isCdCommand() expected error but got nil")
			}
		})
	}
}

// TestValidateBashCommand tests the ValidateBashCommand method
func TestValidateBashCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantError bool
	}{
		// Commands that should return error
		{
			name:      "cd /tmp should return error",
			command:   "cd /tmp",
			wantError: true,
		},
		{
			name:      "cat > file should return error (malicious cat)",
			command:   "cat > file",
			wantError: true,
		},
		// Commands that should NOT return error
		{
			name:      "ls -la should return nil (safe)",
			command:   "ls -la",
			wantError: false,
		},
		{
			name:      "grep \"test\" file should return nil (safe)",
			command:   "grep \"test\" file",
			wantError: false,
		},
		{
			name:      "cat file.txt (reading) should return nil (safe)",
			command:   "cat file.txt",
			wantError: false,
		},
		{
			name:      "echo hello should return nil (safe)",
			command:   "echo hello",
			wantError: false,
		},
		{
			name:      "find /tmp should return nil (safe)",
			command:   "find /tmp",
			wantError: false,
		},
		{
			name:      "mkdir /tmp should return nil (safe)",
			command:   "mkdir /tmp",
			wantError: false,
		},
	}

	tool := BashTool{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.ValidateBashCommand(tt.command)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateBashCommand() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// TestValidateBashCommandEdgeCases tests edge cases for ValidateBashCommand
func TestValidateBashCommandEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantError bool
	}{
		// Edge cases
		{
			name:      "Empty command should return nil",
			command:   "",
			wantError: false,
		},
		{
			name:      "Command with comment after cd should return error",
			command:   "cd /tmp # comment",
			wantError: true,
		},
		{
			name:      "Command with leading tabs should return error",
			command:   "\tcd /tmp",
			wantError: true,
		},
		{
			name:      "Mixed whitespace should return error",
			command:   "  \t  cd /tmp",
			wantError: true,
		},
		{
			name:      "Newline before cd",
			command:   "\ncd /tmp",
			wantError: true,
		},
		{
			name:      "Multiple spaces before cd",
			command:   "    cd /tmp",
			wantError: true,
		},
		{
			name:      "Tab and spaces before cd",
			command:   "\t   cd /tmp",
			wantError: true,
		},
		// Edge cases that should pass
		{
			name:      "Empty string",
			command:   "",
			wantError: false,
		},
		{
			name:      "Only whitespace",
			command:   "   ",
			wantError: false,
		},
		{
			name:      "Comment only",
			command:   "# this is a comment",
			wantError: false,
		},
		{
			name:      "cd_log command",
			command:   "cd_log",
			wantError: false,
		},
		{
			name:      "Safe command",
			command:   "ls -la",
			wantError: false,
		},
		// Edge cases with malicious cat
		{
			name:      "cat with comment",
			command:   "cat > file # comment",
			wantError: true,
		},
		{
			name:      "cat >> file",
			command:   "cat >> file",
			wantError: true,
		},
		{
			name:      "cat 2> file",
			command:   "cat 2> file",
			wantError: true,
		},
		{
			name:      "| cat > file",
			command:   "echo test | cat > file",
			wantError: true,
		},
	}

	tool := BashTool{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.ValidateBashCommand(tt.command)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateBashCommand(%q) error = %v, wantError %v", tt.command, err, tt.wantError)
			}
		})
	}
}

// TestIsCdCommand_WhitespaceVariations tests various whitespace scenarios
func TestIsCdCommand_WhitespaceVariations(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantMatch bool
	}{
		// Various leading whitespace combinations
		{
			name:      "single space",
			command:   " cd /tmp",
			wantMatch: true,
		},
		{
			name:      "two spaces",
			command:   "  cd /tmp",
			wantMatch: true,
		},
		{
			name:      "three spaces",
			command:   "   cd /tmp",
			wantMatch: true,
		},
		{
			name:      "single tab",
			command:   "\tcd /tmp",
			wantMatch: true,
		},
		{
			name:      "two tabs",
			command:   "\t\tcd /tmp",
			wantMatch: true,
		},
		{
			name:      "mixed spaces and tabs",
			command:   " \t cd /tmp",
			wantMatch: true,
		},
		{
			name:      "spaces, tabs, and spaces",
			command:   "  \t  \t  cd /tmp",
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatch, _ := isCdCommand(tt.command)
			if gotMatch != tt.wantMatch {
				t.Errorf("isCdCommand(%q) match = %v, want %v", tt.command, gotMatch, tt.wantMatch)
			}
		})
	}
}

// TestIsCdCommand_NonCdCommands tests that non-cd commands are not matched
func TestIsCdCommand_NonCdCommands(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantMatch bool
	}{
		// Commands that contain "cd" but are not cd commands
		{
			name:      "cd_log",
			command:   "cd_log",
			wantMatch: false,
		},
		{
			name:      "cd_log with args",
			command:   "cd_log arg1",
			wantMatch: false,
		},
		{
			name:      "mkdir (contains d but not cd)",
			command:   "mkdir /tmp",
			wantMatch: false,
		},
		{
			name:      "rmdir",
			command:   "rmdir /tmp",
			wantMatch: false,
		},
		{
			name:      "cdrecord",
			command:   "cdrecord -v",
			wantMatch: false,
		},
		{
			name:      "ncd (ends with cd)",
			command:   "ncd",
			wantMatch: false,
		},
		// Safe cd-like commands
		{
			name:      "cd with comment (should still match)",
			command:   "cd /tmp # comment",
			wantMatch: true,
		},
		{
			name:      "cd with trailing spaces",
			command:   "cd /tmp   ",
			wantMatch: true,
		},
		{
			name:      "cd with trailing tabs",
			command:   "cd /tmp\t\t",
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatch, _ := isCdCommand(tt.command)
			if gotMatch != tt.wantMatch {
				t.Errorf("isCdCommand(%q) match = %v, want %v", tt.command, gotMatch, tt.wantMatch)
			}
		})
	}
}

// TestValidateBashCommand_Comprehensive tests comprehensive validation scenarios
func TestValidateBashCommand_Comprehensive(t *testing.T) {
	tool := BashTool{}

	// Test all malicious cat patterns
	maliciousPatterns := []string{
		"cat > file",
		"cat >> file",
		"cat 2> file",
		"cat 1> file",
		"cat 0> file",
		"echo test | cat > file",
		"echo test | cat >> file",
		"| cat > file",
		"echo test | cat 2> file",
	}

	for _, cmd := range maliciousPatterns {
		t.Run("malicious_"+cmd, func(t *testing.T) {
			err := tool.ValidateBashCommand(cmd)
			if err == nil {
				t.Errorf("Expected error for malicious command %q, got nil", cmd)
			}
		})
	}

	// Test all cd patterns
	cdPatterns := []string{
		"cd /tmp",
		"cd ..",
		"cd -",
		"cd ~",
		// Note: bare "cd" without arguments does NOT match the regex pattern
		// which requires whitespace after "cd"
		"  cd /tmp",
		"\tcd /tmp",
		"cd /tmp # comment",
	}

	for _, cmd := range cdPatterns {
		t.Run("cd_"+cmd, func(t *testing.T) {
			err := tool.ValidateBashCommand(cmd)
			if err == nil {
				t.Errorf("Expected error for cd command %q, got nil", cmd)
			}
		})
	}

	// Test safe commands
	safeCommands := []string{
		"cat file.txt",
		"cat file1.txt file2.txt",
		"cat < file.txt",
		"cat file.txt | grep pattern",
		"ls -la",
		"ls -l",
		"find /tmp",
		"find . -name *.go",
		"grep pattern file.txt",
		"grep -r pattern .",
		"echo hello",
		"echo hello world",
		"pwd",
		"date",
		"whoami",
		"mkdir /tmp",
		"mkdir -p /tmp/test",
		"touch file.txt",
		"head file.txt",
		"tail file.txt",
		"rm file.txt", // Note: rm is whitelisted but not blocked by validation
	}

	for _, cmd := range safeCommands {
		t.Run("safe_"+cmd, func(t *testing.T) {
			err := tool.ValidateBashCommand(cmd)
			if err != nil {
				t.Errorf("Expected no error for safe command %q, got %v", cmd, err)
			}
		})
	}
}

// TestValidateBashCommand_ErrorMessages tests that error messages are informative
func TestValidateBashCommand_ErrorMessages(t *testing.T) {
	tool := BashTool{}

	// Test cd command error message
	err := tool.ValidateBashCommand("cd /tmp")
	if err == nil {
		t.Fatal("Expected error for cd command, got nil")
	}
	errorMsg := err.Error()
	if !contains(errorMsg, "cd") {
		t.Errorf("Error message should mention 'cd', got: %q", errorMsg)
	}
	if !contains(errorMsg, "cwd") {
		t.Errorf("Error message should suggest using 'cwd' parameter, got: %q", errorMsg)
	}

	// Test malicious cat error message
	err = tool.ValidateBashCommand("cat > file")
	if err == nil {
		t.Fatal("Expected error for malicious cat command, got nil")
	}
	errorMsg = err.Error()
	if !contains(errorMsg, "cat cannot be used with output redirection") {
		t.Errorf("Error message should describe cat redirection issue, got: %q", errorMsg)
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
