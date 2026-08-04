package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ThemeInfo describes a single parsed theme file exposed by a plugin.
// Each theme is identified by a namespaced ID of the form
// "<plugin-name>:<theme-name>".
type ThemeInfo struct {
	ID         string            // "<pluginname>:<themename>"
	PluginName string            // owning plugin name
	ThemeName  string            // bare theme name (as declared in the JSON's "name" field)
	Palette    map[string]string // semantic palette: bg/fg/accent/etc.
	Glamour    map[string]any    // glamour-style JSON overrides (merged into base theme)
	SourcePath string            // absolute path to the loaded JSON file
}

// ThemeFile is the on-disk schema for a plugin theme file. Only `name` is
// required; `palette` and `glamour` are optional overrides. Unknown fields
// are tolerated for forward compatibility.
type ThemeFile struct {
	Name    string            `json:"name"`
	Palette map[string]string `json:"palette,omitempty"`
	Glamour map[string]any    `json:"glamour,omitempty"`
}

// resolveThemePath performs the same kind of containment check we use for
// skills/hooks: the absolute path must live inside the plugin's directory.
func resolveThemePath(pluginDir, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("empty theme path")
	}
	// Absolute paths are rejected outright: filepath.Join would silently
	// flatten them ("/etc/passwd" becomes pluginDir/etc/passwd), masking a
	// manifest that is not a plugin-relative path.
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("theme path %q is absolute; only plugin-relative paths are allowed", relPath)
	}
	abs := filepath.Clean(filepath.Join(pluginDir, relPath))
	rel, err := filepath.Rel(pluginDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("theme path %q escapes plugin directory", relPath)
	}
	return abs, nil
}

// loadThemeFile reads & parses a single theme JSON. Errors are returned
// rather than logged so callers can decide whether to surface them.
func loadThemeFile(path string) (*ThemeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read theme: %w", err)
	}
	var f ThemeFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	// Unknown-field strictness is in tension with forward compat, so we
	// first try strict; if it fails, retry with strict off.
	if err := dec.Decode(&f); err != nil {
		var loose ThemeFile
		if err2 := json.Unmarshal(data, &loose); err2 != nil {
			return nil, fmt.Errorf("parse theme: %w (and %v)", err, err2)
		}
		f = loose
	}
	if f.Name == "" {
		return nil, fmt.Errorf("theme at %s is missing required \"name\" field", path)
	}
	return &f, nil
}

// AllThemes enumerates every theme file declared by every enabled plugin,
// parsing each and returning the resulting ThemeInfo list. Invalid or
// unparseable theme files are skipped with a warning.
func (pm *PluginManager) AllThemes() []ThemeInfo {
	pm.mu.RLock()
	plugins := make([]*InstalledPlugin, 0, len(pm.plugins))
	for _, p := range pm.plugins {
		plugins = append(plugins, p)
	}
	pm.mu.RUnlock()

	var themes []ThemeInfo
	for _, p := range plugins {
		if !p.Enabled || p.Late == nil || len(p.Late.Themes) == 0 {
			continue
		}
		for _, rel := range p.Late.Themes {
			tp, err := resolveThemePath(p.Path, rel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[themes] %s: %v\n", p.Name, err)
				continue
			}
			f, err := loadThemeFile(tp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[themes] %s: %v\n", p.Name, err)
				continue
			}
			themes = append(themes, ThemeInfo{
				ID:         p.Name + ":" + f.Name,
				PluginName: p.Name,
				ThemeName:  f.Name,
				Palette:    f.Palette,
				Glamour:    f.Glamour,
				SourcePath: tp,
			})
		}
	}
	return themes
}

// GetTheme looks up a theme by its namespaced ID ("plugin:theme") or by
// bare name (matched against any enabled plugin). For empty id, returns
// (nil, nil) meaning "no theme override; use the base".
func (pm *PluginManager) GetTheme(id string) (*ThemeInfo, error) {
	if id == "" {
		return nil, nil
	}
	if strings.Contains(id, ":") {
		parts := strings.SplitN(id, ":", 2)
		return pm.findTheme(parts[0], parts[1])
	}
	// Bare name lookup across all enabled plugins.
	pm.mu.RLock()
	plugins := make([]*InstalledPlugin, 0, len(pm.plugins))
	for _, p := range pm.plugins {
		plugins = append(plugins, p)
	}
	pm.mu.RUnlock()

	var lastErr error
	for _, p := range plugins {
		if !p.Enabled || p.Late == nil {
			continue
		}
		info, err := pm.findTheme(p.Name, id)
		if err == nil && info != nil {
			return info, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		return nil, fmt.Errorf("theme %q not found", id)
	}
	return nil, lastErr
}

// findTheme walks a single plugin's declared theme files and returns the
// first matching entry by bare theme name.
func (pm *PluginManager) findTheme(pluginName, themeName string) (*ThemeInfo, error) {
	pm.mu.RLock()
	p := pm.plugins[pluginName]
	pm.mu.RUnlock()

	if p == nil || !p.Enabled || p.Late == nil {
		return nil, fmt.Errorf("plugin %q not found or disabled", pluginName)
	}
	for _, rel := range p.Late.Themes {
		tp, err := resolveThemePath(p.Path, rel)
		if err != nil {
			continue
		}
		f, err := loadThemeFile(tp)
		if err != nil {
			continue
		}
		if f.Name == themeName {
			return &ThemeInfo{
				ID:         p.Name + ":" + f.Name,
				PluginName: p.Name,
				ThemeName:  f.Name,
				Palette:    f.Palette,
				Glamour:    f.Glamour,
				SourcePath: tp,
			}, nil
		}
	}
	return nil, fmt.Errorf("theme %q not declared by plugin %q", themeName, pluginName)
}
