package plugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runCommand is the package-level seam used by Update to spawn npm and
// git. Tests override it to assert the exact argv shape without invoking
// a real package manager; production never touches it.
var runCommand = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// runCommandOutput is the same seam but captures stdout/stderr; used for
// commands whose output Update needs to inspect (e.g. version probe).
var runCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// InstallFromNpm installs a plugin from npm by running 'npm install'.
// If project is true and a project dir is configured, installs into the project-local dir.
func InstallFromNpm(pm *PluginManager, pkgName string, projectLocal ...bool) (*InstalledPlugin, error) {
	project := len(projectLocal) > 0 && projectLocal[0]
	targetDir := pm.TargetDir(project)

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugins directory: %w", err)
	}

	out, err := runCommandOutput(context.Background(), "npm", "install", "--prefix", targetDir, pkgName)
	if err != nil {
		return nil, fmt.Errorf("npm install failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// npm installs into node_modules/<pkgname>
	npmDir := filepath.Join(targetDir, "node_modules", pkgName)
	if _, err := os.Stat(npmDir); os.IsNotExist(err) {
		// Try scoped package path: @scope/name -> node_modules/@scope/name
		parts := strings.SplitN(pkgName, "/", 2)
		if len(parts) == 2 && strings.HasPrefix(pkgName, "@") {
			npmDir = filepath.Join(targetDir, "node_modules", parts[0], parts[1])
		}
		if _, err := os.Stat(npmDir); os.IsNotExist(err) {
			return nil, fmt.Errorf("npm installed but package not found at expected path %s", npmDir)
		}
	}

	// Create parent directories for scoped packages (e.g. @scope/name)
	linkDir := filepath.Join(targetDir, pkgName)
	if err := os.MkdirAll(filepath.Dir(linkDir), 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directories for symlink: %w", err)
	}

	if _, err := os.Lstat(linkDir); err == nil {
		os.Remove(linkDir)
	}

	// Use relative symlink for portability.
	// The symlink resolves relative to its OWN parent directory, not the
	// plugins root — for scoped packages (@scope/name) the link is nested
	// one level deeper, so compute from linkDir's parent.
	rel, err := filepath.Rel(filepath.Dir(linkDir), npmDir)
	if err != nil {
		return nil, fmt.Errorf("failed to compute relative path: %w", err)
	}
	if err := os.Symlink(rel, linkDir); err != nil {
		return nil, fmt.Errorf("failed to create symlink: %w", err)
	}

	// Load the plugin from the symlinked directory
	plugin, err := LoadPlugin(linkDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load installed plugin: %w", err)
	}
	plugin.SourceType = "npm"
	plugin.Source = pkgName

	if err := SavePluginMeta(plugin); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to save plugin metadata: %v\n", err)
	}

	pm.Add(plugin)
	return plugin, nil
}

// InstallFromGit installs a plugin from a Git repository.
// Supports URLs like https://github.com/user/repo.git and shorthand like github:user/repo.
// If project is true and a project dir is configured, installs into the project-local dir.
func InstallFromGit(pm *PluginManager, url string, projectLocal ...bool) (*InstalledPlugin, error) {
	project := len(projectLocal) > 0 && projectLocal[0]
	destDir := pm.TargetDir(project)

	// Determine plugin name from URL
	name := pluginNameFromURL(url)
	targetDir := filepath.Join(destDir, name)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugins directory: %w", err)
	}

	if _, err := os.Stat(targetDir); err == nil {
		return nil, fmt.Errorf("plugin %s already exists at %s", name, targetDir)
	}

	// Expand shorthand: github:user/repo -> https://github.com/user/repo.git
	gitURL := expandGitURL(url)

	cmd := exec.Command("git", "clone", "--depth", "1", gitURL, targetDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Clean up partial clone so the user's next attempt isn't blocked
		// by an "already exists" error against a half-populated directory.
		if rmErr := os.RemoveAll(targetDir); rmErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to remove partial clone dir %s: %v\n", targetDir, rmErr)
		}
		return nil, fmt.Errorf("git clone failed: %w", err)
	}

	// Remove .git to keep the store clean
	os.RemoveAll(filepath.Join(targetDir, ".git"))

	plugin, err := LoadPlugin(targetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load installed plugin: %w", err)
	}
	plugin.SourceType = "git"
	plugin.Source = url

	if err := SavePluginMeta(plugin); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to save plugin metadata: %v\n", err)
	}

	pm.Add(plugin)
	return plugin, nil
}

// InstallFromLocal installs a plugin from a local path by symlinking it
// into the plugins directory. This is equivalent to `late plugin link`.
// If project is true and a project dir is configured, installs into the project-local dir.
func InstallFromLocal(pm *PluginManager, localPath string, projectLocal ...bool) (*InstalledPlugin, error) {
	project := len(projectLocal) > 0 && projectLocal[0]
	destDir := pm.TargetDir(project)

	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("local path does not exist: %s", absPath)
	}

	// Soft sanity check: warn when the requested local plugin lives far
	// outside the user's normal scope (home or current directory). We don't
	// hard-reject because legitimate use cases exist (e.g. /opt/dev-plugins),
	// but the warning helps spot typos and accidental malicious-looking links.
	if isSuspiciousPluginPath(absPath) {
		fmt.Fprintf(os.Stderr, "Warning: linking plugin from path outside $HOME or CWD: %s\n", absPath)
	}

	plugin, err := LoadPlugin(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load plugin from %s: %w", absPath, err)
	}

	// Create a symlink in the plugins directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugins directory: %w", err)
	}

	targetDir := filepath.Join(destDir, plugin.Name)
	if err := os.MkdirAll(filepath.Dir(targetDir), 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directories for symlink: %w", err)
	}

	if _, err := os.Lstat(targetDir); err == nil {
		os.Remove(targetDir)
	}

	if err := os.Symlink(absPath, targetDir); err != nil {
		return nil, fmt.Errorf("failed to create symlink: %w", err)
	}

	plugin.Path = targetDir
	plugin.SourceType = "local"
	plugin.Source = absPath

	if err := SavePluginMeta(plugin); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to save plugin metadata: %v\n", err)
	}

	pm.Add(plugin)
	return plugin, nil
}

// Install is the unified entry point for the `late plugin install` CLI.
// It inspects the source string and dispatches to InstallFromGit /
// InstallFromLocal / InstallFromNpm / marketplace-resolved target based
// on the shape (URL? relative path? absolute path? scoped name?).
//
// classifier rules (in order):
//   1. looks like a git URL (https?://, git@, github:, gitlab:, bitbucket:) → InstallFromGit
//   2. looks like a local filesystem path (./, ../, ~/, /) → InstallFromLocal
//   3. looks like an npm package name (always contains a "/" like @scope/name) → InstallFromNpm
//   4. bare name → MarketplaceClient.Resolve. On hit: dispatch to the resolved target;
//      on miss (ErrMarketplaceMiss OR network error): fall back to InstallFromNpm as a
//      "treat-as-npm-package" path so users can `late plugin install some-pkg` without
//      requiring registry support.
//
// projectLocal mirrors the `--project`/`--local` flag and forwards to every
// underlying installer unchanged. mc may be nil; Install then uses DefaultRegistry().
func Install(pm *PluginManager, source string, mc *MarketplaceClient, projectLocal bool) (*InstalledPlugin, error) {
	if source == "" {
		return nil, fmt.Errorf("install: empty source")
	}
	if looksLikeGitSource(source) {
		return InstallFromGit(pm, source, projectLocal)
	}
	if looksLikeLocalPath(source) {
		return InstallFromLocal(pm, source, projectLocal)
	}
	if looksLikeNpmPackage(source) {
		return InstallFromNpm(pm, source, projectLocal)
	}
	// Bare name: marketplace first, then npm-as-fallback.
	if mc == nil {
		mc2 := DefaultRegistry()
		mc = &mc2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	entry, err := mc.Resolve(ctx, source)
	if err == nil {
		switch entry.SourceType() {
		case "npm":
			return InstallFromNpm(pm, entry.Npm, projectLocal)
		case "git":
			return InstallFromGit(pm, entry.Git, projectLocal)
		}
	}
	// Miss or error → keep treating the bare name as an npm package name.
	// This is the OMP behavior: bare names that aren't in the registry just
	// fall through to npm. We log a notice so the user understands the path.
	_, _ = fmt.Fprintf(os.Stderr,
		"Notice: marketplace did not match %q (%v); trying npm\n", source, err)
	return InstallFromNpm(pm, source, projectLocal)
}

// looksLikeGitSource reports whether source is recognizable as a git URL
// or one of the supported shorthand hosts. We deliberately accept any host
// (github:, gitlab:, bitbucket:, or a full URL) but reject bare scopes or
// relative paths so the npm/local branches stay unambiguous.
func looksLikeGitSource(source string) bool {
	if strings.Contains(source, "://") || strings.HasPrefix(source, "git@") {
		return true
	}
	for _, host := range []string{"github:", "gitlab:", "bitbucket:"} {
		if strings.HasPrefix(source, host) {
			return true
		}
	}
	return false
}

// looksLikeLocalPath reports whether source is a filesystem path.
// Recognized shapes: "./...", "../...", "~/...", or absolute ("/...").
func looksLikeLocalPath(source string) bool {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") || strings.HasPrefix(source, "~/") {
		return true
	}
	return strings.HasPrefix(source, "/")
}

// looksLikeNpmPackage reports whether source is shaped like an npm
// package name. Modern npm naming always requires a slash (scoped or
// hierarchical), so this is a safe heuristic. Bare names are intentionally
// NOT classified as npm here — they go through the marketplace branch.
func looksLikeNpmPackage(source string) bool {
	if !strings.Contains(source, "/") {
		return false
	}
	// Scoped shortnames must start with '@' and have a slash after the scope.
	if strings.HasPrefix(source, "@") {
		parts := strings.SplitN(source, "/", 2)
		return len(parts) == 2 && parts[0] != "" && parts[1] != ""
	}
	return true
}

// updateGitTempSuffix is appended to the existing plugin dir name when
// Update clones the new source for an atomic swap. The dot prefix hides
// it from the PollingWatcher's directory-listing (it will be renamed in
// the same Write-Lock window — race is bounded by the mutex).
const updateGitTempSuffix = ".late-update-"

// Update re-installs the named plugin in place using the Source originally
// captured at install time. SourceType drives the dispatch:
//   - "git"  → fresh shallow clone into a sibling tmp dir, atomic rename
//   - "npm"  → `npm install --prefix <dir> <Source>@latest --no-save --quiet` in place
//   - "marketplace" → look Source up via mc.Resolve, then dispatch
//   - "local" or empty Source → error (dev symlinks and legacy records can't be auto-updated)
//
// Concurrency: we hold RLock only long enough to fetch the InstalledPlugin
// snapshot, then run all docker/exec work lock-free. The atomic swap
// (Rename/symlink recreate) happens under pm.mu.Lock() so the watcher
// (which holds the lock in Discover) sees a stable view.
func Update(pm *PluginManager, name string, mc *MarketplaceClient) (*InstalledPlugin, error) {
	if pm == nil {
		return nil, fmt.Errorf("update: nil plugin manager")
	}
	if name == "" {
		return nil, fmt.Errorf("update: empty plugin name")
	}

	// Snapshot phase — short RLock, no I/O.
	pm.mu.RLock()
	old := pm.plugins[name]
	pm.mu.RUnlock()
	if old == nil {
		return nil, fmt.Errorf("update: plugin %s is not installed", name)
	}
	source := old.Source
	sourceType := old.SourceType

	if sourceType == "local" {
		return nil, fmt.Errorf("update: %s is a local dev symlink; edit the source folder then run `late plugin remove && late plugin link`", name)
	}
	if source == "" {
		return nil, fmt.Errorf("update: no install source recorded for %s; reinstall explicitly with `late plugin install <src>`", name)
	}

	// Pre-resolve marketplace source if needed (lock-free).
	if sourceType == "marketplace" {
		if mc == nil {
			mc2 := DefaultRegistry()
			mc = &mc2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		entry, err := mc.Resolve(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("update: marketplace lookup for %s failed: %w", name, err)
		}
		// Reflect the resolved target into the procedure below by
		// recursing once via a synthetic SourceType swap. We don't
		// mutate the live InstalledPlugin until the swap window.
		switch entry.SourceType() {
		case "npm":
			source = entry.Npm
			sourceType = "npm"
		case "git":
			source = entry.Git
			sourceType = "git"
		default:
			return nil, fmt.Errorf("update: marketplace entry for %s has no installable target", name)
		}
	}

	// Determine the target directory the new artifacts should land in.
	// Project-local plugins live under pm.ProjectDir(); everything else
	// goes under pm.PluginsDir(). Use the existing plugin's parent as the
	// source of truth so we never mis-route an update — except for scoped
	// npm/local installs (old.Path = plugins/@scope/name), where the
	// immediate parent is the scope directory, not the plugins root; walk
	// up one more level in that case (mirrors the scoped-path handling
	// updateNpm already does for its own symlink computation below).
	targetDir := filepath.Dir(old.Path)
	if strings.HasPrefix(filepath.Base(targetDir), "@") {
		targetDir = filepath.Dir(targetDir)
	}

	switch sourceType {
	case "git":
		return updateGit(pm, old, source, targetDir)
	case "npm":
		return updateNpm(pm, old, source, targetDir)
	default:
		return nil, fmt.Errorf("update: unsupported source type %q for %s", sourceType, name)
	}
}

// updateGit clones `source` into a unique sibling tmpdir, strips its
// .git subdir, then atomically swaps it over the existing plugin dir.
func updateGit(pm *PluginManager, old *InstalledPlugin, source, targetDir string) (*InstalledPlugin, error) {
	tmp, err := os.MkdirTemp(targetDir, "."+filepath.Base(old.Path)+updateGitTempSuffix)
	if err != nil {
		return nil, fmt.Errorf("update: cannot create temp clone dir: %w", err)
	}
	defer func() {
		// Best-effort cleanup if we never made it to the swap window.
		if _, statErr := os.Stat(tmp); statErr == nil {
			_ = os.RemoveAll(tmp)
		}
	}()

	gitURL := expandGitURL(source)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runCommand(ctx, "git", "clone", "--depth", "1", gitURL, tmp); err != nil {
		return nil, fmt.Errorf("update: git clone %s failed: %w", gitURL, err)
	}
	// Match the fresh-install contract: keep the store clean.
	_ = os.RemoveAll(filepath.Join(tmp, ".git"))

	// Swap window. Hold the write lock so a concurrent Discover() call
	// from the PollingWatcher can't race us mid-rename.
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, statErr := os.Stat(old.Path); statErr != nil {
		return nil, fmt.Errorf("update: original plugin dir vanished mid-update: %w", statErr)
	}

	// Validate the freshly cloned replacement BEFORE touching the working
	// copy: a broken update (bad clone, invalid manifest) must never
	// destroy the working installation.
	if _, err := LoadPlugin(tmp); err != nil {
		return nil, fmt.Errorf("update: replacement clone at %s is not a valid plugin (%v); keeping the current install", tmp, err)
	}

	// Swap with rollback: move the working copy aside, move the clone into
	// place, and restore the working copy if any step fails.
	backup := tmp + ".backup"
	if err := os.Rename(old.Path, backup); err != nil {
		return nil, fmt.Errorf("update: cannot move old plugin dir aside: %w", err)
	}
	if err := os.Rename(tmp, old.Path); err != nil {
		_ = os.Rename(backup, old.Path)
		return nil, fmt.Errorf("update: cannot move new dir into place: %w", err)
	}

	loaded, err := LoadPlugin(old.Path)
	if err != nil {
		// The replacement is broken in place — roll back to the working copy.
		_ = os.RemoveAll(old.Path)
		_ = os.Rename(backup, old.Path)
		return nil, fmt.Errorf("update: replacement plugin is invalid after swap; rolled back: %w", err)
	}
	_ = os.RemoveAll(backup)

	loaded.Source = old.Source
	loaded.SourceType = old.SourceType
	if !old.Enabled {
		loaded.Enabled = false
	}
	if err := SavePluginMeta(loaded); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to save updated plugin metadata: %v\n", err)
	}
	pm.plugins[loaded.Name] = loaded
	return loaded, nil
}

// updateNpm runs `npm install --prefix <dir> <pkg>@latest --no-save --quiet`
// in the existing target directory, then re-creates the symlink the
// installer would have created on first install. Output is silenced on
// success and surfaced in the error on failure so user noise stays low.
func updateNpm(pm *PluginManager, old *InstalledPlugin, source, targetDir string) (*InstalledPlugin, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := runCommandOutput(ctx, "npm", "install",
		"--prefix", targetDir, source+"@latest", "--no-save", "--quiet")
	if err != nil {
		return nil, fmt.Errorf("update: npm install %s@latest failed: %s: %w",
			source, strings.TrimSpace(string(out)), err)
	}

	// Re-resolve the actual node_modules path (covers both bare names and
	// scoped names like @scope/name). Re-create the symlink without
	// trusting the previous version.
	npmDir := filepath.Join(targetDir, "node_modules", source)
	if _, err := os.Stat(npmDir); os.IsNotExist(err) {
		parts := strings.SplitN(source, "/", 2)
		if len(parts) == 2 && strings.HasPrefix(source, "@") {
			npmDir = filepath.Join(targetDir, "node_modules", parts[0], parts[1])
		}
		if _, err := os.Stat(npmDir); os.IsNotExist(err) {
			return nil, fmt.Errorf("update: npm installed but package not found at expected path %s", npmDir)
		}
	}
	linkDir := filepath.Join(targetDir, source)
	if err := os.MkdirAll(filepath.Dir(linkDir), 0755); err != nil {
		return nil, fmt.Errorf("update: cannot prepare symlink parent: %w", err)
	}
	// Use relative symlink for portability. The symlink resolves relative
	// to its OWN parent directory, not the plugins root — for scoped
	// packages (@scope/name) the link is nested one level deeper, so
	// compute from linkDir's parent (mirrors InstallFromNpm).
	rel, err := filepath.Rel(filepath.Dir(linkDir), npmDir)
	if err != nil {
		return nil, fmt.Errorf("update: cannot compute rel symlink: %w", err)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, lerr := os.Lstat(linkDir); lerr == nil {
		_ = os.Remove(linkDir)
	}
	if err := os.Symlink(rel, linkDir); err != nil {
		return nil, fmt.Errorf("update: cannot recreate symlink: %w", err)
	}
	loaded, err := LoadPlugin(linkDir)
	if err != nil {
		return nil, fmt.Errorf("update: cannot load refreshed plugin: %w", err)
	}
	loaded.Source = old.Source
	loaded.SourceType = old.SourceType
	if !old.Enabled {
		loaded.Enabled = false
	}
	if err := SavePluginMeta(loaded); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to save updated plugin metadata: %v\n", err)
	}
	pm.plugins[loaded.Name] = loaded
	return loaded, nil
}

// UpdateAll iterates every installed plugin and calls Update() on each.
// One plugin's failure does not stop the loop. Returns the slice of
// successfully updated plugins (in install order) plus the last error
// encountered, if any. A plugin whose SourceType is "local" or whose
// Source is empty is skipped silently and omitted from the returned
// slice — those are expected to need manual intervention, not auto-update.
func UpdateAll(pm *PluginManager, mc *MarketplaceClient) ([]*InstalledPlugin, error) {
	if pm == nil {
		return nil, fmt.Errorf("update-all: nil plugin manager")
	}
	all := pm.All()
	updated := make([]*InstalledPlugin, 0, len(all))
	var lastErr error
	for _, p := range all {
		if p == nil || p.SourceType == "local" || p.Source == "" {
			continue
		}
		fresh, err := Update(pm, p.Name, mc)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "update: %s: %v\n", p.Name, err)
			lastErr = err
			continue
		}
		updated = append(updated, fresh)
	}
	return updated, lastErr
}

// RemovePlugin removes a plugin from the global or project-local store and the manager registry.
func RemovePlugin(pm *PluginManager, name string, projectLocal ...bool) (*InstalledPlugin, error) {
	plugin := pm.Plugin(name)
	if plugin == nil {
		return nil, fmt.Errorf("plugin %s is not installed", name)
	}

	project := len(projectLocal) > 0 && projectLocal[0]
	destDir := pm.TargetDir(project)

	if err := removeFromDir(destDir, name); err != nil {
		return plugin, err
	}

	// Git-installed plugins live under a directory named after the
	// repository, which may differ from the manifest name. Remove the
	// plugin's actual on-disk location too so removal succeeds even when
	// the two names differ (the name-based remove above is a no-op then).
	if plugin.Path != "" {
		if abs, err := filepath.Abs(plugin.Path); err == nil {
			if rel, err := filepath.Rel(destDir, abs); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "..") {
				if err := os.RemoveAll(abs); err != nil {
					return plugin, fmt.Errorf("failed to remove plugin directory %s: %w", abs, err)
				}
			}
		}
	}

	pm.Remove(name)

	// Drop any stale disabled-state override so a future `late plugin
	// link` reinstalling the same name doesn't inherit a removed plugin's
	// disabled state (see SavePluginMeta / applyLocalDisabledOverride).
	if plugin.SourceType == "local" {
		if err := setDisabledOverride(name, false); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear plugin state override for %s: %v\n", name, err)
		}
	}

	return plugin, nil
}

// removeFromDir removes a plugin directory (or symlink) from a specific directory.
func removeFromDir(dir, name string) error {
	targetDir := filepath.Join(dir, name)
	if _, err := os.Lstat(targetDir); err == nil {
		// Check if it's a symlink
		info, err := os.Lstat(targetDir)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			// Just remove the symlink
			if err := os.Remove(targetDir); err != nil {
				return fmt.Errorf("failed to remove symlink: %w", err)
			}
		} else {
			// Remove the whole directory
			if err := os.RemoveAll(targetDir); err != nil {
				return fmt.Errorf("failed to remove plugin directory: %w", err)
			}
		}
	}

	// Also remove from node_modules if it was npm-installed
	npmPath := filepath.Join(dir, "node_modules", name)
	if _, err := os.Stat(npmPath); err == nil {
		os.RemoveAll(npmPath)
	}

	// Remove scoped plugin artifacts and prune any @scope parent
	// directory that becomes empty.
	//
	// The parent dir can live in two places depending on the install
	// path the user used:
	//   - <dir>/node_modules/@scope/<pkg>   (npm install)
	//   - <dir>/@scope/<pkg>                (`late plugin link` install)
	// Before this fix, `link`-installed scoped plugins left an empty
	// `<dir>/@scope/` orphan after `late plugin remove`. Now we check
	// both locations and clean up whichever is empty.
	if strings.HasPrefix(name, "@") {
		parts := strings.SplitN(name, "/", 2)
		if len(parts) == 2 {
			scope := parts[0]
			// npm install artifact (no-op for link installs).
			os.RemoveAll(filepath.Join(dir, "node_modules", scope, parts[1]))

			// Best-effort removal of any @scope parent that has been
			// emptied. ENOENT is fine — the parent simply didn't exist
			// for this install path. We do NOT remove non-empty scope
			// parents so siblings under the same scope stay installed.
			for _, scopeDir := range []string{
				filepath.Join(dir, "node_modules", scope),
				filepath.Join(dir, scope),
			} {
				if entries, err := os.ReadDir(scopeDir); err == nil && len(entries) == 0 {
					_ = os.Remove(scopeDir)
				}
			}
		}
	}

	return nil
}

// Link creates a development symlink from the plugins directory to a local path.
// If project is true and a project dir is configured, links into the project-local dir.
func Link(pm *PluginManager, localPath string, projectLocal ...bool) (*InstalledPlugin, error) {
	return InstallFromLocal(pm, localPath, projectLocal...)
}

// pluginNameFromURL extracts a plugin name from a Git URL.
func pluginNameFromURL(url string) string {
	// Remove trailing .git
	url = strings.TrimSuffix(url, ".git")

	// Extract the last path component
	parts := strings.Split(url, "/")
	name := parts[len(parts)-1]

	// Handle github:user/repo shorthand
	if strings.Contains(url, ":") && !strings.Contains(url, "://") {
		shorthandParts := strings.Split(url, ":")
		pathParts := strings.Split(shorthandParts[len(shorthandParts)-1], "/")
		if len(pathParts) >= 2 {
			name = pathParts[len(pathParts)-1]
		}
	}

	return name
}

// isSuspiciousPluginPath returns true if the given absolute path is not
// contained within the user's home directory or the process's current
// working directory. Used as a soft warning in `InstallFromLocal` to catch
// obvious typos or suspect link targets — the caller decides how to act.
func isSuspiciousPluginPath(absPath string) bool {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, absPath); err == nil && !strings.HasPrefix(rel, "..") && rel != ".." {
			return false
		}
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		if rel, err := filepath.Rel(cwd, absPath); err == nil && !strings.HasPrefix(rel, "..") && rel != ".." {
			return false
		}
	}
	return true
}

// expandGitURL converts shorthand Git references to proper URLs.
func expandGitURL(url string) string {
	if strings.Contains(url, "://") {
		return url
	}

	// github:user/repo
	if strings.HasPrefix(url, "github:") {
		repo := strings.TrimPrefix(url, "github:")
		return "https://github.com/" + repo + ".git"
	}

	// gitlab:user/repo
	if strings.HasPrefix(url, "gitlab:") {
		repo := strings.TrimPrefix(url, "gitlab:")
		return "https://gitlab.com/" + repo + ".git"
	}

	// bitbucket:user/repo
	if strings.HasPrefix(url, "bitbucket:") {
		repo := strings.TrimPrefix(url, "bitbucket:")
		return "https://bitbucket.org/" + repo + ".git"
	}

	// Assume it's already a valid URL
	return url
}
