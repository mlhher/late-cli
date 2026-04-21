package common

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

func LateSessionDir() (string, error) {
	if runtime.GOOS == "windows" {
		configDir, err := LateConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(configDir, "sessions"), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".local", "share", "late", "sessions"), nil
}

// LateProjectMCPConfigPath returns the project-local MCP config path.
// The returned path is relative and is resolved by the caller.
func LateProjectMCPConfigPath() string {
	return filepath.Join(".late", "mcp_config.json")
}

func LateUserMCPConfigPath() (string, error) {
	configDir, err := LateConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "mcp_config.json"), nil
}
