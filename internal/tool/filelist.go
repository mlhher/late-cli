package tool

import (
	"io/fs"
	"path/filepath"
	"sort"
)

// ListProjectFiles returns the files below dir as slash-separated paths relative
// to dir, honoring the same rules the agent's own search tools apply: .git,
// node_modules and friends are skipped, and so is everything matched by the
// project's .gitignore and .llmignore. At most limit paths are returned (a
// non-positive limit means no limit); the result is sorted.
//
// It exists for the prompt's @file completion, so that what the user is offered
// is what the agent can actually find.
func ListProjectFiles(dir string, limit int) ([]string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	gi, giRoot := getIgnoreForPath(root, false)

	var files []string
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".svn", ".hg":
				return filepath.SkipDir
			}
			if path != root && matchesGitIgnore(gi, giRoot, path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if matchesGitIgnore(gi, giRoot, path, false) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if limit > 0 && len(files) >= limit {
			return fs.SkipAll
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return files, err
	}
	sort.Strings(files)
	return files, nil
}
