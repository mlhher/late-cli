package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Maximum number of characters for search output to prevent session poisoning.
const maxSearchChars = 32768

// SearchTool performs file and content search using Go's standard library.
// It walks directories with filepath.WalkDir, reads files with bufio.Scanner,
// and matches patterns with regexp. No external dependencies.
// Honors .gitignore when searching inside a git repository.
type SearchTool struct{}

func (t *SearchTool) Name() string { return "search_tool" }
func (t *SearchTool) Description() string {
	return "PREFERRED over bash grep/find/rg. " +
		"Search files by regex/literal pattern. Returns {path, line, content}. " +
		"Honors .gitignore, permission gates, and output caps. " +
		"Modes: files_with_matches (paths), content (lines+numbers), count (counts)."
}
func (t *SearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Pattern to search for. Interpreted as a regex unless 'fixed_strings' is true."
			},
			"path": {
				"type": "string",
				"description": "Directory to search in (default: current working directory)"
			},
			"include": {
				"type": "string",
				"description": "File glob pattern to filter, e.g. '*.go' or '*_test.go'. Uses filepath.Match semantics on the file name."
			},
			"output_mode": {
				"type": "string",
				"enum": ["files_with_matches", "matching-files", "content", "text", "count"],
				"description": "Output format: 'files_with_matches' or 'matching-files' for file paths only (grep -l), 'content' or 'text' for matching lines with numbers (grep -n), 'count' for match count per file (grep -c)"
			},
			"case_sensitive": {
				"type": "boolean",
				"description": "If true, do case-sensitive matching (default: false, case-insensitive). Note: grep defaults to case-sensitive; this tool defaults to case-insensitive for code-friendliness."
			},
			"fixed_strings": {
				"type": "boolean",
				"description": "If true, treat pattern as a literal string instead of regex (default: false). Equivalent to grep -F."
			},
			"context_lines": {
				"type": "integer",
				"description": "Number of context lines to show before and after each match (content mode only). Equivalent to grep -C."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return (default: 100, max: 500)"
			},
			"exclude": {
				"type": "string",
				"description": "Glob pattern to exclude files, e.g. '*.min.js'. Uses filepath.Match semantics on the file name."
			},
			"recursive": {
				"type": "boolean",
				"description": "Search subdirectories (default: true)."
			}
		},
		"required": ["pattern"]
	}`)
}

func (t *SearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Pattern       string `json:"pattern"`
		Path          string `json:"path"`
		Include       string `json:"include"`
		Exclude       string `json:"exclude"`
		OutputMode    string `json:"output_mode"`
		CaseSensitive bool   `json:"case_sensitive"`
		FixedStrings  bool   `json:"fixed_strings"`
		ContextLines  int    `json:"context_lines"`
		MaxResults    int    `json:"max_results"`
		Recursive     bool   `json:"recursive"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid search parameters: %w", err)
	}

	// Validate required field
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Defaults
	params.Recursive = true // default to recursive for backward compat
	if params.OutputMode == "" {
		params.OutputMode = "files_with_matches"
	}
	if params.MaxResults <= 0 || params.MaxResults > 500 {
		params.MaxResults = 100
	}

	// Normalize grep-compatible output mode aliases
	switch params.OutputMode {
	case "matching-files":
		params.OutputMode = "files_with_matches"
	case "text":
		params.OutputMode = "content"
	}

	// Validate output mode
	switch params.OutputMode {
	case "files_with_matches", "content", "count":
		// valid
	default:
		return "", fmt.Errorf("invalid output_mode: %s (must be 'files_with_matches', 'content', 'count', 'matching-files', or 'text')", params.OutputMode)
	}

	// Resolve search path
	searchPath := "."
	if params.Path != "" {
		searchPath = params.Path
	}

	// Load .gitignore if available (cached per process from CWD)
	gi, repoRoot := getGitIgnoreForPath(searchPath)

	// Compile matcher
	var matchFunc func(line string) bool
	if params.FixedStrings {
		if params.CaseSensitive {
			matchFunc = func(line string) bool {
				return strings.Contains(line, params.Pattern)
			}
		} else {
			lowerPattern := strings.ToLower(params.Pattern)
			matchFunc = func(line string) bool {
				return strings.Contains(strings.ToLower(line), lowerPattern)
			}
		}
	} else {
		rePattern := params.Pattern
		if !params.CaseSensitive {
			rePattern = "(?i)" + rePattern
		}
		re, err := regexp.Compile(rePattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex pattern: %w", err)
		}
		matchFunc = re.MatchString
	}

	// Build output
	var sb strings.Builder
	matchCount := 0
	fileCount := 0
	truncated := false
	var err error
	stopErr := fmt.Errorf("stop") // sentinel to stop WalkDir

	// Determine walker function
	var walkFn func(path string, d fs.DirEntry, err error) error
	walkFn = func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir // Skip inaccessible dirs
		}

		// Skip directories and hidden/ vendor dirs
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".svn" || name == ".hg" {
				return filepath.SkipDir
			}
			// Check gitignore for directories — skip entire subtree if matched
			if matchesGitIgnore(gi, repoRoot, path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		// Check gitignore for files
		if matchesGitIgnore(gi, repoRoot, path, false) {
			return nil
		}

		// Apply exclude glob filter (before include for efficiency)
		if params.Exclude != "" {
			matched, err := filepath.Match(params.Exclude, d.Name())
			if err == nil && matched {
				return nil
			}
		}

		// Apply include glob filter
		if params.Include != "" {
			matched, err := filepath.Match(params.Include, d.Name())
			if err != nil || !matched {
				return nil
			}
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// --- Count matches in this file ---
		fileMatches, err := countMatches(path, matchFunc)
		if err != nil || fileMatches == 0 {
			return nil
		}

		// Output based on mode
		fileCount++

		switch params.OutputMode {
		case "files_with_matches":
			line := path + "\n"
			if sb.Len()+len(line) > maxSearchChars {
				truncated = true
				return stopErr
			}
			sb.WriteString(line)
			if fileCount >= params.MaxResults {
				truncated = true
				return stopErr
			}

		case "count":
			line := fmt.Sprintf("%s: %d\n", path, fileMatches)
			if sb.Len()+len(line) > maxSearchChars {
				truncated = true
				return stopErr
			}
			sb.WriteString(line)

		case "content":
			fileStr, localCount, err := readFileContentWithCount(path, matchFunc, params.ContextLines, params.MaxResults, &matchCount)
			if err != nil {
				if err == stopErr {
					truncated = true
					return stopErr
				}
				return nil // skip unreadable files silently
			}
			if localCount == 0 {
				// No matches found in this file (shouldn't happen after countMatches but handle gracefully)
				return nil
			}
			if sb.Len()+len(fileStr) > maxSearchChars {
				// partial write — fit what we can
				remaining := maxSearchChars - sb.Len()
				if remaining > 0 {
					sb.WriteString(fileStr[:remaining])
				}
				sb.WriteString("\n... (output truncated)")
				truncated = true
				return stopErr
			}
			sb.WriteString(fileStr)
			sb.WriteString("\n") // file separator
		}

		return nil
	}

	if params.Recursive {
		err = filepath.WalkDir(searchPath, walkFn)
	} else {
		// Non-recursive: read top-level directory entries only
		entries, readErr := os.ReadDir(searchPath)
		if readErr == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				// Reuse walkFn logic by constructing the full path
				fullPath := filepath.Join(searchPath, entry.Name())
				if wErr := walkFn(fullPath, entry, nil); wErr != nil {
					if wErr == stopErr {
						err = stopErr
						break
					}
					if wErr == context.Canceled || wErr == context.DeadlineExceeded {
						err = wErr
						break
					}
				}
			}
		}
	}

	if err != nil && err != stopErr && err != context.Canceled && err != context.DeadlineExceeded {
		return "", fmt.Errorf("search failed: %w", err)
	}

	result := sb.String()
	if result == "" {
		return "No matches found", nil
	}

	if truncated {
		result += "\n... (output truncated)"
	}

	return result, nil
}

func (t *SearchTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t *SearchTool) CallString(args json.RawMessage) string {
	pattern := getToolParam(args, "pattern")
	if pattern == "" {
		return "Using search_tool..."
	}
	return fmt.Sprintf("Using search_tool for: %s", truncate(pattern, 50))
}

// countMatches reads a file and counts lines that match the given function.
// Returns 0 for binary files or unreadable files.
func countMatches(path string, matchFunc func(string) bool) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if IsBinary(data) {
		return 0, nil
	}
	// Handle empty files: strings.Split("", "\n") returns [""] which would count as 1
	if len(data) == 0 {
		return 0, nil
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if matchFunc(line) {
			count++
		}
	}
	return count, nil
}

// readFileContentWithCount reads a file once, counts matches, and formats content output.
// This eliminates the double-read in content mode (previously countMatches + readFileContent).
func readFileContentWithCount(path string, matchFunc func(string) bool, contextLines, maxResults int, matchCount *int) (string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	if IsBinary(data) {
		return "", 0, nil
	}
	if len(data) == 0 {
		return "", 0, nil
	}

	lines := strings.Split(string(data), "\n")
	var fileBuf strings.Builder
	localMatchCount := 0

	fileBuf.WriteString(path + "\n")

	for i, line := range lines {
		if matchFunc(line) {
			localMatchCount++
			*matchCount++
			if *matchCount > maxResults {
				return fileBuf.String(), localMatchCount, fmt.Errorf("stop")
			}

			// Emit context lines before match
			if contextLines > 0 {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				for j := start; j < i; j++ {
					cl := truncateLine(lines[j])
					entry := fmt.Sprintf("  %5d - %s\n", j+1, cl)
					if fileBuf.Len()+len(entry) > maxSearchChars {
						return fileBuf.String(), localMatchCount, nil
					}
					fileBuf.WriteString(entry)
				}
			}

			entry := fmt.Sprintf("  %5d | %s\n", i+1, truncateLine(line))
			if fileBuf.Len()+len(entry) > maxSearchChars {
				return fileBuf.String(), localMatchCount, nil
			}
			fileBuf.WriteString(entry)
		}
	}

	return fileBuf.String(), localMatchCount, nil
}

// readFileContent reads a file and appends matched lines (with context) to the output.
// Kept for backward compatibility; new code should use readFileContentWithCount for single-pass.
func readFileContent(path string, matchFunc func(string) bool, contextLines, maxResults int, matchCount *int, sb *strings.Builder) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if IsBinary(data) {
		return "", nil
	}

	lines := strings.Split(string(data), "\n")
	var fileBuf strings.Builder

	fileBuf.WriteString(path + "\n")

	for i, line := range lines {
		if matchFunc(line) {
			*matchCount++
			if *matchCount > maxResults {
				return fileBuf.String(), fmt.Errorf("stop")
			}

			// Emit context lines before match
			if contextLines > 0 {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				for j := start; j < i; j++ {
					cl := truncateLine(lines[j])
					entry := fmt.Sprintf("  %5d - %s\n", j+1, cl)
					if fileBuf.Len()+len(entry) > maxSearchChars {
						return fileBuf.String(), nil
					}
					fileBuf.WriteString(entry)
				}
			}

			entry := fmt.Sprintf("  %5d | %s\n", i+1, truncateLine(line))
			if fileBuf.Len()+len(entry) > maxSearchChars {
				return fileBuf.String(), nil
			}
			fileBuf.WriteString(entry)
		}
	}

	return fileBuf.String(), nil
}

// truncateLine truncates a single line at 1000 chars to prevent context poisoning.
func truncateLine(s string) string {
	if len(s) > 1000 {
		return s[:997] + "..."
	}
	return s
}
