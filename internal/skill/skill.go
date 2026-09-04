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

// DiscoverSkillReferences returns all files in the skill directory
// (excluding SKILL.md, dotfiles, dotdirs, and scripts/).
func DiscoverSkillReferences(s *Skill) []string {
	var files []string
	_ = filepath.Walk(s.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		base := filepath.Base(path)
		// Skip dotfiles and dotdirs
		if strings.HasPrefix(base, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip scripts/ directory — handled separately as executable tools
		if base == "scripts" {
			return filepath.SkipDir
		}
		// Skip SKILL.md itself
		if base == "SKILL.md" {
			return nil
		}
		// Only include files, not directories
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Path, path)
		if err != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files
}

// DiscoverSkills finds skills in the specified directories.
//
// Entries are resolved with os.Stat (which follows symlinks) rather than
// os.DirEntry.IsDir (which reflects the raw entry type and is false for a
// symlink even when it points at a directory) — plugin-provided skills are
// registered as symlinks (see PluginManager.RegisterPluginSkills) and
// would otherwise be silently skipped. A directory that itself contains no
// SKILL.md (e.g. a scope container like "@scope/", holding one or more
// namespaced "@scope/plugin:skill" symlinks one level down) is scanned one
// level deeper, matching the exact nesting scoped plugin names produce.
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
			skillDir := filepath.Join(dir, entry.Name())
			if !isResolvedDir(skillDir) {
				continue
			}

			if skill, ok := tryLoadSkillDir(skillDir); ok {
				skills = append(skills, skill)
				continue
			}

			// No SKILL.md directly inside — try one level down (scope
			// container case).
			nested, err := os.ReadDir(skillDir)
			if err != nil {
				continue
			}
			for _, ne := range nested {
				nestedDir := filepath.Join(skillDir, ne.Name())
				if !isResolvedDir(nestedDir) {
					continue
				}
				if skill, ok := tryLoadSkillDir(nestedDir); ok {
					skills = append(skills, skill)
				}
			}
		}
	}
	return skills, nil
}

// isResolvedDir reports whether path is a directory once symlinks are
// resolved (os.Stat, unlike os.DirEntry.IsDir, follows symlinks).
func isResolvedDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// tryLoadSkillDir loads dir as a skill if it contains SKILL.md, returning
// (nil, false) for a non-skill directory or one whose SKILL.md fails to
// parse. If dir is (or is inside) a symlink, it is resolved to its real
// target first: LoadSkill validates that the skill's declared name
// matches the directory's basename, but a plugin-registered skill symlink
// is namespaced as "<plugin>:<skill>" (see
// PluginManager.RegisterPluginSkills) — never equal to the skill's own
// bare name — so validating against the symlink's own name would reject
// every plugin skill. Validating against the resolved real directory
// (whose basename is the plain skill name) is what the check actually
// intends.
func tryLoadSkillDir(dir string) (*Skill, bool) {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return nil, false
	}
	loadDir := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		loadDir = resolved
	}
	skill, err := LoadSkill(loadDir)
	if err != nil {
		return nil, false
	}
	return skill, true
}
