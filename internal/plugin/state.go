package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"late/internal/pathutil"
)

// pluginStateFile is the on-disk schema for the global plugin state
// override at ~/.config/late/plugins.json. It exists specifically for
// local dev-symlink plugins (SourceType == "local"): SavePluginMeta
// refuses to write .late-plugin.json into a local plugin's directory
// (that directory is a symlink into the developer's source tree — writing
// through it would pollute the source with Late's cached metadata), so
// there was previously nowhere to persist `late plugin disable` for a
// local plugin. This file gives it one, keyed by plugin name so it works
// uniformly regardless of which local path the plugin happens to be
// linked from at any given time.
type pluginStateFile struct {
	Disabled []string `json:"disabled,omitempty"`
}

// pluginStatePath returns ~/.config/late/plugins.json.
func pluginStatePath() (string, error) {
	dir, err := pathutil.LateConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugins.json"), nil
}

// loadDisabledOverrides reads the disabled-name set from the global plugin
// state file. A missing file is not an error — it means no local plugin
// has ever been disabled.
func loadDisabledOverrides() (map[string]bool, error) {
	path, err := pluginStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var state pluginStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	out := make(map[string]bool, len(state.Disabled))
	for _, name := range state.Disabled {
		out[name] = true
	}
	return out, nil
}

// setDisabledOverride records (or clears) name's disabled state in the
// global plugin state file, leaving every other entry untouched.
func setDisabledOverride(name string, disabled bool) error {
	path, err := pluginStatePath()
	if err != nil {
		return err
	}
	overrides, err := loadDisabledOverrides()
	if err != nil {
		return err
	}
	if disabled {
		overrides[name] = true
	} else {
		delete(overrides, name)
	}

	names := make([]string, 0, len(overrides))
	for n := range overrides {
		names = append(names, n)
	}
	sort.Strings(names)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(pluginStateFile{Disabled: names}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal plugin state: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
