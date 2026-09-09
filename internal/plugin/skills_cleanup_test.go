package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRegisterPluginSkills_CreatesMissingSkillsDir verifies that registering
// the very first plugin skill succeeds even when the skills directory does
// not exist yet (it must be created, not assumed).
func TestRegisterPluginSkills_CreatesMissingSkillsDir(t *testing.T) {
	pluginsDir := t.TempDir()
	skillsDir := filepath.Join(t.TempDir(), "late", "skills") // does not exist

	pluginDir := filepath.Join(pluginsDir, "skill-plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills", "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg := `{"name":"skill-plugin","version":"1.0.0","late":{"skills":["skills"]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "skills", "s1", "SKILL.md"),
		[]byte("---\nname: s1\ndescription: t\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("RegisterPluginSkills with missing dir: %v", err)
	}

	link := filepath.Join(skillsDir, "skill-plugin:s1")
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("expected skill symlink at %s: %v", link, err)
	}
}

// TestRegisterPluginSkills_ScopedPluginNestedLink verifies that scoped
// plugin names (@scope/plugin) create nested symlink paths with their
// parent directories, and that those links are pruned again when the
// plugin is removed.
func TestRegisterPluginSkills_ScopedPluginNestedLink(t *testing.T) {
	pluginsDir := t.TempDir()
	skillsDir := t.TempDir()

	pluginDir := filepath.Join(pluginsDir, "@scope", "nested")
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills", "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg := `{"name":"@scope/nested","version":"1.0.0","late":{"skills":["skills"]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "skills", "s1", "SKILL.md"),
		[]byte("---\nname: s1\ndescription: t\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("RegisterPluginSkills: %v", err)
	}

	link := filepath.Join(skillsDir, "@scope", "nested:s1")
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("expected nested skill symlink at %s: %v", link, err)
	}

	// Removing the plugin must prune the nested link (and its empty @scope
	// parent) on the next registration.
	plugin := pm.Plugin("@scope/nested")
	if plugin == nil {
		t.Fatal("expected @scope/nested to be discovered")
	}
	if _, err := RemovePlugin(pm, "@scope/nested"); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("re-RegisterPluginSkills: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("expected nested skill symlink to be pruned, still exists (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "@scope")); !os.IsNotExist(err) {
		t.Errorf("expected empty @scope parent dir to be pruned, still exists (err=%v)", err)
	}
}

// TestRegisterPluginSkills_PreservesUserSymlinks verifies that cleanup only
// removes symlinks Late itself created. A user-created symlink pointing
// outside the plugin stores must survive registration even when it is not
// in the keep set.
func TestRegisterPluginSkills_PreservesUserSymlinks(t *testing.T) {
	pluginsDir := t.TempDir()
	skillsDir := t.TempDir()

	// A user symlink pointing at their own notes directory.
	userTarget := t.TempDir()
	userLink := filepath.Join(skillsDir, "my-personal-skill")
	if err := os.Symlink(userTarget, userLink); err != nil {
		t.Fatal(err)
	}

	// One real plugin with a skill, so registration runs the prune path.
	pluginDir := filepath.Join(pluginsDir, "keepme")
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills", "k1"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg := `{"name":"keepme","version":"1.0.0","late":{"skills":["skills"]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "skills", "k1", "SKILL.md"),
		[]byte("---\nname: k1\ndescription: t\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatal(err)
	}

	// The user's symlink must still exist.
	if _, err := os.Lstat(userLink); err != nil {
		t.Errorf("user symlink %s was removed by cleanup: %v", userLink, err)
	}
}

// TestRegisterPluginSkills_PrunesPluginOwnedStaleLinks verifies that a
// symlink pointing into the plugin store that is no longer owned by an
// enabled plugin (e.g. the plugin was removed or disabled) is pruned,
// while the plugin's own links are kept.
func TestRegisterPluginSkills_PrunesPluginOwnedStaleLinks(t *testing.T) {
	pluginsDir := t.TempDir()
	skillsDir := t.TempDir()

	// Create a plugin dir with a skill, symlinked into the store like
	// RegisterPluginSkills would do, then remove the plugin from the
	// registry so the link is stale.
	pluginDir := filepath.Join(pluginsDir, "ghost")
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills", "g1"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg := `{"name":"ghost","version":"1.0.0","late":{"skills":["skills"]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "skills", "g1", "SKILL.md"),
		[]byte("---\nname: g1\ndescription: t\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	staleLink := filepath.Join(skillsDir, "ghost:g1")
	if err := os.Symlink(filepath.Join(pluginDir, "skills", "g1"), staleLink); err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(staleLink); !os.IsNotExist(err) {
		t.Errorf("expected stale plugin-owned symlink %s to be pruned, still exists (err=%v)", staleLink, err)
	}
}

// TestIsPluginOwnedSymlink sanity-checks the ownership predicate.
func TestIsPluginOwnedSymlink(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := NewPluginManager(pluginsDir)

	inside := filepath.Join(pluginsDir, "some-plugin", "skills", "x")
	outside := filepath.Join(t.TempDir(), "elsewhere")

	dir := t.TempDir()
	linkIn := filepath.Join(dir, "in")
	linkOut := filepath.Join(dir, "out")
	if err := os.Symlink(inside, linkIn); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linkOut); err != nil {
		t.Fatal(err)
	}

	if !pm.isPluginOwnedSymlink(linkIn) {
		t.Error("expected symlink into the plugins dir to be plugin-owned")
	}
	if pm.isPluginOwnedSymlink(linkOut) {
		t.Error("expected symlink outside the plugins dir NOT to be plugin-owned")
	}

	// A symlink with an unrelated name but a store path is still owned.
	weird := filepath.Join(dir, "user-looking")
	if err := os.Symlink(filepath.Join(pluginsDir, "p", "s"), weird); err != nil {
		t.Fatal(err)
	}
	if !pm.isPluginOwnedSymlink(weird) {
		t.Error("expected store-path symlink to be plugin-owned regardless of name")
	}

	// Path-prefix lookalikes must not count (plugins dir vs plugins-evil).
	pluginsEvil := pluginsDir + "-evil"
	evilLink := filepath.Join(dir, "evil")
	if err := os.Symlink(filepath.Join(pluginsEvil, "x"), evilLink); err != nil {
		t.Fatal(err)
	}
	if pm.isPluginOwnedSymlink(evilLink) {
		t.Error("expected symlink into plugins-evil NOT to be plugin-owned")
	}

	// Non-symlink files are never owned.
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if pm.isPluginOwnedSymlink(plain) {
		t.Error("expected regular file NOT to be plugin-owned")
	}
}