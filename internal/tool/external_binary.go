package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ExternalBinaryTool executes a configured external binary directly without a shell.
type ExternalBinaryTool struct {
	name string
	path string
}

func NewExternalBinaryTool(name, path string) ExternalBinaryTool {
	return ExternalBinaryTool{name: name, path: path}
}

func (t ExternalBinaryTool) Name() string { return t.name }

func (t ExternalBinaryTool) Description() string {
	return fmt.Sprintf("Execute the external binary %q without a shell.", t.path)
}

func (t ExternalBinaryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "args": {
                "type": "array",
                "items": { "type": "string" },
                "description": "Arguments to pass to the binary."
            },
            "cwd": {
                "type": "string",
                "description": "Optional working directory for the process."
            },
            "env": {
                "type": "object",
                "additionalProperties": { "type": "string" },
                "description": "Extra environment variables to set for this call."
            }
        }
    }`)
}

func (t ExternalBinaryTool) RequiresConfirmation(args json.RawMessage) bool {
	return true
}

func (t ExternalBinaryTool) CallString(args json.RawMessage) string {
	return fmt.Sprintf("Running external tool %q...", t.name)
}

func (t ExternalBinaryTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if strings.TrimSpace(t.name) == "" {
		return "", fmt.Errorf("external tool name is required")
	}
	if strings.TrimSpace(t.path) == "" {
		return "", fmt.Errorf("external tool path is required for %q", t.name)
	}

	var params struct {
		Args []string          `json:"args"`
		Cwd  string            `json:"cwd"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters for %q: %w", t.name, err)
	}

	resolvedPath := t.path
	if !strings.ContainsRune(resolvedPath, os.PathSeparator) && resolvedPath != "" {
		if _, err := exec.LookPath(resolvedPath); err != nil {
			return "", fmt.Errorf("external binary %q not found in PATH: %w", resolvedPath, err)
		}
	}

	cmd := exec.CommandContext(ctx, resolvedPath, params.Args...)
	if params.Cwd != "" {
		cmd.Dir = params.Cwd
	}
	if len(params.Env) > 0 {
		env := os.Environ()
		for k, v := range params.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return "", fmt.Errorf("external tool %q failed: %w: %s", t.name, err, strings.TrimSpace(string(output)))
		}
		return "", fmt.Errorf("external tool %q failed: %w", t.name, err)
	}

	return string(output), nil
}
