package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/mcp"
)

// TestExamplePlugin_AllAPIs verifies that the plugin in ./example-plugin/
// consumes all available plugin APIs and that each surface behaves exactly
// according to the codebase's implementations.
func TestExamplePlugin_AllAPIs(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// wd is internal/plugin during `go test ./internal/plugin/...`
	// example-plugin is in the project root.
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	pluginDir := filepath.Join(repoRoot, "example-plugin")

	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("example-plugin directory not found at %s: %v", pluginDir, err)
	}

	// 1. LoadPlugin: tests manifest loading and validation
	p, err := LoadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("LoadPlugin failed: %v", err)
	}

	if p.Name != "example-plugin" {
		t.Errorf("expected name 'example-plugin', got %q", p.Name)
	}
	if p.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", p.Version)
	}

	// 2. ResolveSurfaces
	surfaces := p.ResolveSurfaces()
	if len(surfaces.Skills) != 1 {
		t.Errorf("expected 1 skill directory, got %d", len(surfaces.Skills))
	}
	if len(surfaces.Commands) != 2 {
		t.Errorf("expected 2 commands, got %d", len(surfaces.Commands))
	}
	if len(surfaces.Themes) != 1 {
		t.Errorf("expected 1 theme, got %d", len(surfaces.Themes))
	}
	if len(surfaces.MCPServers) != 1 {
		t.Errorf("expected 1 MCP server, got %d", len(surfaces.MCPServers))
	}

	// 3. PluginManager integration
	pm := NewPluginManager(t.TempDir())
	pm.Add(p)

	// 4. Skills: RegisterPluginSkills
	skillsDir := t.TempDir()
	if err := pm.RegisterPluginSkills(skillsDir); err != nil {
		t.Fatalf("RegisterPluginSkills failed: %v", err)
	}
	expectedSymlink := filepath.Join(skillsDir, "example-plugin:demo")
	if fi, err := os.Lstat(expectedSymlink); err != nil {
		t.Errorf("expected skill symlink at %s: %v", expectedSymlink, err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink", expectedSymlink)
	}

	// 5. Slash Commands: PluginCommands and HandleCommand
	cmds := pm.PluginCommands()
	if len(cmds) != 2 {
		t.Errorf("expected 2 plugin commands, got %d: %v", len(cmds), cmds)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 5a. Command with handler: /echo-cmd
	out, handled, err := pm.HandleCommand(ctx, "/echo-cmd", []string{"foo", "bar"})
	if err != nil {
		t.Errorf("HandleCommand(/echo-cmd) error: %v", err)
	}
	if !handled {
		t.Errorf("expected /echo-cmd to be handled")
	}
	if !strings.Contains(out, "echo-cmd received arguments") || !strings.Contains(out, "foo") {
		t.Errorf("unexpected output from /echo-cmd: %q", out)
	}

	// 5b. Bare command without handler: /bare-cmd (falls through to prompt)
	_, handled, err = pm.HandleCommand(ctx, "/bare-cmd", []string{"arg"})
	if err != nil {
		t.Errorf("HandleCommand(/bare-cmd) error: %v", err)
	}
	if handled {
		t.Errorf("expected bare command to return handled=false (fall through to plain prompt)")
	}

	// 6. Inline Tools: GetInlineTools and Runner
	inlineTools := pm.GetInlineTools(nil)
	if len(inlineTools) != 1 {
		t.Fatalf("expected 1 inline tool, got %d", len(inlineTools))
	}
	greetTool := inlineTools[0]
	if greetTool.Name != "example-plugin__greet" {
		t.Errorf("expected tool name 'example-plugin__greet', got %q", greetTool.Name)
	}

	toolResult, err := greetTool.Runner(ctx, client.ToolCall{
		Function: client.FunctionCall{
			Name:      greetTool.Name,
			Arguments: `{"name":"Tester"}`,
		},
	})
	if err != nil {
		t.Errorf("inline tool Runner error: %v", err)
	}
	if !strings.Contains(toolResult, "Hello, Tester!") {
		t.Errorf("unexpected inline tool result: %q", toolResult)
	}

	// 7. Themes: AllThemes and GetTheme
	allThemes := pm.AllThemes()
	if len(allThemes) != 1 {
		t.Fatalf("expected 1 theme, got %d", len(allThemes))
	}
	themeInfo, err := pm.GetTheme("example-plugin:demo")
	if err != nil {
		t.Fatalf("GetTheme error: %v", err)
	}
	if themeInfo.ID != "example-plugin:demo" {
		t.Errorf("expected theme ID 'example-plugin:demo', got %q", themeInfo.ID)
	}
	if themeInfo.Glamour == nil {
		t.Errorf("expected glamour overrides to be loaded")
	}

	// 8. Hooks
	// 8a. onSessionStart
	pm.CallOnSessionStartHooks()

	// 8b. onMessageSend: normal message passes through, special tag is transformed
	normalMsg := pm.HookedMessage(ctx, "Hello world")
	if normalMsg != "Hello world" {
		t.Errorf("expected normal message to pass through, got %q", normalMsg)
	}
	transformedMsg := pm.HookedMessage(ctx, "Testing [TEST_TRANSFORM]")
	if !strings.Contains(transformedMsg, "transformed by example-plugin") {
		t.Errorf("expected message to be transformed, got %q", transformedMsg)
	}

	// 8c. onToolCall: normal passes through, block_me vetos
	mws := pm.BuildHookMiddlewares()
	if len(mws) != 1 {
		t.Fatalf("expected 1 hook middleware, got %d", len(mws))
	}
	mockRunner := func(ctx context.Context, call client.ToolCall) (string, error) {
		return "tool success", nil
	}
	chained := mws[0](mockRunner)

	// Normal call passes through
	res, err := chained(ctx, client.ToolCall{
		Function: client.FunctionCall{Name: "some_tool", Arguments: `{"key":"val"}`},
	})
	if err != nil || res != "tool success" {
		t.Errorf("expected normal tool call to pass, got res=%q, err=%v", res, err)
	}

	// Blocked call is vetoed
	_, err = chained(ctx, client.ToolCall{
		Function: client.FunctionCall{Name: "some_tool", Arguments: `{"block_me":true}`},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked by plugin") {
		t.Errorf("expected tool call to be blocked, got err: %v", err)
	}

	// 8d. onToolResult: normal passes through, block_result vetos
	resultMWs := pm.BuildToolResultMiddlewares()
	if len(resultMWs) != 1 {
		t.Fatalf("expected 1 tool result middleware, got %d", len(resultMWs))
	}
	resultRunner := resultMWs[0](func(ctx context.Context, call client.ToolCall) (string, error) {
		return call.Function.Arguments, nil
	})
	// Normal result
	res, err = resultRunner(ctx, client.ToolCall{
		Function: client.FunctionCall{Name: "some_tool", Arguments: "normal output"},
	})
	if err != nil || res != "normal output" {
		t.Errorf("expected normal result to pass, got res=%q, err=%v", res, err)
	}
	// Blocked result
	_, err = resultRunner(ctx, client.ToolCall{
		Function: client.FunctionCall{Name: "some_tool", Arguments: `{"block_result":true}`},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked by plugin") {
		t.Errorf("expected tool result to be blocked, got err: %v", err)
	}

	// 9. MCP Server: Connect stdio transport, discover tool, execute tool
	mcpSrv, ok := surfaces.MCPServers["example-plugin:mcp-demo"]
	if !ok {
		t.Fatalf("expected MCPServer 'example-plugin:mcp-demo' in surfaces")
	}
	transport, err := mcp.NewStdioTransport(ctx, mcpSrv.Command, mcpSrv.Args, nil)
	if err != nil {
		t.Fatalf("NewStdioTransport error: %v", err)
	}
	mcpCli := mcp.NewClient()
	defer mcpCli.Close()
	if err := mcpCli.Connect(ctx, transport, "example-plugin:mcp-demo"); err != nil {
		t.Fatalf("mcpCli.Connect error: %v", err)
	}
	mcpTools := mcpCli.GetTools()
	if len(mcpTools) != 1 {
		t.Fatalf("expected 1 MCP tool, got %d", len(mcpTools))
	}
	mcpTool := mcpTools[0]
	mcpResult, err := mcpTool.Execute(ctx, json.RawMessage(`{"message":"testing mcp"}`))
	if err != nil {
		t.Fatalf("MCP tool execute error: %v", err)
	}
	if !strings.Contains(mcpResult, "MCP pong from example-plugin: testing mcp") {
		t.Errorf("unexpected MCP tool result: %q", mcpResult)
	}
}

// TestExamplePlugin_CLICommands tests installing via 'late plugin link',
// listing, enabling, disabling, and removing.
func TestExamplePlugin_CLICommands(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	pluginSrcDir := filepath.Join(repoRoot, "example-plugin")

	pluginsDir := t.TempDir()
	pm := NewPluginManager(pluginsDir)

	// Link plugin
	if !HandlePluginCommand(pm, []string{"link", pluginSrcDir}) {
		t.Fatal("HandlePluginCommand(link) returned false")
	}
	if pm.Count() != 1 {
		t.Fatalf("expected 1 plugin after link, got %d", pm.Count())
	}
	p := pm.Plugin("example-plugin")
	if p == nil {
		t.Fatal("expected 'example-plugin' to be registered")
	}
	if !p.Enabled {
		t.Error("expected linked plugin to be enabled by default")
	}

	// Disable plugin
	if !HandlePluginCommand(pm, []string{"disable", "example-plugin"}) {
		t.Fatal("HandlePluginCommand(disable) returned false")
	}
	if p.Enabled {
		t.Error("expected plugin to be disabled")
	}

	// Enable plugin
	if !HandlePluginCommand(pm, []string{"enable", "example-plugin"}) {
		t.Fatal("HandlePluginCommand(enable) returned false")
	}
	if !p.Enabled {
		t.Error("expected plugin to be re-enabled")
	}

	// Remove plugin
	if !HandlePluginCommand(pm, []string{"remove", "example-plugin"}) {
		t.Fatal("HandlePluginCommand(remove) returned false")
	}
	if pm.Count() != 0 {
		t.Fatalf("expected 0 plugins after remove, got %d", pm.Count())
	}
}
