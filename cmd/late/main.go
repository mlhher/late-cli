package main

import (
	"context"
	"flag"
	"fmt"
	"late/internal/agent"
	"late/internal/common"
	"late/internal/executor"
	"late/internal/orchestrator"
	"os"
	"path/filepath"
	"strings"
	"time"

	"late/internal/assets"
	"late/internal/client"
	appconfig "late/internal/config"
	"late/internal/mcp"
	"late/internal/session"
	"late/internal/tool"
	"late/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
)

func main() {
	// Parse flags
	helpReq := flag.Bool("help", false, "Show help")
	systemPromptReq := flag.String("system-prompt", "", "Set the system prompt (literal string)")
	systemPromptFileReq := flag.String("system-prompt-file", "", "Set the system prompt from a file")
	useToolsReq := flag.Bool("use-tools", true, "Enable tool usage (allows LLM to call tools)")
	enableBashReq := flag.Bool("enable-bash", true, "Enable bash tool execution")
	injectCWDReq := flag.Bool("inject-cwd", true, "Replace ${{CWD}} in system prompt with current working directory")
	enableSubagentsReq := flag.Bool("enable-subagents", true, "Enable subagent usage")
	enableAskToolReq := flag.Bool("enable-ask-tool", false, "Enable ask tool for user input")
	enableNewSessionReq := flag.Bool("new-session", false, "Delete prior session history and start with a clean chat window")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of late:\n")
		fmt.Fprintf(os.Stderr, "  late [flags]\n")
		fmt.Fprintf(os.Stderr, "  late session <command> [args]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  session list     List all saved sessions\n")
		fmt.Fprintf(os.Stderr, "  session load <id>  Load a session by ID\n")
		fmt.Fprintf(os.Stderr, "  session delete <id>  Delete a session by ID\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *helpReq {
		flag.Usage()
		return
	}

	var loadedHistoryPath string
	if flag.NArg() > 0 && flag.Arg(0) == "session" {
		path, shouldExit := handleSessionCommand(flag.Args()[1:])
		if shouldExit {
			return
		}
		loadedHistoryPath = path
	}

	// Determine system prompt
	// Priority: --system-prompt-file > --system-prompt > LATE_SYSTEM_PROMPT env var
	var systemPrompt string

	if *systemPromptFileReq != "" {
		content, err := os.ReadFile(*systemPromptFileReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading system prompt file: %v\n", err)
			os.Exit(1)
		}
		systemPrompt = string(content)
	} else if *systemPromptReq != "" {
		systemPrompt = *systemPromptReq
	} else if envPrompt := os.Getenv("LATE_SYSTEM_PROMPT"); envPrompt != "" {
		systemPrompt = envPrompt
	} else {
		content, err := assets.PromptsFS.ReadFile("prompts/instruction-planning.md")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Fatal error: could not load embedded planning prompt: %v\n", err)
			os.Exit(1)
		}
		systemPrompt = string(content)
	}

	if *injectCWDReq {
		cwd, err := os.Getwd()
		if err == nil {
			systemPrompt = common.ReplacePlaceholders(systemPrompt, map[string]string{
				"${{CWD}}": cwd,
			})
		}
	}

	if !*enableBashReq {
		systemPrompt = common.ReplacePlaceholders(systemPrompt,
			map[string]string{
				"${{NOTICE}}": "Bash is disabled. You must not attempt to use execute any bash commands. Doing so will result in an error.",
			})
	}

	fmt.Println("Starting late TUI...")

	// Define history path with timestamp-based session ID
	sessionsDir, err := session.SessionDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get session directory: %v\n", err)
		os.Exit(1)
	}
	sessionID := fmt.Sprintf("session-%s", time.Now().Format("20060102-150405"))
	historyPath := filepath.Join(sessionsDir, sessionID+".json")

	if loadedHistoryPath != "" {
		historyPath = loadedHistoryPath
	}

	// Delete prior session history if --new-session is set
	if *enableNewSessionReq {
		// Delete all session files
		entries, err := os.ReadDir(sessionsDir)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to list sessions dir: %v\n", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				if err := os.Remove(filepath.Join(sessionsDir, entry.Name())); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: Failed to delete %s: %v\n", entry.Name(), err)
				}
			}
		}
		// Also delete metadata files
		entries, err = os.ReadDir(sessionsDir)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to list sessions dir: %v\n", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".meta.json") {
				if err := os.Remove(filepath.Join(sessionsDir, entry.Name())); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: Failed to delete %s: %v\n", entry.Name(), err)
				}
			}
		}
		fmt.Fprintf(os.Stderr, "Deleted all session history\n")
	}

	// Load existing history
	history, err := session.LoadHistory(historyPath)
	if err != nil {
		history = []client.ChatMessage{}
	}

	// Initialize Core Components
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	cfg := client.Config{BaseURL: baseURL}
	c := client.NewClient(cfg)

	// Initialize MCP client
	mcpClient := mcp.NewClient()
	defer mcpClient.Close()

	// Load MCP configuration
	config, err := mcp.LoadMCPConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to load MCP config: %v\n", err)
	}

	// Try configuration-driven connections first
	if config != nil && len(config.McpServers) > 0 {
		fmt.Println("Connecting to MCP servers from configuration...")
		if err := mcpClient.ConnectFromConfig(context.Background(), config); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to connect to some MCP servers: %v\n", err)
		}
	}

	// Load App configuration
	appConfig, err := appconfig.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to load app config: %v\n", err)
	}
	enabledTools := make(map[string]bool)
	if appConfig != nil {
		for toolName, enabled := range appConfig.EnabledTools {
			enabledTools[toolName] = enabled
		}
	}

	// Flag overrides
	if !*enableBashReq {
		enabledTools["bash"] = false
	}
	if !*enableAskToolReq {
		enabledTools["ask"] = false
	}

	sess := session.New(c, historyPath, history, systemPrompt, *useToolsReq)
	executor.RegisterStandardTools(sess.Registry, enabledTools)

	// Register MCP tools into the session registry
	for _, t := range mcpClient.GetTools() {
		if enabledTools[t.Name()] {
			sess.Registry.Register(t)
		}
	}

	// Initialize common renderer
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(tui.LateTheme),
		glamour.WithWordWrap(80),
		glamour.WithPreservedNewLines(),
	)

	// Create root orchestrator
	// We'll add middlewares later once the program is started
	rootAgent := orchestrator.NewBaseOrchestrator("main", sess, nil)

	model := tui.NewModel(rootAgent, renderer)
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Wire TUI integration
	go func() {
		// Set messenger first
		p.Send(tui.SetMessengerMsg{Messenger: p})

		// Create context with InputProvider
		ctx := context.WithValue(context.Background(), common.InputProviderKey, tui.NewTUIInputProvider(p))
		rootAgent.SetContext(ctx)

		// Set middlewares (e.g. TUI confirmation)
		rootAgent.SetMiddlewares([]common.ToolMiddleware{
			tui.TUIConfirmMiddleware(p, sess.Registry),
		})

		// Start forwarding events from the root agent to the TUI
		ForwardOrchestratorEvents(p, rootAgent)
	}()

	if *enableSubagentsReq {
		runner := func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			child, err := agent.NewSubagentOrchestrator(c, goal, ctxFiles, agentType, enabledTools, *injectCWDReq, rootAgent)
			if err != nil {
				return "", err
			}

			// Inherit TUI connection from parent
			// (This needs better wiring, maybe the Orchestrator hierarchy handles this automatically?)
			// For now, let's just execute the goal and wait.
			res, err := child.Execute("")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("The subagent successfully completed its task. Final result:\n\n%s", res), nil
		}

		sess.Registry.Register(tool.SpawnSubagentTool{
			Runner: runner,
		})
	}

	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

// handleSessionCommand processes session subcommands
func handleSessionCommand(args []string) (string, bool) {
	if len(args) == 0 {
		fmt.Println("Usage: late session <list|load|delete> [args...]")
		fmt.Println("")
		fmt.Println("Commands:")
		fmt.Println("  list           List all saved sessions")
		fmt.Println("  load <id>      Load a session by ID (can use prefix)")
		fmt.Println("  delete <id>    Delete a session by ID")
		return "", true
	}

	switch args[0] {
	case "list":
		handleSessionList()
		return "", true
	case "load":
		if len(args) < 2 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: late session load <id>")
			os.Exit(1)
		}
		return handleSessionLoad(args[1]), false
	case "delete":
		if len(args) < 2 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: late session delete <id>")
			os.Exit(1)
		}
		handleSessionDelete(args[1])
		return "", true
	default:
		fmt.Printf("Unknown session command: %s\n", args[0])
		handleSessionCommand([]string{})
		return "", true
	}
}

// handleSessionList displays all saved sessions
func handleSessionList() {
	metas, err := session.ListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing sessions: %v\n", err)
		os.Exit(1)
	}

	if len(metas) == 0 {
		fmt.Println("No sessions found.")
		fmt.Println("")
		fmt.Println("Use 'late session load <id>' to load a saved session or start a new session with 'late'.")
		return
	}

	fmt.Println("Available sessions:")
	fmt.Println("")
	for _, meta := range metas {
		fmt.Println(session.FormatSessionDisplay(meta))
		fmt.Println("")
	}
	fmt.Println(session.FormatResumePrompt())
}

// handleSessionLoad returns the history path for the given session ID
func handleSessionLoad(id string) string {
	meta, err := session.LoadSessionMeta(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading session: %v\n", err)
		os.Exit(1)
	}
	if meta == nil {
		fmt.Fprintf(os.Stderr, "Session not found: %s\n", id)
		fmt.Println("")
		fmt.Println("Use 'late session list' to see available sessions.")
		os.Exit(1)
	}

	fmt.Printf("Resuming session: %s (%s)\n", meta.ID, meta.Title)
	time.Sleep(500 * time.Millisecond) // Give user a moment to see what's happening
	return meta.HistoryPath
}

// handleSessionDelete removes a session
func handleSessionDelete(id string) {
	meta, err := session.LoadSessionMeta(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading session: %v\n", err)
		os.Exit(1)
	}
	if meta == nil {
		fmt.Fprintf(os.Stderr, "Session not found: %s\n", id)
		fmt.Println("")
		fmt.Println("Use 'late session list' to see available sessions.")
		os.Exit(1)
	}

	// Delete metadata
	sessionsDir, err := session.SessionDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting session directory: %v\n", err)
		os.Exit(1)
	}
	metaPath := filepath.Join(sessionsDir, meta.ID+".meta.json")
	if err := os.Remove(metaPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting metadata: %v\n", err)
		os.Exit(1)
	}

	// Delete history file
	if err := os.Remove(meta.HistoryPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting history: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Deleted session: %s\n", meta.Title)
}

// printUsage displays the complete usage information
func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage of late:\n")
	fmt.Fprintf(os.Stderr, "  late [flags]\n")
	fmt.Fprintf(os.Stderr, "  late session <command> [args]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  session list     List all saved sessions\n")
	fmt.Fprintf(os.Stderr, "  session load <id>  Load a session by ID\n")
	fmt.Fprintf(os.Stderr, "  session delete <id>  Delete a session by ID\n\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	flag.PrintDefaults()
}

// ForwardOrchestratorEvents is a helper that recursively forwards all events from an orchestrator
// to the Bubble Tea program.
func ForwardOrchestratorEvents(p *tea.Program, o common.Orchestrator) {
	go func() {
		for event := range o.Events() {
			p.Send(tui.OrchestratorEventMsg{Event: event})
			if added, ok := event.(common.ChildAddedEvent); ok {
				ForwardOrchestratorEvents(p, added.Child)
			}
		}
	}()
}
