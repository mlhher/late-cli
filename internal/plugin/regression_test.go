package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"late/internal/common"
	"late/internal/executor"
	"late/internal/skill"
	"late/internal/tool"
)

func TestRemovePlugin_PreservesDifferentManifestName(t *testing.T) {
	root := t.TempDir()
	// Git installs use repository names for directories, not manifest names.
	writeBarePlugin(t, filepath.Join(root, "repo-a"), "shared")
	otherPath := writeBarePlugin(t, filepath.Join(root, "shared"), "other")
	pm := NewPluginManager(root)
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	if pm.Count() != 2 {
		t.Fatal("expected two installed plugins")
	}
	if _, err := RemovePlugin(pm, "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("removing shared also deleted unrelated plugin other: %v", err)
	}
}

func TestRegisterPluginSkills_DoesNotLeakAcrossProjects(t *testing.T) {
	global, projectA, projectB, skillsDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeDeploySkillPlugin(t, projectA, "project-a-plugin")
	a := NewPluginManager(global)
	a.SetProjectDir(projectA)
	if err := a.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := a.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatal(err)
	}
	loaded, err := skill.DiscoverSkills([]string{skillsDir})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("expected project A's skill to load: skills=%d, err=%v", len(loaded), err)
	}

	// Start in another project, sharing the same user-level skills directory.
	b := NewPluginManager(global)
	b.SetProjectDir(projectB)
	if err := b.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatal(err)
	}
	loaded, err = skill.DiscoverSkills([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("project B exposes project A's skill: %s", loaded[0].Path)
	}
}

func TestRegisterPluginSkills_PreservesSameNamedSkills(t *testing.T) {
	configDir, pluginsDir := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Chdir(t.TempDir())
	writeDeploySkillPlugin(t, pluginsDir, "alpha")
	writeDeploySkillPlugin(t, pluginsDir, "beta")
	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterPluginSkills(filepath.Join(configDir, "late", "skills")); err != nil {
		t.Fatal(err)
	}
	reg := common.NewToolRegistry()
	executor.RegisterTools(reg, nil)
	activate, ok := reg.Get("activate_skill").(tool.ActivateSkillTool)
	if !ok {
		t.Fatal("missing activate_skill tool")
	}
	if len(activate.Skills) != 2 {
		t.Fatalf("two plugins provide deploy skills, but only %d remains available", len(activate.Skills))
	}
}

func writeDeploySkillPlugin(t *testing.T, root, name string) {
	t.Helper()
	p := writeTestPlugin(t, root, name, &LateManifest{Skills: []string{"skills"}})
	dir := filepath.Join(p.Path, "skills", "deploy")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: deploy for "+name+"\n---\nDeploy using "+name), 0644); err != nil {
		t.Fatal(err)
	}
}
