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

	"encoding/json"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"golang.org/x/term"
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
	saveSubagentHistoriesReq := flag.Bool("save-subagent-histories", false, "Persist subagent conversation histories to disk (default: off)")
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
		fmt.Fprintf(os.Stderr, "  plugin list, ls                      List installed plugins\n")
		fmt.Fprintf(os.Stderr, "  plugin install [--project] <src>     Install a plugin from npm/git/local\n")
		fmt.Fprintf(os.Stderr, "  plugin remove [--project] <name>     Remove a plugin\n")
		fmt.Fprintf(os.Stderr, "  plugin link [--project] <path>       Link a local plugin directory\n")
		fmt.Fprintf(os.Stderr, "  plugin update [<name>]               Update all or a specific plugin\n")
		fmt.Fprintf(os.Stderr, "  plugin enable <name>                 Enable a plugin\n")
		fmt.Fprintf(os.Stderr, "  plugin disable <name>                Disable a plugin\n")
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
	var resumedSessionTitle string
	var loadedSessionMeta *session.SessionMeta

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
		loadedHistoryPath = meta.HistoryPath
		resumedSessionTitle = fmt.Sprintf("Resumed session: %s (%s)", meta.ID, meta.Title)
		loadedSessionMeta = meta
	} else if flag.NArg() > 0 && flag.Arg(0) == "session" {
		sessCmdResult := handleSessionCommand(flag.Args()[1:])
		if sessCmdResult.ShouldExit {
			return
		}
		loadedHistoryPath = sessCmdResult.HistoryPath
		loadedSessionMeta = sessCmdResult.Meta
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

	// Sessions setup

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

	// Effective session ID for this run — derived from the FINAL history path so
	// resumed sessions keep their original ID (the sessionID var above is a fresh
	// timestamp even on resume). Used to place subagent histories under the right
	// per-session folder. The helper falls back to "" for empty or unsafe IDs,
	// which disables subagent history persistence (in-memory fallback) instead of
	// writing files outside the session folder.
	effectiveSessionID := deriveEffectiveSessionID(historyPath)

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

	// Plugin discovery and surface registration
	var (
		skillsDir string
		skillsErr error
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
				// Keep the manager even with zero plugins so plugin command
				// dispatch and hooks remain safely available.
				pluginManager = pm
				// Reconcile skill links even when this project has no plugins.
				skillsDir, skillsErr = pathutil.LateSkillsDir()
				if skillsErr == nil {
					if err := pm.RegisterPluginSkills(skillsDir); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to register plugin skills: %v\n", err)
					}
				}
				if pm.Count() > 0 {
					// Connect plugin MCP servers
					pluginMCP := pm.BuildMCPConfigMap()
					if len(pluginMCP) > 0 && config == nil {
						config = &mcp.MCPConfig{McpServers: make(map[string]mcp.MCPServer)}
					}
					if len(pluginMCP) > 0 && config != nil {
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

	// Resolve subagent history persistence opt-in
	// (explicit CLI flag > saved session preference > config file).
	saveSubagentHistoriesCLI := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "save-subagent-histories" {
			saveSubagentHistoriesCLI = true
		}
	})
	var storedSubagentHistoryPreference *bool
	if loadedSessionMeta != nil {
		storedSubagentHistoryPreference = loadedSessionMeta.SaveSubagentHistories
	}
	saveSubagentHistories := appconfig.ResolveSaveSubagentHistories(appConfig, saveSubagentHistoriesCLI, *saveSubagentHistoriesReq, storedSubagentHistoryPreference)

	// Initialize Core Components
	resolvedOpenAIConfig := appconfig.ResolveOpenAISettings(appConfig)
	resolvedClientConfig := client.Config{
		BaseURL:      resolvedOpenAIConfig.BaseURL,
		APIKey:       resolvedOpenAIConfig.APIKey,
		Model:        resolvedOpenAIConfig.Model,
		EnableImages: *enableImagesReq,
		AppVersion:   common.Version,
	}
	if appConfig != nil {
		if setting, ok := appConfig.GetModelForAgent("orchestrator"); ok {
			resolvedClientConfig.BaseURL = setting.URL
			resolvedClientConfig.APIKey = setting.Key
			resolvedClientConfig.Model = setting.Model
		}
	}
	c := client.NewClient(resolvedClientConfig)

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
			AppVersion:   common.Version,
		})
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
	if loadedSessionMeta != nil {
		sess.SetSubagentMetadata(loadedSessionMeta.SubagentSeq, loadedSessionMeta.SaveSubagentHistories)
	} else {
		sess.SetSubagentMetadata(0, &saveSubagentHistories)
	}
	executor.RegisterTools(sess.Registry, mainTools)

	// Register MCP tools into the session registry.
	// MCP tool names are now namespaced as "{server}__{tool}" (sanitized —
	// e.g. "graph-rag__list_files"). For backwards compatibility with
	// configs that disable tools by bare name (e.g. "list_files": false),
	// we check the namespaced name first, then fall back to the bare name
	// so existing configs keep working without modification.
	//
	// pluginToolNames records every plugin-provided tool registered here so
	// toolSync tracks the initial tool set.
	var pluginToolNames []string
	// usedToolNames records every name registered below (MCP first, then
	// inline) so inline tools are deduped against MCP names too — without
	// this, a plugin's inline tool can silently overwrite an MCP-backed
	// tool that sanitizes to the same namespaced name.
	usedToolNames := make(map[string]bool)
	for _, t := range mcpClient.GetTools() {
		if !mcpToolEnabled(t, enabledTools) {
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

	// Resolve theme: --theme flag > $LATE_THEME > config.json > bundled base.
	themeID := *themeReq
	if themeID == "" {
		themeID = os.Getenv("LATE_THEME")
	}
	if themeID == "" && appConfig != nil && appConfig.Theme != "" {
		themeID = appConfig.Theme
	}
	themeBytes := tui.LateTheme
	if themeID != "" && themeID != "default" && pluginManager != nil {
		if info, err := pluginManager.GetTheme(themeID); err == nil && info != nil {
			if merged, mErr := tui.ResolveRenderTheme(info.ID, info.Glamour); mErr == nil {
				themeBytes = merged
				themeID = info.ID
				fmt.Fprintf(os.Stderr, "Applied plugin theme: %s\n", info.ID)
			} else {
				themeID = "default"
			}
		} else {
			if err != nil {
				fmt.Fprintf(os.Stderr, "Theme lookup failed for %q: %v\n", themeID, err)
			}
			themeID = "default"
		}
	} else {
		themeID = "default"
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
	model.SetActiveThemeStyles(themeBytes)
	if themeID != "" {
		model.SelectedTheme = themeID
	}
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

	// Register plugin command handler + message hook into the TUI.
	if pluginManager != nil {
		if pluginManager.HasMessageSendHooks() {
			model.MessageHook = func(text string) string {
				return pluginManager.HookedMessage(context.Background(), text)
			}
		}
		model.CommandHandler = pluginManager.HandleCommand
	}

	// Register plugin slash commands + theme catalog so plugin commands fire
	// when the user presses Enter.
	if pluginManager != nil && pluginManager.Count() > 0 {
		model.SetPluginCommands(pluginManager.PluginCommands())

		// Map plugin.ThemeInfo to tui.ThemeEntry so the /themes picker and
		// inline `/themes <name>` can resolve plugin themes at runtime.
		// Always include DefaultThemeEntry first so users can revert.
		pluginThemes := pluginManager.AllThemes()
		if len(pluginThemes) > 0 {
			entries := make([]tui.ThemeEntry, 0, len(pluginThemes)+1)
			entries = append(entries, tui.DefaultThemeEntry)
			for _, info := range pluginThemes {
				entries = append(entries, tui.ThemeEntry{
					ID:         info.ID,
					PluginName: info.PluginName,
					ThemeName:  info.ThemeName,
					Glamour:    info.Glamour,
				})
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
	model.LazyHistory = true

	pOpts := []tea.ProgramOption{
		tea.WithFPS(tui.FrameRate),
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		model.SetSize(w, h)
		pOpts = append(pOpts, tea.WithWindowSize(w, h))
	} else if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && w > 0 && h > 0 {
		model.SetSize(w, h)
		pOpts = append(pOpts, tea.WithWindowSize(w, h))
	}

	model.BootstrapStatus = "Starting..."
	p := tea.NewProgram(model, pOpts...)

	// toolSync serializes plugin/MCP tool-registry refreshes triggered by
	// MCP servers' own tools/list_changed notifications (wired via
	// mcpClient.OnToolsChanged below). It recomputes the full current tool/
	// command/theme set and diffs it against the last set sent to the TUI.
	toolSync := &pluginToolSync{prev: append([]string(nil), pluginToolNames...)}
	mcpClient.OnToolsChanged = func() {
		toolSync.refresh(p, mcpClient, pluginManager, enabledTools)
	}

	// Wire TUI integration
	go func() {
		// Set messenger first
		p.Send(tui.SetMessengerMsg{Messenger: p})
		if resumedSessionTitle != "" {
			p.Send(tui.BootstrapStatusMsg{
				Text:   resumedSessionTitle,
				Active: false,
			})
		}

		// Create context with InputProvider
		ctx := context.WithValue(context.Background(), common.InputProviderKey, tui.NewTUIInputProvider(p))
		if *unsupervisedReq {
			ctx = context.WithValue(ctx, common.SkipConfirmationKey, true)
		}
		rootAgent.SetContext(ctx)

		// Set middlewares (see buildMiddlewares for ordering rationale).
		rootAgent.SetMiddlewares(buildMiddlewares(pluginManager, p, sess.Registry))

		// Start forwarding events from the root agent to the TUI
		ForwardOrchestratorEvents(p, rootAgent)

		// Wait only in this background goroutine: the TUI remains usable while
		// connections and discovery finish, but --prompt needs their results.
		runBootstrap(p, mcpClient, config, c, subagentClient, sess, enabledTools, pluginManager, toolSync)

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
						AppVersion:   common.Version,
					})
					currentSubagentClient.DiscoverBackend(ctx)
				}
			}
			if currentSubagentClient == nil {
				currentSubagentClient = subagentClient
			}

			child, err := agent.NewSubagentOrchestrator(currentSubagentClient, goal, ctxFiles, agentType, enabledTools, *injectCWDReq, *gemmaThinkingReq, *subagentMaxTurns, effectiveSessionID, saveSubagentHistories, rootAgent, p)
			if err != nil {
				return "", err
			}
			child.SetMiddlewares(buildMiddlewares(pluginManager, p, child.Registry()))

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

// deriveEffectiveSessionID derives this run's session ID from the FINAL
// history path so resumed sessions keep their original ID. It returns ""
// for empty or unsafe results (a crafted meta file could claim an ID like
// ".."), which disables subagent history persistence for the run
// (in-memory fallback) instead of writing files outside the session folder.
func deriveEffectiveSessionID(historyPath string) string {
	id := strings.TrimSuffix(filepath.Base(historyPath), ".json")
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return id
}
func newModelClient(ctx context.Context, setting appconfig.ModelSetting, enableImages bool) *client.Client {
	c := client.NewClient(client.Config{
		BaseURL:      setting.URL,
		APIKey:       setting.Key,
		Model:        setting.Model,
		EnableImages: enableImages,
		AppVersion:   common.Version,
	})
	c.DiscoverBackend(ctx)
	return c
}

// buildMiddlewares assembles the tool-call middleware chain for rootAgent and subagents.
// Middlewares are applied innermost-last, so the plugin onToolCall hooks
// run FIRST (outermost), then the TUI confirmation, then the onToolResult
// hooks. Confirmation must see the arguments AFTER plugins mutated them —
// otherwise a plugin could change the arguments after the user approved
// the call.
func buildMiddlewares(pluginManager *plugin.PluginManager, p tui.Messenger, registry *common.ToolRegistry) []common.ToolMiddleware {
	mws := []common.ToolMiddleware{}
	if pluginManager != nil {
		mws = append(mws, pluginManager.BuildHookMiddlewares(func(ctx context.Context, tc client.ToolCall) bool {
			return tui.ToolRequiresConfirmation(ctx, registry, tc)
		})...)
	}
	mws = append(mws, tui.TUIConfirmMiddleware(p, registry))
	if pluginManager != nil {
		mws = append(mws, pluginManager.BuildToolResultMiddlewares()...)
	}
	return mws
}

// pluginToolSync serializes tool/command/theme refreshes sent to the TUI.
// An MCP server's own tools/list_changed notification (wired via
// mcp.Client.OnToolsChanged) can trigger it to recompute the current set.
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
		if !mcpToolEnabled(t, enabledTools) {
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
		if len(pluginThemes) > 0 {
			entries = make([]tui.ThemeEntry, 0, len(pluginThemes)+1)
			entries = append(entries, tui.DefaultThemeEntry)
			for _, info := range pluginThemes {
				entries = append(entries, tui.ThemeEntry{
					ID:         info.ID,
					PluginName: info.PluginName,
					ThemeName:  info.ThemeName,
					Glamour:    info.Glamour,
				})
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

// mcpToolEnabled preserves raw-name settings even when the exposed MCP
// name has been sanitized, truncated, or deduplicated.
func mcpToolEnabled(t tool.Tool, enabledTools map[string]bool) bool {
	if enabled, ok := enabledTools[t.Name()]; ok {
		return enabled
	}
	if named, ok := t.(interface{ BareName() string }); ok {
		if enabled, exists := enabledTools[named.BareName()]; exists {
			return enabled
		}
	}
	return toolEnabled(enabledTools, t.Name())
}

// toolEnabled checks exact names before legacy aliases. Unknown tools
// default to enabled.
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
	// Old config keys may contain punctuation removed from exposed names.
	// A disabled matching alias wins when multiple raw keys sanitize alike.
	for raw, enabled := range enabledTools {
		if !enabled && common.SanitizeToolName(raw) == common.BareToolName(name) {
			return false
		}
	}
	return true
}

type sessionCommandResult struct {
	HistoryPath string
	Meta        *session.SessionMeta
	ShouldExit  bool
}

// handleSessionCommand processes session subcommands.
func handleSessionCommand(args []string) sessionCommandResult {
	if len(args) == 0 {
		fmt.Println("Usage: late session <list|load|delete> [args...]")
		fmt.Println("")
		fmt.Println("Commands:")
		fmt.Println("  list [-v]      List all saved sessions (use -v for verbose/detailed view)")
		fmt.Println("  load <id>      Load a session by ID (can use prefix)")
		fmt.Println("  delete <id>    Delete a session by ID")
		return sessionCommandResult{}
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
		return sessionCommandResult{ShouldExit: true}
	case "load":
		if len(commandArgs) < 1 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: late session load <id>")
			os.Exit(1)
		}
		meta := handleSessionLoad(commandArgs[0])
		return sessionCommandResult{HistoryPath: meta.HistoryPath, Meta: meta}
	case "delete":
		if len(commandArgs) < 1 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: late session delete <id>")
			os.Exit(1)
		}
		handleSessionDelete(commandArgs[0])
		return sessionCommandResult{ShouldExit: true}
	default:
		fmt.Printf("Unknown session command: %s\n", args[0])
		handleSessionCommand([]string{})
		return sessionCommandResult{ShouldExit: true}
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

// handleSessionLoad returns metadata for the given session ID.
func handleSessionLoad(id string) *session.SessionMeta {
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
	return meta
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

	// Delete the session's subagent history folder (hierarchical layout). No-op for
	// legacy flat sessions without a folder. Non-fatal: the session itself is already
	// gone, so don't block the success message on leftover artifacts.
	if err := session.RemoveSessionFolder(meta.ID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to delete subagent history folder: %v\n", err)
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

// runBootstrap runs startup work (MCP connections and LLM backend discovery)
// concurrently in the background so the TUI renders immediately. It streams live
// animated status updates into the UI and completes when all tasks finish.
func runBootstrap(p *tea.Program, mcpClient *mcp.Client, config *mcp.MCPConfig, c *client.Client, subagentClient *client.Client, sess *session.Session, enabledTools map[string]bool, pluginManager *plugin.PluginManager, toolSync *pluginToolSync) {
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		connected int
		failed    []string
	)

	hasMCP := config != nil && len(config.McpServers) > 0

	// Initial notification inside TUI
	if hasMCP {
		p.Send(tui.BootstrapStatusMsg{
			Text:   "Connecting MCP servers & discovering backend...",
			Active: true,
		})
	} else {
		p.Send(tui.BootstrapStatusMsg{
			Text:   "Discovering model backend...",
			Active: true,
		})
	}

	// Task 1: MCP Server Connections (concurrent)
	if hasMCP {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mcpClient.ConnectFromConfigConcurrent(context.Background(), config, func(r mcp.ServerConnectResult) {
				mu.Lock()
				defer mu.Unlock()
				if r.Err != nil {
					failed = append(failed, r.Name)
					p.Send(tui.BootstrapStatusMsg{
						Text:    fmt.Sprintf("MCP %s failed: %v", r.Name, r.Err),
						Warning: true,
						Active:  true,
					})
					return
				}
				for _, a := range r.Adapters {
					if !mcpToolEnabled(a, enabledTools) {
						continue
					}
					sess.Registry.Register(a)
				}
				if toolSync != nil {
					toolSync.refresh(p, mcpClient, pluginManager, enabledTools)
				}
				connected++
				p.Send(tui.BootstrapStatusMsg{
					Text:   fmt.Sprintf("MCP: %s connected", r.Name),
					Active: true,
				})
			})
		}()
	}

	// Task 2: Main LLM Backend Discovery (concurrent)
	wg.Add(1)
	go func() {
		defer wg.Done()
		b := c.DiscoverBackend(context.Background())
		ctxSize := c.ContextSize()
		ctxText := ""
		if ctxSize > 0 {
			ctxText = fmt.Sprintf(" (%dk ctx)", ctxSize/1024)
		}
		p.Send(tui.BootstrapStatusMsg{
			Text:        fmt.Sprintf("Backend: %s%s", b, ctxText),
			Active:      true,
			RefreshView: true,
		})
	}()

	// Task 3: Subagent LLM Backend Discovery (if distinct client)
	if subagentClient != c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = subagentClient.DiscoverBackend(context.Background())
		}()
	}

	// Wait for all background bootstrap tasks to finish
	wg.Wait()

	// Build final summary
	mu.Lock()
	totalMCP := connected + len(failed)
	var (
		parts []string
		warn  bool
	)
	if totalMCP > 0 {
		if len(failed) == 0 {
			unit := "server"
			if totalMCP != 1 {
				unit = "servers"
			}
			parts = append(parts, fmt.Sprintf("MCP: %d %s ready", connected, unit))
		} else {
			parts = append(parts, fmt.Sprintf("MCP: %d/%d (failed: %s)", connected, totalMCP, strings.Join(failed, ", ")))
			warn = true
		}
	}

	backendType := c.Backend()
	ctxSize := c.ContextSize()
	if backendType != "" && backendType != client.BackendUnknown {
		if ctxSize > 0 {
			parts = append(parts, fmt.Sprintf("Backend: %s (%dk)", backendType, ctxSize/1024))
		} else {
			parts = append(parts, fmt.Sprintf("Backend: %s", backendType))
		}
	}
	mu.Unlock()

	summary := "Ready"
	if len(parts) > 0 {
		summary = strings.Join(parts, " • ")
	}

	p.Send(tui.BootstrapStatusMsg{
		Text:        summary,
		Warning:     warn,
		Active:      false,
		RefreshView: true,
	})
}
