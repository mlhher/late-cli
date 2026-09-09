package mcp

import (
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// truncateOutput replicates the truncation logic in ToolAdapter.Execute so it
// can be tested independently without a live MCP server.
func truncateOutput(output string) string {
	const maxChars = 32768
	runes := []rune(output)
	if len(runes) > maxChars {
		return string(runes[:maxChars]) + "\n\n[... truncated, output exceeded limit ...]"
	}
	return output
}

func TestTruncateOutput_UnderLimit(t *testing.T) {
	input := strings.Repeat("a", 100)
	got := truncateOutput(input)
	if got != input {
		t.Errorf("expected output to pass through unchanged, got len=%d", len(got))
	}
}

func TestTruncateOutput_AtLimit(t *testing.T) {
	input := strings.Repeat("a", 32768)
	got := truncateOutput(input)
	if got != input {
		t.Errorf("expected output at exactly the limit to pass through unchanged")
	}
}

func TestTruncateOutput_OverLimit(t *testing.T) {
	input := strings.Repeat("a", 32769)
	got := truncateOutput(input)
	if !strings.HasSuffix(got, "\n\n[... truncated, output exceeded limit ...]") {
		t.Errorf("expected truncation marker at end of output, got: %q", got[len(got)-50:])
	}
	runes := []rune(got)
	// Should be exactly maxChars runes plus the marker
	marker := "\n\n[... truncated, output exceeded limit ...]"
	content := string(runes[:len(runes)-len([]rune(marker))])
	if len([]rune(content)) != 32768 {
		t.Errorf("expected 32768 content runes before marker, got %d", len([]rune(content)))
	}
}

// TestTruncateOutput_MultibyteUTF8 verifies that truncation never splits a
// multi-byte UTF-8 character. Each '日' is 3 bytes; byte-slicing at 32768
// would produce invalid UTF-8 if the boundary falls inside a character.
func TestTruncateOutput_MultibyteUTF8(t *testing.T) {
	// Build a string of 32769 Japanese characters (each 3 bytes = 98307 bytes total).
	input := strings.Repeat("日", 32769)
	got := truncateOutput(input)

	// Must be valid UTF-8
	if !strings.ContainsRune(got, '日') {
		t.Error("truncated output contains no valid Japanese characters")
	}

	// The truncated portion must end with the marker, not a broken character
	if !strings.HasSuffix(got, "\n\n[... truncated, output exceeded limit ...]") {
		t.Error("expected truncation marker after multi-byte content")
	}

	// Verify the result decodes cleanly — if any rune is utf8.RuneError it was split
	for i, r := range got {
		if r == '\uFFFD' {
			t.Errorf("invalid UTF-8 rune at position %d — character was split at boundary", i)
			break
		}
	}
}

func TestToolAdapterName_WithServerName(t *testing.T) {
	adapter := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "list_files"},
		serverName: "graph-rag",
	}
	want := "graph-rag__list_files"
	if got := adapter.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestToolAdapterName_WithInvalidChars(t *testing.T) {
	adapter := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "query:docs.v1"},
		serverName: "context.7",
	}
	want := "context_7__query_docs_v1"
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
// ("graph-rag__list_files"), so users are prompted for re-approval after upgrading.
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

func TestAssignToolNames_DeterministicOrderingAndCollisionSuffix(t *testing.T) {
	c := NewClient()

	// Server 1
	a1 := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "run"},
		serverName: "server.a",
	}
	c.assignToolNames("server.a", []*ToolAdapter{a1})
	c.tools[a1.Name()] = a1

	// Server 2 whose sanitized name collides with server 1 ("server_a__run")
	a2 := &ToolAdapter{
		mcpTool:    &sdkmcp.Tool{Name: "run"},
		serverName: "server_a",
	}
	c.assignToolNames("server_a", []*ToolAdapter{a2})

	if a1.Name() != "server_a__run" {
		t.Errorf("expected first tool to keep base name, got %q", a1.Name())
	}
	if a2.Name() == a1.Name() {
		t.Errorf("colliding tool must be disambiguated")
	}
	if !strings.HasPrefix(a2.Name(), "server_a__run-") {
		t.Errorf("expected hash suffix on colliding tool, got %q", a2.Name())
	}

	// Must be endpoint-safe [A-Za-z0-9_-]
	for _, r := range a2.Name() {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			t.Errorf("tool name contains invalid character %q: %q", r, a2.Name())
		}
	}
}

func TestGetTools_Sorted(t *testing.T) {
	c := NewClient()
	c.tools["z_tool"] = &ToolAdapter{name: "z_tool"}
	c.tools["a_tool"] = &ToolAdapter{name: "a_tool"}
	c.tools["m_tool"] = &ToolAdapter{name: "m_tool"}

	tools := c.GetTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	if tools[0].Name() != "a_tool" || tools[1].Name() != "m_tool" || tools[2].Name() != "z_tool" {
		t.Errorf("GetTools() not sorted alphabetically: %v, %v, %v", tools[0].Name(), tools[1].Name(), tools[2].Name())
	}
}
