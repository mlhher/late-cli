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
