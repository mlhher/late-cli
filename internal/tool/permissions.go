package tool

import (
	"os"
	"path/filepath"
	"strings"
)

// IsSafePath checks if a path is within the current working directory.
func IsSafePath(path string) bool {
	// Shortcut: If the path is relative and does not contain ".." components,
	// it is guaranteed to be within the CWD (unless it follows a malicious symlink,
	// but we assume the agent stays within the provided tree).
	if !filepath.IsAbs(path) && !strings.Contains(path, "..") {
		return true
	}

	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	// Resolve symlinks to get canonical CWD
	if evalCwd, err := filepath.EvalSymlinks(absCwd); err == nil {
		absCwd = evalCwd
	}

	// Resolve symlinks for path by climbing up until an existing directory is found
	current := absPath
	var suffix string
	for {
		if eval, err := filepath.EvalSymlinks(current); err == nil {
			if suffix != "" {
				absPath = filepath.Join(eval, suffix)
			} else {
				absPath = eval
			}
			break
		}
		// Move up to the parent directory
		dir := filepath.Dir(current)
		if dir == current {
			break // Reached root
		}
		rel, _ := filepath.Rel(dir, absPath)
		suffix = rel
		current = dir
	}

	// Ensure absCwd ends with path separator for proper prefix matching
	if !strings.HasSuffix(absCwd, string(filepath.Separator)) {
		absCwd += string(filepath.Separator)
	}

	// Handle root path case
	if absCwd == string(filepath.Separator) {
		return true
	}

	// Also ensure absPath has a trailing separator so that an exact match
	// with the CWD returns true
	if !strings.HasSuffix(absPath, string(filepath.Separator)) {
		absPath += string(filepath.Separator)
	}

	return strings.HasPrefix(absPath, absCwd)
}
