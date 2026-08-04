package plugin

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// HandlePluginCommand dispatches `late plugin <subcommand>` to the appropriate handler.
// Returns true if the command was handled (caller should exit), false if the caller
// should continue (e.g. the plugin manager needs to bootstrap first).
//
// Help handling: `-h`, `--help`, or `help` as the first arg prints
// top-level usage; the same tokens anywhere in a subcommand's args
// print the per-subcommand usage. This is critical because install's
// implementation falls through to npm (which prints its own docs when
// passed `--help`), and link's implementation tries to interpret
// `--help` as a filesystem path and dies.
func HandlePluginCommand(pm *PluginManager, args []string) bool {
	if len(args) == 0 {
		printPluginUsage()
		return true
	}
	if isHelpToken(args[0]) {
		printPluginUsage()
		return true
	}

	switch args[0] {
	case "list", "ls":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin list [--project]")
			return true
		}
		handlePluginList(pm)
		return true
	case "install", "i":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin install [--project] <source>")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Install a plugin by source. Source may be:")
			fmt.Fprintln(os.Stderr, "  - npm package:         @late/plugin-graph-rag, some-pkg")
			fmt.Fprintln(os.Stderr, "  - git url:            https://github.com/user/repo.git")
			fmt.Fprintln(os.Stderr, "  - shorthand git:      github:user/repo")
			fmt.Fprintln(os.Stderr, "  - local filesystem:   ./my-plugin, /abs/path, ~/path")
			fmt.Fprintln(os.Stderr, "  - bare name:          resolved via the marketplace; falls")
			fmt.Fprintln(os.Stderr, "                        through to npm on miss.")
			return true
		}
		handlePluginInstall(pm, args[1:])
		return true
	case "remove", "rm", "uninstall":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin remove [--project] <name>")
			return true
		}
		handlePluginRemove(pm, args[1:])
		return true
	case "link":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin link [--project] <path>")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Link a local directory as a plugin (dev mode). The path")
			fmt.Fprintln(os.Stderr, "must be an existing directory containing a native-Late")
			fmt.Fprintln(os.Stderr, "(`late`-keyed package.json) or a Claude Code")
			fmt.Fprintln(os.Stderr, "(.claude-plugin/plugin.json) plugin manifest.")
			return true
		}
		handlePluginLink(pm, args[1:])
		return true
	case "update":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin update [<name>]")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Without a name, updates every installed npm/git plugin")
			fmt.Fprintln(os.Stderr, "in place. With a name, updates that plugin only.")
			return true
		}
		handlePluginUpdate(pm, args[1:])
		return true
	case "enable":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin enable <name>")
			return true
		}
		handlePluginEnable(pm, args[1:], true)
		return true
	case "disable":
		if hasHelpFlag(args[1:]) {
			fmt.Fprintln(os.Stderr, "Usage: late plugin disable <name>")
			return true
		}
		handlePluginEnable(pm, args[1:], false)
		return true
	default:
		fmt.Fprintf(os.Stderr, "Unknown plugin command: %s\n\n", args[0])
		printPluginUsage()
		return true
	}
}

// isHelpToken reports whether s is a help-style flag (-h, --help, or
// the bare word "help", in any combination).
func isHelpToken(s string) bool {
	switch strings.ToLower(s) {
	case "-h", "--help", "help":
		return true
	}
	return false
}

// hasHelpFlag scans args for any help-style token and returns true on
// the first match. Used by every per-subcommand branch of
// HandlePluginCommand so that `late plugin install --help` (and any
// positional variant: `--help` last, `--help` mid-args) prints usage
// rather than being misinterpreted as a plugin source / path / name.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if isHelpToken(a) {
			return true
		}
	}
	return false
}

func printPluginUsage() {
	fmt.Fprintf(os.Stderr, `Usage: late plugin <command> [args...]

Commands:
  list, ls              List installed plugins
  install, i <src>      Install a plugin (npm package, git url, or local path)
  remove, rm <name>     Remove a plugin
  link <path>           Link a local directory as a plugin (dev mode)
  update [name]         Update plugins (all or specific)
  enable <name>         Enable a plugin
  disable <name>        Disable a plugin

Examples:
  late plugin install @late/plugin-graph-rag
  late plugin install https://github.com/user/late-plugin.git
  late plugin install github:user/late-plugin
  late plugin link ./my-plugin
  late plugin list
  late plugin remove @late/plugin-graph-rag
`)
}

// handlePluginList displays all installed plugins.
func handlePluginList(pm *PluginManager) {
	plugins := pm.All()
	if len(plugins) == 0 {
		fmt.Println("No plugins installed.")
		fmt.Println("Run 'late plugin install <source>' to install a plugin.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "Name\tVersion\tSource\tEnabled\tPath")
	fmt.Fprintln(w, "----\t-------\t------\t-------\t----")

	for _, p := range plugins {
		enabled := "✓"
		if !p.Enabled {
			enabled = "✗"
		}
		displayName := p.Name
		// Truncate long paths for display
		displayPath := p.Path
		if home, err := os.UserHomeDir(); err == nil {
			displayPath = strings.Replace(displayPath, home, "~", 1)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", displayName, p.Version, p.SourceType, enabled, displayPath)
	}
	w.Flush()
	fmt.Fprintf(os.Stderr, "\n%d plugin(s) installed. Use 'late plugin enable/disable <name>' to toggle.\n", len(plugins))
}

// handlePluginInstall installs a plugin from the given source.
// Supports --project flag to install into the project-local .late/plugins/ directory.
func handlePluginInstall(pm *PluginManager, args []string) {
	project, source := parseProjectFlag(args)
	if source == "" {
		fmt.Fprintln(os.Stderr, "Error: missing plugin source (npm package name, git URL, or local path)")
		if project && !pm.HasProjectDir() {
			fmt.Fprintln(os.Stderr, "Note: --project flag requires a .late/plugins/ directory (create it first)")
		}
		fmt.Fprintln(os.Stderr, "Usage: late plugin install [--project] <source>")
		return
	}

	// Single dispatcher: classifies URL/path/npm/layout and falls through
	// to marketplace → npm for unresolved bare names.
	plugin, err := Install(pm, source, nil, project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to install plugin: %v\n", err)
		return
	}

	scope := "global"
	if project && pm.HasProjectDir() {
		scope = "project"
	}
	fmt.Printf("Installed plugin: %s v%s (%s)\n", plugin.Name, plugin.Version, scope)
	if plugin.Description != "" {
		fmt.Printf("  %s\n", plugin.Description)
	}
	fmt.Printf("  Path: %s\n", plugin.Path)

	// List available surfaces
	if plugin.Late != nil {
		var surfaces []string
		if len(plugin.Late.Skills) > 0 {
			surfaces = append(surfaces, fmt.Sprintf("%d skill(s)", len(plugin.Late.Skills)))
		}
		if plugin.Late.MCP != nil && len(plugin.Late.MCP.Servers) > 0 {
			surfaces = append(surfaces, fmt.Sprintf("%d MCP server(s)", len(plugin.Late.MCP.Servers)))
		}
		if len(plugin.Late.Commands) > 0 {
			surfaces = append(surfaces, fmt.Sprintf("%d command(s)", len(plugin.Late.Commands)))
		}
		if len(plugin.Late.Themes) > 0 {
			surfaces = append(surfaces, fmt.Sprintf("%d theme(s)", len(plugin.Late.Themes)))
		}
		if plugin.Late.Hooks != nil {
			surfaces = append(surfaces, "hooks")
		}
		if len(surfaces) > 0 {
			fmt.Printf("  Surfaces: %s\n", strings.Join(surfaces, ", "))
		}
	}

	fmt.Println("\nPlugin activated. The filesystem watcher will pick it up within 2 seconds.")
}

// parseProjectFlag checks if --project flag is present in args and returns
// the flag state and remaining args (the source).
func parseProjectFlag(args []string) (project bool, rest string) {
	for i, a := range args {
		if a == "--project" || a == "--local" {
			// Return remaining args after removing the flag
			var remaining []string
			for j, r := range args {
				if j != i {
					remaining = append(remaining, r)
				}
			}
			if len(remaining) > 0 {
				return true, remaining[0]
			}
			return true, ""
		}
	}
	return false, args[0]
}

// handlePluginRemove removes a plugin.
func handlePluginRemove(pm *PluginManager, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: missing plugin name")
		fmt.Fprintln(os.Stderr, "Usage: late plugin remove [--project] <name>")
		return
	}

	project, name := parseProjectFlag(args)
	if name == "" {
		fmt.Fprintln(os.Stderr, "Error: missing plugin name")
		return
	}

	_, err := RemovePlugin(pm, name, project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to remove plugin: %v\n", err)
		return
	}

	// Re-sync the in-memory registry with disk so any chained CLI
	// invocation in the same process doesn't see the now-removed plugin.
	// Best-effort — the on-disk removal already succeeded, so a warning
	// here is recoverable on the next bootstrap or watcher tick.
	if discErr := pm.Discover(); discErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to re-discover plugins after remove: %v\n", discErr)
	}

	// Self-clean: prune the namespaced skill symlinks for this plugin now
	// instead of waiting for the next filesystem watcher tick or TUI
	// bootstrap. Without this, `~/.config/late/skills/<plugin>:<skill>`
	// entries would linger as orphans after `late plugin remove <plugin>`
	// in any process where the watcher hasn't run yet.
	if skillErr := pm.RegisterPluginSkills(""); skillErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to prune plugin skill symlinks: %v\n", skillErr)
	}

	scope := ""
	if project {
		scope = " (project)"
	}
	fmt.Printf("Removed plugin: %s%s\n", name, scope)
}

// handlePluginLink creates a development symlink.
func handlePluginLink(pm *PluginManager, args []string) {
	project, path := parseProjectFlag(args)
	if path == "" {
		fmt.Fprintln(os.Stderr, "Error: missing path")
		fmt.Fprintln(os.Stderr, "Usage: late plugin link [--project] <path>")
		return
	}

	plugin, err := Link(pm, path, project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to link plugin: %v\n", err)
		return
	}

	scope := "global"
	if project {
		scope = "project"
	}
	fmt.Printf("Linked plugin: %s v%s (%s)\n", plugin.Name, plugin.Version, scope)
	fmt.Printf("  Path: %s\n", plugin.Path)
}

// handlePluginUpdate updates installed plugins.
//
// Behavior:
//   - `late plugin update` (no args)    → UpdateAll — re-installs every npm/git
//     plugin in place, skipping local.
//   - `late plugin update <name>`       → Update one plugin by name. Resolves
//     marketplace-source plugins via the default marketplace client on the fly.
//   - `late plugin update <name> local` → Refused with a hint to edit the
//     source directory directly.
func handlePluginUpdate(pm *PluginManager, args []string) {
	if len(args) > 0 {
		name := args[0]
		if _, err := Update(pm, name, nil); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to update plugin %s: %v\n", name, err)
			return
		}
		return
	}

	results, err := UpdateAll(pm, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: bulk update failed: %v\n", err)
		// Surface partial state so the user can see what succeeded.
		for _, p := range results {
			fmt.Printf("  %s v%s\n", p.Name, p.Version)
		}
		return
	}
	for _, p := range results {
		fmt.Printf("Updated %s v%s\n", p.Name, p.Version)
	}
}

// handlePluginEnable enables or disables a plugin.
func handlePluginEnable(pm *PluginManager, args []string, enable bool) {
	if len(args) == 0 {
		action := "enable"
		if !enable {
			action = "disable"
		}
		fmt.Fprintf(os.Stderr, "Error: missing plugin name\n")
		fmt.Fprintf(os.Stderr, "Usage: late plugin %s <name>\n", action)
		return
	}

	name := args[0]
	plugin := pm.Plugin(name)
	if plugin == nil {
		fmt.Fprintf(os.Stderr, "Error: plugin %s is not installed\n", name)
		return
	}

	plugin.Enabled = enable
	if err := SavePluginMeta(plugin); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save metadata: %v\n", err)
	}

	state := "enabled"
	if !enable {
		state = "disabled"
	}
	fmt.Printf("%s %s\n", plugin.Name, state)
}

// (isGitURL and isLocalPath were removed: Install() now classifies the
// source string itself, including marketplace fallback for bare names.)

// Sort plugins by name
type byName []*InstalledPlugin

func (a byName) Len() int           { return len(a) }
func (a byName) Less(i, j int) bool { return a[i].Name < a[j].Name }
func (a byName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// Ensure sort.Interface compliance
var _ sort.Interface = byName{}
