package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a git repository at dir, mirroring the plugin package's
// git-fixture tests (which run `git init` directly and fail loudly).
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
}

// sameDir reports whether two paths refer to the same directory on disk,
// tolerating symlink-resolved aliases (e.g. macOS /tmp -> /private/tmp, which
// makes `git rev-parse --show-toplevel` report a different spelling than
// t.TempDir()).
func sameDir(t *testing.T, a, b string) bool {
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

// TestResolveContinueProjectDir_UsesRepoRoot guards the project resolution
// rule: --continue-project scopes to the git repository root of the current
// working directory, so it also works from inside a subdirectory.
func TestResolveContinueProjectDir_UsesRepoRoot(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)

	sub := filepath.Join(repo, "internal", "deep")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("creating subdirectory: %v", err)
	}
	t.Chdir(sub)

	dir, err := resolveContinueProjectDir()
	if err != nil {
		t.Fatalf("resolveContinueProjectDir(): %v", err)
	}
	if !sameDir(t, dir, repo) {
		t.Errorf("resolveContinueProjectDir() = %q, want the repo root %q", dir, repo)
	}
}

// TestResolveContinueProjectDir_FallsBackToCwdOutsideRepo guards the
// fallback: outside a git repository, the project is the working directory
// itself.
func TestResolveContinueProjectDir_FallsBackToCwdOutsideRepo(t *testing.T) {
	plain := t.TempDir()
	if root, ok := gitRepoRootForTest(t, plain); ok && !sameDir(t, root, plain) {
		t.Skipf("temp dir %s resolves inside git repo %s; cannot test the no-repo fallback here", plain, root)
	}
	t.Chdir(plain)

	dir, err := resolveContinueProjectDir()
	if err != nil {
		t.Fatalf("resolveContinueProjectDir(): %v", err)
	}
	if !sameDir(t, dir, plain) {
		t.Errorf("resolveContinueProjectDir() = %q, want the working directory %q", dir, plain)
	}
}

// gitRepoRootForTest exposes the raw repo-root probe so the fallback test can
// detect a temp directory that unexpectedly lives inside a repository.
func gitRepoRootForTest(t *testing.T, dir string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// TestResolveContinueProjectSession_FindsSessionFromSubdirectory covers the
// end-to-end --continue-project lookup: the session was started at the repo
// root, the user runs from a subdirectory, and the repo root (possibly a
// symlink-resolved spelling of the recorded path) matches via directory
// identity.
func TestResolveContinueProjectSession_FindsSessionFromSubdirectory(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)

	sub := filepath.Join(repo, "cmd", "late")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("creating subdirectory: %v", err)
	}

	sessionsDir := injectSessionDir(t)
	writeTestSession(t, sessionsDir, "session-20250101-100000", repo)

	t.Chdir(sub)

	meta, err := resolveContinueProjectSession()
	if err != nil {
		t.Fatalf("resolveContinueProjectSession(): %v", err)
	}
	if meta == nil {
		t.Fatal("resolveContinueProjectSession() returned nil, want the repo-root session found from a subdirectory")
	}
	if meta.ID != "session-20250101-100000" {
		t.Errorf("resolveContinueProjectSession() = %q, want session-20250101-100000", meta.ID)
	}
}

// TestResolveContinueProjectSession_IgnoresOtherProjects guards the scoping:
// sessions belonging to other projects are never returned, and a project with
// no recorded session resolves to (nil, nil) without an error.
func TestResolveContinueProjectSession_IgnoresOtherProjects(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)

	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("creating subdirectory: %v", err)
	}

	sessionsDir := injectSessionDir(t)
	otherProject := filepath.Join(sessionsDir, "other-project")
	if err := os.MkdirAll(otherProject, 0700); err != nil {
		t.Fatalf("creating other-project: %v", err)
	}
	writeTestSession(t, sessionsDir, "session-20250101-100000", otherProject)

	t.Chdir(sub)

	meta, err := resolveContinueProjectSession()
	if err != nil {
		t.Fatalf("resolveContinueProjectSession(): %v", err)
	}
	if meta != nil {
		t.Fatalf("resolveContinueProjectSession() = %+v, want nil when only other projects have sessions", meta)
	}
}

// TestValidateContinueFlags_MutuallyExclusive guards the --continue /
// --continue-project exclusivity rule.
func TestValidateContinueFlags_MutuallyExclusive(t *testing.T) {
	err := validateContinueFlags(true, true)
	if err == nil {
		t.Fatal("validateContinueFlags(true, true) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want it to mention mutual exclusivity", err)
	}
	for _, tc := range []struct{ cont, contProject bool }{
		{true, false},
		{false, true},
		{false, false},
	} {
		if err := validateContinueFlags(tc.cont, tc.contProject); err != nil {
			t.Errorf("validateContinueFlags(%v, %v) = %v, want nil", tc.cont, tc.contProject, err)
		}
	}
}
