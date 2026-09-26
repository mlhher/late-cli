package main

import (
	"flag"
	"fmt"
	"io"
)

// flagGroups defines the display order and scope grouping of the root flags
// in -h output, plus an optional note rendered right after a group's flag
// block (note lines are pre-indented to align with the flag names). Keep it
// in sync with the registrations in main(): any flag registered but missing
// here is still rendered, under "Other:", by writeGroupedFlags, so new flags
// can never silently vanish from the help.
var flagGroups = []struct {
	heading string
	flags   []string
	note    string
}{
	{"General", []string{"help", "version"}, ""},
	{"Session & startup", []string{"continue", "continue-project", "prompt", "theme", "show-cwd"}, ""},
	{"System prompt", []string{"system-prompt", "system-prompt-file", "append-system-prompt", "inject-cwd", "gemma-thinking"}, ""},
	{"Model & streaming", []string{"logit-bias", "suppress-thinking-words", "max-stream-retries", "max-concurrent-llm-requests"}, ""},
	{"Subagents", []string{"enable-subagents", "subagent-max-turns", "subagent-logit-bias", "save-subagent-histories"}, ""},
	{"Tools", []string{"use-tools", "enable-bash", "enable-images", "enable-sqz"}, ""},
	{"Supervision & safety", []string{"ask-for-user-approval", "i-promise-i-have-backups-and-will-not-file-issues"},
		"These two flags are mutually exclusive: pass at most one. The default\n  (ask-for-user-approval) can be changed by adding a \"permission-mode\"\n  entry to late's config.json with one of the values above."},
}

// writeHelp renders the full `late -h` output. src is the FlagSet whose
// flags are rendered (flag.CommandLine in production; isolated FlagSets in
// tests). Every usage string rendered here must contain no back-quoted
// word: PrintDefaults would render it as the flag's value name.
func writeHelp(w io.Writer, src *flag.FlagSet) {
	fmt.Fprintln(w, "Late — the AI agent that always stays sharp.")
	fmt.Fprintln(w, "Isolates execution steps to keep the model's context clean during long workflows.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  late [flags]")
	fmt.Fprintln(w, "  late session <command> [args]")
	fmt.Fprintln(w, "  late plugin <command> [args]")
	fmt.Fprintln(w, "  late worktree <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  session list [-v]                  List saved sessions (-v for details)")
	fmt.Fprintln(w, "  session load <id>                  Resume a session in the TUI (exact ID or unique prefix)")
	fmt.Fprintln(w, "  session delete <id>                Delete a session by ID")
	fmt.Fprintln(w, "  plugin list | ls                   List installed plugins")
	fmt.Fprintln(w, "  plugin install | i [--project] <source>    Install from npm, git, a local path, or a marketplace name")
	fmt.Fprintln(w, "  plugin remove | rm | uninstall [--project] <name>   Remove a plugin")
	fmt.Fprintln(w, "  plugin link [--project] <path>     Symlink a local plugin directory for development")
	fmt.Fprintln(w, "  plugin update [<name>]             Update one plugin, or all when the name is omitted")
	fmt.Fprintln(w, "  plugin enable <name>               Enable a plugin")
	fmt.Fprintln(w, "  plugin disable <name>              Disable a plugin")
	fmt.Fprintln(w, "  worktree list                      List git worktrees")
	fmt.Fprintln(w, "  worktree create <path> [branch]    Create a worktree (branch defaults to the current one)")
	fmt.Fprintln(w, "  worktree remove <path>             Remove a worktree")
	fmt.Fprintln(w, "  worktree active                    Show the active worktree")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	writeGroupedFlags(w, src)
	fmt.Fprintln(w, "🌟 Enjoying Late? Consider leaving a star on GitHub: https://github.com/mlhher/late-cli")
}

// registerDisplayFlag registers src's flag f on the display-only FlagSet dfs
// used for help rendering. It keeps the display flag sharing f's real
// registered Value — PrintDefaults derives value names ("string", "int", …)
// and zero-value checks from the Value's concrete type — but restores
// DefValue from src: Var snapshots Value.String() at registration time, which
// would advertise a value the caller mutated before -h (e.g.
// `late -show-cwd=false -h`) instead of the flag's true default. DefValue is
// captured once at registration and is never changed by parsing, so src is
// the single source of truth for what -h must advertise.
func registerDisplayFlag(dfs *flag.FlagSet, f *flag.Flag) {
	dfs.Var(f.Value, f.Name, f.Usage)
	dfs.Lookup(f.Name).DefValue = f.DefValue
}

// writeGroupedFlags renders src's flags grouped by scope, in flagGroups
// order, reusing the flag package's PrintDefaults formatting per group, and
// appends each group's note (when non-empty) right after its flag block.
// Each group is rendered through a display-only FlagSet sharing the real
// registered flag Values, so value names stay correct and single-sourced with
// the registrations in main(); defaults are restored from src's DefValue by
// registerDisplayFlag, so they stay true even when flags were parsed before
// -h.
func writeGroupedFlags(w io.Writer, src *flag.FlagSet) {
	listed := make(map[string]bool)
	for _, g := range flagGroups {
		gfs := flag.NewFlagSet(g.heading, flag.ContinueOnError)
		gfs.SetOutput(w)
		count := 0
		for _, name := range g.flags {
			f := src.Lookup(name)
			if f == nil {
				continue
			}
			registerDisplayFlag(gfs, f)
			listed[name] = true
			count++
		}
		if count == 0 {
			continue
		}
		fmt.Fprintln(w, g.heading+":")
		gfs.PrintDefaults()
		if g.note != "" {
			fmt.Fprintln(w, "  "+g.note)
		}
		fmt.Fprintln(w)
	}
	var others []string
	src.VisitAll(func(f *flag.Flag) {
		if !listed[f.Name] {
			others = append(others, f.Name)
		}
	})
	if len(others) > 0 {
		ofs := flag.NewFlagSet("Other", flag.ContinueOnError)
		ofs.SetOutput(w)
		for _, name := range others {
			f := src.Lookup(name)
			registerDisplayFlag(ofs, f)
		}
		fmt.Fprintln(w, "Other:")
		ofs.PrintDefaults()
		fmt.Fprintln(w)
	}
}
