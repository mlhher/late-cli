package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// defaultPollInterval is how often the watcher scans the plugins directory for changes.
const defaultPollInterval = 2 * time.Second

// PollingWatcher monitors the plugins directory(ies) for changes by polling the filesystem.
// It requires no external dependencies and works across all platforms.
type PollingWatcher struct {
	manager      *PluginManager
	interval     time.Duration
	lastSnapshot map[string]pluginSnapshotEntry
	// projectDirs is an additional list of directories to watch
	projectDirs []string
	// projectDirsSnapshot is a per-poll copy of projectDirs taken while
	// holding manager.mu so takeSnapshot doesn't race with SetProjectDir().
	projectDirsSnapshot []string
}

// AddWatchDir adds an additional directory to watch for changes.
func (w *PollingWatcher) AddWatchDir(dir string) {
	if dir == "" {
		return
	}
	for _, d := range w.projectDirs {
		if d == dir {
			return // already watching
		}
	}
	w.projectDirs = append(w.projectDirs, dir)
}

// pluginSnapshotEntry captures the state of a plugin directory at a point in time.
type pluginSnapshotEntry struct {
	exists      bool
	enabled     bool
	modTime     time.Time
	lateFileMod time.Time // mtime of .late-plugin.json (zero if absent)
	hasLateFile bool      // whether a .late-plugin.json or package.json exists
}

// NewPollingWatcher creates a PollingWatcher for the given PluginManager.
func NewPollingWatcher(pm *PluginManager) *PollingWatcher {
	return &PollingWatcher{
		manager:  pm,
		interval: defaultPollInterval,
	}
}

// SetInterval overrides the default poll interval. Must be called before Start.
func (w *PollingWatcher) SetInterval(d time.Duration) {
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	w.interval = d
}

// Start begins polling all configured directories. It blocks until ctx is cancelled.
// The onChanged callback is invoked whenever a change is detected.
// Start is designed to run in a goroutine:
//
//	go watcher.Start(ctx, func() {
//	    p.Send(tui.PluginChangeMsg{})
//	})
func (w *PollingWatcher) Start(ctx context.Context, onChanged func()) {
	if onChanged == nil {
		return
	}

	// Take initial snapshot across all directories
	w.lastSnapshot = w.takeSnapshot()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := w.takeSnapshot()
			if w.snapshotChanged(w.lastSnapshot, current) {
				// Re-discover plugins from all directories
				if err := w.manager.Discover(); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin watcher failed to re-discover: %v\n", err)
					continue
				}
				w.lastSnapshot = current
				onChanged()
			}
		}
	}
}

// takeSnapshot walks all configured plugins directories and records the state
// of each subdirectory. Directories are deduplicated before scanning so that
// callers passing the same path to both SetProjectDir() and AddWatchDir()
// don't trigger double snapshots.
//
// The directory enumeration is performed under pm.mu.RLock so that any
// concurrent SetProjectDir() call (which takes pm.mu.Lock) cannot race with
// our read of pm.pluginsDir/pm.projectDir.
func (w *PollingWatcher) takeSnapshot() map[string]pluginSnapshotEntry {
	snapshot := make(map[string]pluginSnapshotEntry)

	// Snapshot both directory paths under the manager lock so we read a
	// consistent view even if SetProjectDir() runs concurrently.
	w.manager.mu.RLock()
	dirs := make([]string, 0, 2+len(w.projectDirs))
	if d := w.manager.pluginsDir; d != "" {
		dirs = append(dirs, d)
	}
	if d := w.manager.projectDir; d != "" {
		dirs = append(dirs, d)
	}
	w.projectDirsSnapshot = append(w.projectDirsSnapshot[:0], w.projectDirs...)
	w.manager.mu.RUnlock()

	// Deduplicate — SetProjectDir() and AddWatchDir() may produce the same path.
	// Copy-then-dedupe avoids mutating the snapshot taken under the lock.
	seen := make(map[string]bool, len(dirs)+len(w.projectDirsSnapshot))
	addDir := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	for _, dir := range w.projectDirsSnapshot {
		addDir(dir)
	}

	for _, dir := range dirs {
		w.snapshotDir(dir, snapshot)
	}

	return snapshot
}

// snapshotDir records the state of a single plugins directory into the given snapshot map.
func (w *PollingWatcher) snapshotDir(dir string, snapshot map[string]pluginSnapshotEntry) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin watcher failed to read dir %s: %v\n", dir, err)
		}
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == "node_modules" || entry.Name() == ".cache" {
			continue
		}

		dirPath := filepath.Join(dir, entry.Name())
		info, err := os.Stat(dirPath)
		if err != nil {
			continue
		}

		// Check if this directory has a plugin manifest and track the
		// .late-plugin.json mtime so we always detect enable/disable changes
		// even on filesystems where directory mtime does not update when a
		// file within it is rewritten.
		lateFile := filepath.Join(dirPath, ".late-plugin.json")
		lateFileMod := time.Time{}
		hasLateFile := false
		if fi, err := os.Stat(lateFile); err == nil {
			hasLateFile = true
			lateFileMod = fi.ModTime()
		} else {
			if _, err := os.Stat(filepath.Join(dirPath, "package.json")); err == nil {
				hasLateFile = true
			}
		}

		// Check if plugin is enabled by reading only the enabled field from .late-plugin.json
		enabled := true
		if hasLateFile {
			if data, err := os.ReadFile(lateFile); err == nil {
				var meta struct {
					Enabled bool `json:"enabled"`
				}
				if err := json.Unmarshal(data, &meta); err == nil {
					enabled = meta.Enabled
				}
			}
		}

		snapshot[entry.Name()] = pluginSnapshotEntry{
			exists:      true,
			enabled:     enabled,
			modTime:     info.ModTime(),
			lateFileMod: lateFileMod,
			hasLateFile: hasLateFile,
		}
	}
}

// snapshotChanged compares two snapshots and returns true if they differ.
func (w *PollingWatcher) snapshotChanged(old, new map[string]pluginSnapshotEntry) bool {
	// Check for removed or changed plugins
	for name, oldEntry := range old {
		newEntry, exists := new[name]
		if !exists {
			return true // plugin was removed
		}
		if oldEntry.enabled != newEntry.enabled {
			return true // enabled/disabled status changed
		}
		if oldEntry.hasLateFile != newEntry.hasLateFile {
			return true // manifest was added or removed
		}
		if !newEntry.modTime.Equal(oldEntry.modTime) {
			return true // directory mod time changed (plugin added/removed inside)
		}
		if !newEntry.lateFileMod.Equal(oldEntry.lateFileMod) {
			return true // .late-plugin.json touched (enable/disable, settings)
		}
	}

	// Check for newly added plugins
	for name := range new {
		if _, exists := old[name]; !exists {
			return true // new plugin added
		}
	}

	return false
}
