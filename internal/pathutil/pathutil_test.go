package pathutil_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"late/internal/common"
	"late/internal/compaction"
	"late/internal/pathutil"
)

// TestDataPathsShareLateDataDir pins the LateDataDir unification (commit
// ed7465a): the session dir, the compaction shadow log, the compaction
// record store, and the critical-error log all resolve inside ONE data dir
// — pathutil.LateDataDir — so late's mutable data never scatters across two
// roots. On Windows that root is the config dir (all app state under
// AppData, exactly like LateSessionDir's historical windows branch); on
// Unix-likes it is ~/.local/share/late.
func TestDataPathsShareLateDataDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		// Deterministic home on Unix-likes; windows keeps its documented
		// branches (config-dir root) without env surgery.
		t.Setenv("HOME", t.TempDir())
	}

	dataDir, err := pathutil.LateDataDir()
	if err != nil {
		t.Fatalf("LateDataDir() error = %v", err)
	}
	sessionDir, err := pathutil.LateSessionDir()
	if err != nil {
		t.Fatalf("LateSessionDir() error = %v", err)
	}
	shadowPath, err := compaction.DefaultShadowPath()
	if err != nil {
		t.Fatalf("DefaultShadowPath() error = %v", err)
	}
	storePath, err := compaction.DefaultStorePath()
	if err != nil {
		t.Fatalf("DefaultStorePath() error = %v", err)
	}
	errorLogPath, err := common.DefaultErrorLogPath()
	if err != nil {
		t.Fatalf("DefaultErrorLogPath() error = %v", err)
	}

	// Every data file lives directly in the shared data dir (the session
	// dir is the dir's one subdirectory).
	if got := filepath.Dir(sessionDir); got != dataDir || filepath.Base(sessionDir) != "sessions" {
		t.Errorf("LateSessionDir() = %q, want <LateDataDir>/sessions with LateDataDir %q", sessionDir, dataDir)
	}
	for name, p := range map[string]string{
		"DefaultShadowPath":   shadowPath,
		"DefaultStorePath":    storePath,
		"DefaultErrorLogPath": errorLogPath,
	} {
		if got := filepath.Dir(p); got != dataDir {
			t.Errorf("%s() = %q, want it directly inside LateDataDir %q", name, p, dataDir)
		}
	}

	// Platform semantics: one root for everything, per GOOS.
	switch runtime.GOOS {
	case "windows":
		cfgDir, err := pathutil.LateConfigDir()
		if err != nil {
			t.Fatalf("LateConfigDir() error = %v", err)
		}
		if dataDir != cfgDir {
			t.Errorf("LateDataDir() = %q, want the config dir %q on windows (all app state under AppData)", dataDir, cfgDir)
		}
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home dir available: %v", err)
		}
		if want := filepath.Join(home, ".local", "share", "late"); dataDir != want {
			t.Errorf("LateDataDir() = %q, want %q", dataDir, want)
		}
	}
}
