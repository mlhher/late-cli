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
		// Use UserConfigDir on Windows to keep all app state under AppData.
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

// LateProjectMCPConfigPath returns the relative project-local MCP config
// location (".late/mcp_config.json"), resolved relative to process CWD.
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
