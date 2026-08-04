package plugin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)// writeBarePlugin creates a minimal plugin directory with a native-Late
// package.json at the given path and returns the directory. It is
// intentionally a different name from the rich
// `writeMinimalPluginManifest` helper that drives plugin-update tests;
// this helper is for project-dir routing tests that don't need a
// manifest round-trip. The `"late": {}` block is required so LoadPlugin
// recognizes the directory as a valid native-Late plugin; without it,
// the loader fails with "no recognized plugin format" because the
// package.json has no `late`, `omp`, or `.claude-plugin/plugin.json`.
func writeBarePlugin(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create plugin dir %s: %v", dir, err)
	}
	pkg := `{
	"name": "` + name + `",
	"version": "1.0.0",
	"description": "Test plugin ` + name + `",
	"late": {}
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}
	return dir
}

// ---------------------------------------------------------------------------
// PluginManager project-dir methods
// ---------------------------------------------------------------------------

func TestPluginManager_SetProjectDir(t *testing.T) {
	pm := NewPluginManager("/tmp/global-plugins")
	if pm.HasProjectDir() {
		t.Error("expected HasProjectDir to be false initially")
	}
	if pm.ProjectDir() != "" {
		t.Error("expected ProjectDir to be empty initially")
	}

	pm.SetProjectDir("/tmp/project-plugins")
	if !pm.HasProjectDir() {
		t.Error("expected HasProjectDir to be true after SetProjectDir")
	}
	if pm.ProjectDir() != "/tmp/project-plugins" {
		t.Errorf("expected ProjectDir /tmp/project-plugins, got %s", pm.ProjectDir())
	}
}

func TestPluginManager_TargetDir(t *testing.T) {
	global := "/tmp/global-plugins"
	project := "/tmp/project-plugins"

	t.Run("global only", func(t *testing.T) {
		pm := NewPluginManager(global)
		if got := pm.TargetDir(false); got != global {
			t.Errorf("TargetDir(false) = %s, want %s", got, global)
		}
		// When no project dir is set, TargetDir(true) should still return global
		if got := pm.TargetDir(true); got != global {
			t.Errorf("TargetDir(true) without project = %s, want %s", got, global)
		}
	})

	t.Run("global + project", func(t *testing.T) {
		pm := NewPluginManager(global)
		pm.SetProjectDir(project)
		if got := pm.TargetDir(false); got != global {
			t.Errorf("TargetDir(false) = %s, want %s", got, global)
		}
		if got := pm.TargetDir(true); got != project {
			t.Errorf("TargetDir(true) = %s, want %s", got, project)
		}
	})
}

// ---------------------------------------------------------------------------
// Discover with project-local directory
// ---------------------------------------------------------------------------

func TestDiscover_GlobalOnly(t *testing.T) {
	globalDir := t.TempDir()
	writeBarePlugin(t, filepath.Join(globalDir, "global-one"), "global-one")
	writeBarePlugin(t, filepath.Join(globalDir, "global-two"), "global-two")

	pm := NewPluginManager(globalDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if pm.Count() != 2 {
		t.Errorf("expected 2 plugins, got %d", pm.Count())
	}
	if pm.Plugin("global-one") == nil {
		t.Error("expected global-one to be found")
	}
	if pm.Plugin("global-two") == nil {
		t.Error("expected global-two to be found")
	}
}

func TestDiscover_ProjectOnly(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	writeBarePlugin(t, filepath.Join(projectDir, "proj-one"), "proj-one")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if pm.Count() != 1 {
		t.Errorf("expected 1 plugin, got %d", pm.Count())
	}
	if pm.Plugin("proj-one") == nil {
		t.Error("expected proj-one to be found")
	}
}

func TestDiscover_BothDirectories(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	writeBarePlugin(t, filepath.Join(globalDir, "global-a"), "global-a")
	writeBarePlugin(t, filepath.Join(globalDir, "global-b"), "global-b")
	writeBarePlugin(t, filepath.Join(projectDir, "project-a"), "project-a")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if pm.Count() != 3 {
		t.Errorf("expected 3 plugins, got %d", pm.Count())
	}
	if pm.Plugin("global-a") == nil {
		t.Error("expected global-a to be found")
	}
	if pm.Plugin("global-b") == nil {
		t.Error("expected global-b to be found")
	}
	if pm.Plugin("project-a") == nil {
		t.Error("expected project-a to be found")
	}
}

func TestDiscover_ProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Same plugin name in both dirs, different versions
	globalPluginDir := filepath.Join(globalDir, "my-plugin")
	writeBarePlugin(t, globalPluginDir, "my-plugin")
	// Write a version 2.0.0 to the project dir
	projPluginDir := filepath.Join(projectDir, "my-plugin")
	if err := os.MkdirAll(projPluginDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projPkg := `{"name": "my-plugin", "version": "2.0.0", "description": "Project-local override", "late": {}}`
	if err := os.WriteFile(filepath.Join(projPluginDir, "package.json"), []byte(projPkg), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if pm.Count() != 1 {
		t.Errorf("expected 1 plugin (overridden), got %d", pm.Count())
	}
	plugin := pm.Plugin("my-plugin")
	if plugin == nil {
		t.Fatal("expected my-plugin to be found")
	}
	if plugin.Version != "2.0.0" {
		t.Errorf("expected version 2.0.0 (project override), got %s", plugin.Version)
	}
}

func TestDiscover_IgnoresNodeModulesAndCache(t *testing.T) {
	globalDir := t.TempDir()
	writeBarePlugin(t, filepath.Join(globalDir, "real-plugin"), "real-plugin")
	os.MkdirAll(filepath.Join(globalDir, "node_modules", "some-pkg"), 0755)
	os.MkdirAll(filepath.Join(globalDir, ".cache", "stuff"), 0755)

	pm := NewPluginManager(globalDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if pm.Count() != 1 {
		t.Errorf("expected 1 plugin (ignoring node_modules and .cache), got %d", pm.Count())
	}
}

// ---------------------------------------------------------------------------
// InstallFromLocal with project flag
// ---------------------------------------------------------------------------

func TestInstallFromLocal_Project(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeBarePlugin(t, sourceDir, "test-plugin")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	plugin, err := InstallFromLocal(pm, sourceDir, true) // project = true
	if err != nil {
		t.Fatalf("InstallFromLocal(project=true) failed: %v", err)
	}

	if plugin.Name != "test-plugin" {
		t.Errorf("expected plugin name test-plugin, got %s", plugin.Name)
	}
	// Path should be inside the project dir
	expectedPath := filepath.Join(projectDir, "test-plugin")
	if plugin.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, plugin.Path)
	}
	// Symlink should exist in the project dir
	info, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", expectedPath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected a symlink")
	}

	// Plugin should be registered in the manager
	if pm.Plugin("test-plugin") == nil {
		t.Error("expected plugin to be registered in manager")
	}
}

func TestInstallFromLocal_Global(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeBarePlugin(t, sourceDir, "test-plugin")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	plugin, err := InstallFromLocal(pm, sourceDir) // project = default (false)
	if err != nil {
		t.Fatalf("InstallFromLocal(global) failed: %v", err)
	}

	expectedPath := filepath.Join(globalDir, "test-plugin")
	if plugin.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, plugin.Path)
	}
}

// ---------------------------------------------------------------------------
// Link with project flag
// ---------------------------------------------------------------------------

func TestLink_Project(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeBarePlugin(t, sourceDir, "linked-plugin")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	plugin, err := Link(pm, sourceDir, true)
	if err != nil {
		t.Fatalf("Link(project=true) failed: %v", err)
	}

	expectedPath := filepath.Join(projectDir, "linked-plugin")
	if plugin.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, plugin.Path)
	}
}

func TestLink_Global(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeBarePlugin(t, sourceDir, "linked-plugin")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	plugin, err := Link(pm, sourceDir) // global by default
	if err != nil {
		t.Fatalf("Link(global) failed: %v", err)
	}

	expectedPath := filepath.Join(globalDir, "linked-plugin")
	if plugin.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, plugin.Path)
	}
}

// ---------------------------------------------------------------------------
// RemovePlugin with project flag
// ---------------------------------------------------------------------------

func TestRemovePlugin_Project(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeBarePlugin(t, sourceDir, "removable")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	// Install into project dir
	plugin, err := InstallFromLocal(pm, sourceDir, true)
	if err != nil {
		t.Fatalf("InstallFromLocal failed: %v", err)
	}

	// Remove from project dir
	removed, err := RemovePlugin(pm, "removable", true)
	if err != nil {
		t.Fatalf("RemovePlugin failed: %v", err)
	}

	if removed.Name != "removable" {
		t.Errorf("expected removed plugin name removable, got %s", removed.Name)
	}

	// Symlink should be gone
	if _, err := os.Lstat(plugin.Path); !os.IsNotExist(err) {
		t.Error("expected symlink to be removed")
	}

	// Plugin should be removed from manager
	if pm.Plugin("removable") != nil {
		t.Error("expected plugin to be removed from manager")
	}
}

func TestRemovePlugin_Global(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeBarePlugin(t, sourceDir, "removable")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	// Install into global dir
	plugin, err := InstallFromLocal(pm, sourceDir) // default global
	if err != nil {
		t.Fatalf("InstallFromLocal failed: %v", err)
	}

	// Remove from global dir
	_, err = RemovePlugin(pm, "removable") // default global
	if err != nil {
		t.Fatalf("RemovePlugin failed: %v", err)
	}

	if _, err := os.Lstat(plugin.Path); !os.IsNotExist(err) {
		t.Error("expected symlink to be removed")
	}
	if pm.Plugin("removable") != nil {
		t.Error("expected plugin to be removed from manager")
	}
}

func TestRemovePlugin_NotFound(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	_, err := RemovePlugin(pm, "nonexistent", true)
	if err == nil {
		t.Error("expected error when removing non-existent plugin")
	}
}

// ---------------------------------------------------------------------------
// HasProjectDir / TargetDir integration
// ---------------------------------------------------------------------------

func TestHasProjectDir_Methods(t *testing.T) {
	pm := NewPluginManager("/tmp/global")

	if pm.HasProjectDir() {
		t.Error("expected false before SetProjectDir")
	}

	pm.SetProjectDir("/tmp/project")
	if !pm.HasProjectDir() {
		t.Error("expected true after SetProjectDir")
	}

	if pm.ProjectDir() != "/tmp/project" {
		t.Errorf("expected /tmp/project, got %s", pm.ProjectDir())
	}

	// TargetDir(true) should return project dir
	if pm.TargetDir(true) != "/tmp/project" {
		t.Errorf("TargetDir(true) expected /tmp/project, got %s", pm.TargetDir(true))
	}

	// TargetDir(false) should return global dir
	if pm.TargetDir(false) != "/tmp/global" {
		t.Errorf("TargetDir(false) expected /tmp/global, got %s", pm.TargetDir(false))
	}
}

// ---------------------------------------------------------------------------
// parseProjectFlag
// ---------------------------------------------------------------------------

func TestParseProjectFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantProj  bool
		wantRest  string
	}{
		{
			name:     "no flag, has source",
			args:     []string{"some-source"},
			wantProj: false,
			wantRest: "some-source",
		},
		{
			name:     "--project flag, has source",
			args:     []string{"--project", "some-source"},
			wantProj: true,
			wantRest: "some-source",
		},
		{
			name:     "--local flag, has source",
			args:     []string{"--local", "some-source"},
			wantProj: true,
			wantRest: "some-source",
		},
		{
			name:     "source then --project (parser scans all args)",
			args:     []string{"some-source", "--project"},
			wantProj: true, // parseProjectFlag scans every arg for the flags, regardless of position
			wantRest: "some-source",
		},
		{
			name:     "--project first, source second",
			args:     []string{"--project", "source"},
			wantProj: true,
			wantRest: "source",
		},
		{
			name:     "only --project, no source",
			args:     []string{"--project"},
			wantProj: true,
			wantRest: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProj, gotRest := parseProjectFlag(tt.args)
			if gotProj != tt.wantProj {
				t.Errorf("parseProjectFlag() project = %v, want %v", gotProj, tt.wantProj)
			}
			if gotRest != tt.wantRest {
				t.Errorf("parseProjectFlag() rest = %q, want %q", gotRest, tt.wantRest)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PollingWatcher AddWatchDir and takeSnapshot
// ---------------------------------------------------------------------------

func TestAddWatchDir(t *testing.T) {
	w := &PollingWatcher{}
	if len(w.projectDirs) != 0 {
		t.Error("expected empty projectDirs initially")
	}

	w.AddWatchDir("/tmp/dir1")
	if len(w.projectDirs) != 1 {
		t.Errorf("expected 1 dir, got %d", len(w.projectDirs))
	}

	// Adding the same dir again should be a no-op
	w.AddWatchDir("/tmp/dir1")
	if len(w.projectDirs) != 1 {
		t.Errorf("expected still 1 dir after duplicate add, got %d", len(w.projectDirs))
	}

	// Adding empty string should be a no-op
	w.AddWatchDir("")
	if len(w.projectDirs) != 1 {
		t.Errorf("expected still 1 dir after empty add, got %d", len(w.projectDirs))
	}

	// Adding a different dir
	w.AddWatchDir("/tmp/dir2")
	if len(w.projectDirs) != 2 {
		t.Errorf("expected 2 dirs, got %d", len(w.projectDirs))
	}
}

func TestTakeSnapshot_MultipleDirs(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Create one plugin in global, one in project
	writeBarePlugin(t, filepath.Join(globalDir, "global-pkg"), "global-pkg")
	writeBarePlugin(t, filepath.Join(projectDir, "project-pkg"), "project-pkg")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)
	w := NewPollingWatcher(pm)

	snapshot := w.takeSnapshot()

	// Both plugins should appear in the snapshot
	globalEntry, ok := snapshot["global-pkg"]
	if !ok {
		t.Error("expected global-pkg in snapshot")
	} else {
		if !globalEntry.exists {
			t.Error("expected global-pkg exists=true")
		}
		if !globalEntry.hasLateFile {
			t.Error("expected global-pkg hasLateFile=true")
		}
	}

	projectEntry, ok := snapshot["project-pkg"]
	if !ok {
		t.Error("expected project-pkg in snapshot")
	} else {
		if !projectEntry.exists {
			t.Error("expected project-pkg exists=true")
		}
		if !projectEntry.hasLateFile {
			t.Error("expected project-pkg hasLateFile=true")
		}
	}

	// Global dir file entries should not appear
	if _, ok := snapshot["node_modules"]; ok {
		t.Error("expected node_modules not in snapshot")
	}
}

func TestTakeSnapshot_ProjectPluginOverrides(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Same plugin name in both dirs
	writeBarePlugin(t, filepath.Join(globalDir, "shared-pkg"), "shared-pkg")
	writeBarePlugin(t, filepath.Join(projectDir, "shared-pkg"), "shared-pkg")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)
	w := NewPollingWatcher(pm)

	snapshot := w.takeSnapshot()

	// The project-local version should win (last writer wins in the snapshot map)
	if _, ok := snapshot["shared-pkg"]; !ok {
		t.Error("expected shared-pkg in snapshot")
	}
}

func TestTakeSnapshot_WithAddedWatchDir(t *testing.T) {
	globalDir := t.TempDir()
	extraDir := t.TempDir()

	writeBarePlugin(t, filepath.Join(globalDir, "global-pkg"), "global-pkg")
	writeBarePlugin(t, filepath.Join(extraDir, "extra-pkg"), "extra-pkg")

	pm := NewPluginManager(globalDir)
	w := NewPollingWatcher(pm)
	w.AddWatchDir(extraDir)

	snapshot := w.takeSnapshot()

	if _, ok := snapshot["global-pkg"]; !ok {
		t.Error("expected global-pkg in snapshot")
	}
	if _, ok := snapshot["extra-pkg"]; !ok {
		t.Error("expected extra-pkg in snapshot")
	}
}

func TestSnapshotChanged_DetectsChangesAcrossDirs(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()

	// Pretend we have two plugins from different dirs in the snapshot
	old := map[string]pluginSnapshotEntry{
		"global-pkg": {exists: true, enabled: true, modTime: now, hasLateFile: true},
		"proj-pkg":   {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}

	// One is removed
	new := map[string]pluginSnapshotEntry{
		"global-pkg": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}

	if !w.snapshotChanged(old, new) {
		t.Error("expected true when a plugin is removed")
	}
}

func TestSnapshotChanged_NoChangeAcrossDirs(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()

	snapshot := map[string]pluginSnapshotEntry{
		"pkg-a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
		"pkg-b": {exists: true, enabled: false, modTime: now.Add(-time.Hour), hasLateFile: true},
	}

	if w.snapshotChanged(snapshot, snapshot) {
		t.Error("expected false for identical snapshots with multi-dir entries")
	}
}

// ---------------------------------------------------------------------------
// PluginPathInDir
// ---------------------------------------------------------------------------

func TestPluginPathInDir(t *testing.T) {
	pm := NewPluginManager("/tmp/global")

	expected := "/tmp/global/test-plugin"
	if got := pm.PluginPath("test-plugin"); got != expected {
		t.Errorf("PluginPath = %s, want %s", got, expected)
	}

	expected2 := "/tmp/project/test-plugin"
	if got := pm.PluginPathInDir("/tmp/project", "test-plugin"); got != expected2 {
		t.Errorf("PluginPathInDir = %s, want %s", got, expected2)
	}
}

// ---------------------------------------------------------------------------
// orphan @scope parent cleanup
// ---------------------------------------------------------------------------

// writeScopedPluginLike creates a plugin manifest whose `name` field uses
// the `@scope/pkg` shape that InstallFromLocal writes to the plugins store
// at `<pluginsdir>/@scope/pkg`, so we can drive the orphan-parent cleanup
// path in removeFromDir.
func writeScopedPluginLike(t *testing.T, dir, scopedName string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	pkg := `{"name": "` + scopedName + `", "version": "0.1.0", "description": "scoped test", "late": {}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return dir
}

func TestRemovePlugin_ScopedLink_CleansEmptyScopeParent(t *testing.T) {
	globalDir := t.TempDir()
	sourceDir := t.TempDir()
	writeScopedPluginLike(t, sourceDir, "@late/scoped-plugin")

	pm := NewPluginManager(globalDir)
	if _, err := InstallFromLocal(pm, sourceDir); err != nil {
		t.Fatalf("InstallFromLocal failed: %v", err)
	}

	// Install must have created the @late parent dir (symlink lives at
	// `<globalDir>/@late/scoped-plugin`).
	scopeParent := filepath.Join(globalDir, "@late")
	if _, err := os.Stat(scopeParent); err != nil {
		t.Fatalf("expected @late parent %s after scoped install: %v", scopeParent, err)
	}

	if _, err := RemovePlugin(pm, "@late/scoped-plugin"); err != nil {
		t.Fatalf("RemovePlugin failed: %v", err)
	}

	// The orphan @late dir must NOT survive after a successful remove.
	if _, err := os.Stat(scopeParent); !os.IsNotExist(err) {
		t.Errorf("expected orphan @late parent %s to be cleaned, but it still exists (err=%v)", scopeParent, err)
	}
}

func TestRemovePlugin_ScopedLink_KeepsNonEmptyScopeParent(t *testing.T) {
	globalDir := t.TempDir()
	srcA := t.TempDir()
	srcB := t.TempDir()
	writeScopedPluginLike(t, srcA, "@late/scope-a")
	writeScopedPluginLike(t, srcB, "@late/scope-b")

	pm := NewPluginManager(globalDir)
	if _, err := InstallFromLocal(pm, srcA); err != nil {
		t.Fatalf("install A: %v", err)
	}
	if _, err := InstallFromLocal(pm, srcB); err != nil {
		t.Fatalf("install B: %v", err)
	}

	scopeParent := filepath.Join(globalDir, "@late")
	if entries, _ := os.ReadDir(scopeParent); len(entries) != 2 {
		t.Fatalf("expected 2 entries under @late before remove, got %d", len(entries))
	}

	if _, err := RemovePlugin(pm, "@late/scope-a"); err != nil {
		t.Fatalf("RemovePlugin failed: %v", err)
	}

	// @late must still exist because scope-b is a sibling.
	if _, err := os.Stat(scopeParent); err != nil {
		t.Errorf("expected non-empty @late parent to survive (has scope-b): err=%v", err)
	}
	if entries, _ := os.ReadDir(scopeParent); len(entries) != 1 {
		t.Errorf("expected 1 entry left under @late (scope-b), got %d", len(entries))
	}
}

// TestRemovePlugin_Project_ScopedLink_CleansEmptyScopeParent exercises the
// project-scoped branch of removeFromDir. The empty-@scope-parent cleanup
// must trigger for project-local installs the same way it does for global
// installs — the `dir` parameter flows through pm.TargetDir(project), but
// without an explicit test the project's branch is never asserted.
func TestRemovePlugin_Project_ScopedLink_CleansEmptyScopeParent(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	sourceDir := t.TempDir()
	writeScopedPluginLike(t, sourceDir, "@late/scoped-plugin")

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)
	if _, err := InstallFromLocal(pm, sourceDir, true); err != nil {
		t.Fatalf("InstallFromLocal(project=true) failed: %v", err)
	}

	scopeParent := filepath.Join(projectDir, "@late")
	if _, err := os.Stat(scopeParent); err != nil {
		t.Fatalf("expected project @late parent %s after scoped install: %v", scopeParent, err)
	}

	if _, err := RemovePlugin(pm, "@late/scoped-plugin", true); err != nil {
		t.Fatalf("RemovePlugin failed: %v", err)
	}

	if _, err := os.Stat(scopeParent); !os.IsNotExist(err) {
		t.Errorf("expected orphan project @late parent %s to be cleaned, but it still exists (err=%v)", scopeParent, err)
	}
}

// ---------------------------------------------------------------------------
// handlePluginRemove self-clean of stale skill symlinks
// ---------------------------------------------------------------------------

// writeSkillPlugin creates a plugin with one real SKILL.md under
// `skills/<name>/SKILL.md` so RegisterPluginSkills synthesizes a
// `<plugin>:<skill>` symlink that we can later watch get pruned.
func writeSkillPlugin(t *testing.T, dir, pluginName, skillName string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "skills", skillName), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pkg := `{"name": "` + pluginName + `", "version": "0.1.0", "description": "skills plugin", "late": {"skills": ["skills"]}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	body := "---\nname: " + skillName + "\ndescription: test skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "skills", skillName, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func TestHandlePluginRemove_PurgesStaleSkillSymlink(t *testing.T) {
	globalDir := t.TempDir()
	sourceDir := t.TempDir()
	xdgRoot := t.TempDir()

	// Sandbox `lateSkillsDir()` so the test cannot damage the user's real
	// `~/.config/late/skills` if our internal invariants drift. Go's
	// `os.UserConfigDir()` honors `XDG_CONFIG_HOME` on every Unix-like
	// target (Linux, macOS, BSD) per the freedesktop spec; pinning only
	// this variable is sufficient and avoids sideways effects on
	// `os.UserHomeDir()`/`isSuspiciousPluginPath` that overriding HOME
	// would introduce.
	t.Setenv("XDG_CONFIG_HOME", xdgRoot)
	skillsDir := filepath.Join(xdgRoot, "late", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if resolved, _ := lateSkillsDir(); resolved != skillsDir {
		t.Fatalf("lateSkillsDir() = %q, want %q (XDG/HOME override misconfigured)", resolved, skillsDir)
	}

	writeSkillPlugin(t, sourceDir, "skills-plugin", "my-skill")

	pm := NewPluginManager(globalDir)
	if _, err := InstallFromLocal(pm, sourceDir); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Discover + register so the namespaced skill symlink is materialized.
	if err := pm.Discover(); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("initial RegisterPluginSkills: %v", err)
	}

	linkPath := filepath.Join(skillsDir, "skills-plugin:my-skill")
	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("expected initial skill symlink at %s: %v", linkPath, err)
	}

	// Behavior under test: handlePluginRemove self-cleans the namespaced
	// skill symlinks for the removed plugin immediately, without waiting
	// for the next watcher tick.
	handlePluginRemove(pm, []string{"skills-plugin"})

	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Errorf("expected stale skill symlink %s to be pruned by handlePluginRemove, but it still exists", linkPath)
	}
}

// TestHandlePluginRemove_PreservesSiblingSkillSymlink guards against an
// over-pruning regression: if handlePluginRemove's self-clean step ever
// miscomputed `keep` (e.g. by mistake that matched on bare names), a sibling
// plugin's namespaced skill symlink would be wrongly removed.
func TestHandlePluginRemove_PreservesSiblingSkillSymlink(t *testing.T) {
	globalDir := t.TempDir()
	srcA := t.TempDir()
	srcB := t.TempDir()
	xdgRoot := t.TempDir()

	// Same sandboxing strategy as the single-plugin self-clean test.
	t.Setenv("XDG_CONFIG_HOME", xdgRoot)
	skillsDir := filepath.Join(xdgRoot, "late", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}

	// Two distinct plugins, intentionally both declaring a skill with the
	// same basename to maximize the chance a bogus "name only" keep key
	// would stomp both symlinks.
	writeSkillPlugin(t, srcA, "sibling-a", "shared-skill")
	writeSkillPlugin(t, srcB, "sibling-b", "shared-skill")

	pm := NewPluginManager(globalDir)
	if _, err := InstallFromLocal(pm, srcA); err != nil {
		t.Fatalf("install A: %v", err)
	}
	if _, err := InstallFromLocal(pm, srcB); err != nil {
		t.Fatalf("install B: %v", err)
	}
	if err := pm.Discover(); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("initial RegisterPluginSkills: %v", err)
	}

	linkA := filepath.Join(skillsDir, "sibling-a:shared-skill")
	linkB := filepath.Join(skillsDir, "sibling-b:shared-skill")
	if _, err := os.Lstat(linkA); err != nil {
		t.Fatalf("expected initial symlink %s: %v", linkA, err)
	}
	if _, err := os.Lstat(linkB); err != nil {
		t.Fatalf("expected initial symlink %s: %v", linkB, err)
	}

	// Remove A; B's symlink must survive.
	handlePluginRemove(pm, []string{"sibling-a"})

	if _, err := os.Lstat(linkA); !os.IsNotExist(err) {
		t.Errorf("expected symlink %s to be pruned, but it still exists", linkA)
	}
	if _, err := os.Lstat(linkB); err != nil {
		t.Errorf("expected sibling symlink %s to survive the cleanup, but got err: %v", linkB, err)
	}
}
