package tool

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// GitIgnore parses and matches .gitignore patterns using Go's stdlib regex.
// It aims to be compatible with git's gitignore rules for the common cases
// that matter in a search tool: skipping build artifacts, vendored deps,
// and generated output directories.
type GitIgnore struct {
	patterns []giPattern
}

type giPattern struct {
	re       *regexp.Regexp // compiled regex
	negate   bool           // ! prefix — un-ignore
	dirOnly  bool           // trailing / — only match directories
	anchored bool           // pattern contains / — match relative to gitignore dir
}

// LoadGitIgnore reads and parses a .gitignore file from the given path.
// Returns nil (no error) if the file doesn't exist.
func LoadGitIgnore(path string) (*GitIgnore, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	gi := &GitIgnore{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p, err := parseGitIgnorePattern(line)
		if err != nil {
			// Skip patterns we can't compile — don't block the whole file
			continue
		}
		gi.patterns = append(gi.patterns, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Empty file is valid — no patterns
	if len(gi.patterns) == 0 {
		return nil, nil
	}
	return gi, nil
}

// Matches checks whether the given relative path (e.g. "cmd/late/main.go")
// should be ignored. isDir should be true for directories.
// Implements "last matching pattern wins" — negated patterns (!) can
// un-ignore paths matched by earlier patterns.
func (gi *GitIgnore) Matches(relPath string, isDir bool) bool {
	if gi == nil || len(gi.patterns) == 0 {
		return false
	}

	normalized := filepath.ToSlash(relPath)
	ignored := false

	for _, p := range gi.patterns {
		// Directory-only patterns don't match files
		if p.dirOnly && !isDir {
			continue
		}

		if p.re.MatchString(normalized) {
			ignored = !p.negate // negate flips, positive sets to true
		}
	}

	return ignored
}

// FindRepoRoot walks up from dir to find a directory containing a .git
// subdirectory. Returns the empty string if no repo root is found.
func FindRepoRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}

	current := abs
	for {
		info, err := os.Stat(filepath.Join(current, ".git"))
		if err == nil && info.IsDir() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding .git
			return ""
		}
		current = parent
	}
}

// ResolveRepoGitIgnore walks up from dir to find the repo root, then loads
// the .gitignore from that root. Returns the parsed GitIgnore and the repo
// root path (for computing relative paths).
func ResolveRepoGitIgnore(dir string) (*GitIgnore, string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}

	repoRoot := FindRepoRoot(absDir)
	if repoRoot == "" {
		return nil, "", nil // no repo found
	}

	gi, err := LoadGitIgnore(filepath.Join(repoRoot, ".gitignore"))
	return gi, repoRoot, err
}

// ──────────────────────────────────────────────
// Package-level CWD-keyed cache.
// The cache is invalidated automatically when the process CWD changes,
// enabling correct behavior across IDE workspace switches without requiring
// a daemon restart or manual invalidation.
// ──────────────────────────────────────────────

var (
	mu              sync.RWMutex
	cachedCWD       string
	cachedRepoRoot  string
	cachedGitIgnore *GitIgnore
)

// getRepoRoot returns the repo root and root .gitignore for the current CWD.
// Results are cached and keyed by CWD so that switching directories in a
// persistent IDE daemon automatically triggers a cache refresh.
func getRepoRoot() (string, *GitIgnore) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil
	}

	// Fast path: read-lock and check
	mu.RLock()
	if cwd == cachedCWD {
		root, gi := cachedRepoRoot, cachedGitIgnore
		mu.RUnlock()
		return root, gi
	}
	mu.RUnlock()

	// Slow path: acquire write lock and double-check
	mu.Lock()
	defer mu.Unlock()

	if cwd == cachedCWD {
		// Another goroutine already updated the cache
		return cachedRepoRoot, cachedGitIgnore
	}

	cachedCWD = cwd
	cachedRepoRoot = FindRepoRoot(cwd)
	cachedGitIgnore = nil
	if cachedRepoRoot != "" {
		gi, err := LoadGitIgnore(filepath.Join(cachedRepoRoot, ".gitignore"))
		if err == nil {
			cachedGitIgnore = gi
		}
	}
	return cachedRepoRoot, cachedGitIgnore
}

// getGitIgnoreForPath returns the gitignore and its originating directory
// applicable to the given search path.
//
// It first walks upward from searchPath looking for nested .gitignore files,
// which is essential for monorepos where sub-projects define their own ignore
// rules. If no nested .gitignore is found, it falls back to the CWD-keyed
// cached repo root .gitignore.
func getGitIgnoreForPath(searchPath string) (*GitIgnore, string) {
	absPath, err := filepath.Abs(searchPath)
	if err != nil {
		return nil, ""
	}

	// Prime the CWD-keyed cache so we know the repo root boundary.
	cachedRoot, _ := getRepoRoot()

	// Walk upward from searchPath looking for a nested .gitignore.
	// Return the closest one found (with its directory as the root so that
	// relative path computation in matchesGitIgnore is correct).
	dir := absPath
	for {
		gi, err := LoadGitIgnore(filepath.Join(dir, ".gitignore"))
		if err == nil && gi != nil {
			return gi, dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}

		// Stop at the repo root — the root-level .gitignore is already
		// cached and will be returned as the fallback below.
		if cachedRoot != "" && dir == cachedRoot {
			break
		}

		dir = parent
	}

	// Fall back to cached repo root .gitignore
	root, gi := getRepoRoot()
	return gi, root
}

// ResetGitIgnoreCache clears the CWD-keyed cache.
// Used in testing to force re-computation.
func ResetGitIgnoreCache() {
	mu.Lock()
	defer mu.Unlock()
	cachedCWD = ""
	cachedRepoRoot = ""
	cachedGitIgnore = nil
}

// matchesGitIgnore checks if a path is gitignored. A convenience wrapper that
// handles the nil guard and relative path computation. Returns false if no
// gitignore is loaded or the path can't be made relative.
func matchesGitIgnore(gi *GitIgnore, repoRoot, path string, isDir bool) bool {
	if gi == nil || repoRoot == "" {
		return false
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return false
	}
	return gi.Matches(rel, isDir)
}

// parseGitIgnorePattern parses a single non-empty, non-comment line from
// a .gitignore file into a compiled pattern.
func parseGitIgnorePattern(line string) (giPattern, error) {
	raw := line

	// Handle negation
	negate := false
	if strings.HasPrefix(raw, "!") {
		negate = true
		raw = raw[1:]
	}

	// Handle trailing / (directory-only match)
	dirOnly := false
	if strings.HasSuffix(raw, "/") {
		dirOnly = true
		raw = raw[:len(raw)-1]
	}

	// A pattern is "anchored" if it contains a / (which is not a trailing /,
	// which we already stripped). Anchored patterns match relative to the
	// directory containing the .gitignore file.
	anchored := strings.Contains(raw, "/")

	// Convert the gitignore glob to a regex.
	re, err := compileGitIgnoreGlob(raw, anchored, dirOnly)
	if err != nil {
		return giPattern{}, fmt.Errorf("invalid gitignore pattern %q: %w", line, err)
	}

	return giPattern{
		re:       re,
		negate:   negate,
		dirOnly:  dirOnly,
		anchored: anchored,
	}, nil
}

// compileGitIgnoreGlob converts a gitignore glob pattern to a compiled regex.
//
// Gitignore semantics (from git-scm.com/docs/gitignore):
//   - * matches anything except /
//   - ? matches any single character except /
//   - ** matches zero or more directories
//   - [...] character class (same as glob)
//   - [^...] or [!...] negated character class
//   - A leading / anchors the pattern to the gitignore dir
//   - A pattern without / can match at any depth (basename match)
//   - \ escapes the next character
func compileGitIgnoreGlob(pattern string, anchored, dirOnly bool) (*regexp.Regexp, error) {
	// Strip leading / (anchoring marker) — it doesn't participate in matching
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
		anchored = true
	}

	var sb strings.Builder
	sb.WriteByte('^')

	// For unanchored patterns, allow matching at any directory depth.
	// e.g., "foo" should match "foo", "dir/foo", "a/b/foo", etc.
	if !anchored {
		sb.WriteString("(.*/)?")
	}

	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch {
		case c == '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// ** — match zero or more directory levels
				sb.WriteString("(.*/)?")
				// Skip the second *
				i += 2
				// Skip a following / (since **/ already covers the separator)
				if i < len(pattern) && pattern[i] == '/' {
					i++
				}
			} else {
				// * — match anything except /
				sb.WriteString("[^/]*")
				i++
			}
		case c == '?':
			sb.WriteString("[^/]")
			i++
		case c == '\\':
			// Next character is literal
			if i+1 < len(pattern) {
				i++
				sb.WriteString(regexp.QuoteMeta(string(pattern[i])))
				i++
			} else {
				// Trailing backslash — treat as literal
				sb.WriteString(regexp.QuoteMeta(string(c)))
				i++
			}
		case c == '[':
			// Character class — find the closing ]
			j := i + 1
			// Inside bracket: handle negation, escaped chars
			var classSB strings.Builder
			classSB.WriteByte('[')
			if j < len(pattern) && pattern[j] == '!' {
				classSB.WriteByte('^')
				j++
			} else if j < len(pattern) && pattern[j] == '^' {
				// ^ has same meaning as ! inside brackets in gitignore
				classSB.WriteByte('^')
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				if pattern[j] == '\\' && j+1 < len(pattern) {
					j++
					classSB.WriteString(regexp.QuoteMeta(string(pattern[j])))
				} else {
					classSB.WriteString(regexp.QuoteMeta(string(pattern[j])))
				}
				j++
			}
			if j >= len(pattern) {
				// Unclosed bracket — treat as literal
				sb.WriteString(regexp.QuoteMeta(string(c)))
				i++
			} else {
				classSB.WriteByte(']')
				sb.WriteString(classSB.String())
				i = j + 1
			}
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}

	// For directory patterns, match the path as a directory prefix so that
	// "build" or "build/" matches "build/foo/bar.go" and "build/".
	// For file patterns, match only the exact path component.
	//
	// A matched directory means everything inside it is also ignored.
	// So if our pattern matches the directory itself, we also need to
	// match anything beneath it.
	//
	// e.g., pattern "build" with anchored=false, dirOnly=false:
	//   Matches:  "build", "build/foo.go", "dir/build/foo.go"
	//   Because the first component "build" is matched by our unanchored
	//   pattern "(.*/)?build(...)"
	//
	// We handle this by appending "(/.*)?$" instead of "$" for both
	// dirOnly and non-dirOnly patterns, because if a non-dir pattern
	// matches a directory name, the contents should also be ignored.
	sb.WriteString("(/.*)?")
	sb.WriteByte('$')

	return regexp.Compile(sb.String())
}
