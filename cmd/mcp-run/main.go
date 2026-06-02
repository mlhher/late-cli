package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"late/internal/mcp"
)

func main() {
	serverCmd := flag.String("server", "", "Command to run the MCP server (e.g., 'node /path/to/server.js')")
	toolName := flag.String("tool", "", "Name of the tool to execute")
	toolArgs := flag.String("args", "{}", "JSON arguments for the tool")
	envVars := flag.String("env", "", "Comma-separated environment variables (KEY=VALUE,KEY2=VALUE2)")
	listTools := flag.Bool("list", false, "List available tools")

	flag.Parse()

	if *serverCmd == "" {
		fmt.Println("Error: --server is required")
		flag.Usage()
		os.Exit(1)
	}

	// Parse server command
	parts := strings.Fields(*serverCmd)
	if len(parts) == 0 {
		fmt.Println("Error: invalid server command")
		os.Exit(1)
	}
	cmd := parts[0]
	args := parts[1:]

	// Parse env vars
	var env []string
	if *envVars != "" {
		env = strings.Split(*envVars, ",")
	}

	// Create transport
	ctx := context.Background()
	transport, err := mcp.NewStdioTransport(ctx, cmd, args, env)
	if err != nil {
		fmt.Printf("Error creating transport: %v\n", err)
		os.Exit(1)
	}

	// Create client
	client := mcp.NewClient()
	defer client.Close()

	fmt.Println("Connecting to server...")
	// "default" is used as the server name for ad-hoc single-server connections
	// that are not configured via mcp_config.json.
	if err := client.Connect(ctx, transport, "default"); err != nil {
		fmt.Printf("Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Connected!")

	if *listTools {
		tools := client.GetTools()
		fmt.Printf("Available tools (%d):\n", len(tools))
		for _, t := range tools {
			fmt.Printf("- %s: %s\n", t.Name(), t.Description())
			fmt.Printf("  Params: %s\n", string(t.Parameters()))
		}
		return
	}

	if *toolName == "" {
		fmt.Println("Error: --tool or --list is required")
		flag.Usage()
		os.Exit(1)
	}

	// Get tool
	tool := client.GetTool(*toolName)
	if tool == nil {
		fmt.Printf("Error: tool '%s' not found\n", *toolName)
		os.Exit(1)
	}

	// Parse args
	var arguments json.RawMessage
	if err := json.Unmarshal([]byte(*toolArgs), &arguments); err != nil {
		fmt.Printf("Error parsing JSON arguments: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Executing tool '%s' with args: %s\n", *toolName, *toolArgs)
	result, err := tool.Execute(ctx, arguments)
	if err != nil {
		fmt.Printf("Error executing tool: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nResult:")
	fmt.Println(result)
}
