package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/common"
	"late/internal/skill"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ScriptTool executes a script from a skill's scripts/ directory.
type ScriptTool struct {
	SkillName  string
	ScriptName string
	ScriptPath string
}

func (t ScriptTool) Name() string {
	// sanitized script name to be used as tool name
	return fmt.Sprintf("skill_%s_%s", t.SkillName, sanitizeToolName(t.ScriptName))
}

func (t ScriptTool) Description() string {
	return fmt.Sprintf("Execute the '%s' script from the '%s' skill.", t.ScriptName, t.SkillName)
}

func (t ScriptTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"args": { "type": "array", "items": { "type": "string" }, "description": "Arguments to pass to the script" }
		}
	}`)
}

func (t ScriptTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	// Determine how to run the script based on extension
	ext := filepath.Ext(t.ScriptPath)
	var cmd *exec.Cmd

	switch ext {
	case ".py":
		cmd = exec.CommandContext(ctx, "python3", append([]string{t.ScriptPath}, params.Args...)...)
	case ".js":
		cmd = exec.CommandContext(ctx, "node", append([]string{t.ScriptPath}, params.Args...)...)
	default:
		// Assume it's an executable or shell script
		cmd = exec.CommandContext(ctx, t.ScriptPath, params.Args...)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Script failed with error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
}

func (t ScriptTool) RequiresConfirmation(args json.RawMessage) bool {
	return true // Always require confirmation for skill scripts for safety
}

func (t ScriptTool) CallString(args json.RawMessage) string {
	return fmt.Sprintf("Running script '%s' from skill '%s'", t.ScriptName, t.SkillName)
}

// ActivateSkillTool is a tool that "activates" a skill.
type ActivateSkillTool struct {
	Skills map[string]*skill.Skill
	Reg    *common.ToolRegistry
}

func (t ActivateSkillTool) Name() string { return "activate_skill" }
func (t ActivateSkillTool) Description() string {
	return "Activate a skill by name to see its instructions and enable its scripts as tools."
}
func (t ActivateSkillTool) Parameters() json.RawMessage {
	names := make([]string, 0, len(t.Skills))
	var descBuilder strings.Builder
	descBuilder.WriteString("The name of the skill to activate. Available skills:\n")

	for name, s := range t.Skills {
		names = append(names, name)
		descBuilder.WriteString(fmt.Sprintf("- %s: %s\n", name, s.Metadata.Description))
	}
	enumStr, _ := json.Marshal(names)

	return json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"name": { 
				"type": "string", 
				"enum": %s, 
				"description": %q 
			}
		},
		"required": ["name"]
	}`, string(enumStr), descBuilder.String()))
}

func (t ActivateSkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	s, ok := t.Skills[params.Name]
	if !ok {
		return fmt.Sprintf("Skill '%s' not found", params.Name), nil
	}

	// Register scripts as tools
	scriptsDir := filepath.Join(s.Path, "scripts")
	if entries, err := os.ReadDir(scriptsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				scriptPath := filepath.Join(scriptsDir, entry.Name())
				st := ScriptTool{
					SkillName:  s.Metadata.Name,
					ScriptName: entry.Name(),
					ScriptPath: scriptPath,
				}
				t.Reg.Register(st)
			}
		}
	}

	refs := skill.DiscoverSkillReferences(s)
	var resp strings.Builder
	resp.WriteString(fmt.Sprintf("Skill '%s' activated.\n\nInstructions:\n%s", s.Metadata.Name, s.Instructions))
	if len(refs) > 0 {
		resp.WriteString("\n\n## Available References\n")
		for _, ref := range refs {
			resp.WriteString(fmt.Sprintf("- `%s`\n", ref))
		}
		resp.WriteString("\nTo read a reference file, use the `skill_read_reference` tool with the skill name and file path.\n")
	}
	return resp.String(), nil
}

func (t ActivateSkillTool) RequiresConfirmation(args json.RawMessage) bool { return false }
func (t ActivateSkillTool) CallString(args json.RawMessage) string {
	var params struct {
		Name string `json:"name"`
	}
	json.Unmarshal(args, &params)
	return fmt.Sprintf("Activating skill '%s'", params.Name)
}

func sanitizeToolName(name string) string {
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return name
}
