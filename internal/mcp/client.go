package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"late/internal/tool"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"late/internal/common"
)

// Client manages MCP connections and tools.
type Client struct {
	sessions map[string]*mcp.ClientSession
	tools    map[string]*ToolAdapter
}

// NewClient creates a new MCP client.
func NewClient() *Client {
	return &Client{
		sessions: make(map[string]*mcp.ClientSession),
		tools:    make(map[string]*ToolAdapter),
	}
}

// ToolAdapter adapts MCP tools to the Tool interface.
type ToolAdapter struct {
	mcpTool    *mcp.Tool
	session    *mcp.ClientSession
	serverName string // the MCP server name from mcp_config.json
}

// Name returns the namespaced tool name in the form "{server}:{tool}".
// Namespacing prevents allowed_tools.json collisions when multiple MCP
// servers expose tools with the same bare name.
func (t *ToolAdapter) Name() string {
	if t.serverName != "" {
		return t.serverName + ":" + t.mcpTool.Name
	}
	return t.mcpTool.Name
}

// BareName returns the bare (unnamespaced) tool name as reported by the MCP
// server. Used by the tool-enable config check for backwards compatibility
// with configs written before namespacing was introduced.
func (t *ToolAdapter) BareName() string {
	return t.mcpTool.Name
}

// Description returns the tool description.
func (t *ToolAdapter) Description() string {
	return t.mcpTool.Description
}

// Parameters returns the tool parameters schema.
func (t *ToolAdapter) Parameters() json.RawMessage {
	paramsJSON, _ := json.Marshal(t.mcpTool.InputSchema)
	return json.RawMessage(paramsJSON)
}

// Execute executes the MCP tool.
func (t *ToolAdapter) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	result, err := t.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      t.mcpTool.Name,
		Arguments: params,
	})
	if err != nil {
		return "", err
	}

	// Convert result to string
	var sb strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			sb.WriteString(text.Text)
		} else if image, ok := content.(*mcp.ImageContent); ok {
			sb.WriteString(fmt.Sprintf("[Image: %s]", image.MIMEType))
		}
	}

	output := sb.String()

	// Truncate to ~1k tokens (~4000 chars)
	const maxChars = 4000
	if len(output) > maxChars {
		output = output[:maxChars] + "\n\n[... truncated, output exceeded limit ...]"
	}

	return output, nil
}

// RequiresConfirmation always returns true for MCP tools.
func (t *ToolAdapter) RequiresConfirmation(args json.RawMessage) bool {
	return true
}

// CallString returns a string representation for calling the tool.
func (t *ToolAdapter) CallString(args json.RawMessage) string {
	return fmt.Sprintf("Calling MCP tool '%s'...", t.Name())
}

// Connect establishes a connection to an MCP server.
// serverName is stored on each ToolAdapter so that tool names are namespaced
// as "{server}:{tool}" in allowed_tools.json, preventing collisions between
// servers that expose tools with the same bare name.
func (c *Client) Connect(ctx context.Context, transport mcp.Transport, serverName string) error {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "late",
		Version: common.Version,
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	// Store session keyed by server name so Close() shuts down every
	// subprocess, not just the last one connected.
	c.sessions[serverName] = session

	// List and store tools using iterator
	for tool := range session.Tools(ctx, &mcp.ListToolsParams{}) {
		if tool != nil {
			adapter := &ToolAdapter{
				mcpTool:    tool,
				session:    session,
				serverName: serverName,
			}
			c.tools[adapter.Name()] = adapter
		}
	}

	return nil
}

// GetTools returns all MCP tools as Tool interface instances.
func (c *Client) GetTools() []tool.Tool {
	tools := make([]tool.Tool, 0, len(c.tools))
	for _, t := range c.tools {
		tools = append(tools, t)
	}
	return tools
}

// GetTool returns a specific MCP tool by name.
func (c *Client) GetTool(name string) tool.Tool {
	t, ok := c.tools[name]
	if !ok {
		return nil
	}
	return t
}

// Close closes all MCP connections.
func (c *Client) Close() error {
	for name, session := range c.sessions {
		if err := session.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing MCP session '%s': %v\n", name, err)
		}
	}
	return nil
}

// NewStdioTransport creates a new transport that communicates with a subprocess.
func NewStdioTransport(ctx context.Context, command string, args []string, env []string) (mcp.Transport, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = append(os.Environ(), env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Discard stderr to prevent output from bleeding into TUI
	go func() {
		io.Copy(io.Discard, stderr)
	}()

	// Kill the subprocess when the context is cancelled.
	go func() {
		<-ctx.Done()
		cmd.Process.Kill()
	}()

	return &mcp.IOTransport{
		Reader: stdout,
		Writer: stdin,
	}, nil
}

func (c *Client) ConnectFromConfig(ctx context.Context, config *MCPConfig) error {
	for name, server := range config.McpServers {
		if server.Disabled {
			fmt.Printf("Skipping disabled MCP server: %s\n", name)
			continue
		}

		// Expand environment variables in server configuration
		ExpandServerEnvVars(&server)

		// Convert server.Env map to KEY=VALUE slice for the subprocess
		envSlice := make([]string, 0, len(server.Env))
		for k, v := range server.Env {
			envSlice = append(envSlice, k+"="+v)
		}

		// Create transport for this server
		transport, err := NewStdioTransport(ctx, server.Command, server.Args, envSlice)
		if err != nil {
			return fmt.Errorf("failed to create transport for server %s: %w", name, err)
		}

		// Connect to the server, passing the server name so tools are registered
		// with namespaced names ("{server}:{tool}") in the tool registry and
		// allowed_tools.json.
		if err := c.Connect(ctx, transport, name); err != nil {
			return fmt.Errorf("failed to connect to server %s: %w", name, err)
		}
	}

	return nil
}
