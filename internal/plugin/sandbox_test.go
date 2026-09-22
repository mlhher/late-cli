package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// sandboxUserConfig isolates a test from the developer's real user-level
// late configuration (~/Library/Application Support/late on macOS,
// ~/.config/late on Linux): every path late resolves through
// os.UserConfigDir() (pathutil.LateConfigDir, pathutil.LateSkillsDir,
// pluginStatePath, manager lateSkillsDir) lands inside a throwaway
// directory instead.
//
// It sets BOTH environment variables os.UserConfigDir() consults:
//   - HOME governs darwin/ios, where UserConfigDir() always returns
//     "$HOME/Library/Application Support" and XDG_CONFIG_HOME is ignored;
//   - XDG_CONFIG_HOME governs the remaining Unix targets.
//
// Callers must derive expected paths from the returned skillsDir (or from
// os.UserConfigDir() after this call), never from XDG alone.
func sandboxUserConfig(t *testing.T) (root string, skillsDir string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	configBase, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir after sandboxing: %v", err)
	}
	skillsDir = filepath.Join(configBase, "late", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir sandboxed skills dir: %v", err)
	}
	return root, skillsDir
}
