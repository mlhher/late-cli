package mcp

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolAdapterName_WithServerName(t *testing.T) {
	adapter := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "list_files"},
		serverName: "graph-rag",
	}
	want := "graph-rag:list_files"
	if got := adapter.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestToolAdapterName_WithoutServerName(t *testing.T) {
	adapter := &ToolAdapter{
		mcpTool: &sdkmcp.Tool{Name: "list_files"},
	}
	want := "list_files"
	if got := adapter.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestToolAdapterBareName(t *testing.T) {
	adapter := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "list_files"},
		serverName: "graph-rag",
	}
	want := "list_files"
	if got := adapter.BareName(); got != want {
		t.Errorf("BareName() = %q, want %q", got, want)
	}
}

// TestToolAdapterNameCollisionPrevention verifies that two MCP servers exposing
// a tool with the same bare name produce distinct registry keys.
func TestToolAdapterNameCollisionPrevention(t *testing.T) {
	a1 := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "list_files"},
		serverName: "graph-rag",
	}
	a2 := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "list_files"},
		serverName: "github",
	}
	if a1.Name() == a2.Name() {
		t.Errorf("two servers with the same bare tool name produced identical keys: %q", a1.Name())
	}
}

// TestBareNameDoesNotMatchNamespacedKey verifies that a legacy allowed_tools.json
// entry keyed by bare name ("list_files") does not match the namespaced key
// ("graph-rag:list_files"), so users are prompted for re-approval after upgrading.
func TestBareNameDoesNotMatchNamespacedKey(t *testing.T) {
	adapter := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "list_files"},
		serverName: "graph-rag",
	}

	// Simulate what LoadAllAllowedTools would return for an old config.
	legacyAllowed := map[string]bool{
		"list_files": true,
	}

	if legacyAllowed[adapter.Name()] {
		t.Errorf("legacy bare-name entry %q should not match namespaced key %q", "list_files", adapter.Name())
	}
}
