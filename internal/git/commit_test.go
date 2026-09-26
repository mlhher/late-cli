package git

import (
	"strings"
	"testing"
)

// TestNewGitCmd_HangGuards verifies the fail-fast credential environment flags
// and the bounded-context wiring that newGitCmd applies to every git
// invocation made by this package. A hang test against a real prompt is not
// practical here, so this pins the two guards that prevent it instead.
func TestNewGitCmd_HangGuards(t *testing.T) {
	cmd, cancel := newGitCmd(t.TempDir(), "status")
	defer cancel()

	// cmd.Env is os.Environ() with the fail-fast flags appended, so scan into
	// a map (later duplicates overwrite earlier ones, matching the appended
	// flags' intended effect).
	env := map[string]string{}
	for _, kv := range cmd.Env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want \"0\" (interactive prompts must fail fast, not hang)", env["GIT_TERMINAL_PROMPT"])
	}
	if env["GIT_ASKPASS"] != "echo" {
		t.Errorf("GIT_ASKPASS = %q, want \"echo\" (askpass credential prompts must fail fast, not hang)", env["GIT_ASKPASS"])
	}

	// exec.CommandContext populates Cancel (Go 1.20+); a plain exec.Command
	// leaves it nil — so a non-nil Cancel proves the 60s bounded context is
	// wired in and a wedged git process will be killed.
	if cmd.Cancel == nil {
		t.Error("cmd.Cancel is nil: command was not created with a bounded context (exec.CommandContext)")
	}
}

// TestNewGitCmd_StillRunsGit sanity-checks that the hang guards do not break
// normal local git execution (--version works in any directory).
func TestNewGitCmd_StillRunsGit(t *testing.T) {
	cmd, cancel := newGitCmd(t.TempDir(), "--version")
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git --version via newGitCmd: %v", err)
	}
	if !strings.Contains(string(out), "git version") {
		t.Fatalf("unexpected git --version output: %q", string(out))
	}
}
