package git

import (
	"fmt"
	"os/exec"
	"strings"
)

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
	// Get current HEAD hash to mark it
	headCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	headCmd.Dir = cwd
	headOut, err := headCmd.Output()
	headHash := strings.TrimSpace(string(headOut))
	if err != nil {
		headHash = ""
	}

	format := "%h|%an|%ar|%s"
	args := []string{"log", fmt.Sprintf("--max-count=%d", count), fmt.Sprintf("--format=%s", format)}
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
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
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show: %w", err)
	}
	return string(out), nil
}
