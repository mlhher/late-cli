package ast

import "strings"

// Issue #1, suggestion #2: read/write split by subcommand.
//
// The tables below classify gh and git invocations that can only READ state
// (remote or local). When every invocation in a parsed statement is classified
// read-only — alone, or piped/chained into other read-only operations — the
// AST adapters emit the ReasonReadOnly flag and the policy engine
// auto-approves the statement: no confirmation prompt, no OTP re-evaluation.
//
// The classifier is deliberately conservative:
//   - unknown subcommands, bare groups (e.g. `gh issue`) and unrecognized flag
//     shapes keep the current behavior (NeedsConfirmation via the default
//     policy gates);
//   - flags that mutate (gh issue edit, gh pr merge, gh repo delete,
//     gh gist create, gh api with write verbs or body flags) are not in the
//     tables and stay dangerous;
//   - sudo, redirections, command substitution, expansions and pipes into
//     write-capable programs (sh, bash, xargs, tee, sed, awk, find -exec, …)
//     are not read-only and keep their existing gates.
//
// Windows note: WindowsParser produces its IR via the PowerShell bridge and
// does not emit ReasonReadOnly yet; Windows keeps the current behavior.

// ghReadOnlyActions maps a gh command group to the sub-actions that only read
// state. A group absent from the map, or an action absent from its set, keeps
// the current confirmation gate.
var ghReadOnlyActions = map[string]map[string]bool{
	"auth":    {"status": true},
	"config":  {"get": true},
	"gist":    {"view": true, "list": true},
	"issue":   {"view": true, "list": true, "status": true},
	"label":   {"list": true},
	"pr":      {"view": true, "list": true, "checks": true, "diff": true, "status": true},
	"release": {"view": true, "list": true},
	"repo":    {"view": true},
	"run":     {"view": true, "list": true, "watch": true},
	"search":  {"code": true, "commits": true, "issues": true, "prs": true, "repos": true, "users": true},
}

// ghAPIReadOnlyVerbs are the HTTP methods under which `gh api` cannot mutate
// remote state.
var ghAPIReadOnlyVerbs = map[string]bool{
	"GET":  true,
	"HEAD": true,
}

// ghCommandIsReadOnly reports whether a resolved `gh <args…>` invocation is a
// recognized read-only operation.
func ghCommandIsReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	group := args[0]
	if group == "" || group[0] == '-' {
		// Bare `gh` or global flags before the subcommand: keep the gate.
		return false
	}
	if group == "api" {
		return ghAPIIsReadOnly(args[1:])
	}
	actions, ok := ghReadOnlyActions[group]
	if !ok || len(args) < 2 {
		return false
	}
	action := args[1]
	if action == "" || action[0] == '-' {
		return false
	}
	return actions[action]
}

// ghAPIIsReadOnly enforces the GET-only rule for `gh api`: write verbs on
// -X/--method and body-carrying flags (-f/-F/--field/--raw-field/--input/
// --field-file, which imply a POST body) keep the current gate.
func ghAPIIsReadOnly(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--method" || a == "-X":
			// The verb is the next argument; a missing verb fails closed.
			if i+1 >= len(args) {
				return false
			}
			i++
			if !ghAPIReadOnlyVerbs[strings.ToUpper(args[i])] {
				return false
			}
		case strings.HasPrefix(a, "--method="):
			if !ghAPIReadOnlyVerbs[strings.ToUpper(strings.TrimPrefix(a, "--method="))] {
				return false
			}
		case len(a) > 2 && a[0] == '-' && a[1] == 'X' && a[2] != '-':
			// pflag attached shorthand verb: -XGET, -X=GET.
			if !ghAPIReadOnlyVerbs[strings.ToUpper(strings.TrimPrefix(a[2:], "="))] {
				return false
			}
		case isGhAPIWriteFlag(a):
			return false
		}
	}
	return true
}

// isGhAPIWriteFlag reports whether a gh api flag carries a request body.
// Single-dash attached forms (-ffoo=bar) are covered by checking the first
// shorthand character.
func isGhAPIWriteFlag(a string) bool {
	if strings.HasPrefix(a, "--") {
		switch normalizeFlagKey(a) {
		case "--field", "--raw-field", "--input", "--field-file":
			return true
		}
		return false
	}
	if len(a) >= 2 && a[0] == '-' {
		switch a[1] {
		case 'f', 'F':
			return true
		}
	}
	return false
}

// gitReadOnlySubcommands are git subcommands whose every flag/operand form is
// read-only (minus the gitWriteFlags below).
var gitReadOnlySubcommands = map[string]bool{
	"log":       true,
	"show":      true,
	"diff":      true,
	"status":    true,
	"blame":     true,
	"rev-parse": true,
}

// gitWriteFlags are flags that write files even for otherwise read-only git
// subcommands (e.g. `git diff --output=f.patch`).
var gitWriteFlags = map[string]bool{
	"--output": true,
}

// gitWriteFlagChars are single-dash shorthand characters that write files
// (covers attached forms like -ofile).
var gitWriteFlagChars = map[byte]bool{
	'o': true,
}

// gitBranchReadOnlyFlags are the flags under which `git branch` only lists
// information. Any positional operand keeps the gate, because
// `git branch <name>` creates a branch — the IR-level classifier cannot
// distinguish creation from listing without them.
var gitBranchReadOnlyFlags = map[string]bool{
	"-a": true, "--all": true,
	"-r": true, "--remotes": true,
	"-v": true, "-vv": true, "--verbose": true,
	"-l": true, "--list": true,
	"--show-current": true,
	"--sort":         true,
	"--format":       true,
	"--color":        true, "--no-color": true,
	"--abbrev": true, "--no-abbrev": true,
	"--contains": true, "--no-contains": true,
	"--merged": true, "--no-merged": true,
	"--points-at": true,
	"--column":    true, "--no-column": true,
}

// gitCommandIsReadOnly reports whether a resolved `git <args…>` invocation is
// a recognized read-only operation. Global options before the subcommand
// (e.g. `git -C path log`) keep the current gate.
func gitCommandIsReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub := args[0]
	if sub == "" || sub[0] == '-' {
		return false
	}
	switch sub {
	case "branch":
		return gitArgsReadOnly(args[1:], gitBranchReadOnlyFlags)
	case "remote":
		return gitRemoteIsReadOnly(args[1:])
	}
	if gitReadOnlySubcommands[sub] {
		return gitArgsReadOnly(args[1:], nil)
	}
	return false
}

// gitArgsReadOnly reports whether the remaining git arguments keep the
// invocation read-only. When allowed is non-nil (git branch) every argument
// must be an explicitly whitelisted flag — no positional operands, no
// unrecognized flags.
func gitArgsReadOnly(rest []string, allowed map[string]bool) bool {
	for _, a := range rest {
		if gitWriteFlags[normalizeFlagKey(a)] {
			return false
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && gitWriteFlagChars[a[1]] {
			return false
		}
		if allowed != nil {
			if a[0] != '-' {
				return false // operand: could create/rename/delete
			}
			if !allowed[normalizeFlagKey(a)] {
				return false
			}
		}
	}
	return true
}

// gitRemoteIsReadOnly reports whether `git remote <rest…>` only reads:
// bare listing, -v/--verbose, and the show/get-url sub-actions.
// add/remove/rename/set-url/prune/update keep the current gate.
func gitRemoteIsReadOnly(rest []string) bool {
	if len(rest) == 0 {
		return true // bare `git remote` lists remotes
	}
	allVerbose := true
	for _, a := range rest {
		if a != "-v" && a != "--verbose" {
			allVerbose = false
			break
		}
	}
	if allVerbose {
		return true
	}
	switch rest[0] {
	case "show", "get-url":
		return true
	}
	return false
}

// readOnlyFilterCommands are plain Unix inspection/text filters that cannot
// modify state. They may appear standalone or as pipeline stages of a
// read-only gh/git command. Write-capable programs (tee, xargs, sed, awk,
// find, shells) are deliberately absent, so pipelines through them keep the
// current confirmation gate.
var readOnlyFilterCommands = map[string]bool{
	"basename": true, "cat": true, "cksum": true, "column": true,
	"comm": true, "cut": true, "date": true, "diff": true, "dirname": true,
	"echo": true, "expand": true, "file": true, "fold": true, "fmt": true,
	"grep": true, "head": true, "hexdump": true, "jq": true, "less": true,
	"ls": true, "md5sum": true, "more": true, "nl": true, "od": true,
	"pwd": true, "realpath": true, "rev": true, "seq": true, "sha256sum": true,
	"sort": true, "stat": true, "strings": true, "tac": true, "tail": true,
	"tr": true, "uniq": true, "unexpand": true, "wc": true, "whoami": true,
	"xxd": true,
}

// readOnlyFilterWriteFlags maps a filter command to the exact flags that
// write files and must keep the gate (e.g. `sort -o /etc/cron.d/x`).
var readOnlyFilterWriteFlags = map[string]map[string]bool{
	"sort": {"--output": true},
}

// readOnlyFilterWriteFlagChars maps a filter command to single-dash shorthand
// characters that write files, covering attached forms like -ofile.
var readOnlyFilterWriteFlagChars = map[string]map[byte]bool{
	"sort": {'o': true},
}

// normalizeFlagKey strips a long flag's attached value (--flag=value →
// --flag); single-dash flags are returned unchanged.
func normalizeFlagKey(a string) string {
	if strings.HasPrefix(a, "--") {
		if idx := strings.Index(a, "="); idx != -1 {
			return a[:idx]
		}
	}
	return a
}

// unixCommandIsReadOnly reports whether a single invocation (program name plus
// statically resolved arguments) is a recognized read-only operation.
func unixCommandIsReadOnly(name string, args []string) bool {
	switch name {
	case "gh":
		return ghCommandIsReadOnly(args)
	case "git":
		return gitCommandIsReadOnly(args)
	default:
		if !readOnlyFilterCommands[name] {
			return false
		}
		return filterArgsReadOnly(name, args)
	}
}

// filterArgsReadOnly reports whether a filter invocation carries no
// write-capable flags.
func filterArgsReadOnly(name string, args []string) bool {
	writeFlags := readOnlyFilterWriteFlags[name]
	writeChars := readOnlyFilterWriteFlagChars[name]
	for _, a := range args {
		if writeFlags[normalizeFlagKey(a)] {
			return false
		}
		if writeChars != nil && len(a) > 1 && a[0] == '-' && a[1] != '-' && writeChars[a[1]] {
			return false
		}
	}
	return true
}
