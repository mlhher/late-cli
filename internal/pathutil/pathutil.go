package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
)

func LateConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "late"), nil
}

// LateDataDir returns the late data directory — the parent of every mutable
// data file (session histories, the compaction shadow log and record store,
// the critical-error log): ~/.local/share/late on Unix-likes. Windows keeps
// all app state under the config dir (AppData), exactly like LateSessionDir.
func LateDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		return LateConfigDir()
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".local", "share", "late"), nil
}

func LateSessionDir() (string, error) {
	lateDataDir, err := LateDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(lateDataDir, "sessions"), nil
}

// LateProjectMCPConfigPath returns the relative project-local MCP config
// location (".late/mcp_config.json"), resolved relative to process CWD.
func LateProjectMCPConfigPath() string {
	return filepath.Join(".late", "mcp_config.json")
}

func LateUserMCPConfigPath() (string, error) {
	lateConfigDir, err := LateConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(lateConfigDir, "mcp_config.json"), nil
}

func LateSkillsDir() (string, error) {
	lateConfigDir, err := LateConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(lateConfigDir, "skills"), nil
}

func LateProjectSkillsDir() string {
	return filepath.Join(".late", "skills")
}

func LatePluginsDir() (string, error) {
	lateConfigDir, err := LateConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(lateConfigDir, "plugins"), nil
}

func LateProjectPluginsDir() string {
	return filepath.Join(".late", "plugins")
}
