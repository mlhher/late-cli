package mcp

import (
	"encoding/json"
	"fmt"
	"late/internal/common"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MCPConfig represents the top-level configuration structure
type MCPConfig struct {
	McpServers map[string]MCPServer `json:"mcpServers"`
}

// MCPServer represents a single MCP server configuration
type MCPServer struct {
	// Stdio subprocess (default when Command is set):
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	// Remote server (used when URL is set):
	URL           string `json:"url"`
	TransportType string `json:"transportType,omitempty"` // "stdio", "sse", or "streamable-http"
	Disabled      bool   `json:"disabled,omitempty"`
	// Dir is set by Late (not serialized from JSON) when a plugin
	// supplies the server. When non-empty and the transport is stdio,
	// the subprocess is launched with `cmd.Dir = Dir` so that any
	// relative paths in Args resolve against the plugin's directory
	// even when the user's CWD is elsewhere. Keep this field out of
	// JSON tags so user-authored mcp_config.json files don't carry it.
	Dir string `json:"-"`
}

// LoadMCPConfig loads the MCP configuration from the first available config file
func LoadMCPConfig() (*MCPConfig, error) {
	configPath, err := findConfigPath()
	if err != nil {
		return nil, err
	}

	if configPath == "" {
		lateConfigDir, err := common.LateConfigDir()
		if err != nil {
			return &MCPConfig{McpServers: make(map[string]MCPServer)}, nil
		}

		defaultUserPath := filepath.Join(lateConfigDir, "mcp_config.json")

		// Pre-populate with a default config
		emptyConfig := MCPConfig{McpServers: make(map[string]MCPServer)}
		defaultData, _ := json.MarshalIndent(emptyConfig, "", "  ")

		if err := os.MkdirAll(lateConfigDir, 0700); err == nil {
			if err := os.WriteFile(defaultUserPath, defaultData, 0600); err != nil {
				// fmt.Fprintf(os.Stderr, "Warning: Could not write default MCP config to %s: %v\n", defaultUserPath, err)
			}
		}

		return &emptyConfig, nil
	}

	return loadConfigFromFile(configPath)
}

// findConfigPath searches for config files in order of precedence
func findConfigPath() (string, error) {
	// 1. Project-level: .late/mcp_config.json in current directory
	projectPath := common.LateProjectMCPConfigPath()
	if _, err := os.Stat(projectPath); err == nil {
		return projectPath, nil
	}

	// 2. User-level config path
	userPath, err := common.LateUserMCPConfigPath()
	if err != nil {
		return "", fmt.Errorf("failed to get config directory: %w", err)
	}

	if _, err := os.Stat(userPath); err == nil {
		return userPath, nil
	}

	// No config file found
	return "", nil
}

// loadConfigFromFile loads configuration from a specific file
func loadConfigFromFile(path string) (*MCPConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	// Ensure McpServers is not nil
	if config.McpServers == nil {
		config.McpServers = make(map[string]MCPServer)
	}

	return &config, nil
}

// ExpandEnvVars replaces ${VARIABLE} patterns with environment variable values
func ExpandEnvVars(value string) string {
	// Pattern to match ${VARIABLE_NAME}
	re := regexp.MustCompile(`\$\{([^}]+)\}`)

	return re.ReplaceAllStringFunc(value, func(match string) string {
		// Extract variable name from ${VARIABLE_NAME}
		varName := strings.TrimPrefix(strings.TrimSuffix(match, "}"), "${")
		return os.Getenv(varName)
	})
}

// ExpandServerEnvVars expands environment variables in server configuration
func ExpandServerEnvVars(server *MCPServer) {
	// Expand command
	server.Command = ExpandEnvVars(server.Command)
	// Expand URL
	server.URL = ExpandEnvVars(server.URL)
	// Expand args
	for i := range server.Args {
		server.Args[i] = ExpandEnvVars(server.Args[i])
	}
	// Expand env values
	for key, value := range server.Env {
		server.Env[key] = ExpandEnvVars(value)
	}
}
