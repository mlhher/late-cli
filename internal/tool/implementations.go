package tool

import (
	"context"
	"encoding/json" // used for hash generation
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
		lineStr := fmt.Sprintf("%d | %s\n", l.lineNum, l.content)
		if sb.Len()+len(lineStr) > maxReadFileChars {
			sb.WriteString("... (output truncated)")
			break
		}
		sb.WriteString(lineStr)
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

func (t *ShellTool) getAnalyzer(cwd string) CommandAnalyzer {
	if runtime.GOOS == "windows" {
		return &PowerShellAnalyzer{Cwd: cwd}
	}
	allowed, _ := LoadAllowedCommands()
	return &BashAnalyzer{ProjectAllowedCommands: allowed}
}

// SaveToAllowList persists a command to the project-specific allow-list.
func (t *ShellTool) SaveToAllowList(command string) error {
	return SaveAllowedCommand(command)
}

// analyzeBashCommand is now a wrapper around the platform-specific analyzer.
func (t *ShellTool) analyzeBashCommand(command string, cwd string) (isBlocked bool, blockReason error, needsConfirmation bool) {
	analyzer := t.getAnalyzer(cwd)
	analysis := analyzer.Analyze(command)
	return analysis.IsBlocked, analysis.BlockReason, analysis.NeedsConfirmation
}

// ValidateBashCommand validates shell commands before execution.
// Returns an error if the command uses malicious patterns like cat shenanigans or cd commands.
func (t *ShellTool) ValidateBashCommand(command string, cwd string) error {
	blocked, err, _ := t.analyzeBashCommand(command, cwd)
	if blocked {
		return err
	}
	return nil
}

// WrapError wraps a validation error with descriptive guidance based on the orchestrator ID.
func (t *ShellTool) WrapError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	orchestratorID := common.GetOrchestratorID(ctx)

	var errorMsg string
	if strings.Contains(strings.ToLower(orchestratorID), "coder") {
		errorMsg = fmt.Sprintf("Do not use %s commands like `cat > file` or `echo > file` to write files. Use the native `write_file` or `target_edit` tools instead. %s", shellDisplayName(), err.Error())
	} else {
		errorMsg = fmt.Sprintf("You are an architect/planner agent. You cannot write files. To modify files, you must spawn a coder subagent using `spawn_subagent` tool. %s", err.Error())
	}

	return fmt.Errorf("%s", errorMsg)
}

// IsCommandBlocked checks if a shell command should be blocked entirely (not asked for confirmation).
// Returns true and an error if the command is blocked (e.g., cd commands).
func (t *ShellTool) IsCommandBlocked(command string, cwd string) (bool, error) {
	blocked, err, _ := t.analyzeBashCommand(command, cwd)
	return blocked, err
}

// Maximum number of output lines to prevent memory exhaustion
const maxBashOutputLines = 1024

// Roughly 8192 tokens (assuming ~4 chars per token)
const maxReadFileChars = 32768

// ShellTool executes host-native shell commands with security restrictions.
type ShellTool struct{}

func shellDisplayName() string {
	if runtime.GOOS == "windows" {
		return "PowerShell"
	}
	return "bash"
}

func (t ShellTool) Name() string { return "bash" }
func (t ShellTool) Description() string {
	return fmt.Sprintf("Execute a %s command.", shellDisplayName())
}
func (t ShellTool) Parameters() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"command": { "type": "string", "description": "The full %s command to execute." },
			"cwd": { "type": "string", "description": "Working directory for execution. Use this instead of 'cd' commands to change directories." }
		},
		"required": ["command"]
	}`, shellDisplayName()))
}
func (t ShellTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	// Validate command before any execution
	if err := t.ValidateBashCommand(params.Command, params.Cwd); err != nil {
		return "", t.WrapError(ctx, err)
	}

	// Enforce approval in the execution path so shell commands fail closed
	// even if middleware wiring is missing.
	if t.RequiresConfirmation(args) {
		approved, ok := ctx.Value(common.ToolApprovalKey).(bool)
		if !ok || !approved {
			return "", fmt.Errorf("shell command requires explicit approval before execution")
		}
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

	// Execute command using a platform-specific shell wrapper.
	cmd := newShellCommand(ctx, params.Command)
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
func (t ShellTool) RequiresConfirmation(args json.RawMessage) bool {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return true // Default to requiring confirmation if we can't parse
	}

	_, _, needsConfirmation := t.analyzeBashCommand(params.Command, params.Cwd)
	return needsConfirmation
}

func (t ShellTool) CallString(args json.RawMessage) string {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		if runtime.GOOS == "windows" {
			return "Executing in PowerShell: (invalid args)"
		}
		return "Executing: (invalid args)"
	}

	// Build the display string
	var result string
	if runtime.GOOS == "windows" {
		result = fmt.Sprintf("Executing in PowerShell: %s", params.Command)
	} else {
		result = fmt.Sprintf("Executing: %s", params.Command)
	}
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
