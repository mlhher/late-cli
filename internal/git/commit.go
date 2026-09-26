package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitCmdTimeout bounds every git invocation made by this package.
const gitCmdTimeout = 60 * time.Second

// newGitCmd builds a git command with the package-wide hang guards applied.
//
//   - Bounded context: these helpers intentionally take no ctx parameter
//     (out-of-scope signature churn), so Background+timeout is the accepted
//     emergency pattern — a wedged git process is killed after 60s instead of
//     hanging the caller forever. Callers MUST defer the returned cancel func.
//
//   - Fail-fast credential handling: a remote needing auth can otherwise block
//     forever on an interactive terminal or SSH/askpass prompt — the exact hang
//     class reported in production. GIT_TERMINAL_PROMPT=0 makes git error out
//     instead of prompting on the terminal, and GIT_ASKPASS=echo turns any
//     askpass-based credential lookup into an immediate "no credential"
//     failure. Both are inert for the purely local operations this package
//     performs today.
//
// dir may be empty to inherit the process working directory.
func newGitCmd(dir string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo")
	return cmd, cancel
}

// CommitEntry holds parsed data from git log.
type CommitEntry struct {
	Hash    string
	Author  string
	Date    string
	Message string
	IsHEAD  bool
}

// LogCommits returns the last N commits from the repo at cwd.
func LogCommits(cwd string, count int) ([]CommitEntry, error) {
	// Get current HEAD hash to mark it (failure here is non-fatal: IsHEAD is
	// best-effort).
	headCmd, cancelHead := newGitCmd(cwd, "rev-parse", "--short", "HEAD")
	defer cancelHead()
	headOut, err := headCmd.Output()
	headHash := strings.TrimSpace(string(headOut))
	if err != nil {
		headHash = ""
	}

	format := "%h|%an|%ar|%s"
	args := []string{"log", fmt.Sprintf("--max-count=%d", count), fmt.Sprintf("--format=%s", format)}
	// Bounded + fail-fast env via newGitCmd: git must never hang the TUI.
	cmd, cancelLog := newGitCmd(cwd, args...)
	defer cancelLog()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return []CommitEntry{}, nil
	}

	entries := make([]CommitEntry, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		entry := CommitEntry{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    parts[2],
			Message: parts[3],
			IsHEAD:  parts[0] == headHash,
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ShowCommit returns the full commit message and diff for a given hash.
func ShowCommit(cwd string, hash string) (string, error) {
	args := []string{"show", "--stat", hash}
	// Bounded + fail-fast env via newGitCmd: git must never hang the TUI.
	cmd, cancel := newGitCmd(cwd, args...)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show: %w", err)
	}
	return string(out), nil
}
