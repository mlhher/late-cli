package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSkillFile(t *testing.T) {
	content := `---
name: test-skill
description: A test skill
---
# Instructions
Do something.
`
	metadata, body, err := parseSkillFile(content)
	if err != nil {
		t.Fatalf("Failed to parse skill file: %v", err)
	}

	if metadata.Name != "test-skill" {
		t.Errorf("Expected name 'test-skill', got '%s'", metadata.Name)
	}
	if metadata.Description != "A test skill" {
		t.Errorf("Expected description 'A test skill', got '%s'", metadata.Description)
	}
	if body != "# Instructions\nDo something." {
		t.Errorf("Expected body '# Instructions\nDo something.', got '%s'", body)
	}
}

func TestLoadSkill(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "skill-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.Mkdir(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `---
name: my-skill
description: My test skill
---
Instructions here.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	skill, err := LoadSkill(skillDir)
	if err != nil {
		t.Fatalf("LoadSkill failed: %v", err)
	}

	if skill.Metadata.Name != "my-skill" {
		t.Errorf("Expected name 'my-skill', got '%s'", skill.Metadata.Name)
	}
}

func TestDiscoverSkillReferences(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "discover-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "z-dir"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "a-dir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(fmt.Sprintf("---\nname: %s\ndescription: test\n---\n", filepath.Base(tmpDir))), 0644)
	os.WriteFile(filepath.Join(tmpDir, "z-dir", "z.md"), []byte("z"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a-dir", "a.md"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "m.md"), []byte("m"), 0644)

	skill, err := LoadSkill(tmpDir)
	if err != nil {
		t.Fatalf("LoadSkill failed: %v", err)
	}

	refs := DiscoverSkillReferences(skill)

	expected := map[string]bool{
		"m.md":                         true,
		filepath.Join("a-dir", "a.md"): true,
		filepath.Join("z-dir", "z.md"): true,
	}
	if len(refs) != len(expected) {
		t.Errorf("Expected %d refs, got %d: %v", len(expected), len(refs), refs)
	}
	for _, ref := range refs {
		if !expected[ref] {
			t.Errorf("Unexpected ref: %q", ref)
		}
	}
}

func TestDiscoverSkills(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "discover-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	skill1Dir := filepath.Join(tmpDir, "skill-one")
	os.Mkdir(skill1Dir, 0755)
	os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte("---\nname: skill-one\ndescription: one\n---\nbody"), 0644)

	skill2Dir := filepath.Join(tmpDir, "skill-two")
	os.Mkdir(skill2Dir, 0755)
	os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte("---\nname: skill-two\ndescription: two\n---\nbody"), 0644)

	skills, err := DiscoverSkills([]string{tmpDir})
	if err != nil {
		t.Fatalf("DiscoverSkills failed: %v", err)
	}

	if len(skills) != 2 {
		t.Errorf("Expected 2 skills, got %d", len(skills))
	}
}

// TestDiscoverSkills_FollowsSymlinks is a regression test: plugin-provided
// skills are registered as symlinks namespaced "<plugin>:<skill>" (see
// PluginManager.RegisterPluginSkills), but os.DirEntry.IsDir() reports
// false for a symlink even when it points at a directory. DiscoverSkills
// must resolve entries with os.Stat so symlinked skill directories aren't
// silently skipped, and must validate/load against the symlink's resolved
// target (whose basename is the plain skill name) rather than the
// namespaced link name itself, which never matches the skill's own
// declared name.
func TestDiscoverSkills_FollowsSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	realDir := filepath.Join(tmpDir, "real-target", "linked-skill")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "SKILL.md"), []byte("---\nname: linked-skill\ndescription: via symlink\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(skillsDir, "myplugin:linked-skill")); err != nil {
		t.Fatal(err)
	}

	skills, err := DiscoverSkills([]string{skillsDir})
	if err != nil {
		t.Fatalf("DiscoverSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected symlinked skill to be discovered, got %d skills", len(skills))
	}
	if skills[0].Metadata.Name != "linked-skill" {
		t.Errorf("expected name 'linked-skill', got %q", skills[0].Metadata.Name)
	}
}

// TestDiscoverSkills_ScopedNesting is a regression test: a scoped plugin
// name ("@scope/plugin") produces a namespaced skill symlink one directory
// level deeper than an unscoped plugin's (skillsDir/@scope/plugin:skill
// rather than skillsDir/plugin:skill — see
// PluginManager.RegisterPluginSkills). DiscoverSkills must descend into a
// directory that itself has no SKILL.md (a scope container) to find the
// symlinked skill(s) one level down.
func TestDiscoverSkills_ScopedNesting(t *testing.T) {
	tmpDir := t.TempDir()

	realDir := filepath.Join(tmpDir, "real-target", "scoped-skill")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "SKILL.md"), []byte("---\nname: scoped-skill\ndescription: nested\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(tmpDir, "skills")
	scopeDir := filepath.Join(skillsDir, "@scope")
	if err := os.MkdirAll(scopeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(scopeDir, "plugin:scoped-skill")); err != nil {
		t.Fatal(err)
	}

	skills, err := DiscoverSkills([]string{skillsDir})
	if err != nil {
		t.Fatalf("DiscoverSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected scoped-nested skill to be discovered, got %d skills", len(skills))
	}
	if skills[0].Metadata.Name != "scoped-skill" {
		t.Errorf("expected name 'scoped-skill', got %q", skills[0].Metadata.Name)
	}
}
