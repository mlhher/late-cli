package plugin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSnapshotDir_FollowsSymlinks is a regression test: InstallFromNpm and
// InstallFromLocal always install a plugin as a symlink into the plugins
// dir (only InstallFromGit produces a real directory), but os.ReadDir's
// entry.IsDir() reports false for a symlink even when it points at a
// directory. snapshotDir must resolve entries (os.Stat, which follows
// symlinks) rather than gating on entry.IsDir(), or npm/local plugin
// installs, removals, and enable/disable toggles are never detected.
func TestSnapshotDir_FollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	realDir := filepath.Join(root, "real-target", "my-plugin")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, ".late-plugin.json"), []byte(`{"enabled":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(pluginsDir, "my-plugin")); err != nil {
		t.Fatal(err)
	}

	w := &PollingWatcher{}
	snapshot := make(map[string]pluginSnapshotEntry)
	w.snapshotDir(pluginsDir, snapshot)

	entry, ok := snapshot["my-plugin"]
	if !ok {
		t.Fatalf("expected symlinked plugin dir to be snapshotted, got %v", snapshot)
	}
	if !entry.hasLateFile || !entry.enabled {
		t.Errorf("expected hasLateFile=true enabled=true, got %+v", entry)
	}
}

// TestSnapshotChanged_NoChange verifies that identical snapshots do not trigger a change.
func TestSnapshotChanged_NoChange(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()
	snapshot := map[string]pluginSnapshotEntry{
		"plugin-a": {
			exists:      true,
			enabled:     true,
			modTime:     now,
			hasLateFile: true,
		},
		"plugin-b": {
			exists:      true,
			enabled:     false,
			modTime:     now.Add(-time.Hour),
			hasLateFile: true,
		},
	}

	if w.snapshotChanged(snapshot, snapshot) {
		t.Error("expected false for identical snapshots")
	}
}

// TestSnapshotChanged_PluginAdded detects a newly added plugin directory.
func TestSnapshotChanged_PluginAdded(t *testing.T) {
	w := &PollingWatcher{}
	old := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: time.Now(), hasLateFile: true},
	}
	new := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: time.Now(), hasLateFile: true},
		"plugin-b": {exists: true, enabled: true, modTime: time.Now(), hasLateFile: true},
	}

	if !w.snapshotChanged(old, new) {
		t.Error("expected true when a plugin is added")
	}
}

// TestSnapshotChanged_PluginRemoved detects a removed plugin directory.
func TestSnapshotChanged_PluginRemoved(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()
	old := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
		"plugin-b": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}
	new := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}

	if !w.snapshotChanged(old, new) {
		t.Error("expected true when a plugin is removed")
	}
}

// TestSnapshotChanged_EnabledChanged detects when a plugin is enabled or disabled.
func TestSnapshotChanged_EnabledChanged(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()
	old := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}
	new := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: false, modTime: now, hasLateFile: true},
	}

	if !w.snapshotChanged(old, new) {
		t.Error("expected true when enabled status changes")
	}

	// Reverse: enabled -> true
	new["plugin-a"] = pluginSnapshotEntry{exists: true, enabled: true, modTime: now, hasLateFile: true}
	old["plugin-a"] = pluginSnapshotEntry{exists: true, enabled: false, modTime: now, hasLateFile: true}
	if !w.snapshotChanged(old, new) {
		t.Error("expected true when enabled status changes back")
	}
}

// TestSnapshotChanged_ModTimeChanged detects when a plugin directory's mod time changes.
func TestSnapshotChanged_ModTimeChanged(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()
	old := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now.Add(-time.Hour), hasLateFile: true},
	}
	new := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}

	if !w.snapshotChanged(old, new) {
		t.Error("expected true when mod time changes")
	}
}

// TestSnapshotChanged_HasLateFileChanged detects when a manifest appears or disappears.
func TestSnapshotChanged_HasLateFileChanged(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()
	old := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now, hasLateFile: false},
	}
	new := map[string]pluginSnapshotEntry{
		"plugin-a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}

	if !w.snapshotChanged(old, new) {
		t.Error("expected true when hasLateFile changes")
	}

	// Reverse: remove manifest
	new["plugin-a"] = pluginSnapshotEntry{exists: true, enabled: true, modTime: now, hasLateFile: false}
	old["plugin-a"] = pluginSnapshotEntry{exists: true, enabled: true, modTime: now, hasLateFile: true}
	if !w.snapshotChanged(old, new) {
		t.Error("expected true when hasLateFile disappears")
	}
}

// TestSnapshotChanged_EmptyMaps verifies that comparing empty snapshots returns false.
func TestSnapshotChanged_EmptyMaps(t *testing.T) {
	w := &PollingWatcher{}
	if w.snapshotChanged(nil, nil) {
		t.Error("expected false for nil nil")
	}
	if w.snapshotChanged(map[string]pluginSnapshotEntry{}, map[string]pluginSnapshotEntry{}) {
		t.Error("expected false for empty maps")
	}
}

// TestSnapshotChanged_AllFieldsSameDetectsNoChange verifies that multiple plugins
// with identical fields do not trigger a change.
func TestSnapshotChanged_AllFieldsSameDetectsNoChange(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()
	snapshot := map[string]pluginSnapshotEntry{
		"alpha": {exists: true, enabled: true, modTime: now, hasLateFile: true},
		"beta":  {exists: true, enabled: false, modTime: now.Add(-2 * time.Hour), hasLateFile: true},
		"gamma": {exists: true, enabled: true, modTime: now.Add(-30 * time.Minute), hasLateFile: false},
	}

	if w.snapshotChanged(snapshot, snapshot) {
		t.Error("expected false for identical multi-plugin snapshots")
	}
}

// TestNewPollingWatcher verifies the constructor sets up defaults correctly.
func TestNewPollingWatcher(t *testing.T) {
	pm := NewPluginManager("/tmp/test-plugins")
	w := NewPollingWatcher(pm)

	if w.manager != pm {
		t.Error("manager should match the provided PluginManager")
	}
	if w.interval != defaultPollInterval {
		t.Errorf("expected interval %v, got %v", defaultPollInterval, w.interval)
	}
	if w.lastSnapshot != nil {
		t.Error("lastSnapshot should be nil initially")
	}
}

// TestSetInterval verifies that SetInterval clamps to the minimum value.
func TestSetInterval(t *testing.T) {
	w := &PollingWatcher{interval: defaultPollInterval}

	// Below minimum — should clamp
	w.SetInterval(10 * time.Millisecond)
	if w.interval != 100*time.Millisecond {
		t.Errorf("expected clamped interval 100ms, got %v", w.interval)
	}

	// Valid value — should pass through
	w.SetInterval(5 * time.Second)
	if w.interval != 5*time.Second {
		t.Errorf("expected 5s, got %v", w.interval)
	}

	// Exactly minimum — should pass through
	w.SetInterval(100 * time.Millisecond)
	if w.interval != 100*time.Millisecond {
		t.Errorf("expected 100ms, got %v", w.interval)
	}
}

// TestSnapshotChanged_ReversedOrder verifies that order doesn't matter.
func TestSnapshotChanged_ReversedOrder(t *testing.T) {
	w := &PollingWatcher{}
	now := time.Now()

	// Same plugin data, different map construction order
	old := map[string]pluginSnapshotEntry{
		"a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
		"b": {exists: true, enabled: false, modTime: now, hasLateFile: false},
	}
	new := map[string]pluginSnapshotEntry{
		"b": {exists: true, enabled: false, modTime: now, hasLateFile: false},
		"a": {exists: true, enabled: true, modTime: now, hasLateFile: true},
	}

	if w.snapshotChanged(old, new) {
		t.Error("expected false — same data regardless of order")
	}
}
