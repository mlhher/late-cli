package skill

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillMetadata represents the YAML frontmatter of a SKILL.md file.
type SkillMetadata struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
}

// Skill represents a loaded agent skill.
type Skill struct {
	Path         string
	Metadata     SkillMetadata
	Instructions string
}

// LoadSkill loads a skill from the specified directory.
func LoadSkill(skillDir string) (*Skill, error) {
	skillFile := filepath.Join(skillDir, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read SKILL.md: %w", err)
	}

	metadata, body, err := parseSkillFile(string(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse SKILL.md in %s: %w", skillDir, err)
	}

	// Validation
	if metadata.Name == "" {
		return nil, fmt.Errorf("SKILL.md in %s is missing 'name' field", skillDir)
	}
	if metadata.Description == "" {
		return nil, fmt.Errorf("SKILL.md in %s is missing 'description' field", skillDir)
	}

	expectedName := filepath.Base(skillDir)
	if metadata.Name != expectedName {
		return nil, fmt.Errorf("skill name '%s' does not match directory name '%s'", metadata.Name, expectedName)
	}

	return &Skill{
		Path:         skillDir,
		Metadata:     *metadata,
		Instructions: body,
	}, nil
}

// parseSkillFile separates YAML frontmatter from Markdown body.
func parseSkillFile(content string) (*SkillMetadata, string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var frontmatter strings.Builder
	var body strings.Builder
	var inFrontmatter bool
	var frontmatterFound bool
	var frontmatterComplete bool

	lineNum := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		if lineNum == 1 && line == "---" {
			inFrontmatter = true
			frontmatterFound = true
			continue
		}

		if inFrontmatter && line == "---" {
			inFrontmatter = false
			frontmatterComplete = true
			continue
		}

		if inFrontmatter {
			frontmatter.WriteString(line + "\n")
		} else {
			body.WriteString(line + "\n")
		}
	}

	if !frontmatterFound || !frontmatterComplete {
		return nil, "", fmt.Errorf("SKILL.md must have YAML frontmatter enclosed in '---'")
	}

	const maxFrontmatterSize = 1 << 20 // 1 MB
	if frontmatter.Len() > maxFrontmatterSize {
		return nil, "", fmt.Errorf("YAML frontmatter exceeds maximum size of %d bytes", maxFrontmatterSize)
	}

	var metadata SkillMetadata
	if err := yaml.Unmarshal([]byte(frontmatter.String()), &metadata); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal YAML frontmatter: %w", err)
	}

	return &metadata, strings.TrimSpace(body.String()), nil
}

// DiscoverSkills finds skills in the specified directories.
func DiscoverSkills(dirs []string) ([]*Skill, error) {
	var skills []*Skill
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to read skills directory %s: %w", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				skillDir := filepath.Join(dir, entry.Name())
				skill, err := LoadSkill(skillDir)
				if err != nil {
					// We might want to log this and continue instead of failing entirely
					// For now, let's just log to stderr or something if I had a logger.
					// Since I don't have a logger here, I'll just skip it.
					continue
				}
				skills = append(skills, skill)
			}
		}
	}
	return skills, nil
}
