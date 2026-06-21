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
type SearchTool struct{}

func (t *SearchTool) Name() string { return "search_tool" }
func (t *SearchTool) Description() string {
	return "Search files and file contents using regex or literal patterns. " +
		"Returns matching files and/or content with line numbers. " +
		"Use this instead of bash grep/find."
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
				"enum": ["files_with_matches", "content", "count"],
				"description": "Output format: 'files_with_matches' (default, file paths only), 'content' (matching lines with line numbers), 'count' (match count per file)"
			},
			"case_sensitive": {
				"type": "boolean",
				"description": "If true, do case-sensitive matching (default: false, case-insensitive)"
			},
			"fixed_strings": {
				"type": "boolean",
				"description": "If true, treat pattern as a literal string instead of regex (default: false)"
			},
			"context_lines": {
				"type": "integer",
				"description": "Number of context lines to show before and after each match (content mode only)"
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return (default: 100, max: 500)"
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
		OutputMode    string `json:"output_mode"`
		CaseSensitive bool   `json:"case_sensitive"`
		FixedStrings  bool   `json:"fixed_strings"`
		ContextLines  int    `json:"context_lines"`
		MaxResults    int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid search parameters: %w", err)
	}

	// Validate required field
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Defaults
	if params.OutputMode == "" {
		params.OutputMode = "files_with_matches"
	}
	if params.MaxResults <= 0 || params.MaxResults > 500 {
		params.MaxResults = 100
	}

	// Validate output mode
	switch params.OutputMode {
	case "files_with_matches", "content", "count":
		// valid
	default:
		return "", fmt.Errorf("invalid output_mode: %s (must be 'files_with_matches', 'content', or 'count')", params.OutputMode)
	}

	// Resolve search path
	searchPath := "."
	if params.Path != "" {
		searchPath = params.Path
	}

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
	stopErr := fmt.Errorf("stop") // sentinel to stop WalkDir

	err := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir // Skip inaccessible dirs
		}

		// Skip directories and hidden/ vendor dirs
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".svn" || name == ".hg" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
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

		// --- Count matches in this file (cheap first pass) ---
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
			fileStr, err := readFileContent(path, matchFunc, params.ContextLines, params.MaxResults, &matchCount, &sb)
			if err != nil {
				if err == stopErr {
					truncated = true
					return stopErr
				}
				return nil // skip unreadable files silently
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
	})

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

// readFileContent reads a file and appends matched lines (with context) to the output.
// It returns a string suitable for appending, or an error to stop walking.
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
