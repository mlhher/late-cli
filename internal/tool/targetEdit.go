package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TargetEditTool performs targeted file edits using search and replace blocks.
type TargetEditTool struct{}

func NewTargetEditTool() *TargetEditTool {
	return &TargetEditTool{}
}

func (t *TargetEditTool) Name() string { return "target_edit" }
func (t *TargetEditTool) Description() string {
	return "Perform targeted edits on a file using search and replace blocks. You must read the file you want to edit before you apply an edit. You must provide a unique search block that exists exactly once in the file."
}
func (t *TargetEditTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file": { "type": "string", "description": "Path to the file to edit" },
			"search": { "type": "string", "description": "The exact code block to search for in the file. Must be unique." },
			"replace": { "type": "string", "description": "The new code block to replace the search block with." }
		},
		"required": ["file", "search", "replace"]
	}`)
}

func (t *TargetEditTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		File    string `json:"file"`
		Search  string `json:"search"`
		Replace string `json:"replace"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	// Validate file exists
	if _, err := os.Stat(params.File); os.IsNotExist(err) {
		return "", fmt.Errorf("file does not exist: %s", params.File)
	}

	// Read file content
	data, err := os.ReadFile(params.File)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %v", err)
	}
	content := string(data)

	// Detect and normalize line endings
	lineEnding := detectLineEnding(content)
	content = normalizeToUnix(content)
	search := normalizeToUnix(params.Search)
	replace := normalizeToUnix(params.Replace)

	// Validate search block
	if search == "" {
		return "", fmt.Errorf("search block cannot be empty")
	}

	// count occurrences
	count := strings.Count(content, search)
	if count == 0 {
		if hint := formatWhitespaceMismatchHint(content, search); hint != "" {
			return "", fmt.Errorf("search block not found in file, %s", hint)
		}
		return "", fmt.Errorf("search block not found in file")
	}
	if count > 1 {
		return "", fmt.Errorf("search block found %d times in file, must be unique", count)
	}

	// Perform replacement
	newContent := strings.Replace(content, search, replace, 1)

	// Restore line endings
	newContent = restoreLineEnding(newContent, lineEnding)

	// Write back
	if err := os.WriteFile(params.File, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %v", err)
	}

	return fmt.Sprintf("Successfully applied edit to %s", params.File), nil
}

func (t *TargetEditTool) RequiresConfirmation(args json.RawMessage) bool {
	file := getToolParam(args, "file")
	if file == "" {
		return true // Default to requiring confirmation if we can't parse yet (streaming)
	}
	return !IsSafePath(file)
}

func (t *TargetEditTool) CallString(args json.RawMessage) string {
	file := getToolParam(args, "file")
	if file == "" {
		return "Editing file..."
	}

	// Use just the filename for display, with truncated path if needed
	filename := filepath.Base(file)
	return fmt.Sprintf("Editing file %s...", truncate(filename, 50))
}

const (
	maxTargetEditHintLines     = 25
	maxTargetEditHintLocations = 5
	maxTargetEditHintDiffs     = 5
)

func trimHorizontal(s string) string {
	return strings.Trim(s, " \t")
}

func leadingHorizontal(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

func describeIndent(prefix string) string {
	if prefix == "" {
		return "no leading whitespace"
	}
	hasSpace := strings.ContainsRune(prefix, ' ')
	hasTab := strings.ContainsRune(prefix, '\t')
	if hasSpace && hasTab {
		return fmt.Sprintf("mixed whitespace %q", prefix)
	}
	if hasSpace {
		if len(prefix) == 1 {
			return "1 space"
		}
		return fmt.Sprintf("%d spaces", len(prefix))
	}
	if len(prefix) == 1 {
		return "1 tab"
	}
	return fmt.Sprintf("%d tabs", len(prefix))
}

func describeLineWhitespaceDiff(lineNum int, fileLine, searchLine string) string {
	fileLead := leadingHorizontal(fileLine)
	searchLead := leadingHorizontal(searchLine)
	if fileLead != searchLead {
		return fmt.Sprintf("line %d has %s, search had %s", lineNum, describeIndent(fileLead), describeIndent(searchLead))
	}
	return fmt.Sprintf("line %d differs in trailing whitespace", lineNum)
}

func formatHintRange(startLine, endLine int) string {
	if startLine == endLine {
		return fmt.Sprintf("line %d", startLine)
	}
	return fmt.Sprintf("lines %d-%d", startLine, endLine)
}

// formatWhitespaceMismatchHint locates blocks that match search when ignoring
// horizontal leading/trailing whitespace so models can self-correct indentation
// in one turn without guessing.
func formatWhitespaceMismatchHint(content, search string) string {
	contentLines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	searchLines := strings.Split(strings.TrimSuffix(search, "\n"), "\n")
	if len(searchLines) == 0 || len(searchLines) > len(contentLines) {
		return ""
	}

	hasNonBlank := false
	trimmedSearch := make([]string, len(searchLines))
	for i, line := range searchLines {
		trimmed := trimHorizontal(line)
		trimmedSearch[i] = trimmed
		if trimmed != "" {
			hasNonBlank = true
		}
	}
	if !hasNonBlank {
		return ""
	}

	var matches []int
	limit := len(contentLines) - len(searchLines)
	for i := 0; i <= limit; i++ {
		matched := true
		for k := 0; k < len(searchLines); k++ {
			if trimHorizontal(contentLines[i+k]) != trimmedSearch[k] {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, i)
		}
	}

	if len(matches) == 0 {
		return ""
	}

	if len(matches) > 1 {
		shown := min(len(matches), maxTargetEditHintLocations)
		locs := make([]string, 0, shown+1)
		for _, start := range matches[:shown] {
			locs = append(locs, formatHintRange(start+1, start+len(searchLines)))
		}
		if len(matches) > maxTargetEditHintLocations {
			locs = append(locs, "...")
		}
		return fmt.Sprintf(
			"but matched %d locations when ignoring leading/trailing whitespace (%s); include more surrounding lines and match exact indentation",
			len(matches),
			strings.Join(locs, ", "),
		)
	}

	start := matches[0]
	startLine := start + 1
	endLine := start + len(searchLines)
	loc := formatHintRange(startLine, endLine)

	var diffs []string
	totalDiffs := 0
	for k := 0; k < len(searchLines); k++ {
		if contentLines[start+k] != searchLines[k] {
			totalDiffs++
			if len(diffs) < maxTargetEditHintDiffs {
				diffs = append(diffs, describeLineWhitespaceDiff(startLine+k, contentLines[start+k], searchLines[k]))
			}
		}
	}
	reason := "differs only in trailing newline"
	if totalDiffs > 0 {
		reason = strings.Join(diffs, "; ")
		if totalDiffs > maxTargetEditHintDiffs {
			reason += fmt.Sprintf("; ... (+%d more)", totalDiffs-maxTargetEditHintDiffs)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "but matched %s when ignoring leading/trailing whitespace (%s). Actual lines (exact content after '|'):\n", loc, reason)
	emitCount := min(len(searchLines), maxTargetEditHintLines)
	for k := 0; k < emitCount; k++ {
		fmt.Fprintf(&sb, "%d|%s\n", startLine+k, contentLines[start+k])
	}
	if len(searchLines) > maxTargetEditHintLines {
		fmt.Fprintf(&sb, "... (%d more lines)\n", len(searchLines)-maxTargetEditHintLines)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
