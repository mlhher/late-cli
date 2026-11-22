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

// Maximum number of characters for search output to prevent context window poisoning.
const maxSearchChars = 32768

// =============================================================================
// Tool 1: search_content (Grep / Content Search)
// =============================================================================

// SearchContentTool searches file contents using regex or literal text.
// Outputs matching lines in 'filepath:line: content' format.
type SearchContentTool struct{}

// SearchTool is retained for backward-compatibility.
type SearchTool = SearchContentTool

func (t *SearchContentTool) Name() string { return "search_content" }

func (t *SearchContentTool) Description() string {
	return "Search for text or regex patterns INSIDE file contents (like grep/ripgrep). " +
		"Returns matching lines formatted as 'filepath:line: content'. " +
		"Always skips binary files and .git, node_modules, .svn, .hg."
}

func (t *SearchContentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Text or regex pattern to search for inside files. Regex and case-insensitive by default. Set 'literal: true' for exact string matching (e.g. code with brackets or punctuation)."
			},
			"path": {
				"type": "string",
				"description": "Directory or file to search in (default: current working directory)."
			},
			"literal": {
				"type": "boolean",
				"description": "If true, treats pattern as exact literal text instead of regex (default: false)."
			},
			"case_sensitive": {
				"type": "boolean",
				"description": "If true, search is case-sensitive (default: false, case-insensitive)."
			},
			"include": {
				"type": "string",
				"description": "File glob pattern to include, e.g. '*.go' or '*.ts'. Filters match file basenames only, not path segments."
			},
			"exclude": {
				"type": "string",
				"description": "File/directory glob pattern to exclude, e.g. '*.min.js' or 'dist'. Filters match file/directory basenames only, not path segments."
			},
			"context_lines": {
				"type": "integer",
				"description": "Number of context lines before and after match (default: 0)."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of matching lines to return (default: 100, max: 500)."
			},
			"include_gitignored": {
				"type": "boolean",
				"description": "If true, include files excluded by .gitignore (default: false)."
			}
		},
		"required": ["pattern"]
	}`)
}

func (t *SearchContentTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t *SearchContentTool) CallString(args json.RawMessage) string {
	pattern := getToolParam(args, "pattern")
	if pattern == "" {
		return "Searching file contents..."
	}
	return fmt.Sprintf("Searching content for: %s", truncate(pattern, 50))
}

func (t *SearchContentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Pattern           string `json:"pattern"`
		Path              string `json:"path"`
		Literal           bool   `json:"literal"`
		CaseSensitive     bool   `json:"case_sensitive"`
		Include           string `json:"include"`
		Exclude           string `json:"exclude"`
		ContextLines      int    `json:"context_lines"`
		MaxResults        int    `json:"max_results"`
		IncludeGitignored bool   `json:"include_gitignored"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters for search_content: %w", err)
	}

	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	if params.MaxResults < 0 {
		return "", fmt.Errorf("max_results must be >= 1 (got %d)", params.MaxResults)
	}
	if params.MaxResults == 0 {
		params.MaxResults = 100
	}
	if params.MaxResults > 500 {
		params.MaxResults = 500
	}
	if params.ContextLines < 0 {
		params.ContextLines = 0
	}

	searchPath := "."
	if params.Path != "" {
		searchPath = params.Path
	}

	gi, repoRoot := getIgnoreForPath(searchPath, params.IncludeGitignored)

	// Compile match function
	var countFunc func(line string) int
	if params.Literal {
		if params.CaseSensitive {
			countFunc = func(line string) int {
				return strings.Count(line, params.Pattern)
			}
		} else {
			lowerPattern := strings.ToLower(params.Pattern)
			countFunc = func(line string) int {
				return strings.Count(strings.ToLower(line), lowerPattern)
			}
		}
	} else {
		rePattern := params.Pattern
		if !params.CaseSensitive {
			rePattern = "(?i)" + rePattern
		}
		re, err := regexp.Compile(rePattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex pattern: %w. If you meant to search for literal text with special characters, set 'literal: true'", err)
		}
		countFunc = func(line string) int {
			return len(re.FindAllString(line, -1))
		}
	}

	var sb strings.Builder
	matchCount := 0
	truncated := false
	stopErr := fmt.Errorf("stop")

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}

		name := d.Name()

		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == ".svn" || name == ".hg" {
				return filepath.SkipDir
			}
			if matchesGitIgnore(gi, repoRoot, path, true) {
				return filepath.SkipDir
			}
			if params.Exclude != "" {
				if matched, err := filepath.Match(params.Exclude, name); err == nil && matched {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if matchesGitIgnore(gi, repoRoot, path, false) {
			return nil
		}

		if params.Exclude != "" {
			if matched, err := filepath.Match(params.Exclude, name); err == nil && matched {
				return nil
			}
		}

		if params.Include != "" {
			if matched, err := filepath.Match(params.Include, name); err != nil || !matched {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fileMatches, err := countMatches(path, countFunc)
		if err != nil || fileMatches == 0 {
			return nil
		}

		fileStr, localCount, err := readFileContentWithCount(path, countFunc, params.ContextLines, params.MaxResults, &matchCount)
		if localCount > 0 {
			if sb.Len()+len(fileStr) > maxSearchChars {
				remaining := maxSearchChars - sb.Len()
				if remaining > 0 {
					sb.WriteString(fileStr[:remaining])
				}
				truncated = true
				return stopErr
			}
			sb.WriteString(fileStr)
		}

		if err != nil {
			if err.Error() == "stop" {
				truncated = true
				return stopErr
			}
			return nil
		}

		return nil
	}

	err := filepath.WalkDir(searchPath, walkFn)
	if err != nil && err != stopErr && err != context.Canceled && err != context.DeadlineExceeded {
		return "", fmt.Errorf("search failed: %w", err)
	}

	result := sb.String()
	if result == "" {
		msg := fmt.Sprintf("No matches found for '%s'. Consider widening 'path', toggling 'case_sensitive', or setting 'literal: true'.", params.Pattern)
		if strings.Contains(params.Include, "/") || strings.Contains(params.Exclude, "/") {
			msg += " Note: include/exclude filters match file/directory names, not path segments (e.g. use '*.go' instead of 'src/*.go')."
		}
		return msg, nil
	}

	if truncated {
		result += "\n... (output truncated). To see more, narrow your 'path', use 'include'/'exclude' filters, or increase 'max_results'."
	}

	return result, nil
}

// =============================================================================
// Tool 2: find_files (Find / Path Search)
// =============================================================================

// FindFilesTool finds files and directories by name or path pattern (like find/fd).
type FindFilesTool struct{}

func (t *FindFilesTool) Name() string { return "find_files" }

func (t *FindFilesTool) Description() string {
	return "Find files and directories by name or path pattern (like find/fd). " +
		"Recursively searches from 'path' and returns a newline-separated list of matching paths (directories end with '/'). " +
		"Always skips .git, node_modules, .svn, .hg."
}

func (t *FindFilesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Name, substring, or glob pattern to find (e.g. 'README', '*.go', 'workflows', '.github/*'). Patterns use filepath.Match glob syntax; '[...]' denotes a character class (e.g. '[id]' matches 'i' or 'd'). Escape literal bracket folders/files with backslashes: '\\[id\\]'. Use '*' to match all files."
			},
			"path": {
				"type": "string",
				"description": "Directory to search in (default: current working directory)."
			},
			"type": {
				"type": "string",
				"enum": ["any", "file", "directory"],
				"description": "Filter by type: 'any' (default), 'file', or 'directory'."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of paths to return (default: 100, max: 500)."
			},
			"include_gitignored": {
				"type": "boolean",
				"description": "If true, include files/directories excluded by .gitignore (default: false)."
			}
		},
		"required": ["pattern"]
	}`)
}

func (t *FindFilesTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t *FindFilesTool) CallString(args json.RawMessage) string {
	pattern := getToolParam(args, "pattern")
	if pattern == "" {
		return "Finding files..."
	}
	return fmt.Sprintf("Finding files matching: %s", truncate(pattern, 50))
}

func (t *FindFilesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Pattern           string `json:"pattern"`
		Path              string `json:"path"`
		Type              string `json:"type"`
		MaxResults        int    `json:"max_results"`
		IncludeGitignored bool   `json:"include_gitignored"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters for find_files: %w", err)
	}

	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	if params.MaxResults < 0 {
		return "", fmt.Errorf("max_results must be >= 1 (got %d)", params.MaxResults)
	}
	if params.MaxResults == 0 {
		params.MaxResults = 100
	}
	if params.MaxResults > 500 {
		params.MaxResults = 500
	}
	if params.Type == "" {
		params.Type = "any"
	}

	searchPath := "."
	if params.Path != "" {
		searchPath = params.Path
	}

	gi, repoRoot := getIgnoreForPath(searchPath, params.IncludeGitignored)

	// Build matcher function that "just works"
	cleanPattern := filepath.ToSlash(strings.TrimSuffix(params.Pattern, "/"))
	hasGlobstar := strings.Contains(cleanPattern, "**")
	hasGlobMeta := strings.ContainsAny(cleanPattern, "*?[")
	lowerPattern := strings.ToLower(cleanPattern)

	nameMatchFunc := func(name, relPath string) bool {
		normRel := filepath.ToSlash(relPath)

		// 1. If pattern contains globstar (**), match against relative path across separators
		if hasGlobstar {
			return matchGlobstar(cleanPattern, normRel)
		}

		// 2. Standard glob match on filename and relative path
		if matched, err := filepath.Match(cleanPattern, name); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(cleanPattern, normRel); err == nil && matched {
			return true
		}

		// 3. Case-insensitive substring fallback for non-glob queries (e.g. 'README' -> 'README.md', 'workflows' -> '.github/workflows')
		if !hasGlobMeta {
			if strings.Contains(strings.ToLower(name), lowerPattern) {
				return true
			}
			if strings.Contains(strings.ToLower(normRel), lowerPattern) {
				return true
			}
		}

		return false
	}

	var sb strings.Builder
	resultCount := 0
	truncated := false
	stopErr := fmt.Errorf("stop")

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}

		name := d.Name()

		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == ".svn" || name == ".hg" {
				return filepath.SkipDir
			}
			if matchesGitIgnore(gi, repoRoot, path, true) {
				return filepath.SkipDir
			}

			// Check directory match (skip root directory itself)
			if path != searchPath && params.Type != "file" {
				relPath, err := filepath.Rel(searchPath, path)
				if err != nil {
					relPath = name
				}

				if nameMatchFunc(name, relPath) {
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
					}

					if resultCount >= params.MaxResults {
						truncated = true
						return stopErr
					}

					line := path + "/\n"
					if sb.Len()+len(line) > maxSearchChars {
						truncated = true
						return stopErr
					}
					sb.WriteString(line)
					resultCount++
				}
			}
			return nil
		}

		// Files
		if params.Type == "directory" {
			return nil
		}

		if matchesGitIgnore(gi, repoRoot, path, false) {
			return nil
		}

		relPath, err := filepath.Rel(searchPath, path)
		if err != nil {
			relPath = name
		}

		if !nameMatchFunc(name, relPath) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if resultCount >= params.MaxResults {
			truncated = true
			return stopErr
		}

		line := path + "\n"
		if sb.Len()+len(line) > maxSearchChars {
			truncated = true
			return stopErr
		}
		sb.WriteString(line)
		resultCount++

		return nil
	}

	err := filepath.WalkDir(searchPath, walkFn)
	if err != nil && err != stopErr && err != context.Canceled && err != context.DeadlineExceeded {
		return "", fmt.Errorf("find failed: %w", err)
	}

	result := sb.String()
	if result == "" {
		msg := fmt.Sprintf("No files or directories matching '%s' found. Note: .git, node_modules, .svn, .hg are skipped by default.", params.Pattern)
		if strings.Contains(params.Pattern, "**") {
			msg += " Note: find_files is already recursive by default from 'path'; you can also use '*.ext' instead of '**/*.ext'."
		}
		return msg, nil
	}

	if truncated {
		result += "\n... (output truncated). To see more, narrow your 'path' or increase 'max_results'."
	}

	return result, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// matchGlobstar reports whether pattern (may contain "**") matches path.
// "**" matches zero or more path segments; all other metacharacters
// (*, ?, [...]) follow filepath.Match semantics within a single segment.
func matchGlobstar(pattern, path string) bool {
	normPat := filepath.ToSlash(strings.Trim(pattern, "/"))
	normPath := filepath.ToSlash(strings.Trim(path, "/"))

	ps := strings.Split(normPat, "/")
	ss := strings.Split(normPath, "/")

	var rec func(pi, si int) bool
	rec = func(pi, si int) bool {
		if pi == len(ps) {
			return si == len(ss)
		}
		if ps[pi] == "**" {
			for k := si; k <= len(ss); k++ { // zero or more segments
				if rec(pi+1, k) {
					return true
				}
			}
			return false
		}
		if si >= len(ss) {
			return false
		}
		ok, err := filepath.Match(ps[pi], ss[si])
		return err == nil && ok && rec(pi+1, si+1)
	}
	return rec(0, 0)
}

// countMatches reads a file and counts occurrences that match the given function.
// Returns 0 for binary files or unreadable files.
func countMatches(path string, countFunc func(string) int) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if IsBinary(data) {
		return 0, nil
	}
	if len(data) == 0 {
		return 0, nil
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		count += countFunc(line)
	}
	return count, nil
}

// readFileContentWithCount reads a file once, counts matches, and formats content output.
// Matches are formatted as 'filepath:line: content', context lines as 'filepath:line- content'.
func readFileContentWithCount(path string, countFunc func(string) int, contextLines, maxResults int, matchCount *int) (string, int, error) {
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

	for i, line := range lines {
		occurrences := countFunc(line)
		if occurrences > 0 {
			if *matchCount >= maxResults {
				return fileBuf.String(), localMatchCount, fmt.Errorf("stop")
			}

			localMatchCount += occurrences
			*matchCount += occurrences

			reachedLimit := *matchCount > maxResults

			// Emit context lines before match
			if contextLines > 0 {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				for j := start; j < i; j++ {
					cl := truncateLine(lines[j])
					entry := fmt.Sprintf("%s:%d-%s\n", path, j+1, cl)
					if fileBuf.Len()+len(entry) > maxSearchChars {
						return fileBuf.String(), localMatchCount, nil
					}
					fileBuf.WriteString(entry)
				}
			}

			entry := fmt.Sprintf("%s:%d:%s\n", path, i+1, truncateLine(line))
			if fileBuf.Len()+len(entry) > maxSearchChars {
				return fileBuf.String(), localMatchCount, nil
			}
			fileBuf.WriteString(entry)

			// Emit context lines after match if contextLines > 0
			if contextLines > 0 {
				end := i + 1 + contextLines
				if end > len(lines) {
					end = len(lines)
				}
				for j := i + 1; j < end; j++ {
					// Stop if next line is itself a match (will be rendered as a match line)
					if countFunc(lines[j]) > 0 {
						break
					}
					cl := truncateLine(lines[j])
					entry := fmt.Sprintf("%s:%d-%s\n", path, j+1, cl)
					if fileBuf.Len()+len(entry) > maxSearchChars {
						return fileBuf.String(), localMatchCount, nil
					}
					fileBuf.WriteString(entry)
				}
			}

			if reachedLimit {
				return fileBuf.String(), localMatchCount, fmt.Errorf("stop")
			}
		}
	}

	return fileBuf.String(), localMatchCount, nil
}

// truncateLine truncates a single line at 1000 chars to prevent context poisoning.
func truncateLine(s string) string {
	if len(s) > 1000 {
		return s[:997] + "..."
	}
	return s
}
