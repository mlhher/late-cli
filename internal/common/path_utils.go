package common

import (
	"late/internal/pathutil"
)

func LateConfigDir() (string, error) {
	return pathutil.LateConfigDir()
}

func LateSessionDir() (string, error) {
	return pathutil.LateSessionDir()
}

// LateProjectMCPConfigPath returns the relative project-local MCP config
// location (".late/mcp_config.json"), resolved relative to process CWD.
func LateProjectMCPConfigPath() string {
	return pathutil.LateProjectMCPConfigPath()
}

func LateUserMCPConfigPath() (string, error) {
	return pathutil.LateUserMCPConfigPath()
}
