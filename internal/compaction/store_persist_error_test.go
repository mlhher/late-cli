package compaction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/common"
)

// TestStorePersistFailureIsLogged pins the diagnosability contract of the
// file-backed store: an append that fails (unwritable destination, disk
// full) cannot return an error through Put/PutRecord, but it must never be
// SILENT — the record stays memory-only and the pointers that reference it
// die at the next restart, so the failure belongs in the critical-error log.
func TestStorePersistFailureIsLogged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "late-errors.log")
	l, err := common.OpenErrorLogAt(logPath)
	if err != nil {
		t.Fatal(err)
	}
	common.SetErrorLog(l)
	t.Cleanup(func() { common.SetErrorLog(nil) })

	// The store's backing path is a DIRECTORY: the O_CREATE|O_WRONLY open
	// fails with EISDIR on every append.
	dirPath := filepath.Join(t.TempDir(), "store-as-directory")
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	s := NewStore()
	s.path = dirPath

	s.Put("r:deadbeef", "original text")

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("critical-error log unreadable: %v", err)
	}
	if !strings.Contains(string(logged), "compaction-store") || !strings.Contains(string(logged), dirPath) {
		t.Fatalf("critical-error log missing the store append failure:\n%s", logged)
	}
}
