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
	"sync"
	"time"

	"late/internal/assets"
	"late/internal/client"
	appconfig "late/internal/config"
	"late/internal/mcp"
	"late/internal/pathutil"
	"late/internal/plugin"
	"late/internal/session"
	"late/internal/tool"
	"late/internal/tui"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"encoding/json"
)

// pluginInlineTool adapts a plugin.InlineTool (defined in internal/plugin/tools.go)
// into a common.Tool so the CLI's session registry can dispatch invocations to
// plugin-declared runners. It exists because upstream repurposed
// tool.ScriptTool for skill dispatch only; for arbitrary plugin-defined tools,
// we wrap them here.
//
// The wrapper synthesizes a client.ToolCall from the executor's (args
// json.RawMessage) payload by stitching in the registered name — args is
// strictly the JSON parameters (e.g. {"path": "/foo"}) the model emitted;
// the function name is provided by the registry at dispatch time, so we
// surface the wrapped name rather than re-parse it from args.
type pluginInlineTool struct {
	name        string
	description string
	parameters  json.RawMessage
	runner      func(ctx context.Context, call client.ToolCall) (string, error)
}

func (p pluginInlineTool) Name() string                { return p.name }
func (p pluginInlineTool) Description() string         { return p.description }
func (p pluginInlineTool) Parameters() json.RawMessage { return p.parameters }

// RequiresConfirmation always returns true: an inline tool runs an
// arbitrary plugin script, so it must go through the normal user
// confirmation flow like skill scripts (tool.ScriptTool) and MCP tools
// (tool adapter). The plugin docs promise exactly this — plugin-example.md:
// "user confirmation still prompts the user".
func (p pluginInlineTool) RequiresConfirmation(args json.RawMessage) bool {
	return true
}
func (p pluginInlineTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return p.runner(ctx, client.ToolCall{
		Type:     "function",
		Function: client.FunctionCall{Name: p.name, Arguments: string(args)},
	})
}
func (p pluginInlineTool) CallString(args json.RawMessage) string {
	return fmt.Sprintf("Calling plugin tool %q...", p.name)
}

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
	enableSqzReq := flag.Bool("enable-sqz", false, "Enable sqz context compression (if available)")
	appendSystemPromptReq := flag.String("append-system-prompt", "", "Append text to the system prompt after processing")
	versionReq := flag.Bool("version", false, "Show version")
	unsupervisedReq := flag.Bool("i-promise-i-have-backups-and-will-not-file-issues", false, "Unsupported: Execute all tools without supervision. Do not use this, bad things will happen. You have been warned.")
	enableImagesReq := flag.Bool("enable-images", false, "Force enable support for image attachments for unsupported servers.")
	continueReq := flag.Bool("continue", false, "Load and start the latest session")
	showCWDReq := flag.Bool("show-cwd", true, "Show current working directory in status bar")
	themeReq := flag.String("theme", "", "Plugin theme id ('<plugin>:<name>'); falls back to $LATE_THEME")
	promptReq := flag.String("prompt", "", "Start the agent immediately with the given prompt")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of late:\n")
		fmt.Fprintf(os.Stderr, "  late [flags]\n")
		fmt.Fprintf(os.Stderr, "  late session <command> [args]\n")
		fmt.Fprintf(os.Stderr, "  late plugin <command> [args]\n")
		fmt.Fprintf(os.Stderr, "  late worktree <command> [args]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  session list [-v]      List all saved sessions (use -v for verbose/detailed view)\n")
		fmt.Fprintf(os.Stderr, "  session load <id>      Load a session by ID\n")
		fmt.Fprintf(os.Stderr, "  session delete <id>    Delete a session by ID\n")
		fmt.Fprintf(os.Stderr, "  plugin list, ls        List installed plugins\n")
		fmt.Fprintf(os.Stderr, "  plugin install <src>   Install a plugin from npm/git/local\n")
		fmt.Fprintf(os.Stderr, "  plugin remove <name>   Remove a plugin\n")
		fmt.Fprintf(os.Stderr, "  plugin link <path>     Link a local plugin directory\n")
		fmt.Fprintf(os.Stderr, "  plugin enable <name>   Enable a plugin\n")
		fmt.Fprintf(os.Stderr, "  plugin disable <name>  Disable a plugin\n")
		fmt.Fprintf(os.Stderr, "  worktree list          List all worktrees\n")
		fmt.Fprintf(os.Stderr, "  worktree create <path> [branch]  Create a new worktree\n")
		fmt.Fprintf(os.Stderr, "  worktree remove <path>           Remove a worktree\n")
		fmt.Fprintf(os.Stderr, "  worktree active        Show current worktree\n\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\n🌟 Enjoying Late? Consider leaving a star on GitHub: https://github.com/mlhher/late-cli\n")
	}
	flag.Parse()

	tool.SetSqzEnabled(*enableSqzReq)

	if *versionReq {
		fmt.Printf("late %s\n", common.Version)
		return
	}

	if *helpReq {
		flag.Usage()
		return
	}

	var loadedHistoryPath string
	if *continueReq {
		meta, err := session.GetLatestSession()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting latest session: %v\n", err)
			os.Exit(1)
		}
		if meta == nil {
			fmt.Fprintln(os.Stderr, "No sessions found to continue.")
			os.Exit(1)
		}
		fmt.Printf("Resuming session: %s (%s)\n", meta.ID, meta.Title)
		time.Sleep(500 * time.Millisecond) // Give user a moment to see what's happening
		loadedHistoryPath = meta.HistoryPath
	} else if flag.NArg() > 0 && flag.Arg(0) == "session" {
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

	// Plugin command handler — dispatches before TUI startup
	var pluginManager *plugin.PluginManager
	cwd, _ := os.Getwd()
	projectPluginsDir := filepath.Join(cwd, common.LateProjectPluginsDir())
	if flag.NArg() > 0 && flag.Arg(0) == "plugin" {
		pluginsDir, err := common.LatePluginsDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to get plugins directory: %v\n", err)
		} else {
			pm := plugin.NewPluginManager(pluginsDir)
			if _, err := os.Stat(projectPluginsDir); err == nil {
				pm.SetProjectDir(projectPluginsDir)
			}
			if err := pm.Discover(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to discover plugins: %v\n", err)
			}
			pluginManager = pm
			if plugin.HandlePluginCommand(pm, flag.Args()[1:]) {
				return
			}
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
		content, _ := assets.PromptsFS.ReadFile("prompts/instruction-orchestrator.md")
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

	startMsg := "Starting late TUI..."
	if tool.IsSqzAvailable() {
		startMsg = "Starting late TUI (sqz-enabled)..."
	}
	fmt.Println(startMsg)

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

	// Snapshot the user-authored MCP config before plugin servers are merged
	// into it, so the plugin watcher can recompute the desired server set
	// (user config + current plugin servers) on every change.
	baseMCPConfig := cloneMCPConfig(config)

	// Plugin discovery and surface registration
	var (
		skillsDir  string
		skillsErr  error
	)
	if pluginManager == nil {
		pluginsDir, err := common.LatePluginsDir()
		if err == nil {
			pm := plugin.NewPluginManager(pluginsDir)
			// Set project-local dir if it exists
			if _, statErr := os.Stat(projectPluginsDir); statErr == nil {
				pm.SetProjectDir(projectPluginsDir)
			}
			if err := pm.Discover(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to discover plugins: %v\n", err)
			} else {
				// Keep the manager even with zero plugins so the watcher runs
				// and picks up the first install without a restart.
				pluginManager = pm
				if pm.Count() > 0 {
					fmt.Printf("Loading %d plugin(s)...\n", pm.Count())

					// Register plugin skills into the skills directory
					skillsDir, skillsErr = pathutil.LateSkillsDir()
					if skillsErr == nil {
						if err := pm.RegisterPluginSkills(skillsDir); err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to register plugin skills: %v\n", err)
						}
					}

					// Connect plugin MCP servers
					pluginMCP := pm.BuildMCPConfigMap()
					if len(pluginMCP) > 0 && config == nil {
						config = &mcp.MCPConfig{McpServers: make(map[string]mcp.MCPServer)}
					}
					if len(pluginMCP) > 0 && config != nil {
						fmt.Println("Connecting to plugin MCP servers...")
						for name, srv := range pluginMCP {
							config.McpServers[name] = mcp.MCPServer{
								Command:       srv.Command,
								Args:          srv.Args,
								Env:           srv.Env,
								URL:           srv.URL,
								TransportType: srv.TransportType,
								Disabled:      srv.Disabled,
								Dir:           srv.Dir,
							}
						}
						// Servers already connected from the user config are
						// skipped, so this only connects the plugin servers.
						if err := mcpClient.ConnectFromConfig(context.Background(), config); err != nil {
							fmt.Fprintf(os.Stderr, "Warning: Failed to connect to plugin MCP servers: %v\n", err)
						}
					}
				}
			}
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
		BaseURL:      resolvedOpenAIConfig.BaseURL,
		APIKey:       resolvedOpenAIConfig.APIKey,
		Model:        resolvedOpenAIConfig.Model,
		EnableImages: *enableImagesReq,
	}
	if appConfig != nil {
		if setting, ok := appConfig.GetModelForAgent("orchestrator"); ok {
			resolvedClientConfig.BaseURL = setting.URL
			resolvedClientConfig.APIKey = setting.Key
			resolvedClientConfig.Model = setting.Model
		}
	}
	c := client.NewClient(resolvedClientConfig)
	c.DiscoverBackend(context.Background())

	// Initialize Subagent Client
	resolvedSubagentConfig := appconfig.ResolveSubagentSettings(appConfig, resolvedOpenAIConfig)

	subagentClient := c
	if resolvedSubagentConfig.BaseURL != resolvedClientConfig.BaseURL ||
		resolvedSubagentConfig.APIKey != resolvedClientConfig.APIKey ||
		resolvedSubagentConfig.Model != resolvedClientConfig.Model {
		subagentClient = client.NewClient(client.Config{
			BaseURL:      resolvedSubagentConfig.BaseURL,
			APIKey:       resolvedSubagentConfig.APIKey,
			Model:        resolvedSubagentConfig.Model,
			EnableImages: *enableImagesReq,
		})
		subagentClient.DiscoverBackend(context.Background())
	}

	// Flag overrides
	if !*enableBashReq {
		enabledTools["bash"] = false
	}

	// Main agent is a planner: explicitly enable planner tools and disable coding tools
	mainTools := make(map[string]bool)
	for k, v := range enabledTools {
		mainTools[k] = v
	}
	mainTools["write_implementation_plan"] = true
	mainTools["write_file"] = false
	mainTools["target_edit"] = false

	sess := session.New(c, historyPath, history, systemPrompt, *useToolsReq)
	executor.RegisterTools(sess.Registry, mainTools)

	// Register MCP tools into the session registry.
	// MCP tool names are now namespaced as "{server}__{tool}" (sanitized —
	// e.g. "graph-rag__list_files"). For backwards compatibility with
	// configs that disable tools by bare name (e.g. "list_files": false),
	// we check the namespaced name first, then fall back to the bare name
	// so existing configs keep working without modification.
	//
	// pluginToolNames records every plugin-provided tool registered here so
	// the watcher can unregister the stale set on the next plugin change.
	var pluginToolNames []string
	// usedToolNames records every name registered below (MCP first, then
	// inline) so inline tools are deduped against MCP names too — without
	// this, a plugin's inline tool can silently overwrite an MCP-backed
	// tool that sanitizes to the same namespaced name.
	usedToolNames := make(map[string]bool)
	for _, t := range mcpClient.GetTools() {
		if !toolEnabled(enabledTools, t.Name()) {
			continue
		}
		sess.Registry.Register(t)
		pluginToolNames = append(pluginToolNames, t.Name())
		usedToolNames[t.Name()] = true
	}

	// Register inline plugin tools (declared in the manifest's `late.tools`
	// field). Each inline tool is run as a local script via runHook and
	// hooks into the same ToolMiddleware chain as MCP-backed tools so
	// onToolCall hooks, confirmations, and tool-result reporting all work
	// uniformly for plugin-declared tools.
	if pluginManager != nil {
		for _, t := range pluginManager.GetInlineTools(usedToolNames) {
			if !toolEnabled(enabledTools, t.Name) {
				continue
			}
			sess.Registry.Register(pluginInlineTool{
				name:        t.Name,
				description: t.Description,
				parameters:  t.Parameters,
				runner:      t.Runner,
			})
			pluginToolNames = append(pluginToolNames, t.Name)
		}
	}

	// Resolve theme: --theme flag > $LATE_THEME > bundled base.
	themeID := *themeReq
	if themeID == "" {
		themeID = os.Getenv("LATE_THEME")
	}
	themeBytes := tui.LateTheme
	if themeID != "" && pluginManager != nil {
		if info, err := pluginManager.GetTheme(themeID); err == nil && info != nil {
			if merged, mErr := tui.ResolveRenderTheme(info.ID, info.Glamour); mErr == nil {
				themeBytes = merged
				fmt.Fprintf(os.Stderr, "Applied plugin theme: %s\n", info.ID)
			}
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Theme lookup failed for %q: %v\n", themeID, err)
		}
	}

	// Initialize common renderer
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(themeBytes),
		glamour.WithWordWrap(80),
		glamour.WithPreservedNewLines(),
	)

	// Create root orchestrator
	// We'll add middlewares later once the program is started
	rootAgent := orchestrator.NewBaseOrchestrator("main", sess, nil, 0)

	model := tui.NewModel(rootAgent, renderer, appConfig)
	model.ApplyOrchestratorModel = func(setting appconfig.ModelSetting) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.SetClient(newModelClient(ctx, setting, *enableImagesReq))
			return nil
		}
	}
	if appConfig != nil {
		if setting, ok := appConfig.GetModelForAgent("orchestrator"); ok {
			model.ModelName = setting.Model
		} else {
			model.ModelName = resolvedOpenAIConfig.Model
		}

		var subagentInfos []string
		for _, sub := range assets.GetSubagents() {
			if setting, ok := appConfig.GetModelForAgent(sub.Name); ok {
				subagentInfos = append(subagentInfos, fmt.Sprintf("%s:%s", sub.Name, setting.Model))
			}
		}
		if len(subagentInfos) > 0 {
			model.SubagentInfo = strings.Join(subagentInfos, ", ")
		} else {
			model.SubagentInfo = resolvedSubagentConfig.Model
		}
	} else {
		model.ModelName = resolvedOpenAIConfig.Model
		model.SubagentInfo = resolvedSubagentConfig.Model
	}

	// Register plugin command handler + message hook into the TUI. These are
	// wired even with zero plugins so a plugin installed while Late is
	// running becomes fully functional (commands, message hooks) after the
	// watcher fires — no restart needed.
	if pluginManager != nil {
		model.MessageHook = func(text string) string {
			return pluginManager.HookedMessage(context.Background(), text)
		}
		model.CommandHandler = pluginManager.HandleCommand
	}

	// Register plugin slash commands + theme catalog so plugin commands fire
	// when the user presses Enter.
	if pluginManager != nil && pluginManager.Count() > 0 {
		model.SetPluginCommands(pluginManager.PluginCommands())
		model.SelectedTheme = themeID

		// Map plugin.ThemeInfo to tui.ThemeEntry so the /themes picker and
		// inline `/themes <name>` can resolve plugin themes at runtime.
		pluginThemes := pluginManager.AllThemes()
		if len(pluginThemes) > 0 {
			entries := make([]tui.ThemeEntry, len(pluginThemes))
			for i, info := range pluginThemes {
				entries[i] = tui.ThemeEntry{
					ID:         info.ID,
					PluginName: info.PluginName,
					ThemeName:  info.ThemeName,
					Glamour:    info.Glamour,
				}
			}
			model.SetThemes(entries)
		}
	}

	// Fire OnSessionStart hooks for every enabled plugin in parallel. This
	// runs once, before the orchestrator is dispatched, so plugin scripts
	// can warm caches, register tools, or print startup announcements.
	if pluginManager != nil {
		pluginManager.CallOnSessionStartHooks()
	}

	// Detect if subagents use a different model/backend
	if resolvedSubagentConfig.BaseURL != resolvedOpenAIConfig.BaseURL ||
		resolvedSubagentConfig.APIKey != resolvedOpenAIConfig.APIKey ||
		resolvedSubagentConfig.Model != resolvedOpenAIConfig.Model {
		model.SubagentInfo = resolvedSubagentConfig.Model
	}
	model.ShowCWD = *showCWDReq

	p := tea.NewProgram(model)

	// toolSync serializes plugin/MCP tool-registry refreshes triggered from
	// multiple goroutines: the plugin filesystem watcher below and MCP
	// servers' own tools/list_changed notifications (wired via
	// mcpClient.OnToolsChanged just below). Both paths recompute the full
	// current tool/command/theme set and diff it against the last set sent
	// to the TUI, so the two triggers can't race each other into sending a
	// stale diff.
	toolSync := &pluginToolSync{prev: append([]string(nil), pluginToolNames...)}
	mcpClient.OnToolsChanged = func() {
		toolSync.refresh(p, mcpClient, pluginManager, enabledTools)
	}

	// Start plugin filesystem watcher. It runs even with zero plugins so
	// the first install is picked up without a restart. On every change it
	// fully re-registers the plugin surfaces: skills, MCP sessions, tools,
	// commands, and themes — not just commands/themes.
	if pluginManager != nil {
		watcher := plugin.NewPollingWatcher(pluginManager)
		// Also watch project-local dir if configured
		if pluginManager.HasProjectDir() {
			watcher.AddWatchDir(pluginManager.ProjectDir())
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go watcher.Start(ctx, func() {
			// 1. Re-sync skill dirs (creates new ones, prunes stale ones
			// for removed/disabled plugins). When Late started with zero
			// plugins the skills dir was never resolved, so compute it lazily.
			if skillsDir == "" {
				if d, err := pathutil.LateSkillsDir(); err == nil {
					skillsDir = d
				}
			}
			if skillsDir != "" {
				if err := pluginManager.RegisterPluginSkills(skillsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to re-register plugin skills: %v\n", err)
				}
			}

			// 2. Reconcile MCP sessions against the desired set: user config
			// + current plugin servers. Removed or disabled plugins drop
			// their servers (sessions closed); new ones connect. A server
			// whose command/args/env/url/dir changed while its name stayed
			// the same is closed and reconnected too — see
			// mcp.Client.Reconcile.
			desired := cloneMCPConfig(baseMCPConfig)
			pluginMCP := pluginManager.BuildMCPConfigMap()
			for name, srv := range pluginMCP {
				desired.McpServers[name] = mcp.MCPServer{
					Command:       srv.Command,
					Args:          srv.Args,
					Env:           srv.Env,
					URL:           srv.URL,
					TransportType: srv.TransportType,
					Disabled:      srv.Disabled,
					Dir:           srv.Dir,
				}
			}
			if err := mcpClient.Reconcile(context.Background(), desired); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to reconcile plugin MCP servers: %v\n", err)
			}

			// 3. Rebuild tool-call middlewares so onToolCall/onToolResult
			// hooks from plugins installed, edited, or removed since
			// startup take effect immediately instead of only after a
			// restart (BuildHookMiddlewares/BuildToolResultMiddlewares
			// snapshot live from pluginManager on every call — only the
			// SetMiddlewares call itself was previously startup-only).
			rootAgent.SetMiddlewares(buildMiddlewares(pluginManager, p, sess.Registry))

			// 4. Refresh the tool/command/theme set the TUI sees.
			toolSync.refresh(p, mcpClient, pluginManager, enabledTools)
		})
	}

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

		// Set middlewares (see buildMiddlewares for ordering rationale).
		// This is also rebuilt by the plugin watcher's onChanged callback
		// above so hook changes take effect without a restart.
		rootAgent.SetMiddlewares(buildMiddlewares(pluginManager, p, sess.Registry))

		// Start forwarding events from the root agent to the TUI
		ForwardOrchestratorEvents(p, rootAgent)

		if *promptReq != "" {
			p.Send(tui.StartPromptMsg(*promptReq))
		}
	}()

	if *enableSubagentsReq {
		runner := func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			var currentSubagentClient *client.Client
			if appConfig != nil {
				if setting, ok := appConfig.GetModelForAgent(agentType); ok {
					currentSubagentClient = client.NewClient(client.Config{
						BaseURL:      setting.URL,
						APIKey:       setting.Key,
						Model:        setting.Model,
						EnableImages: *enableImagesReq,
					})
					currentSubagentClient.DiscoverBackend(ctx)
				}
			}
			if currentSubagentClient == nil {
				currentSubagentClient = subagentClient
			}

			child, err := agent.NewSubagentOrchestrator(currentSubagentClient, goal, ctxFiles, agentType, enabledTools, *injectCWDReq, *gemmaThinkingReq, *subagentMaxTurns, rootAgent, p)
			if err != nil {
				return "", err
			}

			res, err := child.Execute("")
			if err != nil {
				return "", err
			}

			if child.IsStopRequested() {
				return fmt.Sprintf("The subagent task was explicitly cancelled by the user. Final output before cancellation:\n\n%s", res), nil
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

func newModelClient(ctx context.Context, setting appconfig.ModelSetting, enableImages bool) *client.Client {
	c := client.NewClient(client.Config{
		BaseURL:      setting.URL,
		APIKey:       setting.Key,
		Model:        setting.Model,
		EnableImages: enableImages,
	})
	c.DiscoverBackend(ctx)
	return c
}

// buildMiddlewares assembles the tool-call middleware chain for rootAgent.
// Middlewares are applied innermost-last, so the plugin onToolCall hooks
// run FIRST (outermost), then the TUI confirmation, then the onToolResult
// hooks. Confirmation must see the arguments AFTER plugins mutated them —
// otherwise a plugin could change the arguments after the user approved
// the call. Called both at startup and by the plugin watcher's onChanged
// callback so hook changes take effect without a restart.
func buildMiddlewares(pluginManager *plugin.PluginManager, p *tea.Program, registry *common.ToolRegistry) []common.ToolMiddleware {
	mws := []common.ToolMiddleware{}
	if pluginManager != nil {
		mws = append(mws, pluginManager.BuildHookMiddlewares()...)
	}
	mws = append(mws, tui.TUIConfirmMiddleware(p, registry))
	if pluginManager != nil {
		mws = append(mws, pluginManager.BuildToolResultMiddlewares()...)
	}
	return mws
}

// pluginToolSync serializes tool/command/theme refreshes sent to the TUI.
// Two independent triggers can fire it: the plugin filesystem watcher (a
// plugin was installed/removed/edited) and an MCP server's own
// tools/list_changed notification (wired via mcp.Client.OnToolsChanged).
// Without the mutex, concurrent refreshes could interleave and send a
// diff computed against a stale `prev`.
type pluginToolSync struct {
	mu   sync.Mutex
	prev []string
}

// refresh recomputes the full current tool set (MCP + inline, with
// cross-source name collisions resolved the same way as the initial
// registration in main()), plus the current plugin commands/themes, and
// sends one PluginChangeMsg diffed against the last set this synced. The
// full command/theme set is always included — never a partial message —
// so a tools-only trigger (an MCP tool list change) can't blank out
// plugin commands/themes in the TUI.
func (s *pluginToolSync) refresh(p *tea.Program, mcpClient *mcp.Client, pluginManager *plugin.PluginManager, enabledTools map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	used := make(map[string]bool)
	var added []common.Tool
	for _, t := range mcpClient.GetTools() {
		if !toolEnabled(enabledTools, t.Name()) {
			continue
		}
		added = append(added, t)
		used[t.Name()] = true
	}

	var cmds []string
	var entries []tui.ThemeEntry
	if pluginManager != nil {
		for _, t := range pluginManager.GetInlineTools(used) {
			if !toolEnabled(enabledTools, t.Name) {
				continue
			}
			added = append(added, pluginInlineTool{
				name:        t.Name,
				description: t.Description,
				parameters:  t.Parameters,
				runner:      t.Runner,
			})
		}

		cmds = pluginManager.PluginCommands()
		pluginThemes := pluginManager.AllThemes()
		entries = make([]tui.ThemeEntry, len(pluginThemes))
		for i, info := range pluginThemes {
			entries[i] = tui.ThemeEntry{
				ID:         info.ID,
				PluginName: info.PluginName,
				ThemeName:  info.ThemeName,
				Glamour:    info.Glamour,
			}
		}
	}

	p.Send(tui.PluginChangeMsg{
		Commands:     cmds,
		Themes:       entries,
		RemovedTools: s.prev,
		AddedTools:   added,
	})
	s.prev = s.prev[:0]
	for _, t := range added {
		s.prev = append(s.prev, t.Name())
	}
}

// toolEnabled reports whether a namespaced tool name is enabled in the
// enabledTools config: the namespaced name takes priority, then the
// pre-namespacing "server:tool" form (reconstructed from "server__tool"),
// then the bare name (the part after the last "__" or ":" separator) so
// configs written before namespacing — either the old colon-joined keys
// or plain bare-name keys — keep working. Unknown tools default to
// enabled.
func toolEnabled(enabledTools map[string]bool, name string) bool {
	if v, ok := enabledTools[name]; ok {
		return v
	}
	if idx := strings.Index(name, "__"); idx >= 0 {
		legacy := name[:idx] + ":" + name[idx+2:]
		if v, ok := enabledTools[legacy]; ok {
			return v
		}
	}
	if v, ok := enabledTools[common.BareToolName(name)]; ok {
		return v
	}
	return true
}

// cloneMCPConfig returns a shallow copy of an MCP config (the server map
// is copied; server values are shared). A nil config becomes an empty one.
func cloneMCPConfig(c *mcp.MCPConfig) *mcp.MCPConfig {
	if c == nil {
		return &mcp.MCPConfig{McpServers: make(map[string]mcp.MCPServer)}
	}
	out := &mcp.MCPConfig{McpServers: make(map[string]mcp.MCPServer, len(c.McpServers))}
	for k, v := range c.McpServers {
		out.McpServers[k] = v
	}
	return out
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
