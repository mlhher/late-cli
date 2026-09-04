package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"late/internal/tool"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"late/internal/common"
)

// Client manages MCP connections and tools.
type Client struct {
	mu        sync.RWMutex
	sessions  map[string]*mcp.ClientSession
	tools     map[string]*ToolAdapter
	sdkClient *mcp.Client // single SDK client instance managing all sessions

	// serverConfigs records the MCPServer config each active session was
	// last connected with (pre-env-expansion), keyed by server name. Used
	// by Reconcile to detect a same-named server whose command/args/env/
	// url/dir changed, so it gets closed and reconnected instead of being
	// silently left on its old config.
	serverConfigs map[string]MCPServer

	// OnToolsChanged, if set, is called (without c.mu held) after
	// handleToolListChanged updates c.tools in response to a server's own
	// tools/list_changed notification. Callers use this to re-pull
	// GetTools() and refresh any downstream tool registry — otherwise a
	// live server's dynamic tool changes are only picked up incidentally,
	// whenever something else happens to re-call GetTools().
	OnToolsChanged func()
}

// NewClient creates a new MCP client with a single underlying SDK client
// instance managing all sessions. The ToolListChangedHandler is wired to
// re-discover tools when a server's tool list changes dynamically.
func NewClient() *Client {
	c := &Client{
		sessions:      make(map[string]*mcp.ClientSession),
		tools:         make(map[string]*ToolAdapter),
		serverConfigs: make(map[string]MCPServer),
	}
	c.sdkClient = mcp.NewClient(
		&mcp.Implementation{
			Name:    "late",
			Version: common.Version,
		},
		&mcp.ClientOptions{
			Capabilities: &mcp.ClientCapabilities{}, // suppress the SDK default "roots" capability
			ToolListChangedHandler: func(ctx context.Context, req *mcp.ToolListChangedRequest) {
				c.handleToolListChanged(ctx, req)
			},
		},
	)
	return c
}

// ToolAdapter adapts MCP tools to the Tool interface.
type ToolAdapter struct {
	mcpTool    *mcp.Tool
	session    *mcp.ClientSession
	serverName string // the MCP server name from mcp_config.json
	// name is the sanitized, length-limited, collision-free tool name
	// assigned at connect time; empty falls back to the computed form.
	name string
}

// Name returns the namespaced tool name in the form "{server}__{tool}",
// sanitized for OpenAI-compatible endpoints (only [A-Za-z0-9_-], capped
// at common.MaxToolNameLen). Namespacing prevents allowed_tools.json
// collisions when multiple MCP servers expose tools with the same bare
// name. When a sanitized name would still collide with another server's
// tool, Connect assigns a deterministic hash suffix before registering.
func (t *ToolAdapter) Name() string {
	if t.name != "" {
		return t.name
	}
	if t.serverName != "" {
		return common.NamespaceToolName(t.serverName, t.mcpTool.Name)
	}
	return common.SanitizeToolName(t.mcpTool.Name)
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
		switch c := content.(type) {
		case *mcp.TextContent:
			sb.WriteString(c.Text)
		case *mcp.ImageContent:
			sb.WriteString(fmt.Sprintf("[Image: %s]", c.MIMEType))
		case *mcp.AudioContent:
			sb.WriteString(fmt.Sprintf("[Audio: %s]", c.MIMEType))
		case *mcp.ResourceLink:
			sb.WriteString(fmt.Sprintf("[Resource: %s]", c.URI))
		case *mcp.EmbeddedResource:
			sb.WriteString("[Embedded resource]")
		default:
			// fmt.Fprintf(os.Stderr, "MCP tool returned unhandled content type: %T\n", content)
		}
	}

	output := sb.String()

	// Truncate to 32,768 Unicode characters — matching the built-in tool limit
	// (maxReadFileChars / maxBashOutputChars in internal/tool/implementations.go).
	// Slicing by rune rather than byte prevents splitting multi-byte UTF-8 sequences.
	const maxChars = 32768
	runes := []rune(output)
	if len(runes) > maxChars {
		output = string(runes[:maxChars]) + "\n\n[... truncated, output exceeded limit ...]"
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

// Connect establishes a connection to an MCP server using the shared SDK client.
// serverName is stored on each ToolAdapter so that tool names are namespaced
// as "{server}__{tool}" in allowed_tools.json, preventing collisions between
// servers that expose tools with the same bare name.
//
// Connecting to a server name that already has a session closes the old
// session first (reconnecting must not leak the previous subprocess).
func (c *Client) Connect(ctx context.Context, transport mcp.Transport, serverName string) error {
	// Close any previous session for the same server name before opening a
	// new one so reconnects never leak the old subprocess.
	c.mu.Lock()
	old := c.sessions[serverName]
	if old != nil {
		delete(c.sessions, serverName)
		for n, t := range c.tools {
			if t.serverName == serverName {
				delete(c.tools, n)
			}
		}
	}
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	session, err := c.sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	// Collect adapters without holding the lock; the SDK's Tools iterator may
	// perform RPCs, and we want to avoid blocking readers.
	var adapters []*ToolAdapter
	for tool := range session.Tools(ctx, &mcp.ListToolsParams{}) {
		if tool != nil {
			adapters = append(adapters, &ToolAdapter{
				mcpTool:    tool,
				session:    session,
				serverName: serverName,
			})
		}
	}

	// Assign collision-free names (avoiding names taken by other servers)
	// and store the session keyed by server name so Close() shuts down
	// every subprocess, not just the last one connected.
	c.assignToolNames(serverName, adapters)
	c.mu.Lock()
	c.sessions[serverName] = session
	for _, adapter := range adapters {
		c.tools[adapter.Name()] = adapter
	}
	c.mu.Unlock()

	return nil
}

// assignToolNames assigns each adapter a name that is unique across all
// currently registered tools (excluding this server's own, which are
// about to be replaced). Rare sanitization collisions get a deterministic
// hash suffix.
func (c *Client) assignToolNames(serverName string, adapters []*ToolAdapter) {
	c.mu.RLock()
	used := make(map[string]bool, len(c.tools))
	for n, t := range c.tools {
		if t.serverName != serverName {
			used[n] = true
		}
	}
	c.mu.RUnlock()

	bases := make([]string, len(adapters))
	for i, a := range adapters {
		bases[i] = a.Name()
	}
	uniq := common.DeduplicateToolNames(bases, used)
	for i, a := range adapters {
		a.name = uniq[i]
	}
}
// handleToolListChanged re-discovers tools for a server when the SDK notifies
// us of a tools/list change. It removes stale tool adapters for that server
// and re-enumerates via the session's paginating Tools iterator.
func (c *Client) handleToolListChanged(ctx context.Context, req *mcp.ToolListChangedRequest) {
	sess := req.Session

	// Map the SDK session back to the server name we assigned in Connect.
	c.mu.RLock()
	var serverName string
	for name, s := range c.sessions {
		if s == sess {
			serverName = name
			break
		}
	}
	c.mu.RUnlock()
	if serverName == "" {
		// fmt.Fprintf(os.Stderr, "MCP tool list changed notification for unknown session\n")
		return
	}

	// fmt.Printf("MCP server '%s' tools changed, re-discovering...\n", serverName)

	// Collect the new tool set without holding the lock; the SDK's Tools
	// iterator may perform RPCs.
	var adapters []*ToolAdapter
	for tool := range sess.Tools(ctx, &mcp.ListToolsParams{}) {
		if tool != nil {
			adapters = append(adapters, &ToolAdapter{
				mcpTool:    tool,
				session:    sess,
				serverName: serverName,
			})
		}
	}

	// Assign collision-free names, then remove stale tool adapters for this
	// server and register the new set.
	c.assignToolNames(serverName, adapters)
	c.mu.Lock()
	for name, t := range c.tools {
		if t.serverName == serverName {
			delete(c.tools, name)
		}
	}
	for _, adapter := range adapters {
		c.tools[adapter.Name()] = adapter
	}
	c.mu.Unlock()

	if c.OnToolsChanged != nil {
		c.OnToolsChanged()
	}
}

// GetTools returns all MCP tools as Tool interface instances.
func (c *Client) GetTools() []tool.Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tools := make([]tool.Tool, 0, len(c.tools))
	for _, t := range c.tools {
		tools = append(tools, t)
	}
	return tools
}

// GetTool returns a specific MCP tool by name.
func (c *Client) GetTool(name string) tool.Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[name]
	if !ok {
		return nil
	}
	return t
}

// Close closes all MCP connections.
func (c *Client) Close() error {
	c.mu.RLock()
	sessions := make([]*mcp.ClientSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.mu.RUnlock()

	for _, session := range sessions {
		if err := session.Close(); err != nil {
			// TODO: log session close error when global logger exists
		}
	}
	return nil
}

// lockedBuffer wraps a bytes.Buffer with a mutex so it can be safely written
// by a background goroutine and read by another goroutine on connection failure.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const maxSize = 8192
	if b.buf.Len() < maxSize {
		writeLen := len(p)
		if b.buf.Len()+writeLen > maxSize {
			writeLen = maxSize - b.buf.Len()
		}
		b.buf.Write(p[:writeLen])
	}
	// returning len(p) so the pipe keeps draining
	return len(p), nil
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// NewStdioTransport creates a new transport that communicates with a subprocess
// using the SDK's CommandTransport for proper process lifecycle (SIGTERM → wait → SIGKILL).
// Stderr is discarded to prevent MCP server output from bleeding into the TUI.
func NewStdioTransport(ctx context.Context, command string, args []string, env []string) (mcp.Transport, error) {
	return NewStdioTransportWithStderr(ctx, command, args, env, io.Discard, "")
}

// NewStdioTransportWithStderr is like NewStdioTransport but writes the subprocess's
// stderr to the provided writer instead of discarding it. This is used by
// ConnectFromConfig to buffer stderr for diagnostics on connection failure.
//
// When `workDir` is non-empty, the subprocess launches with `cmd.Dir = workDir`,
// so any relative paths in `args` (and any CWD-relative behavior the user
// scripts rely on) resolve against that directory. This matters for plugin
// servers shipped via npm-scoped guests whose script paths are plugin-relative.
func NewStdioTransportWithStderr(ctx context.Context, command string, args []string, env []string, stderr io.Writer, workDir string) (mcp.Transport, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = append(os.Environ(), env...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	serr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	go func() {
		io.Copy(stderr, serr)
	}()

	return &mcp.CommandTransport{
		Command: cmd,
	}, nil
}

// TransportForServer creates the appropriate mcp.Transport for a server config.
// Supported transport types:
//   - "stdio" (default when command is set) — local subprocess via CommandTransport
//   - "sse"   (default when url is set)   — remote SSE endpoint via SSEClientTransport
//   - "streamable-http"                   — remote streamable HTTP endpoint
//
// The server config should have already had environment variables expanded.
func TransportForServer(ctx context.Context, server *MCPServer) (mcp.Transport, error) {
	switch server.TransportType {
	case "sse":
		if server.URL == "" {
			return nil, fmt.Errorf("sse transport requires 'url'")
		}
		return &mcp.SSEClientTransport{
			Endpoint: server.URL,
		}, nil
	case "streamable-http":
		if server.URL == "" {
			return nil, fmt.Errorf("streamable-http transport requires 'url'")
		}
		return &mcp.StreamableClientTransport{
			Endpoint: server.URL,
		}, nil
	case "stdio":
		// handled below
	case "":
		// Infer from available fields: URL → remote, Command → stdio
		if server.URL != "" {
			return &mcp.SSEClientTransport{
				Endpoint: server.URL,
			}, nil
		}
	default:
		return nil, fmt.Errorf("unknown transport type: %q", server.TransportType)
	}

	// Stdio subprocess transport (explicit or default)
	if server.Command == "" {
		return nil, fmt.Errorf("server config must specify 'command' for stdio transport or 'url' for remote transport")
	}

	envSlice := make([]string, 0, len(server.Env))
	for k, v := range server.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	t, err := NewStdioTransportWithStderr(ctx, server.Command, server.Args, envSlice, io.Discard, server.Dir)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ConnectFromConfig connects to every enabled server in config. Servers
// that are already connected (same name) are skipped, so calling this
// with a config that includes previously-connected servers does not
// reconnect them — this is what lets main merge plugin servers into an
// already-connected user config without restarting every user server.
func (c *Client) ConnectFromConfig(ctx context.Context, config *MCPConfig) error {
	for name, server := range config.McpServers {
		if server.Disabled {
			// fmt.Printf("Skipping disabled MCP server: %s\n", name)
			continue
		}

		// Snapshot the desired config before env-var expansion so change
		// detection (here and in Reconcile) isn't sensitive to expansion
		// producing a different string each reload for the same inputs.
		desiredConfig := server

		c.mu.RLock()
		_, already := c.sessions[name]
		unchanged := already && mcpServerEqual(c.serverConfigs[name], desiredConfig)
		c.mu.RUnlock()
		if unchanged {
			continue // already connected with this exact config — don't reconnect
		}

		// Expand server variables in server configuration
		ExpandServerEnvVars(&server)

		var transport mcp.Transport
		var err error

		// For stdio transports, buffer stderr so we can include diagnostics if
		// the connection fails. Remote transports don't have stderr.
		if server.TransportType == "stdio" || (server.TransportType == "" && server.Command != "" && server.URL == "") {
			envSlice := make([]string, 0, len(server.Env))
			for k, v := range server.Env {
				envSlice = append(envSlice, k+"="+v)
			}
			var stderrBuf lockedBuffer
			transport, err = NewStdioTransportWithStderr(ctx, server.Command, server.Args, envSlice, &stderrBuf, server.Dir)
			if err == nil {
				err = c.Connect(ctx, transport, name)
			}
			if err != nil {
				// Give the stderr goroutine a moment to capture the error.
				time.Sleep(50 * time.Millisecond)
				if stderrBuf.Len() > 0 {
					return fmt.Errorf("failed to connect to server %s: %w\nstderr:\n%s", name, err, stderrBuf.String())
				}
				return fmt.Errorf("failed to connect to server %s: %w", name, err)
			}
		} else {
			transport, err = TransportForServer(ctx, &server)
			if err != nil {
				return fmt.Errorf("failed to create transport for server %s: %w", name, err)
			}

			if err := c.Connect(ctx, transport, name); err != nil {
				return fmt.Errorf("failed to connect to server %s: %w", name, err)
			}
		}

		c.mu.Lock()
		c.serverConfigs[name] = desiredConfig
		c.mu.Unlock()
	}

	return nil
}

// mcpServerEqual reports whether two MCPServer configs are equivalent for
// reconnect-detection purposes: same command/args/env/url/transport/dir.
// Disabled is deliberately excluded — callers handle enable/disable via
// the desired-set membership check, not this comparison.
func mcpServerEqual(a, b MCPServer) bool {
	if a.Command != b.Command || a.URL != b.URL || a.TransportType != b.TransportType || a.Dir != b.Dir {
		return false
	}
	if len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	if len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			return false
		}
	}
	return true
}

// Reconcile brings the connected server set in line with config: sessions
// for servers that are no longer present (or disabled) are closed, and
// new servers are connected. A server whose name is still present and
// enabled but whose command/args/env/url/dir changed is also closed so
// ConnectFromConfig reconnects it with the new config instead of leaving
// it running on the old one. Used by the plugin watcher so
// installing/removing/editing a plugin's MCP server takes effect without
// restarting Late.
func (c *Client) Reconcile(ctx context.Context, config *MCPConfig) error {
	desired := make(map[string]bool)
	for name, server := range config.McpServers {
		if !server.Disabled {
			desired[name] = true
		}
	}

	c.mu.Lock()
	var toClose []*mcp.ClientSession
	for name, s := range c.sessions {
		server, stillDesired := config.McpServers[name]
		changed := stillDesired && !server.Disabled && !mcpServerEqual(c.serverConfigs[name], server)
		if !desired[name] || changed {
			delete(c.sessions, name)
			delete(c.serverConfigs, name)
			toClose = append(toClose, s)
			for n, t := range c.tools {
				if t.serverName == name {
					delete(c.tools, n)
				}
			}
		}
	}
	c.mu.Unlock()

	for _, s := range toClose {
		_ = s.Close()
	}

	return c.ConnectFromConfig(ctx, config)
}
