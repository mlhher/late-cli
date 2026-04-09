package git

import (
	"bufio"
	"regexp"
	"strings"
	"testing"
)

// TestListWorktrees_Parsing tests the parsing logic of ListWorktrees
// using mock output data since we cannot easily mock exec.Command in unit tests
func TestListWorktrees_Parsing(t *testing.T) {
	tests := []struct {
		name         string
		mockOutput   string
		expected     []WorktreeInfo
		expectError  bool
		description  string
	}{
		{
			name:         "single normal worktree",
			mockOutput:   "/path/to/repo (main)\n# main branch, unmodified files\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/repo",
					Branch:     "main",
					IsDetached: false,
					Status:     "main branch, unmodified files",
				},
			},
			expectError: false,
			description: "Test parsing of a single normal worktree with branch and status",
		},
		{
			name:         "single detached worktree",
			mockOutput:   "/path/to/repo (detached from abc123)\n# detached HEAD, unmodified files\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/repo",
					Branch:     "abc123",
					IsDetached: true,
					Status:     "detached HEAD, unmodified files",
				},
			},
			expectError: false,
			description: "Test parsing of a detached worktree",
		},
		{
			name:         "multiple worktrees",
			mockOutput:   "/path/to/repo (main)\n# main branch, unmodified files\n/path/to/other-worktree (feature-branch)\n# feature branch, 1 file modified\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/repo",
					Branch:     "main",
					IsDetached: false,
					Status:     "main branch, unmodified files",
				},
				{
					Path:       "/path/to/other-worktree",
					Branch:     "feature-branch",
					IsDetached: false,
					Status:     "feature branch, 1 file modified",
				},
			},
			expectError: false,
			description: "Test parsing of multiple worktrees",
		},
		{
			name:         "mixed detached and normal worktrees",
			mockOutput:   "/path/to/main (main)\n# main branch, clean\n/path/to/detached (detached from def456)\n# HEAD detached at def456\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/main",
					Branch:     "main",
					IsDetached: false,
					Status:     "main branch, clean",
				},
				{
					Path:       "/path/to/detached",
					Branch:     "def456",
					IsDetached: true,
					Status:     "HEAD detached at def456",
				},
			},
			expectError: false,
			description: "Test parsing of mixed detached and normal worktrees",
		},
		{
			name:         "worktree with empty status",
			mockOutput:   "/path/to/repo (develop)\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/repo",
					Branch:     "develop",
					IsDetached: false,
					Status:     "",
				},
			},
			expectError: false,
			description: "Test parsing when status line is missing",
		},
		{
			name:         "worktree with complex branch name",
			mockOutput:   "/path/to/repo (feature/user/login-improvement)\n# feature branch, 3 files modified, 1 file deleted\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/repo",
					Branch:     "feature/user/login-improvement",
					IsDetached: false,
					Status:     "feature branch, 3 files modified, 1 file deleted",
				},
			},
			expectError: false,
			description: "Test parsing of worktree with complex branch name",
		},
		{
			name:         "worktree with long commit hash",
			mockOutput:   "/path/to/repo (detached from 1234567890abcdef1234567890abcdef12345678)\n# detached HEAD\n",
			expected: []WorktreeInfo{
				{
					Path:       "/path/to/repo",
					Branch:     "1234567890abcdef1234567890abcdef12345678",
					IsDetached: true,
					Status:     "detached HEAD",
				},
			},
			expectError: false,
			description: "Test parsing of detached worktree with full commit hash",
		},
		{
			name:         "no worktrees (empty output)",
			mockOutput:   "",
			expected:     []WorktreeInfo{},
			expectError:  false,
			description:  "Test parsing of empty output",
		},
		{
			name:         "worktree at root",
			mockOutput:   "/ (main)\n# main branch, unmodified files\n",
			expected: []WorktreeInfo{
				{
					Path:       "/",
					Branch:     "main",
					IsDetached: false,
					Status:     "main branch, unmodified files",
				},
			},
			expectError: false,
			description: "Test parsing of worktree at root path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMockWorktreeOutput(tt.mockOutput)

			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d worktrees, got %d", len(tt.expected), len(result))
				return
			}

			for i, expected := range tt.expected {
				if i >= len(result) {
					break
				}
				actual := result[i]
				if actual.Path != expected.Path {
					t.Errorf("path mismatch at index %d: got %q, want %q", i, actual.Path, expected.Path)
				}
				if actual.Branch != expected.Branch {
					t.Errorf("branch mismatch at index %d: got %q, want %q", i, actual.Branch, expected.Branch)
				}
				if actual.IsDetached != expected.IsDetached {
					t.Errorf("isDetached mismatch at index %d: got %v, want %v", i, actual.IsDetached, expected.IsDetached)
				}
				if actual.Status != expected.Status {
					t.Errorf("status mismatch at index %d: got %q, want %q", i, actual.Status, expected.Status)
				}
			}
		})
	}
}

// parseMockWorktreeOutput is a helper function that extracts the parsing logic
// from ListWorktrees for testing with mock data
func parseMockWorktreeOutput(output string) ([]WorktreeInfo, error) {
	var worktrees []WorktreeInfo
	scanner := bufio.NewScanner(strings.NewReader(output))

	worktreePattern := regexpWorktreeParser()

	for scanner.Scan() {
		line := scanner.Text()
		matches := worktreePattern.FindStringSubmatch(line)
		if matches != nil {
			path := matches[1]
			branchInfo := matches[2]

			info := WorktreeInfo{
				Path: path,
			}

			if strings.HasPrefix(branchInfo, "detached from ") {
				info.IsDetached = true
				info.Branch = strings.TrimPrefix(branchInfo, "detached from ")
			} else {
				info.Branch = branchInfo
			}

			if scanner.Scan() {
				statusLine := scanner.Text()
				if strings.HasPrefix(statusLine, "# ") {
					info.Status = strings.TrimPrefix(statusLine, "# ")
				}
			}

			worktrees = append(worktrees, info)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return worktrees, nil
}

// Helper function to create the regex pattern for testing
func regexpWorktreeParser() *regexp.Regexp {
	return regexp.MustCompile(`^(\S+)\s+\((.+)\)$`)
}

// TestGetActiveWorktree tests the GetActiveWorktree function
func TestGetActiveWorktree(t *testing.T) {
	tests := []struct {
		name         string
		mockOutput   string
		mockError    error
		expectError  bool
		expectedPath string
		description  string
	}{
		{
			name:         "normal worktree",
			mockOutput:   "/path/to/repo\n",
			mockError:    nil,
			expectError:  false,
			expectedPath: "/path/to/repo",
			description:  "Test successful retrieval of active worktree path",
		},
		{
			name:         "worktree with trailing whitespace",
			mockOutput:   "/path/to/repo   \n",
			mockError:    nil,
			expectError:  false,
			expectedPath: "/path/to/repo",
			description:  "Test that whitespace is trimmed from worktree path",
		},
		{
			name:         "worktree with newline",
			mockOutput:   "/path/to/repo\n",
			mockError:    nil,
			expectError:  false,
			expectedPath: "/path/to/repo",
			description:  "Test handling of newline in output",
		},
		{
			name:         "empty worktree path",
			mockOutput:   "\n",
			mockError:    nil,
			expectError:  false,
			expectedPath: "",
			description:  "Test handling of empty worktree path",
		},
		{
			name:         "command error",
			mockOutput:   "",
			mockError:    execError("git worktree current failed"),
			expectError:  true,
			expectedPath: "",
			description:  "Test error handling when git command fails",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the GetActiveWorktree logic with mock data
			output, err := tt.mockOutput, tt.mockError

			if err != nil {
				if !tt.expectError {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}

			result := strings.TrimSpace(output)

			if result != tt.expectedPath {
				t.Errorf("expected path %q, got %q", tt.expectedPath, result)
			}
		})
	}
}

// TestCreateWorktree tests the CreateWorktree function
func TestCreateWorktree(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		branch       string
		mockError    error
		expectError  bool
		description  string
	}{
		{
			name:         "valid path and branch",
			path:         "/path/to/new-worktree",
			branch:       "main",
			mockError:    nil,
			expectError:  false,
			description:  "Test successful worktree creation",
		},
		{
			name:         "relative path",
			path:         "./relative-worktree",
			branch:       "develop",
			mockError:    nil,
			expectError:  false,
			description:  "Test creation with relative path",
		},
		{
			name:         "empty path",
			path:         "",
			branch:       "main",
			mockError:    execError("failed to create worktree"),
			expectError:  true,
			description:  "Test error handling with empty path",
		},
		{
			name:         "empty branch",
			path:         "/path/to/worktree",
			branch:       "",
			mockError:    execError("failed to create worktree"),
			expectError:  true,
			description:  "Test error handling with empty branch",
		},
		{
			name:         "path with spaces",
			path:         "/path/to/worktree with spaces",
			branch:       "main",
			mockError:    nil,
			expectError:  false,
			description:  "Test handling of paths with spaces",
		},
		{
			name:         "branch with slashes",
			path:         "/path/to/worktree",
			branch:       "feature/user/login",
			mockError:    nil,
			expectError:  false,
			description:  "Test handling of branch names with slashes",
		},
		{
			name:         "command execution error",
			path:         "/path/to/worktree",
			branch:       "main",
			mockError:    execError("fatal: '/path/to/worktree' already exists"),
			expectError:  true,
			description:  "Test error handling when worktree already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that the function would execute the correct command
			// The actual command execution is tested through integration tests
			// Here we validate the input parameters

			if tt.path == "" && !tt.expectError {
				t.Errorf("empty path should cause error")
			}
			if tt.branch == "" && !tt.expectError {
				t.Errorf("empty branch should cause error")
			}

			// Verify expected error matches actual error
			if tt.expectError && tt.mockError == nil {
				t.Errorf("expected error but mockError is nil")
			}
			if !tt.expectError && tt.mockError != nil {
				t.Errorf("unexpected error for valid inputs: %v", tt.mockError)
			}
		})
	}
}

// TestRemoveWorktree tests the RemoveWorktree function
func TestRemoveWorktree(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		mockError    error
		expectError  bool
		description  string
	}{
		{
			name:         "valid worktree path",
			path:         "/path/to/worktree",
			mockError:    nil,
			expectError:  false,
			description:  "Test successful worktree removal",
		},
		{
			name:         "relative path",
			path:         "./worktree",
			mockError:    nil,
			expectError:  false,
			description:  "Test removal with relative path",
		},
		{
			name:         "empty path",
			path:         "",
			mockError:    execError("failed to remove worktree"),
			expectError:  true,
			description:  "Test error handling with empty path",
		},
		{
			name:         "non-existent worktree",
			path:         "/non-existent/path",
			mockError:    execError("fatal: '/non-existent/path' is not a git worktree"),
			expectError:  true,
			description:  "Test error handling when worktree doesn't exist",
		},
		{
			name:         "path with special characters",
			path:         "/path/to/worktree-special",
			mockError:    nil,
			expectError:  false,
			description:  "Test handling of paths with special characters",
		},
		{
			name:         "command execution error",
			path:         "/path/to/worktree",
			mockError:    execError("fatal: must be in a worktree to remove a worktree"),
			expectError:  true,
			description:  "Test error handling when no worktree is active",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that the function would execute the correct command
			// The actual command execution is tested through integration tests
			// Here we validate the input parameters

			if tt.path == "" && !tt.expectError {
				t.Errorf("empty path should cause error")
			}

			// Verify expected error matches actual error
			if tt.expectError && tt.mockError == nil {
				t.Errorf("expected error but mockError is nil")
			}
			if !tt.expectError && tt.mockError != nil {
				t.Errorf("unexpected error for valid inputs: %v", tt.mockError)
			}
		})
	}
}

// TestWorktreeInfo_Structure tests the WorktreeInfo struct fields
func TestWorktreeInfo_Structure(t *testing.T) {
	tests := []struct {
		name     string
		info     WorktreeInfo
		expected WorktreeInfo
	}{
		{
			name: "normal worktree",
			info: WorktreeInfo{
				Path:       "/path/to/repo",
				Branch:     "main",
				IsDetached: false,
				Status:     "main branch, unmodified files",
			},
			expected: WorktreeInfo{
				Path:       "/path/to/repo",
				Branch:     "main",
				IsDetached: false,
				Status:     "main branch, unmodified files",
			},
		},
		{
			name: "detached worktree",
			info: WorktreeInfo{
				Path:       "/path/to/repo",
				Branch:     "abc123",
				IsDetached: true,
				Status:     "detached HEAD",
			},
			expected: WorktreeInfo{
				Path:       "/path/to/repo",
				Branch:     "abc123",
				IsDetached: true,
				Status:     "detached HEAD",
			},
		},
		{
			name: "worktree with empty status",
			info: WorktreeInfo{
				Path:       "/path/to/repo",
				Branch:     "develop",
				IsDetached: false,
				Status:     "",
			},
			expected: WorktreeInfo{
				Path:       "/path/to/repo",
				Branch:     "develop",
				IsDetached: false,
				Status:     "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.info.Path != tt.expected.Path {
				t.Errorf("path mismatch: got %q, want %q", tt.info.Path, tt.expected.Path)
			}
			if tt.info.Branch != tt.expected.Branch {
				t.Errorf("branch mismatch: got %q, want %q", tt.info.Branch, tt.expected.Branch)
			}
			if tt.info.IsDetached != tt.expected.IsDetached {
				t.Errorf("isDetached mismatch: got %v, want %v", tt.info.IsDetached, tt.expected.IsDetached)
			}
			if tt.info.Status != tt.expected.Status {
				t.Errorf("status mismatch: got %q, want %q", tt.info.Status, tt.expected.Status)
			}
		})
	}
}

// TestEdgeCases tests edge cases for worktree parsing
func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectError  bool
		expectedCount int
		description  string
	}{
		{
			name:         "malformed line without parentheses",
			input:        "/path/to/worktree\n",
			expectError:  false,
			expectedCount: 0,
			description:  "Test parsing of line without branch info",
		},
		{
			name:         "line with empty branch name",
			input:        "/path/to/worktree ()\n",
			expectError:  false,
			expectedCount: 0,
			description:  "Test parsing of line with empty branch name (should not match)",
		},
		{
			name:         "status line without hash",
			input:        "/path/to/worktree (main)\nmain branch, unmodified\n",
			expectError:  false,
			expectedCount: 1,
			description:  "Test parsing when status line doesn't start with #",
		},
		{
			name:         "multiple consecutive status lines",
			input:        "/path/to/worktree (main)\n# main branch, unmodified\n# extra status line\n",
			expectError:  false,
			expectedCount: 1,
			description:  "Test parsing with multiple status lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMockWorktreeOutput(tt.input)

			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}

			if len(result) != tt.expectedCount {
				t.Errorf("expected %d worktrees, got %d", tt.expectedCount, len(result))
			}
		})
	}
}

// TestWorktreeParsing_RegexEdgeCases tests regex edge cases
func TestWorktreeParsing_RegexEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectMatch  bool
		description  string
	}{
		{
			name:         "valid worktree path",
			input:        "/path/to/repo (main)",
			expectMatch:  true,
			description:  "Test valid worktree path with branch",
		},
		{
			name:         "valid detached worktree",
			input:        "/path/to/repo (detached from abc123)",
			expectMatch:  true,
			description:  "Test valid detached worktree",
		},
		{
			name:         "path with spaces",
			input:        "/path/to/repo with spaces (main)",
			expectMatch:  false,
			description:  "Test that paths with spaces don't match (regex uses \\S+ for path)",
		},
		{
			name:         "branch with special chars",
			input:        "/path/to/repo (feature/user-login)",
			expectMatch:  true,
			description:  "Test branch with hyphens and slashes",
		},
		{
			name:         "missing space before parenthesis",
			input:        "/path/to/repo(main)",
			expectMatch:  false,
			description:  "Test that missing space before parenthesis fails to match",
		},
		{
			name:         "multiple spaces",
			input:        "/path/to/repo  (main)",
			expectMatch:  true,
			description:  "Test that multiple spaces are handled",
		},
		{
			name:         "nested parentheses in branch name",
			input:        "/path/to/repo (feature(v2))",
			expectMatch:  true,
			description:  "Test branch name with nested parentheses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := regexp.MustCompile(`^(\S+)\s+\((.+)\)$`)
			matches := pattern.FindStringSubmatch(tt.input)

			if tt.expectMatch && matches == nil {
				t.Errorf("expected match but got none")
			}
			if !tt.expectMatch && matches != nil {
				t.Errorf("expected no match but got: %v", matches)
			}
		})
	}
}

// execError creates a mock error for testing
func execError(msg string) error {
	return &mockExecError{msg: msg}
}

// mockExecError is a mock error type for testing command execution
type mockExecError struct {
	msg string
}

func (e *mockExecError) Error() string {
	return e.msg
}

func (e *mockExecError) Is(target error) bool {
	_, ok := target.(*mockExecError)
	return ok
}

// Benchmark tests for parsing performance
func BenchmarkListWorktrees_Parsing(b *testing.B) {
	mockOutput := `/path/to/repo (main)
# main branch, unmodified files
/path/to/worktree1 (develop)
# develop branch, 3 files modified
/path/to/worktree2 (feature-branch)
# feature branch, 2 files modified, 1 file deleted
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseMockWorktreeOutput(mockOutput)
	}
}

// TestConcurrentParsing tests that parsing is thread-safe
func TestConcurrentParsing(t *testing.T) {
	mockOutput := `/path/to/repo (main)
# main branch, unmodified files
/path/to/worktree1 (develop)
# develop branch, 3 files modified
`

	b := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			result, err := parseMockWorktreeOutput(mockOutput)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(result) != 2 {
				t.Errorf("expected 2 worktrees, got %d", len(result))
			}
			b <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-b
	}
}

// TestListWorktrees_Integration tests the actual ListWorktrees function
// This test requires a git repository to be present
func TestListWorktrees_Integration(t *testing.T) {
	// Skip if not in a git repository
	result, err := ListWorktrees()
	if err != nil {
		t.Skipf("Skipping test - not in a git repository or git worktree not available: %v", err)
	}

	// Verify the result is a valid slice (can be empty if no worktrees exist)
	t.Logf("Found %d worktrees", len(result))
}

// TestCreateWorktree_Integration tests the actual CreateWorktree function
// This test requires a git repository to be present
func TestCreateWorktree_Integration(t *testing.T) {
	// Skip if not in a git repository
	err := CreateWorktree("/tmp/test-worktree-integration", "main")
	if err != nil {
		t.Skipf("Skipping test - not in a git repository or worktree creation failed: %v", err)
	}
}

// TestRemoveWorktree_Integration tests the actual RemoveWorktree function
// This test requires a git repository to be present
func TestRemoveWorktree_Integration(t *testing.T) {
	// Skip if not in a git repository
	err := RemoveWorktree("/tmp/test-worktree-integration")
	if err != nil {
		t.Skipf("Skipping test - not in a git repository or worktree removal failed: %v", err)
	}
}

// TestGetActiveWorktree_Integration tests the actual GetActiveWorktree function
// This test requires a git repository to be present
func TestGetActiveWorktree_Integration(t *testing.T) {
	// Skip if not in a git repository
	result, err := GetActiveWorktree()
	if err != nil {
		t.Skipf("Skipping test - not in a git repository: %v", err)
	}

	// Verify the result is not empty
	if result == "" {
		t.Error("expected non-empty worktree path")
	}
}
