package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepoForTest creates a git repository at dir, mirroring the plugin
// package's git-fixture tests (which run `git init` directly and fail loudly).
func initRepoForTest(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
}

// sameDirForTest reports whether two paths refer to the same directory on
// disk, tolerating symlink-resolved aliases (macOS /tmp -> /private/tmp makes
// `git rev-parse --show-toplevel` report a different spelling than
// t.TempDir()).
func sameDirForTest(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		t.Fatalf("Stat(%s): %v", a, err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatalf("Stat(%s): %v", b, err)
	}
	return os.SameFile(ai, bi)
}

// TestRepoRoot_InsideRepo guards the repo-root resolution: from the repo root
// and from a nested subdirectory alike, RepoRoot must report the repository
// root.
func TestRepoRoot_InsideRepo(t *testing.T) {
	repo := t.TempDir()
	initRepoForTest(t, repo)

	// From the repo root itself.
	root, ok := RepoRoot(repo)
	if !ok {
		t.Fatalf("RepoRoot(%q) ok = false, want the repo root", repo)
	}
	if !sameDirForTest(t, root, repo) {
		t.Errorf("RepoRoot(%q) = %q, want the repository root", repo, root)
	}

	// From a nested subdirectory.
	sub := filepath.Join(repo, "internal", "deep")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", sub, err)
	}
	root, ok = RepoRoot(sub)
	if !ok {
		t.Fatalf("RepoRoot(%q) ok = false, want the repo root", sub)
	}
	if !sameDirForTest(t, root, repo) {
		t.Errorf("RepoRoot(%q) = %q, want the repository root", sub, root)
	}
}

// TestRepoRoot_OutsideRepo guards the not-a-repo handling: outside a git
// repository (and when git is unavailable) RepoRoot must report false with an
// empty root instead of an error or a bogus path.
func TestRepoRoot_OutsideRepo(t *testing.T) {
	plain := t.TempDir()
	if root, ok := probeRepoRoot(t, plain); ok && !sameDirForTest(t, root, plain) {
		t.Skipf("temp dir %s resolves inside git repo %s; cannot test the not-a-repo case here", plain, root)
	}

	root, ok := RepoRoot(plain)
	if ok {
		t.Errorf("RepoRoot(%q) ok = true (root %q), want false outside a git repository", plain, root)
	}
	if root != "" {
		t.Errorf("RepoRoot(%q) root = %q, want empty outside a git repository", plain, root)
	}
}

// probeRepoRoot exposes the raw repo-root probe so the not-a-repo test can
// detect a temp directory that unexpectedly lives inside a repository.
func probeRepoRoot(t *testing.T, dir string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
