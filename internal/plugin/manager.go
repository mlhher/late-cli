package plugin

import (
	"fmt"
	"late/internal/skill"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// PluginManager manages the lifecycle of installed plugins.
type PluginManager struct {
	mu         sync.RWMutex
	pluginsDir string                     // absolute path to the global plugins store directory
	projectDir string                     // optional absolute path to project-local plugins dir (.late/plugins/)
	plugins    map[string]*InstalledPlugin
}

// NewPluginManager creates a new PluginManager for the given plugins directory.
// If projectDir is non-empty, it is also scanned during Discover and takes
// priority over global plugins with the same name.
func NewPluginManager(pluginsDir string) *PluginManager {
	return &PluginManager{
		pluginsDir: pluginsDir,
		plugins:    make(map[string]*InstalledPlugin),
	}
}

// SetProjectDir sets the project-local plugins directory.
// Must be called before Discover() for it to take effect.
func (pm *PluginManager) SetProjectDir(dir string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.projectDir = dir
}

// HasProjectDir returns true if a project-local plugins directory is configured.
func (pm *PluginManager) HasProjectDir() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.projectDir != ""
}

// ProjectDir returns the project-local plugins directory path, or empty string.
func (pm *PluginManager) ProjectDir() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.projectDir
}

// TargetDir returns the plugins directory appropriate for the target scope.
// If project is true and a project dir is configured, returns the project dir;
// otherwise returns the global dir.
func (pm *PluginManager) TargetDir(project bool) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if project && pm.projectDir != "" {
		return pm.projectDir
	}
	return pm.pluginsDir
}

// PluginsDir returns the absolute path to the plugins store.
func (pm *PluginManager) PluginsDir() string {
	return pm.pluginsDir
}

// Discover scans all configured plugins directories and loads installed plugins.
// It reconciles the in-memory map with what's on disk: plugins that were
// removed from disk are removed from memory.
// Project-local plugins override global plugins with the same name.
//
// Discover holds the write lock for its entire duration so that concurrent
// callers of Plugin / All / Count / BuildMCPConfigMap / PluginCommands do
// not panic on concurrent map access.
func (pm *PluginManager) Discover() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Start fresh: clear all plugins and rebuild from scratch
	pm.plugins = make(map[string]*InstalledPlugin)

	// Discover global plugins first
	if err := pm.discoverFromDir(pm.pluginsDir); err != nil {
		return err
	}

	// Discover project-local plugins (overrides global ones with same name)
	if pm.projectDir != "" {
		if err := pm.discoverFromDir(pm.projectDir); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to discover project plugins: %v\n", err)
			// Non-fatal — continue with global plugins only
		}
	}

	return nil
}

// discoverFromDir scans a single directory and loads all installed plugins from it.
func (pm *PluginManager) discoverFromDir(dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read plugins directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		// Accept real directories AND symlinks (which may point to dirs).
		// Go's os.ReadDir reports IsDir()=false for symlinks, so we
		// must check the type explicitly.
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := entry.Name()

		// Skip npm-managed directories: node_modules and .cache.
		if name == "node_modules" || name == ".cache" {
			continue
		}

		// @-prefixed directories are npm scoped-package parent dirs.
		// They aren't plugins themselves — recurse into them to find
		// the actual plugins (e.g. @scope/name).
		if strings.HasPrefix(name, "@") {
			subDir := filepath.Join(dir, name)
			if err := pm.discoverFromDir(subDir); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to scan scoped dir %s: %v\n", subDir, err)
			}
			continue
		}

		pluginDir := filepath.Join(dir, name)
		plugin, err := LoadPluginMeta(pluginDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to load plugin from %s: %v\n", pluginDir, err)
			continue
		}

		pm.plugins[plugin.Name] = plugin
	}

	return nil
}

// Plugin returns a loaded plugin by name.
func (pm *PluginManager) Plugin(name string) *InstalledPlugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.plugins[name]
}

// All returns all loaded plugins, sorted by name.
func (pm *PluginManager) All() []*InstalledPlugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.allLocked()
}

// allLocked returns all loaded plugins, sorted by name. The caller MUST
// already hold the manager lock (read or write). Exposed separately from
// All so locked callers can iterate without a second RLock — a nested
// RLock would deadlock against a queued writer (Go RWMutex blocks new
// readers while a writer waits).
func (pm *PluginManager) allLocked() []*InstalledPlugin {
	all := make([]*InstalledPlugin, 0, len(pm.plugins))
	for _, p := range pm.plugins {
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Name < all[j].Name
	})
	return all
}

// Add adds a plugin to the manager's in-memory registry (after installation).
func (pm *PluginManager) Add(plugin *InstalledPlugin) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.plugins[plugin.Name] = plugin
}

// Remove removes a plugin from the in-memory registry.
func (pm *PluginManager) Remove(name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.plugins, name)
}

// Count returns the number of loaded plugins.
func (pm *PluginManager) Count() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.plugins)
}

// PluginPath returns the expected path for a plugin with the given name
// in the global plugins directory.
func (pm *PluginManager) PluginPath(name string) string {
	return filepath.Join(pm.pluginsDir, name)
}

// PluginPathInDir returns the expected path for a plugin within a specific target dir.
func (pm *PluginManager) PluginPathInDir(targetDir, name string) string {
	return filepath.Join(targetDir, name)
}

// RegisterPluginSkills creates symlinks from the plugin's skill dirs into
// the Late skills directory so the existing SkillLoader can discover them.
// This should be called during bootstrap after all plugins are discovered.
// It also cleans up stale symlinks for removed or disabled plugins.
func (pm *PluginManager) RegisterPluginSkills(skillsDir string) error {
	if skillsDir == "" {
		var err error
		skillsDir, err = lateSkillsDir()
		if err != nil {
			return nil
		}
	}

	// The skills directory may not exist yet (e.g. the very first plugin
	// install); create it so the symlinks below have a home.
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}

	// Collect the set of valid skill symlink names to keep
	keep := make(map[string]bool)

	pm.mu.RLock()
	plugins := make([]*InstalledPlugin, 0, len(pm.plugins))
	for _, p := range pm.plugins {
		plugins = append(plugins, p)
	}
	pm.mu.RUnlock()

	for _, p := range plugins {
		if !p.Enabled || p.Late == nil {
			continue
		}

		surfaces := p.ResolveSurfaces()
		for _, skillPath := range surfaces.Skills {
			// Reject skill paths that escape the plugin's own directory to
			// prevent path traversal attacks via malicious manifests.
			relPath, relErr := filepath.Rel(p.Path, skillPath)
			if relErr != nil || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
				_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin %s declares skill at %s which escapes the plugin directory; skipping\n", p.Name, skillPath)
				continue
			}

			if _, err := os.Stat(skillPath); os.IsNotExist(err) {
				_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin %s declares skill at %s but directory does not exist\n", p.Name, skillPath)
				continue
			}

			// SKILL.md / subdirectory layout resolution:
			//
			// Plugins can declare either:
			//   <plugin>/skills/SKILL.md                         (single file at root)
			//   <plugin>/skills/<skillname>/SKILL.md             (one skill per subdir)
			// We support both. The single-root-file case names the skill
			// after the basename of the parent dir ("skills") so the
			// namespaced identifier stays stable across minor plugin
			// refactors.
			var loaded []*skill.Skill

			if _, err := os.Stat(filepath.Join(skillPath, "SKILL.md")); err == nil {
				// Single-file layout at the root.
				sk, loadErr := skill.LoadSkill(skillPath)
				if loadErr != nil {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin %s has invalid skill at %s: %v\n", p.Name, skillPath, loadErr)
					continue
				}
				loaded = append(loaded, sk)
			} else {
				// Subdir layout: enumerate one level of subdirectories,
				// each expected to contain its own SKILL.md. This matches
				// the documented Agent Skills convention and is
				// manifest-format-agnostic — native Late and omp plugins
				// both use it.
				entries, rerr := os.ReadDir(skillPath)
				if rerr != nil {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin %s skill dir %s unreadable: %v\n", p.Name, skillPath, rerr)
					continue
				}
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					sub := filepath.Join(skillPath, e.Name())
					if _, err := os.Stat(filepath.Join(sub, "SKILL.md")); err != nil {
						continue
					}
					sk, loadErr := skill.LoadSkill(sub)
					if loadErr != nil {
						_, _ = fmt.Fprintf(os.Stderr, "Warning: plugin %s has invalid skill %s: %v\n", p.Name, sub, loadErr)
						continue
					}
					loaded = append(loaded, sk)
				}
			}

			for _, sk := range loaded {
				// Namespace skill symlinks to prevent collisions between plugins
				namespacedName := p.Name + ":" + sk.Metadata.Name
				linkName := filepath.Join(skillsDir, namespacedName)

				// Scoped plugin names (e.g. "@scope/plugin:skill") produce
				// nested link paths; create the parent directories first.
				if err := os.MkdirAll(filepath.Dir(linkName), 0755); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to create parent dir for plugin skill %s: %v\n", namespacedName, err)
					continue
				}
				if _, err := os.Lstat(linkName); err == nil {
					os.Remove(linkName)
				}

				if err := os.Symlink(sk.Path, linkName); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to create symlink for plugin skill %s: %v\n", namespacedName, err)
					continue
				}
				keep[namespacedName] = true
			}
		}
	}

	// Clean up stale symlinks for removed or disabled plugins. Only
	// symlinks that Late itself created are ever removed — a symlink is
	// considered plugin-owned when its target lives inside one of the
	// plugin store directories. User-created symlinks pointing elsewhere
	// (e.g. a personal "my-skill" link into ~/notes) are left untouched.
	// The walk is recursive because scoped plugin names produce nested
	// link paths under skillsDir (e.g. skillsDir/@scope/plugin:skill).
	var prune func(dir string) error
	prune = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			fullPath := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := prune(fullPath); err != nil {
					return err
				}
				// Remove scoped-parent dirs (e.g. skillsDir/@scope) that the
				// plugin system created and that are now empty. Empty
				// @-prefixed dirs are Late's scoped-package convention; a
				// user's non-empty dirs are never touched.
				if strings.HasPrefix(entry.Name(), "@") {
					if entries, rerr := os.ReadDir(fullPath); rerr == nil && len(entries) == 0 {
						_ = os.Remove(fullPath)
					}
				}
				continue
			}
			info, err := os.Lstat(fullPath)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			rel, err := filepath.Rel(skillsDir, fullPath)
			if err != nil {
				continue
			}
			if keep[rel] {
				continue
			}
			if pm.isPluginOwnedSymlink(fullPath) {
				os.Remove(fullPath)
			}
		}
		return nil
	}
	if err := prune(skillsDir); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to prune stale plugin skill symlinks: %v\n", err)
	}

	return nil
}

// isPluginOwnedSymlink reports whether the symlink at linkPath was created
// by the plugin system: its target resolves inside one of the plugin store
// directories (pluginsDir or projectDir). Dangling targets of removed
// plugins still carry the store path prefix, so they are recognized too.
func (pm *PluginManager) isPluginOwnedSymlink(linkPath string) bool {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return false
	}
	for _, base := range []string{pm.pluginsDir, pm.projectDir} {
		if base == "" {
			continue
		}
		if target == base || strings.HasPrefix(target, base+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// BuildMCPConfigMap returns all enabled MCP server configurations
// declared by plugins, with namespaced server names (plugin:server).
// The caller should merge these into the existing MCP config and connect.
func (pm *PluginManager) BuildMCPConfigMap() map[string]MCPServerConfig {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make(map[string]MCPServerConfig)
	for _, p := range pm.plugins {
		if !p.Enabled || p.Late == nil || p.Late.MCP == nil {
			continue
		}
		surfaces := p.ResolveSurfaces()
		for name, srv := range surfaces.MCPServers {
			result[name] = srv
		}
	}
	return result
}

// PluginCommands returns all slash commands declared by enabled plugins.
// Names are normalized to start with "/".
func (pm *PluginManager) PluginCommands() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var cmds []string
	for _, p := range pm.plugins {
		if !p.Enabled || p.Late == nil {
			continue
		}
		for _, c := range p.Late.Commands {
			name := c.Name
			if name == "" {
				continue
			}
			if !strings.HasPrefix(name, "/") {
				name = "/" + name
			}
			cmds = append(cmds, name)
		}
	}
	sort.Strings(cmds)
	return cmds
}

// HasCommandHandler reports whether any enabled plugin declares a
// Handler script for the given slash command. The "/" prefix is
// tolerated.
//
// This is a pure, side-effect-free lookup: it must never spawn a
// subprocess or write to stderr. Callers (e.g. ToolRegistry filters,
// permission gates) invoke it on the hot path, so duplicating the
// scan here is intentional and cheaper than delegating to
// HandleCommand.
func (pm *PluginManager) HasCommandHandler(cmdName string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	normalized := strings.TrimPrefix(cmdName, "/")
	for _, p := range pm.plugins {
		if !p.Enabled || p.Late == nil {
			continue
		}
		for _, c := range p.Late.Commands {
			if c.Handler == "" {
				continue
			}
			if strings.TrimPrefix(c.Name, "/") == normalized {
				return true
			}
		}
	}
	return false
}

// lateSkillsDir returns the user-level skills directory.
func lateSkillsDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "late", "skills"), nil
}
