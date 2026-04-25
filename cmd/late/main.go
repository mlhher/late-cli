package main

import (
	"context"
	"flag"
	"fmt"
	"late/internal/agent"
	"late/internal/common"
	"late/internal/executor"
	"late/internal/git"
	"late/internal/orchestrator"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"late/internal/assets"
	"late/internal/client"
	appconfig "late/internal/config"
	"late/internal/mcp"
	"late/internal/session"
	"late/internal/tool"
	"late/internal/tui"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
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
	gemmaThinkingReq := flag.Bool("gemma-thinking", false, "Prepend <|think|> token to system prompt for Gemma 4 models")
	subagentMaxTurns := flag.Int("subagent-max-turns", 500, "Maximum number of turns for subagents (default: 500)")
	appendSystemPromptReq := flag.String("append-system-prompt", "", "Append text to the system prompt after processing")
	versionReq := flag.Bool("version", false, "Show version")
	unsupervisedReq := flag.Bool("i-promise-i-have-backups-and-will-not-file-issues", false, "Unsupported: Execute all tools without supervision. Do not use this, bad things will happen. You have been warned.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of late:\n")
		fmt.Fprintf(os.Stderr, "  late [flags]\n")
		fmt.Fprintf(os.Stderr, "  late session <command> [args]\n")
		fmt.Fprintf(os.Stderr, "  late worktree <command> [args]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  session list [-v]      List all saved sessions (use -v for verbose/detailed view)\n")
		fmt.Fprintf(os.Stderr, "  session load <id>      Load a session by ID\n")
		fmt.Fprintf(os.Stderr, "  session delete <id>    Delete a session by ID\n")
		fmt.Fprintf(os.Stderr, "  worktree list          List all worktrees\n")
		fmt.Fprintf(os.Stderr, "  worktree create <path> [branch]  Create a new worktree\n")
		fmt.Fprintf(os.Stderr, "  worktree remove <path>           Remove a worktree\n")
		fmt.Fprintf(os.Stderr, "  worktree active        Show current worktree\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *versionReq {
		fmt.Printf("late %s\n", common.Version)
		return
	}

	if *helpReq {
		flag.Usage()
		return
	}

	var loadedHistoryPath string
	if flag.NArg() > 0 && flag.Arg(0) == "session" {
		path, _, shouldExit := handleSessionCommand(flag.Args()[1:])
		if shouldExit {
			return
		}
		loadedHistoryPath = path
	}

	if flag.NArg() > 0 && flag.Arg(0) == "worktree" {
		shouldExit := handleWorktreeCommand(flag.Args()[1:])
		if shouldExit {
			return
		}
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
		content, _ := assets.PromptsFS.ReadFile("prompts/instruction-planning.md")
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

	if *gemmaThinkingReq {
		systemPrompt = "<|think|>" + systemPrompt
	}

	if !*enableBashReq {
		systemPrompt = common.ReplacePlaceholders(systemPrompt,
			map[string]string{
				"${{NOTICE}}": "Bash is disabled. You must not attempt to use execute any bash commands. Doing so will result in an error.",
			})
	}

	if runtime.GOOS == "windows" {
		systemPrompt += "\n\n## Platform Note\nYou are running on **Windows** and commands execute in **PowerShell**. Prefer PowerShell-native commands and syntax:\n- Prefer `Get-ChildItem` (or `dir`) for directory listing\n- Prefer `Get-Content` for reading files\n- Prefer `Remove-Item` for deleting files/directories\n- Prefer `Copy-Item` and `Move-Item` for copy/move operations\n- Prefer `New-Item -ItemType Directory` for explicit directory creation\n- Use PowerShell quoting/escaping rules and avoid Unix-only shell syntax\n- Do NOT use bash/sh-specific features unless explicitly required"
	}

	if *appendSystemPromptReq != "" {
		systemPrompt = systemPrompt + *appendSystemPromptReq
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

	// Load existing history
	history, err := session.LoadHistory(historyPath)
	if err != nil {
		history = []client.ChatMessage{}
	}
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

	// Initialize Core Components
	resolvedOpenAIConfig := appconfig.ResolveOpenAISettings(appConfig)
	resolvedClientConfig := client.Config{
		BaseURL: resolvedOpenAIConfig.BaseURL,
		APIKey:  resolvedOpenAIConfig.APIKey,
		Model:   resolvedOpenAIConfig.Model,
	}
	c := client.NewClient(resolvedClientConfig)
	c.DiscoverBackend(context.Background())

	// Initialize Subagent Client
	resolvedSubagentConfig := appconfig.ResolveSubagentSettings(appConfig, resolvedOpenAIConfig)

	subagentClient := c
	if resolvedSubagentConfig.Model != "" {
		subagentClient = client.NewClient(client.Config{
			BaseURL: resolvedSubagentConfig.BaseURL,
			APIKey:  resolvedSubagentConfig.APIKey,
			Model:   resolvedSubagentConfig.Model,
		})
		subagentClient.DiscoverBackend(context.Background())
	}

	// Flag overrides
	if !*enableBashReq {
		enabledTools["bash"] = false
	}

	sess := session.New(c, historyPath, history, systemPrompt, *useToolsReq)
	executor.RegisterTools(sess.Registry, enabledTools, true)

	// Register MCP tools into the session registry
	for _, t := range mcpClient.GetTools() {
		// MCP tools are enabled by default unless explicitly set to false in config.json
		if enabled, exists := enabledTools[t.Name()]; exists && !enabled {
			continue
		}
		sess.Registry.Register(t)
	}

	// Initialize common renderer
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(tui.LateTheme),
		glamour.WithWordWrap(80),
		glamour.WithPreservedNewLines(),
	)

	// Create root orchestrator
	// We'll add middlewares later once the program is started
	rootAgent := orchestrator.NewBaseOrchestrator("main", sess, nil, 0)

	model := tui.NewModel(rootAgent, renderer)
	p := tea.NewProgram(model)

	// Wire TUI integration
	go func() {
		// Set messenger first
		p.Send(tui.SetMessengerMsg{Messenger: p})

		// Create context with InputProvider
		ctx := context.WithValue(context.Background(), common.InputProviderKey, tui.NewTUIInputProvider(p))
		if *unsupervisedReq {
			ctx = context.WithValue(ctx, common.SkipConfirmationKey, true)
		}
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
			child, err := agent.NewSubagentOrchestrator(subagentClient, goal, ctxFiles, agentType, enabledTools, *injectCWDReq, *gemmaThinkingReq, *subagentMaxTurns, rootAgent, p)
			if err != nil {
				return "", err
			}

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
		fmt.Printf("Unspecified error: %v", err)
		os.Exit(1)
	}
}

// handleSessionCommand processes session subcommands
// Returns: command, args (remaining), verbose flag
func handleSessionCommand(args []string) (string, []string, bool) {
	if len(args) == 0 {
		fmt.Println("Usage: late session <list|load|delete> [args...]")
		fmt.Println("")
		fmt.Println("Commands:")
		fmt.Println("  list [-v]      List all saved sessions (use -v for verbose/detailed view)")
		fmt.Println("  load <id>      Load a session by ID (can use prefix)")
		fmt.Println("  delete <id>    Delete a session by ID")
		return "", nil, false
	}

	// Parse flags for specific commands
	verbose := false
	commandArgs := args

	switch args[0] {
	case "list":
		// Parse flags for list command
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		verbosePtr := fs.Bool("v", false, "Verbose output")
		fs.Parse(args[1:])
		verbose = *verbosePtr
		commandArgs = fs.Args()
	case "load", "delete":
		// These commands don't use flags, just pass through
		// commandArgs should be args[1:] to skip the command name
		if len(args) > 1 {
			commandArgs = args[1:]
		} else {
			commandArgs = []string{}
		}
	}

	switch args[0] {
	case "list":
		handleSessionList(verbose)
		return "", nil, true
	case "load":
		if len(commandArgs) < 1 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: late session load <id>")
			os.Exit(1)
		}
		return handleSessionLoad(commandArgs[0]), nil, false
	case "delete":
		if len(commandArgs) < 1 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: late session delete <id>")
			os.Exit(1)
		}
		handleSessionDelete(commandArgs[0])
		return "", nil, true
	default:
		fmt.Printf("Unknown session command: %s\n", args[0])
		handleSessionCommand([]string{})
		return "", nil, true
	}
}

// handleSessionList displays all saved sessions
func handleSessionList(verbose bool) {
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
	for _, meta := range metas {
		fmt.Print(strings.TrimSpace(session.FormatSessionDisplay(meta, verbose)) + "\n")
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
	// TODO: remove
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

// handleWorktreeCommand processes worktree subcommands
// Returns: true if a valid command was handled, false otherwise
func handleWorktreeCommand(args []string) bool {
	if len(args) == 0 {
		fmt.Println("Usage: late worktree <command> [args...]")
		fmt.Println("")
		fmt.Println("Commands:")
		fmt.Println("  list              List all worktrees")
		fmt.Println("  create <path> [branch]  Create a new worktree at given path (defaults to current branch)")
		fmt.Println("  remove <path>     Remove a worktree")
		fmt.Println("  active            Show current worktree")
		return false
	}

	switch args[0] {
	case "list":
		handleWorktreeList()
		return true
	case "create":
		if len(args) < 2 {
			fmt.Println("Error: path required for create command")
			fmt.Println("Usage: late worktree create <path> [branch]")
			return true
		}
		path := args[1]
		branch := ""
		if len(args) >= 3 {
			branch = args[2]
		}
		if branch == "" {
			// Get current branch
			cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
			output, err := cmd.Output()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current branch: %v\n", err)
				return true
			}
			branch = strings.TrimSpace(string(output))
		}
		if err := git.CreateWorktree(path, branch); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
			return true
		}
		fmt.Printf("Created worktree at %s (branch: %s)\n", path, branch)
		return true
	case "remove":
		if len(args) < 2 {
			fmt.Println("Error: path required for remove command")
			fmt.Println("Usage: late worktree remove <path>")
			return true
		}
		path := args[1]
		if err := git.RemoveWorktree(path); err != nil {
			fmt.Fprintf(os.Stderr, "Error removing worktree: %v\n", err)
			return true
		}
		fmt.Printf("Removed worktree at %s\n", path)
		return true
	case "active":
		path, err := git.GetActiveWorktree()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting active worktree: %v\n", err)
			return true
		}
		fmt.Println(path)
		return true
	default:
		fmt.Printf("Unknown worktree command: %s\n", args[0])
		fmt.Println("")
		fmt.Println("Usage: late worktree <command> [args...]")
		fmt.Println("")
		fmt.Println("Commands:")
		fmt.Println("  list              List all worktrees")
		fmt.Println("  create <path> [branch]  Create a new worktree at given path (defaults to current branch)")
		fmt.Println("  remove <path>     Remove a worktree")
		fmt.Println("  active            Show current worktree")
		return false
	}
}

// handleWorktreeList displays all git worktrees
func handleWorktreeList() {
	worktrees, err := git.ListWorktrees()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing worktrees: %v\n", err)
		os.Exit(1)
	}

	if len(worktrees) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	fmt.Println("Git worktrees:")
	for _, wt := range worktrees {
		fmt.Printf("  %s", wt.Path)
		if wt.IsDetached {
			fmt.Printf(" (detached from %s)", wt.Branch)
		} else {
			fmt.Printf(" (%s)", wt.Branch)
		}
		if wt.Status != "" {
			fmt.Printf(" - %s", wt.Status)
		}
		fmt.Println()
	}
}

// handleWorktreeCreate creates a new worktree at the specified path
func handleWorktreeCreate(path string, branch string) {
	// If branch not specified, use current branch
	if branch == "" {
		cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
		output, err := cmd.Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current branch: %v\n", err)
			os.Exit(1)
		}
		branch = strings.TrimSpace(string(output))
	}

	// Create the worktree
	if err := git.CreateWorktree(path, branch); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created worktree at %s (branch: %s)\n", path, branch)
}

// handleWorktreeRemove removes an existing worktree
func handleWorktreeRemove(path string) {
	if err := git.RemoveWorktree(path); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing worktree: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed worktree at %s\n", path)
}

// handleWorktreeActive shows the currently active worktree
func handleWorktreeActive() {
	path, err := git.GetActiveWorktree()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting active worktree: %v\n", err)
		os.Exit(1)
	}

	// Check if this is the main worktree (path is empty or indicates main)
	if path == "" || path == "." {
		fmt.Println("Currently in main worktree")
	} else {
		fmt.Printf("Currently in worktree: %s\n", path)
	}
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
