package git

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// WorktreeInfo contains information about a git worktree
type WorktreeInfo struct {
	Path     string
	Branch   string
	IsDetached bool
	Status   string
}

// ListWorktrees executes `git worktree list` and parses the output
// to return a slice of WorktreeInfo structures.
func ListWorktrees() ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "worktree", "list")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var worktrees []WorktreeInfo
	lines := strings.Split(string(output), "\n")

	// Regex pattern to match worktree lines
	// Format: /path/to/worktree  commit-hash [branch-name]
	// or: /path/to/worktree  commit-hash (no branch)
	worktreePattern := regexp.MustCompile(`^(\S+)\s+([a-f0-9]+)\s+\[([^\]]*)\]`)

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		matches := worktreePattern.FindStringSubmatch(line)
		if matches != nil {
			path := matches[1]
			commitHash := matches[2]
			branchName := matches[3]

			info := WorktreeInfo{
				Path: path,
			}

			// Check if detached (branch name is empty or looks like a commit hash)
			if branchName == "" || (len(branchName) == 40 && regexp.MustCompile(`^[a-f0-9]+$`).MatchString(branchName)) {
				info.IsDetached = true
				info.Branch = commitHash
			} else {
				info.IsDetached = false
				info.Branch = branchName
			}

			// Check if next line is a status line (starts with "# ")
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "# ") {
				info.Status = strings.TrimPrefix(lines[i+1], "# ")
				i++ // Skip the status line
			}

			worktrees = append(worktrees, info)
		}
	}

	return worktrees, nil
}

// CreateWorktree executes `git worktree add <path> <branch>` to create a new worktree.
func CreateWorktree(path, branch string) error {
	cmd := exec.Command("git", "worktree", "add", path, branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	_ = output // Output can be logged if needed
	return nil
}

// RemoveWorktree executes `git worktree remove <path>` to remove a worktree.
func RemoveWorktree(path string) error {
	cmd := exec.Command("git", "worktree", "remove", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	_ = output // Output can be logged if needed
	return nil
}

// GetActiveWorktree returns the current worktree path by comparing
// the current working directory with the paths from `git worktree list`.
// If no matching worktree is found, it returns the main repository path.
func GetActiveWorktree() (string, error) {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Get all worktrees
	worktrees, err := ListWorktrees()
	if err != nil {
		return "", err
	}

	// Compare CWD with worktree paths
	for _, wt := range worktrees {
		if wt.Path == cwd {
			return wt.Path, nil
		}
	}

	// If no match found, return the main repository path
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
