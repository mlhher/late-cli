package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// newHelpTestFlagSet mirrors main()'s root flag registrations (names and
// kinds only) on an isolated FlagSet so rendering can be tested without
// running main().
func newHelpTestFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("help-test", flag.ContinueOnError)
	bools := []string{
		"help", "version", "continue", "continue-project", "show-cwd", "inject-cwd", "gemma-thinking",
		"suppress-thinking-words", "save-subagent-histories", "enable-sqz",
		"ask-for-user-approval", "i-promise-i-have-backups-and-will-not-file-issues",
		"enable-images",
		"use-tools", "enable-bash", "enable-subagents", "check-compaction",
	}
	for _, name := range bools {
		def := name == "use-tools" || name == "enable-bash" || name == "enable-subagents"
		fs.Bool(name, def, "usage of "+name)
	}
	strs := []string{"system-prompt", "system-prompt-file", "append-system-prompt", "theme", "prompt", "logit-bias", "subagent-logit-bias", "compaction-mode", "replay-shadow"}
	for _, name := range strs {
		fs.String(name, "", "usage of "+name)
	}
	fs.Int("subagent-max-turns", 500, "usage of subagent-max-turns")
	fs.Int("max-stream-retries", 100, "usage of max-stream-retries")
	fs.Float64("compaction-threshold", 0.35, "usage of compaction-threshold")
	return fs
}

// countRenderedFlagLines counts output lines that render the flag `name`:
// a line starting with "  -" whose flag-name token (up to the first space
// or tab) equals name exactly. Token-boundary matching keeps "-system-prompt"
// from being miscounted against the "-system-prompt-file" line, which a
// plain substring count ("  -"+name) would wrongly attribute to both.
func countRenderedFlagLines(out, name string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "  -")
		if !ok {
			continue
		}
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			rest = rest[:i]
		}
		if rest == name {
			n++
		}
	}
	return n
}

func TestWriteGroupedFlagsCoversAllGroupedFlagsOnce(t *testing.T) {
	var buf bytes.Buffer
	writeGroupedFlags(&buf, newHelpTestFlagSet(t))
	out := buf.String()
	for _, g := range flagGroups {
		for _, name := range g.flags {
			if n := countRenderedFlagLines(out, name); n != 1 {
				t.Errorf("flag -%s rendered %d times, want exactly 1", name, n)
			}
		}
	}
	// Headings appear, in the declared order.
	last := -1
	for _, g := range flagGroups {
		idx := strings.Index(out, g.heading+":")
		if idx < 0 {
			t.Fatalf("missing heading %q in output:\n%s", g.heading, out)
		}
		if idx < last {
			t.Errorf("heading %q appears out of order", g.heading)
		}
		last = idx
	}
	if strings.Contains(out, "Other:") {
		t.Errorf("all grouped flags were listed, unexpected Other section:\n%s", out)
	}
	// Boolean flags must not render a value name; string flags render "string".
	if !strings.Contains(out, "  -enable-bash\n") {
		t.Errorf("bool flag should render with no value name:\n%s", out)
	}
	if !strings.Contains(out, "  -system-prompt string") {
		t.Errorf("string flag should render with 'string' value name:\n%s", out)
	}
	if !strings.Contains(out, "(default 500)") {
		t.Errorf("expected '(default 500)' for subagent-max-turns:\n%s", out)
	}
}

// TestWriteGroupedFlagsShowsTrueDefaultsWhenMutated guards the grouped-help
// default rendering: flag values mutated before -h (e.g. `late
// -show-cwd=false -h`) must not change what the help advertises. Defaults are
// rendered from the DefValue captured at registration in the source FlagSet —
// which parsing never touches — so a render after mutation must be
// byte-identical to an unmutated render of the same flags.
func TestWriteGroupedFlagsShowsTrueDefaultsWhenMutated(t *testing.T) {
	newTestFlagSet := func() *flag.FlagSet {
		fs := newHelpTestFlagSet(t)
		// Extra ungrouped flag so the Other: section is exercised too.
		fs.String("zzz-future-flag", "", "usage of zzz-future-flag")
		return fs
	}

	var baseline bytes.Buffer
	writeGroupedFlags(&baseline, newTestFlagSet())

	// Mirror production order: values are parsed (mutated) before -h renders.
	fs := newTestFlagSet()
	for _, m := range []struct{ name, value string }{
		{"use-tools", "false"},       // test default true
		{"show-cwd", "true"},         // test default false
		{"subagent-max-turns", "1"},  // test default 500
		{"theme", "gruvbox"},         // test default ""
		{"zzz-future-flag", "later"}, // renders under Other:
	} {
		if err := fs.Set(m.name, m.value); err != nil {
			t.Fatalf("Set(%s, %s): %v", m.name, m.value, err)
		}
	}

	var mutated bytes.Buffer
	writeGroupedFlags(&mutated, fs)

	if got, want := mutated.String(), baseline.String(); got != want {
		t.Fatalf("help rendered after mutating flag values must equal the unmutated render\n--- mutated ---\n%s\n--- unmutated ---\n%s", got, want)
	}

	out := mutated.String()
	// True non-zero defaults must survive the mutation; usage strings are
	// "usage of <name>", so each pattern matches exactly one flag's line.
	for _, want := range []string{
		"\tusage of use-tools (default true)\n",
		"\tusage of subagent-max-turns (default 500)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mutated render missing %q:\n%s", want, out)
		}
	}
	// Flags whose true default is the zero value must still render none.
	for _, want := range []string{
		"\tusage of show-cwd\n",
		"\tusage of theme\n",
		"\tusage of zzz-future-flag\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mutated render missing %q:\n%s", want, out)
		}
	}
	// Parsed values must not leak in as advertised defaults ("(default 1)"
	// cannot false-match "(default 100)" because of the closing paren).
	for _, banned := range []string{"(default \"gruvbox\")", "(default \"later\")", "(default 1)"} {
		if strings.Contains(out, banned) {
			t.Errorf("mutated render advertises a parsed value as a default (%s):\n%s", banned, out)
		}
	}
}

func TestWriteGroupedFlagsUncategorizedFallToOther(t *testing.T) {
	fs := newHelpTestFlagSet(t)
	fs.String("zzz-future-flag", "", "a flag added later and not yet grouped")
	var buf bytes.Buffer
	writeGroupedFlags(&buf, fs)
	out := buf.String()
	if !strings.Contains(out, "Other:") || !strings.Contains(out, "  -zzz-future-flag") {
		t.Fatalf("ungrouped flag must still be rendered under Other:\n%s", out)
	}
}

func TestWriteHelpSections(t *testing.T) {
	var buf bytes.Buffer
	writeHelp(&buf, newHelpTestFlagSet(t))
	out := buf.String()
	for _, want := range []string{
		"Usage:", "Commands:", "Flags:",
		"session list [-v]", "session load <id>", "session delete <id>",
		"plugin list | ls", "plugin install | i", "plugin remove | rm | uninstall",
		"plugin update [<name>]", "worktree create <path> [branch]", "worktree active",
		"ask-for-user-approval",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeHelp output missing %q", want)
		}
	}
}

// TestWriteHelp_ShowsPermissionModeNote guards the Supervision & safety note:
// the three mutually exclusive permission flags are grouped together with a
// note explaining that the default (ask-for-user-approval) can be overridden
// via the permission-mode entry in late's config.json.
func TestWriteHelp_ShowsPermissionModeNote(t *testing.T) {
	var buf bytes.Buffer
	writeHelp(&buf, newHelpTestFlagSet(t))
	out := buf.String()
	for _, want := range []string{
		"-ask-for-user-approval",
		"mutually exclusive",
		"permission-mode",
		"config.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeHelp output missing %q", want)
		}
	}
}
