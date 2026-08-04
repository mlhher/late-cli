package mcp

import (
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// ExpandEnvVars
// ---------------------------------------------------------------------------

func TestExpandEnvVars_NoVars(t *testing.T) {
	input := "plain-string-without-vars"
	result := ExpandEnvVars(input)
	if result != input {
		t.Errorf("expected %q, got %q", input, result)
	}
}

func TestExpandEnvVars_EmptyString(t *testing.T) {
	result := ExpandEnvVars("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestExpandEnvVars_BasicExpansion(t *testing.T) {
	os.Setenv("TEST_MCP_KEY", "my-secret-value")
	defer os.Unsetenv("TEST_MCP_KEY")

	result := ExpandEnvVars("${TEST_MCP_KEY}")
	if result != "my-secret-value" {
		t.Errorf("expected 'my-secret-value', got %q", result)
	}
}

func TestExpandEnvVars_MultipleVars(t *testing.T) {
	os.Setenv("TEST_HOST", "localhost")
	os.Setenv("TEST_PORT", "8080")
	defer os.Unsetenv("TEST_HOST")
	defer os.Unsetenv("TEST_PORT")

	result := ExpandEnvVars("http://${TEST_HOST}:${TEST_PORT}/api")
	if result != "http://localhost:8080/api" {
		t.Errorf("expected 'http://localhost:8080/api', got %q", result)
	}
}

func TestExpandEnvVars_MissingVar(t *testing.T) {
	// Unset to ensure it's missing
	os.Unsetenv("TEST_MISSING_VAR")

	result := ExpandEnvVars("prefix-${TEST_MISSING_VAR}-suffix")
	if result != "prefix--suffix" {
		t.Errorf("expected 'prefix--suffix', got %q", result)
	}
}

func TestExpandEnvVars_MixedWithStaticText(t *testing.T) {
	os.Setenv("TEST_API_KEY", "abc123")
	defer os.Unsetenv("TEST_API_KEY")

	result := ExpandEnvVars("Bearer ${TEST_API_KEY}")
	if result != "Bearer abc123" {
		t.Errorf("expected 'Bearer abc123', got %q", result)
	}
}

func TestExpandEnvVars_WithDollarSignOnly(t *testing.T) {
	os.Setenv("TEST_VAR", "value")
	defer os.Unsetenv("TEST_VAR")

	// $VAR (without braces) should NOT be expanded
	result := ExpandEnvVars("$TEST_VAR")
	if result != "$TEST_VAR" {
		t.Errorf("expected '$TEST_VAR' (no expansion for $ without braces), got %q", result)
	}
}

// ---------------------------------------------------------------------------
// ExpandServerEnvVars
// ---------------------------------------------------------------------------

func TestExpandServerEnvVars_AllFields(t *testing.T) {
	os.Setenv("TEST_CMD", "node")
	os.Setenv("TEST_SCRIPT", "server.js")
	os.Setenv("TEST_API_KEY", "key-123")
	os.Setenv("TEST_ENDPOINT", "https://api.example.com")
	defer os.Unsetenv("TEST_CMD")
	defer os.Unsetenv("TEST_SCRIPT")
	defer os.Unsetenv("TEST_API_KEY")
	defer os.Unsetenv("TEST_ENDPOINT")

	server := &MCPServer{
		Command: "${TEST_CMD}",
		Args:    []string{"${TEST_SCRIPT}", "--port", "8080"},
		URL:     "${TEST_ENDPOINT}/v1",
		Env: map[string]string{
			"API_KEY": "${TEST_API_KEY}",
			"STATIC":  "static-value",
		},
	}

	ExpandServerEnvVars(server)

	if server.Command != "node" {
		t.Errorf("expected Command 'node', got %q", server.Command)
	}
	if server.URL != "https://api.example.com/v1" {
		t.Errorf("expected URL 'https://api.example.com/v1', got %q", server.URL)
	}
	if len(server.Args) != 3 || server.Args[0] != "server.js" || server.Args[1] != "--port" || server.Args[2] != "8080" {
		t.Errorf("expected Args ['server.js', '--port', '8080'], got %v", server.Args)
	}
	if server.Env["API_KEY"] != "key-123" {
		t.Errorf("expected Env[API_KEY] = 'key-123', got %q", server.Env["API_KEY"])
	}
	if server.Env["STATIC"] != "static-value" {
		t.Errorf("expected Env[STATIC] = 'static-value', got %q", server.Env["STATIC"])
	}
}

func TestExpandServerEnvVars_EmptyFields(t *testing.T) {
	server := &MCPServer{
		Command: "",
		Args:    nil,
		URL:     "",
		Env:     nil,
	}

	ExpandServerEnvVars(server)

	if server.Command != "" {
		t.Errorf("expected empty Command, got %q", server.Command)
	}
	if server.URL != "" {
		t.Errorf("expected empty URL, got %q", server.URL)
	}
	if server.Env != nil {
		t.Errorf("expected nil Env, got %v", server.Env)
	}
}

func TestExpandServerEnvVars_NoVars(t *testing.T) {
	server := &MCPServer{
		Command: "npx",
		Args:    []string{"-y", "@scope/pkg"},
		URL:     "",
		Env: map[string]string{
			"KEY": "value",
		},
	}

	ExpandServerEnvVars(server)

	if server.Command != "npx" {
		t.Errorf("expected Command 'npx', got %q", server.Command)
	}
	if server.Args[0] != "-y" {
		t.Errorf("expected Args[0] '-y', got %q", server.Args[0])
	}
	if server.Env["KEY"] != "value" {
		t.Errorf("expected Env[KEY] 'value', got %q", server.Env["KEY"])
	}
}

func TestExpandServerEnvVars_MultipleVarsInOneField(t *testing.T) {
	os.Setenv("TEST_USER", "admin")
	os.Setenv("TEST_PASS", "secret")
	defer os.Unsetenv("TEST_USER")
	defer os.Unsetenv("TEST_PASS")

	server := &MCPServer{
		Command: "auth-${TEST_USER}-${TEST_PASS}",
	}

	ExpandServerEnvVars(server)

	expected := "auth-admin-secret"
	if server.Command != expected {
		t.Errorf("expected %q, got %q", expected, server.Command)
	}
}
