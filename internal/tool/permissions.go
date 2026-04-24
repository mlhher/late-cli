package tool

import (
	"os"
	"path/filepath"
	"strings"
)

// canonicalizePath resolves symlinks for the nearest existing ancestor of absPath
// and then reapplies the non-existing suffix. This gives a canonical target path
// even when the leaf does not exist yet.
func canonicalizePath(absPath string) (string, error) {
	absPath = filepath.Clean(absPath)
	current := absPath

	for {
		if _, err := os.Lstat(current); err == nil {
			resolvedCurrent, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}

			suffix, err := filepath.Rel(current, absPath)
			if err != nil {
				return "", err
			}
			if suffix == "." {
				return filepath.Clean(resolvedCurrent), nil
			}

			return filepath.Clean(filepath.Join(resolvedCurrent, suffix)), nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return filepath.Clean(absPath), nil
}

// isNewPath returns true when the resolved target path does not yet exist,
// falls within the project root, and stays within the provided session cwd.
// Creation outside the project root or outside the active cwd always prompts.
func isNewPath(path string, cwd string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}

	baseDir := strings.TrimSpace(cwd)
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return false
		}
	}

	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return false
	}
	if evalBaseDir, err := filepath.EvalSymlinks(absBaseDir); err == nil {
		absBaseDir = evalBaseDir
	}

	resolvedPath := path
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(absBaseDir, resolvedPath)
	}
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return false
	}
	canonicalPath, err := canonicalizePath(absPath)
	if err != nil {
		return false
	}

	if !IsSafePath(canonicalPath) {
		return false
	}

	relToBase, err := filepath.Rel(absBaseDir, canonicalPath)
	if err != nil {
		return false
	}
	if relToBase == ".." || strings.HasPrefix(relToBase, ".."+string(filepath.Separator)) {
		return false
	}

	_, err = os.Stat(absPath)
	return os.IsNotExist(err)
}

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
