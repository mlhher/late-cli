package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/skill"
	"os"
	"path/filepath"
	"strings"
)

const maxSkillReferenceChars = 32768 // Same as maxReadFileChars

// SkillReadReferenceTool reads reference files from an activated skill's directory.
type SkillReadReferenceTool struct {
	// Skills is a map of skill name -> skill (same type as ActivateSkillTool.Skills)
	Skills map[string]*skill.Skill
}

func (t SkillReadReferenceTool) Name() string {
	return "skill_read_reference"
}

func (t SkillReadReferenceTool) Description() string {
	return "Read a reference file from an activated skill's directory. The skill must have been activated via activate_skill first."
}

func (t SkillReadReferenceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"skill_name": {
				"type": "string",
				"description": "The name of the skill"
			},
			"file_path": {
				"type": "string",
				"description": "The relative path to the reference file"
			}
		},
		"required": ["skill_name", "file_path"]
	}`)
}

func (t SkillReadReferenceTool) RequiresConfirmation(args json.RawMessage) bool {
	return false
}

func (t SkillReadReferenceTool) CallString(args json.RawMessage) string {
	var params struct {
		SkillName string `json:"skill_name"`
		FilePath  string `json:"file_path"`
	}
	json.Unmarshal(args, &params)
	return fmt.Sprintf("Reading skill reference: %s/%s", params.SkillName, params.FilePath)
}

func (t SkillReadReferenceTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		SkillName string `json:"skill_name"`
		FilePath  string `json:"file_path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	s, ok := t.Skills[params.SkillName]
	if !ok {
		return fmt.Sprintf("Skill '%s' not found. Activate it first with activate_skill.", params.SkillName), nil
	}

	// Security check: reject path traversal
	if strings.Contains(params.FilePath, "..") {
		return "Error: path traversal not allowed", nil
	}

	// Construct absolute path
	absPath := filepath.Join(s.Path, params.FilePath)

	// Read the file
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("Reference file not found: %s", params.FilePath), nil
		}
		return fmt.Sprintf("Error reading file: %v", err), nil
	}

	content := string(data)

	// Truncate if too large
	if len(content) > maxSkillReferenceChars {
		content = content[:maxSkillReferenceChars] + "... (output truncated)"
	}

	return content, nil
}
