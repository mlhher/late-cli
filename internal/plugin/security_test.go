package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Discover mutex (#1-3)
// ---------------------------------------------------------------------------

// TestDiscover_ConcurrentReadsDontPanic runs Discover and parallel readers
// in tandem. With the fix in place, the read paths take RLock while Discover
// holds the write lock, so reads are serialized — no concurrent map access.
func TestDiscover_ConcurrentReadsDontPanic(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	pluginDir := filepath.Join(globalDir, "concurrent-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pkg := `{"name": "concurrent-plugin", "version": "1.0.0", "late": {}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	pm := NewPluginManager(globalDir)
	pm.SetProjectDir(projectDir)

	// Pre-populate via the (locked) Add path
	if err := pm.Discover(); err != nil {
		t.Fatalf("initial Discover: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 4 reader goroutines hammer the read API
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = pm.Plugin("concurrent-plugin")
					_ = pm.Count()
					_ = pm.All()
					_ = pm.PluginCommands()
					_ = pm.BuildMCPConfigMap()
				}
			}
		}()
	}

	// 1 writer goroutine repeatedly calls the locked Discover
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := pm.Discover(); err != nil {
				t.Errorf("Discover failed: %v", err)
				return
			}
		}
	}()

	// Run reader+writer concurrently for a moment, then stop the readers
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Final sanity: at least one plugin should still be present
	if pm.Count() == 0 {
		t.Error("expected plugin to still be present after concurrent reads")
	}
}

// TestConcurrentSetProjectDirAndRead verifies SetProjectDir takes the lock
// and reads happen consistently.
func TestConcurrentSetProjectDirAndRead(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			pm.SetProjectDir(filepath.Join(t.TempDir(), "proj"))
		}(i)
		go func() {
			defer wg.Done()
			_ = pm.HasProjectDir()
			_ = pm.ProjectDir()
			_ = pm.TargetDir(true)
			_ = pm.TargetDir(false)
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Watcher (#4, #7)
// ---------------------------------------------------------------------------

// TestTakeSnapshot_DedupesAcrossSources verifies that takeSnapshot walks a
// directory at most once even when it's referenced through both
// AddWatchDir() and SetProjectDir().
func TestTakeSnapshot_DedupesAcrossSources(t *testing.T) {
	// Use a single dir as global, and reference same path as project + addWatch
	dir := t.TempDir()
	mkPlugin(t, dir, "uniq")

	pm := NewPluginManager(dir)
	pm.SetProjectDir(dir) // duplicate -> should be deduped

	w := NewPollingWatcher(pm)
	w.AddWatchDir(dir) // duplicate again -> should be deduped

	snap := w.takeSnapshot()
	if len(snap) != 1 {
		t.Fatalf("expected exactly 1 plugin entry after dedupe, got %d", len(snap))
	}
	if _, ok := snap["uniq"]; !ok {
		t.Errorf("expected 'uniq' plugin in snapshot, got keys: %v", keys(snap))
	}
}

// TestSnapshotChanged_LateFileModDetectsEnableToggle verifies that writing
// the .late-plugin.json file (with the Enabled field changed) is detected
// even when the parent directory's mtime did not change.
func TestSnapshotChanged_LateFileModDetectsEnableToggle(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "toggle")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write initial .late-plugin.json (enabled)
	writeLateMeta(t, pluginDir, true)

	pm := NewPluginManager(dir)
	w := NewPollingWatcher(pm)

	before := w.takeSnapshot()

	// Write a NEW .late-plugin.json (disabled), forcing its own mtime bump
	time.Sleep(10 * time.Millisecond) // ensure clock granularity moves
	writeLateMeta(t, pluginDir, false)

	after := w.takeSnapshot()

	if !w.snapshotChanged(before, after) {
		t.Error("expected snapshotChanged to detect enable/disable toggle (lateFileMod change)")
	}

	if after["toggle"].enabled {
		t.Error("expected enabled=false after toggle")
	}
	if before["toggle"].enabled != true {
		t.Errorf("expected initial enabled=true, got %v", before["toggle"].enabled)
	}
}

// TestSnapshotChanged_NoLateFileMod verifies the same content does not trigger.
func TestSnapshotChanged_NoLateFileMod(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "same")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLateMeta(t, pluginDir, true)

	w := NewPollingWatcher(nil) // we don't need pm for snapshot comparison
	// build snapshot manually to avoid DependOnPM
	directDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directDir, "same"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLateMeta(t, filepath.Join(directDir, "same"), true)

	pm := NewPluginManager(directDir)
	w2 := NewPollingWatcher(pm)
	a := w2.takeSnapshot()
	b := w2.takeSnapshot()
	if w2.snapshotChanged(a, b) {
		t.Error("expected identical snapshots to NOT register as changed")
	}
	_ = dir
	_ = pluginDir
	_ = w
}

// TestWatcher_StartRunsWithoutPanic sanity-checks that the watcher goroutine
// takes/drops the lock without panic in a single tick. ctx is canceled
// immediately so the goroutine exits promptly.
func TestWatcher_StartRunsWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	mkPlugin(t, dir, "p1")

	pm := NewPluginManager(dir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	w := NewPollingWatcher(pm)
	w.SetInterval(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancel

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Start(ctx, func() {})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit promptly on canceled context")
	}
}

// ---------------------------------------------------------------------------
// Installer (#6)
// ---------------------------------------------------------------------------

// TestInstallFromGit_CleansUpOnCloneFailure verifies that a failed git clone
// leaves no half-populated directory behind. A fake `git` on PATH creates
// the target directory and then fails, deterministically exercising the
// partial-clone cleanup with zero network access — the old version cloned a
// nonexistent GitHub URL and could hang on DNS or prompt for credentials.
func TestInstallFromGit_CleansUpOnCloneFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git shell script only works on POSIX")
	}
	fakeBin := t.TempDir()
	// argv: git clone --depth 1 <url> <target> — create the target dir
	// (simulating a partial clone) then fail.
	fakeGit := filepath.Join(fakeBin, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nmkdir -p \"$5\"\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	targetRoot := t.TempDir()
	pm := NewPluginManager(targetRoot)

	url := "https://example.invalid/repo.git"
	got, err := InstallFromGit(pm, url)
	if err == nil {
		t.Fatalf("expected InstallFromGit to fail with fake git, got plugin %v", got)
	}
	if got != nil {
		t.Errorf("expected nil plugin on failure, got %v", got)
	}
	// The leftover plugin dir for our derived name should not exist
	name := pluginNameFromURL(url)
	expected := filepath.Join(targetRoot, name)
	if _, err := os.Stat(expected); err == nil {
		t.Errorf("expected leftover dir %s to be cleaned up, but it exists", expected)
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected stat error: %v", err)
	}
}

// TestInstallFromLocal_WarnsOnSuspiciousPath verifies isSuspiciousPluginPath
// recognises paths clearly outside the user's scope.
func TestInstallFromLocal_WarnsOnSuspiciousPath(t *testing.T) {
	// Build a path guaranteed to be outside home AND outside cwd
	home, err := os.UserHomeDir()
	if err == nil {
		home, _ = filepath.Abs(home)
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, _ = filepath.Abs(cwd)
	}
	// Use /tmp (typical unix tempdir) + a tag unlikely to exist
	suspicious := filepath.Join("/tmp", "late-test-suspicious-path-"+t.Name())

	if home != "" {
		if rel, err := filepath.Rel(home, suspicious); err == nil && rel != ".." && !strings.HasPrefix(rel, "..") {
			t.Skip("test environment layout makes /tmp inside home; skipping")
		}
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, suspicious); err == nil && rel != ".." && !strings.HasPrefix(rel, "..") {
			t.Skip("test environment layout makes /tmp inside cwd; skipping")
		}
	}

	if !isSuspiciousPluginPath(suspicious) {
		t.Errorf("expected %s to be flagged as suspicious", suspicious)
	}
}

// TestInstallFromLocal_AcceptsInScopePath verifies legitimate in-scope paths
// don't trigger the suspicious flag.
func TestInstallFromLocal_AcceptsInScopePath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	inScope := filepath.Join(cwd, "plugins-test")
	if isSuspiciousPluginPath(inScope) {
		t.Errorf("expected %s (under cwd) to NOT be suspicious", inScope)
	}
}

// ---------------------------------------------------------------------------
// RegisterPluginSkills (#27)
// ---------------------------------------------------------------------------

// TestRegisterPluginSkills_RejectsPathTraversal verifies that a plugin whose
// manifest declares a skill path that escapes the plugin directory is
// silently skipped (the symlink is not created).
func TestRegisterPluginSkills_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "evil-plugin")
	traversalTarget := filepath.Join(dir, "secret-skills")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(traversalTarget, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	plugin := &InstalledPlugin{
		Name:    "evil-plugin",
		Path:    pluginDir,
		Enabled: true,
		Late: &LateManifest{
			Skills: []string{
				filepath.Join("..", "secret-skills"), // escapes plugin dir
			},
		},
	}

	pm := NewPluginManager(dir)
	pm.Add(plugin)

	skillsDir := t.TempDir()
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("RegisterPluginSkills failed: %v", err)
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("ReadDir skills: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected no symlinks created for path-traversal skill, got: %v", names)
	}
}

// TestRegisterPluginSkills_AllowsInDirSkill verifies normal in-plugin-dir
// skills still create symlinks.
func TestRegisterPluginSkills_AllowsInDirSkill(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "good-plugin")
	skillDir := filepath.Join(pluginDir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Minimal SKILL.md so skill.LoadSkill succeeds
	skillMD := "---\nname: my-skill\ndescription: test\n---\n# Test"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	plugin := &InstalledPlugin{
		Name:    "good-plugin",
		Path:    pluginDir,
		Enabled: true,
		Late: &LateManifest{
			Skills: []string{"skills"},
		},
	}

	pm := NewPluginManager(dir)
	pm.Add(plugin)

	skillsDir := t.TempDir()
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("RegisterPluginSkills failed: %v", err)
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("ReadDir skills: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 symlink for in-dir skill, got %d", len(entries))
	}
	if entries[0].Name() != "good-plugin:my-skill" {
		t.Errorf("unexpected symlink name: %s", entries[0].Name())
	}
}

// ---------------------------------------------------------------------------
// Manifest name traversal (#17)
// ---------------------------------------------------------------------------

// TestLoadPlugin_RejectsTraversalName verifies a manifest whose name would
// escape the plugin store (.. components, empty components, backslashes) is
// rejected at load time, so no installer/remover ever joins it into a path.
func TestLoadPlugin_RejectsTraversalName(t *testing.T) {
	for _, name := range []string{"..", "../evil", "a/../../evil", ".", "./evil", "a//b", "@scope/", `a\..\evil`} {
		dir := t.TempDir()
		pkg := `{"name":"` + name + `","version":"1.0.0","late":{}}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadPlugin(dir); err == nil {
			t.Errorf("LoadPlugin accepted traversal name %q", name)
		}
	}
}

// TestLoadPlugin_AcceptsScopedName verifies legitimate npm-scoped names
// still load.
func TestLoadPlugin_AcceptsScopedName(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"@scope/my-plugin","version":"1.0.0","late":{}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin rejected scoped name: %v", err)
	}
	if p.Name != "@scope/my-plugin" {
		t.Errorf("unexpected name: %s", p.Name)
	}
}

// TestInstallFromLocal_RejectsTraversalName verifies InstallFromLocal fails
// before creating anything outside the plugin store when the manifest name
// escapes it.
func TestInstallFromLocal_RejectsTraversalName(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	pkg := `{"name":"../../escaped","version":"1.0.0","late":{}}`
	if err := os.WriteFile(filepath.Join(srcDir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pm := NewPluginManager(storeDir)
	if _, err := InstallFromLocal(pm, srcDir); err == nil {
		t.Fatal("InstallFromLocal accepted a traversal manifest name")
	}

	// The escape target (store parent + "escaped") must never be created.
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Errorf("install created symlink outside the plugin store: %v", err)
	}
}

// TestRemovePlugin_RejectsTraversalName verifies removal can't target paths
// outside the plugin store: a traversal name is never in the registry (only
// validated manifest names are), so RemovePlugin fails before any delete.
func TestRemovePlugin_RejectsTraversalName(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(victim, 0755); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	marker := filepath.Join(victim, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	pm := NewPluginManager(storeDir)
	if _, err := RemovePlugin(pm, "../victim"); err == nil {
		t.Fatal("RemovePlugin accepted a traversal name")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("victim directory was modified: %v", err)
	}
}

// TestDiscover_SkipsTraversalMetaName verifies a planted .late-plugin.json
// with a traversal name is rejected by Discover instead of registered, so
// it can never be removed through the registry.
func TestDiscover_SkipsTraversalMetaName(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	evilDir := filepath.Join(storeDir, "evil")
	if err := os.MkdirAll(evilDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	meta := InstalledPlugin{Name: "../victim", Path: evilDir, Late: &LateManifest{}}
	data, err := json.Marshal(&meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(evilDir, ".late-plugin.json"), data, 0644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	pm := NewPluginManager(storeDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if p := pm.Plugin("../victim"); p != nil {
		t.Error("traversal-named plugin was registered")
	}
	if _, err := RemovePlugin(pm, "../victim"); err == nil {
		t.Error("RemovePlugin removed a traversal-named plugin")
	}
}

// ---------------------------------------------------------------------------
// SavePluginMeta mtime bump (#7 robustness)
// ---------------------------------------------------------------------------

// TestSavePluginMeta_BumpsMtime verifies SavePluginMeta writes touches the
// file's mtime (so the watcher's snapshot detects the change reliably).
func TestSavePluginMeta_BumpsMtime(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	plugin := &InstalledPlugin{
		Name:    "mtime-test",
		Path:    dir,
		Enabled: true,
		Late:    &LateManifest{},
	}

	// First write
	before := time.Now().Add(-1 * time.Hour) // deliberately past
	metaPath := filepath.Join(dir, ".late-plugin.json")
	if err := os.Chtimes(metaPath, before, before); err == nil {
		// Pre-create the file so SavePluginMeta overwrites it
		_ = os.Chtimes(metaPath, before, before)
	}

	if err := SavePluginMeta(plugin); err != nil {
		t.Fatalf("SavePluginMeta: %v", err)
	}
	info, err := os.Stat(metaPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().After(before) {
		t.Errorf("expected mtime > %v after SavePluginMeta, got %v", before, info.ModTime())
	}
}

// TestSavePluginMeta_PersistsEnabledField verifies the JSON has the enabled field
// so callers can read it back.
func TestSavePluginMeta_PersistsEnabledField(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	plugin := &InstalledPlugin{
		Name:    "persist-test",
		Path:    dir,
		Enabled: false,
		Late:    &LateManifest{},
	}
	if err := SavePluginMeta(plugin); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".late-plugin.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got InstalledPlugin
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Enabled {
		t.Error("expected enabled=false to be persisted")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mkPlugin(t *testing.T, dir, name string) {
	t.Helper()
	pdir := filepath.Join(dir, name)
	if err := os.MkdirAll(pdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// `"late": {}` is required so LoadPlugin accepts this directory as a
	// valid native-Late plugin. Without it, every discover/install test in
	// this package fails with `no recognized plugin format in <dir>`.
	pkg := `{"name":"` + name + `","version":"1.0.0","late":{}}`
	if err := os.WriteFile(filepath.Join(pdir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func writeLateMeta(t *testing.T, pluginDir string, enabled bool) {
	t.Helper()
	meta := InstalledPlugin{
		Name:    filepath.Base(pluginDir),
		Path:    pluginDir,
		Enabled: enabled,
		Late:    &LateManifest{},
	}
	if err := SavePluginMeta(&meta); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func keys(m map[string]pluginSnapshotEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
