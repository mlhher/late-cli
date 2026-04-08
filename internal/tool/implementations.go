package tool

import (
	"context"
	"encoding/json" // used for hash generation
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"late/internal/common"
)

// ReadFileTool reads content from a file.
type ReadFileTool struct {
	LastReads map[string]ReadState
}

type ReadState struct {
	ModTime    time.Time
	Size       int64
	LastParams string
}

func NewReadFileTool() *ReadFileTool {
	return &ReadFileTool{
		LastReads: make(map[string]ReadState),
	}
}

func (t *ReadFileTool) Name() string        { return "read_file" }
func (t *ReadFileTool) Description() string { return "Read the content of a file" }
func (t *ReadFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": { "type": "string", "description": "Path to the file to read" },
			"start_line": { "type": "integer", "description": "Optional: Start reading from this line number (1-indexed)" },
			"end_line": { "type": "integer", "description": "Optional: Stop reading at this line number (inclusive)" }
		},
		"required": ["path"]
	}`)
}
func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	data, err := os.ReadFile(params.Path)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	type lineInfo struct {
		lineNum int
		content string
	}
	fileLines := make([]lineInfo, totalLines)
	for i, line := range lines {
		fileLines[i] = lineInfo{
			lineNum: i + 1,
			content: line,
		}
	}

	start := 1
	end := totalLines

	if params.StartLine > 0 {
		start = params.StartLine
	}
	if params.EndLine > 0 {
		end = params.EndLine
	}

	if start < 1 {
		start = 1
	}
	if end > totalLines {
		end = totalLines
	}
	if start > end {
		return fmt.Sprintf("Invalid range: start_line %d > end_line %d (total: %d)", start, end, totalLines), nil
	}

	result := fileLines[start-1 : end]

	var sb strings.Builder
	for _, l := range result {
		sb.WriteString(fmt.Sprintf("%d | %s\n", l.lineNum, l.content))
	}

	return sb.String(), nil
}
func (t *ReadFileTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t *ReadFileTool) CallString(args json.RawMessage) string {
	path := getToolParam(args, "path")
	if cwd, err := os.Getwd(); err == nil {
		path = strings.Replace(path, cwd, ".", 1)
	}
	return fmt.Sprintf("Reading file %s", truncate(path, 50))
}

// WriteFileTool writes content to a file.
type WriteFileTool struct{}

func (t WriteFileTool) Name() string { return "write_file" }
func (t WriteFileTool) Description() string {
	return "Write content to a file. Requires confirmation if writing outside CWD."
}
func (t WriteFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": { "type": "string", "description": "Path to the file to write" },
			"content": { "type": "string", "description": "Content to write to the file" }
		},
		"required": ["path", "content"]
	}`)
}
func (t WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	if params.Content == "" {
		return "", fmt.Errorf("Your edit to %s failed: content cannot be empty", params.Path)
	}
	if err := os.WriteFile(params.Path, []byte(params.Content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully wrote to %s", params.Path), nil
}
func (t WriteFileTool) RequiresConfirmation(args json.RawMessage) bool {
	path := getToolParam(args, "path")
	if path == "" {
		return true // Default to safe if we can't parse yet
	}
	return !IsSafePath(path)
}

func (t WriteFileTool) CallString(args json.RawMessage) string {
	path := getToolParam(args, "path")
	if path == "" {
		return "Writing to file..."
	}
	if cwd, err := os.Getwd(); err == nil {
		path = strings.Replace(path, cwd, ".", 1)
	}
	return fmt.Sprintf("Writing to file %s", truncate(path, 50))
}

// Commands that do not require user confirmation for BashTool
var whitelistedCommands = map[string]bool{
	"grep":   true,
	"find":   true,
	"ls":     true,
	"cat":    true,
	"head":   true,
	"tail":   true,
	"echo":   true,
	"pwd":    true,
	"date":   true,
	"whoami": true,
	"mkdir":  true,
	"touch":  true,
	"seq":    true,
}

// Maximum number of output lines to prevent memory exhaustion
const maxBashOutputLines = 1024

// isMaliciousCatCommand detects when cat is used with output redirection to write files.
// Returns true if the command attempts to write using cat shenanigans, false if safe.
func isMaliciousCatCommand(command string) (bool, error) {
	// Pattern to detect cat with output redirection (>)
	// Matches: cat > file, cat >> file, cat 2> file, echo | cat > file, etc.
	// Does NOT match: cat file.txt (reading), cat < file.txt (input redirection), cat file | grep (piping)
	
	// First, strip comments and quotes to avoid false positives
	cleanCmd := command
	// Remove single-line comments
	if idx := strings.Index(cleanCmd, "#"); idx != -1 {
		cleanCmd = cleanCmd[:idx]
	}
	
	// Pattern explanation:
	// - Match "cat" command (possibly with whitespace before it)
	// - Followed by output redirection (>, >>, 2>)
	// - The redirection must be a standalone redirection, not part of a pipe
	
	// This regex matches:
	// - cat followed by whitespace and > (output redirection)
	// - cat followed by whitespace and >> (append redirection)
	// - cat followed by 2> (stderr redirection)
	// - | cat followed by whitespace and > (pipe to cat with output redirection)
	maliciousPatterns := []string{
		`(?i)\bcat\s+>>\s+`,            // cat >> file
		`(?i)\bcat\s+>\s+`,             // cat > file
		`(?i)\bcat\s+2>\s+`,            // cat 2> file
		`(?i)\|\s*cat\s+>\s+`,          // | cat > file
		`(?i)\|\s*cat\s+>>\s+`,         // | cat >> file
		`(?i)\|\s*cat\s+2>\s+`,         // | cat 2> file
		`(?i)cat\s+\d+\s*>`,            // cat 0> file, cat 1> file, cat 1 > file, etc.
		`(?i)\|\s*cat\s+\d+\s*>`,       // | cat 1> file
	}
	
	for _, pattern := range maliciousPatterns {
		re := regexp.MustCompile(pattern)
		if re.MatchString(cleanCmd) {
			return true, fmt.Errorf("cat cannot be used with output redirection (>) to write files")
		}
	}
	
	return false, nil
}

// isCdCommand detects when a bash command contains `cd` to change directories.
// Returns true if the command attempts to change directories, false if safe.
// Returns an error with instructions on using the `cwd` parameter instead.
func isCdCommand(command string) (bool, error) {
	// First, strip comments to avoid false positives
	cleanCmd := command
	if idx := strings.Index(cleanCmd, "#"); idx != -1 {
		cleanCmd = cleanCmd[:idx]
	}
	
	// Pattern explanation:
	// - Optional leading whitespace
	// - "cd" as a standalone word (not part of another word like cd_log or mkdir)
	// - Followed by optional space and any arguments
	// - The \b word boundary ensures we match "cd" but not "cd_log" or "mkdir"
	pattern := `^\s*cd\s+`
	re := regexp.MustCompile(pattern)
	
	if re.MatchString(cleanCmd) {
		return true, fmt.Errorf("Do not use `cd` to change directories. Use the `cwd` parameter in the bash tool instead.")
	}
	
	return false, nil
}

// ValidateBashCommand validates bash commands before execution.
// Returns an error if the command uses malicious patterns like cat shenanigans or cd commands.
func (t *BashTool) ValidateBashCommand(command string) error {
	// Check for malicious cat commands
	isMalicious, err := isMaliciousCatCommand(command)
	if isMalicious {
		return err
	}
	
	// Check for cd commands
	isCd, err := isCdCommand(command)
	if isCd {
		return err
	}
	
	return nil
}

// IsCommandBlocked checks if a bash command should be blocked entirely (not asked for confirmation).
// Returns true and an error if the command is blocked (e.g., cd commands).
func (t *BashTool) IsCommandBlocked(command string) (bool, error) {
	// Block cd commands immediately - they should never be confirmed, only rejected
	isCd, err := isCdCommand(command)
	if isCd {
		return true, err
	}
	
	// Block cat with output redirection immediately
	isMalicious, err := isMaliciousCatCommand(command)
	if isMalicious {
		return true, err
	}
	
	return false, nil
}

// BashTool executes a bash command with security restrictions.
type BashTool struct{}

func (t BashTool) Name() string { return "bash" }
func (t BashTool) Description() string {
	return "Execute a bash command."
}
func (t BashTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": { "type": "string", "description": "The full command to execute." },
			"cwd": { "type": "string", "description": "Working directory for execution. Use this instead of 'cd' commands to change directories." }
		},
		"required": ["command"]
	}`)
}
func (t BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	// Validate command before any execution
	if err := t.ValidateBashCommand(params.Command); err != nil {
		// Generate appropriate error message based on agent type
		orchestratorID := common.GetOrchestratorID(ctx)
		
		var errorMsg string
		if strings.Contains(strings.ToLower(orchestratorID), "coder") {
			errorMsg = fmt.Sprintf("Do not use bash commands like `cat > file` or `echo > file` to write files. Use the native `write_file` or `target_edit` tools instead. %s", err.Error())
		} else {
			errorMsg = fmt.Sprintf("You are an architect/planner agent. You cannot write files. To modify files, you must spawn a coder subagent using `spawn_subagent` tool. %s", err.Error())
		}
		
		return "", fmt.Errorf("%s", errorMsg)
	}

	// Validate and set working directory
	if params.Cwd != "" {
		if !IsSafePath(params.Cwd) {
			return "", fmt.Errorf("cwd '%s' is outside the allowed directory", params.Cwd)
		}
	} else {
		// Default to current directory
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current working directory: %w", err)
		}
		params.Cwd = cwd
	}

	// Execute the command using bash -c to handle parsing correctly
	cmd := exec.CommandContext(ctx, "bash", "-c", params.Command)
	cmd.Dir = params.Cwd

	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("Command failed with exit code %d\n%s", exitErr.ExitCode(), string(output)), nil
		}
		return fmt.Sprintf("Error executing command: %v\n%s", err, string(output)), nil
	}

	// Limit output to prevent memory exhaustion
	lines := strings.Split(string(output), "\n")
	if len(lines) > maxBashOutputLines {
		lines = lines[:maxBashOutputLines]
		lines = append(lines, "... (output truncated)")
	}

	return strings.Join(lines, "\n"), nil
}
func (t BashTool) RequiresConfirmation(args json.RawMessage) bool {
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return true // Default to requiring confirmation if we can't parse
	}
	// If agent put full command string, extract base command
	cmd := params.Command
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		cmd = cmd[:i]
	}
	return !whitelistedCommands[cmd]
}

func (t BashTool) CallString(args json.RawMessage) string {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "Executing: (invalid args)"
	}

	// Build the display string
	result := fmt.Sprintf("Executing: %s", params.Command)
	if params.Cwd != "" {
		result += " in dir: " + params.Cwd
	}
	return result
}

// WriteImplementationPlanTool writes the implementation plan to a fixed file.
type WriteImplementationPlanTool struct{}

func (t WriteImplementationPlanTool) Name() string { return "write_implementation_plan" }
func (t WriteImplementationPlanTool) Description() string {
	return "Write the implementation plan to ./implementation_plan.md in the current working directory."
}
func (t WriteImplementationPlanTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"plan": { "type": "string", "description": "The full content of the implementation plan in Markdown format." }
		},
		"required": ["plan"]
	}`)
}
func (t WriteImplementationPlanTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	if params.Plan == "" {
		return "", fmt.Errorf("Implementation plan cannot be empty")
	}

	path := "implementation_plan.md"
	if err := os.WriteFile(path, []byte(params.Plan), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully wrote implementation plan to %s", path), nil
}
func (t WriteImplementationPlanTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t WriteImplementationPlanTool) CallString(args json.RawMessage) string {
	return "Writing implementation plan to ./implementation_plan.md"
}
