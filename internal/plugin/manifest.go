package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LateManifest represents the "late" field inside a plugin's package.json.
type LateManifest struct {
	Skills   []string            `json:"skills,omitempty"`   // relative paths to skill directories
	MCP      *LateMCPManifest    `json:"mcp,omitempty"`      // MCP server definitions
	Commands LateCommands        `json:"commands,omitempty"` // slash command names (see LateCommands for back-compat)
	Themes   []string            `json:"themes,omitempty"`   // relative paths to theme JSON files
	Hooks    *LateHooksManifest  `json:"hooks,omitempty"`    // hook script definitions
	Tools    []LateToolManifest  `json:"tools,omitempty"`    // inline agent-callable tools (no MCP needed)
}

// OmpManifest represents the "omp" field inside an omp plugin's package.json.
// Late translates these into its own manifest format at load time.
type OmpManifest struct {
	Skills     []string            `json:"skills,omitempty"`
	Extensions []string            `json:"extensions,omitempty"`
	Commands   []string            `json:"commands,omitempty"`
	MCP        *LateMCPManifest    `json:"mcp,omitempty"`
	Hooks      *LateHooksManifest  `json:"hooks,omitempty"`
}

// LateCommands is a backward-compatible adapter for the "commands" field.
// Plugins written before command handlers existed declare commands as a
// flat array of strings; plugins written after can declare objects with
// a per-command "handler" script path. This type accepts both shapes:
//
//	"commands": ["/weather", "/git"]
//
//	"commands": [{"name": "/weather", "handler": "scripts/weather.sh"}]
type LateCommands []LateCommandManifest

// UnmarshalJSON accepts commands arrays in three shapes, including the
// heterogeneous case documented in `docs/plugin-example.md` where one
// entry is a bare string ("legacy" slash command — dispatcher falls
// back to plain-prompt) and another carries an explicit Handler
// script:
//
//	"commands": ["/format", "/lint"]                           // strings only
//	"commands": [{"name":"/lint","handler":"x.sh"}]            // objects only
//	"commands": ["/format", {"name":"/lint","handler":"x.sh"}]  // mixed (preserved)
//
// Each element is dispatched individually: a string becomes a manifest
// with Handler=="" (legacy fall-through); an object becomes a manifest
// with whatever fields it carries. Anything that is neither is reported
// verbatim so authors notice malformed entries.
//
// Heterogeneous arrays are common in real plugins (a plugin author
// wants a `/help`-style bare alias for one command and a scripted
// handler for another), so handling them here removes a long-standing
// loader footgun.
func (lc *LateCommands) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(LateCommands, 0, len(raws))
	for _, raw := range raws {
		// Try string form first.
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			out = append(out, LateCommandManifest{Name: s})
			continue
		}
		// Then object form.
		var obj LateCommandManifest
		if err := json.Unmarshal(raw, &obj); err == nil {
			out = append(out, obj)
			continue
		}
		return fmt.Errorf("commands entry must be a string or {name,handler?}, got %s", string(raw))
	}
	*lc = out
	return nil
}

// MarshalJSON encodes the late commands back to a string array when no
// command has a handler, so round-tripping through DefaultManifest stays
// readable. Otherwise emits the object form so handlers survive.
func (lc LateCommands) MarshalJSON() ([]byte, error) {
	hasHandler := false
	for _, c := range lc {
		if c.Handler != "" {
			hasHandler = true
			break
		}
	}
	if !hasHandler {
		names := make([]string, len(lc))
		for i, c := range lc {
			names[i] = c.Name
		}
		return json.Marshal(names)
	}
	return json.Marshal([]LateCommandManifest(lc))
}

// LateCommandManifest describes a single plugin slash command. The Name
// is required; Handler is optional. When Handler is set, the TUI runs
// the script with the trailing args (JSON-encoded) on stdin and shows
// the stdout as the chat response. When Handler is empty, the command
// falls back to the legacy "dispatch as a plain prompt" behavior.
type LateCommandManifest struct {
	Name    string `json:"name"`              // slash command name, with or without leading "/"
	Handler string `json:"handler,omitempty"` // optional relative path to a handler script
}

// LateToolManifest declares a single agent-callable tool inline within
// the manifest, removing the need for an MCP server wrapper. Scripts
// receive the tool arguments JSON on stdin and must return the result
// on stdout.
type LateToolManifest struct {
	Name        string          `json:"name"`                  // tool name, will be namespaced as "<plugin>:<name>"
	Description string          `json:"description"`           // shown to the model in the tool list
	Script      string          `json:"script"`                // relative path to the executable script
	Parameters  json.RawMessage `json:"parameters"`            // JSON Schema fragment describing arguments
}

// LateMCPManifest holds MCP server definitions declared by a plugin.
type LateMCPManifest struct {
	Servers map[string]MCPServerConfig `json:"servers"`
}

// MCPServerConfig mirrors the MCP server config structure from mcp_config.json.
type MCPServerConfig struct {
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	URL           string            `json:"url,omitempty"`
	TransportType string            `json:"transportType,omitempty"`
	Disabled      bool              `json:"disabled,omitempty"`
	// Dir is populated by the plugin loader (not serialized) and is used
	// to set cmd.Dir for the stdio transport so any plugin-relative
	// paths in Args resolve against the plugin's directory.
	Dir string `json:"-"`
}

// LateHooksManifest defines hook scripts a plugin provides.
//
// Hook contract:
//   - onToolCall receives the ToolCall as JSON on stdin. The hook may:
//     1. Return JSON (any valid JSON object/string) to mutate the call's
//        "arguments" field before next() runs (Gate via mutate).
//     2. Return exactly the string "blocked" to veto the tool execution.
//        The next() in the chain is skipped and late returns an error
//        result to the agent.
//     3. Return empty / non-JSON to pass through unchanged.
//   - onToolResult receives {"tool": "...", "result": "..."} via stdin.
//     Non-empty JSON stdout replaces the result the LLM sees; "blocked"
//     vetoes it; empty/non-JSON passes through. Runs after every tool
//     execution via BuildToolResultMiddlewares, so it IS integrated (not
//     observation-only).
//   - onSessionStart fires once when Late starts. It receives an empty
//     JSON object on stdin. Errors and stderr are forwarded to the user's
//     TUI.
//   - onMessageSend forms a sequential transform pipeline; each hook sees
//     the previous hook's stdout. Smoke (no stdout) is treated as a no-op
//     so a hook can be a no-op for some inputs.
type LateHooksManifest struct {
	OnToolCall     []string `json:"onToolCall,omitempty"`     // relative paths to scripts
	OnToolResult   []string `json:"onToolResult,omitempty"`   // relative paths to scripts
	OnSessionStart []string `json:"onSessionStart,omitempty"` // relative paths to scripts
	OnMessageSend  []string `json:"onMessageSend,omitempty"`  // relative paths to scripts
}

// PackageJSON represents the minimal package.json fields we care about.
type PackageJSON struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description,omitempty"`
	Late        *LateManifest `json:"late,omitempty"`
	Omp         *OmpManifest  `json:"omp,omitempty"`
}

// InstalledPlugin represents an installed plugin with its manifest and metadata.
type InstalledPlugin struct {
	Name        string       `json:"name"`        // plugin name (from package.json)
	Version     string       `json:"version"`     // plugin version
	Description string       `json:"description,omitempty"`
	Path        string       `json:"path"`        // absolute path to the plugin directory
	SourceType  string       `json:"source_type"` // "npm", "git", "local", "marketplace"
	Source      string       `json:"source,omitempty"` // original install string passed by the user (pkg, URL, path, or marketplace name); empty for symlinked local plugins
	Enabled     bool         `json:"enabled"`
	Late        *LateManifest `json:"late"`        // the late extension manifest
}

// Source holds the resolved absolute paths for each surface after registration.
type SurfaceSources struct {
	Skills    []string // resolved absolute paths to skill dirs
	MCPServers map[string]MCPServerConfig
	Themes    []string // resolved absolute paths to theme JSON files
	Commands  []string
}

// ResolveSurfaces resolves relative paths from the manifest into absolute paths
// rooted at the plugin's directory. Returns a SurfaceSources struct.
func (p *InstalledPlugin) ResolveSurfaces() *SurfaceSources {
	src := &SurfaceSources{
		MCPServers: make(map[string]MCPServerConfig),
	}

	if p.Late == nil {
		return src
	}

	for _, rel := range p.Late.Skills {
		abs := filepath.Join(p.Path, rel)
		abs = filepath.Clean(abs)
		src.Skills = append(src.Skills, abs)
	}

	if p.Late.MCP != nil {
		for name, srv := range p.Late.MCP.Servers {
			// Prefix server name with plugin name to avoid collisions
			namespaced := p.Name + ":" + name
			srv.Args = resolveArgs(p.Path, srv.Args)
			srv.Dir = p.Path
			src.MCPServers[namespaced] = srv
		}
	}

	for _, rel := range p.Late.Themes {
		abs := filepath.Join(p.Path, rel)
		abs = filepath.Clean(abs)
		src.Themes = append(src.Themes, abs)
	}

	src.Commands = make([]string, 0, len(p.Late.Commands))
	for _, c := range p.Late.Commands {
		if c.Name != "" {
			src.Commands = append(src.Commands, c.Name)
		}
	}

	return src
}

// resolveArgs resolves relative paths in args to absolute paths rooted at pluginDir.
//
// The intent is that a plugin author can write either:
//   - "args": ["./scripts/server.sh"]    (explicit ./ — resolved against pluginDir)
//   - "args": ["../shared/server.sh"]    (explicit ../ — resolved against pluginDir)
//   - "args": ["/abs/path/to/server.sh"] (absolute, pass-through)
//   - "args": ["node", "src/index.js"]   (literal args, pass-through)
//
// Only args with an explicit relative prefix (`./` or `../`) are
// resolved. Bare names are NOT rewritten — even when a same-named
// file exists under pluginDir — because doing so silently changes
// argv semantics for the spawned process and surprises authors who
// pass positional args with cwd-relative intent. The transport
// already sets `cmd.Dir` to pluginDir, so plugin authors can rely on
// the kernel's classic "argv is what you wrote" behaviour when they
// want cwd-relative scripts.
func resolveArgs(pluginDir string, args []string) []string {
	resolved := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.HasPrefix(arg, "/") {
			resolved[i] = arg
			continue
		}
		if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
			resolved[i] = filepath.Join(pluginDir, arg)
			continue
		}
		resolved[i] = arg
	}
	return resolved
}

// LoadPlugin loads a plugin from the specified directory. It recognizes
// two plugin formats in order of precedence:
//
//  1. package.json with "late" field (native Late format)
//  2. package.json with "omp" field  (Oh My Pi / omp format) — translated at load time
//
// For format 2, the manifest is translated into a LateManifest so the rest
// of the system (skill registration, MCP, commands, hooks) works identically.
func LoadPlugin(dir string) (*InstalledPlugin, error) {
	plugin, err := tryLoadNativeLate(dir)
	if err == nil {
		return plugin, nil
	}

	plugin, err = tryLoadOmp(dir)
	if err == nil {
		return plugin, nil
	}

	return nil, fmt.Errorf("no recognized plugin format in %s: %w", dir, err)
}

// validatePluginName rejects manifest names that would escape the plugin
// store when joined into a filesystem path (install/remove/update all use
// the name as a path component). Plain ("my-plugin") and npm-scoped
// ("@scope/my-plugin") names are accepted; anything with an empty, "." or
// ".." path component is rejected, as is any name containing a backslash
// (a path separator on Windows).
func validatePluginName(name string) error {
	if name == "" {
		return fmt.Errorf("plugin name is empty")
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("plugin name %q contains a path separator", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("plugin name %q contains unsafe path component %q", name, part)
		}
	}
	return nil
}

// tryLoadNativeLate loads a plugin from a package.json with a "late" field.
func tryLoadNativeLate(dir string) (*InstalledPlugin, error) {
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", pkgPath, err)
	}

	var pkg PackageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", pkgPath, err)
	}

	if err := validatePluginName(pkg.Name); err != nil {
		return nil, fmt.Errorf("plugin at %s has invalid name: %w", dir, err)
	}

	if pkg.Late == nil {
		return nil, fmt.Errorf("no 'late' field in %s", pkgPath)
	}

	return buildPlugin(dir, pkg.Name, pkg.Version, pkg.Description, pkg.Late), nil
}

// tryLoadOmp loads a plugin from a package.json with an "omp" field.
func tryLoadOmp(dir string) (*InstalledPlugin, error) {
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", pkgPath, err)
	}

	var pkg PackageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", pkgPath, err)
	}

	if err := validatePluginName(pkg.Name); err != nil {
		return nil, fmt.Errorf("plugin at %s has invalid name: %w", dir, err)
	}

	if pkg.Omp == nil {
		return nil, fmt.Errorf("no 'omp' field in %s", pkgPath)
	}

	late := translateOmpToLate(pkg.Omp)
	return buildPlugin(dir, pkg.Name, pkg.Version, pkg.Description, late), nil
}

// translateOmpToLate converts an omp manifest to the internal LateManifest.
func translateOmpToLate(omp *OmpManifest) *LateManifest {
	late := &LateManifest{
		Skills:   omp.Skills,
		Commands: make(LateCommands, 0, len(omp.Commands)),
		MCP:      omp.MCP,
		Hooks:    omp.Hooks,
	}

	for _, cmd := range omp.Commands {
		late.Commands = append(late.Commands, LateCommandManifest{Name: cmd})
	}
	// omp extensions are not directly surfaced in Late — they're TypeScript
	// entry points for the omp harness itself and don't map to Late surfaces.

	return late
}

// buildPlugin constructs an InstalledPlugin and detects its source type.
func buildPlugin(dir, name, version, description string, late *LateManifest) *InstalledPlugin {
	plugin := &InstalledPlugin{
		Name:        name,
		Version:     version,
		Description: description,
		Path:        dir,
		SourceType:  "unknown",
		Enabled:     true,
		Late:        late,
	}

	if plugin.Late == nil {
		plugin.Late = &LateManifest{}
	}

	// Detect source type from directory contents
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		plugin.SourceType = "git"
	} else if isSymlink(dir) {
		plugin.SourceType = "local"
	} else {
		plugin.SourceType = "npm"
	}

	return plugin
}

// isSymlink checks if a path is a symbolic link.
func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// SavePluginMeta persists a minimal metadata file for the plugin. After
// writing, force the file's mtime to "now" so the PollingWatcher's snapshot
// always detects the change even on filesystems that coalesce rapid writes.
//
// Local dev-symlink plugins skip the per-directory write below: the plugin
// directory is a symlink into the developer's source tree, and writing
// through it would pollute the source with Late's cached metadata (which
// then overrides later manifest edits). Their install provenance is
// re-derived on every load. The Enabled bit is the one thing that still
// needs to persist across restarts for a local plugin, so it's recorded
// instead in the global override file (see setDisabledOverride) —
// LoadPluginMeta applies it back on load.
func SavePluginMeta(plugin *InstalledPlugin) error {
	if plugin.SourceType == "local" {
		return setDisabledOverride(plugin.Name, !plugin.Enabled)
	}
	metaPath := filepath.Join(plugin.Path, ".late-plugin.json")
	data, err := json.MarshalIndent(plugin, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal plugin metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		return err
	}
	now := time.Now()
	_ = os.Chtimes(metaPath, now, now)
	return nil
}

// LoadPluginMeta loads the metadata from a plugin directory.
//
// Manifest-derived fields (name, version, description, surfaces) always
// come from the live package.json — the cached metadata file is a snapshot
// that must not override later manifest changes (this mattered for local
// devlinks, whose source dirs could previously accumulate a stale meta
// file through the symlink). The meta file only supplies what package.json
// cannot carry: install provenance (Source/SourceType) and the user's
// Enabled preference.
//
// Local plugins never have a .late-plugin.json (see SavePluginMeta), so
// they always take the fallback LoadPlugin(dir) path below and always come
// back Enabled: true from buildPlugin — applyLocalDisabledOverride patches
// that with whatever `late plugin disable` last recorded in the global
// override file, so the preference survives across restarts.
func LoadPluginMeta(dir string) (*InstalledPlugin, error) {
	metaPath := filepath.Join(dir, ".late-plugin.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			fresh, err := LoadPlugin(dir)
			if err != nil {
				return nil, err
			}
			applyLocalDisabledOverride(fresh)
			return fresh, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", metaPath, err)
	}

	var meta InstalledPlugin
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", metaPath, err)
	}

	// The meta file is user-editable; re-validate the name so a planted
	// traversal name can't reach remove/update path joins via the registry.
	if err := validatePluginName(meta.Name); err != nil {
		return nil, fmt.Errorf("plugin metadata at %s has invalid name: %w", metaPath, err)
	}

	// Fresh manifest wins for manifest-derived fields.
	fresh, err := LoadPlugin(dir)
	if err != nil {
		// package.json is unreadable but the meta snapshot exists — fall
		// back to the snapshot rather than dropping the plugin entirely.
		fresh = &meta
	} else {
		fresh.SourceType = meta.SourceType
		fresh.Source = meta.Source
		fresh.Enabled = meta.Enabled
		fresh.Path = dir
	}
	applyLocalDisabledOverride(fresh)

	return fresh, nil
}

// applyLocalDisabledOverride patches plugin.Enabled from the global plugin
// state file when plugin is a local dev-symlink (the only source type
// whose Enabled bit isn't already durably persisted elsewhere). A read
// failure is logged and otherwise ignored — Enabled just falls back to
// whatever LoadPlugin/the meta snapshot already set.
func applyLocalDisabledOverride(plugin *InstalledPlugin) {
	if plugin == nil || plugin.SourceType != "local" {
		return
	}
	overrides, err := loadDisabledOverrides()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load plugin state overrides: %v\n", err)
		return
	}
	if overrides[plugin.Name] {
		plugin.Enabled = false
	}
}
