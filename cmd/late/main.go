package main

import (
	"context"
	"errors"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"late/internal/assets"
	"late/internal/client"
	"late/internal/compaction"
	appconfig "late/internal/config"
	"late/internal/mcp"
	"late/internal/pathutil"
	"late/internal/plugin"
	"late/internal/session"
	"late/internal/tool"
	"late/internal/tui"

	"encoding/json"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"golang.org/x/term"
)

// askForUserApprovalUsage is the -h description of -ask-for-user-approval.
//
// This string must contain no back-quoted word: flag.PrintDefaults renders
// the first back-quoted word as the flag's value name, which would advertise
// this boolean flag as taking an argument.
const askForUserApprovalUsage = "Require explicit user approval before running potentially dangerous commands (default; overrides config.json permission-mode)."

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
	helpReq := flag.Bool("help", false, "Show this help and exit.")
	systemPromptReq := flag.String("system-prompt", "", "Replace the built-in system prompt with this text.")
	systemPromptFileReq := flag.String("system-prompt-file", "", "Replace the built-in system prompt with a file's contents (highest priority).")
	useToolsReq := flag.Bool("use-tools", true, "Offer tools to the main agent at all.")
	enableBashReq := flag.Bool("enable-bash", true, "Enable the bash tool.")
	injectCWDReq := flag.Bool("inject-cwd", true, "Replace ${{CWD}} in the system prompt with the working directory.")
	enableSubagentsReq := flag.Bool("enable-subagents", true, "Allow the agent to spawn subagents.")
	gemmaThinkingReq := flag.Bool("gemma-thinking", false, "Prepend the Gemma <|think|> token to the system prompt.")
	subagentMaxTurns := flag.Int("subagent-max-turns", 500, "Maximum turns per subagent.")
	// LATE_MAX_STREAM_RETRIES optionally overrides the default retry budget
	// for LLM stream errors; an explicit -max-stream-retries flag wins over it.
	maxStreamRetriesDefault := executor.DefaultMaxStreamRetries
	if v := os.Getenv("LATE_MAX_STREAM_RETRIES"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			maxStreamRetriesDefault = parsed
		} else {
			fmt.Fprintf(os.Stderr, "Warning: ignoring invalid LATE_MAX_STREAM_RETRIES %q: %v\n", v, err)
		}
	}
	maxStreamRetries := flag.Int("max-stream-retries", maxStreamRetriesDefault, "Retries for LLM stream errors with backoff; 0 disables. Env: LATE_MAX_STREAM_RETRIES")
	saveSubagentHistoriesReq := flag.Bool("save-subagent-histories", false, "Persist subagent histories to disk (overrides session and config).")
	enableSqzReq := flag.Bool("enable-sqz", false, "Compress bash tool output with the external 'sqz' binary if available.")
	appendSystemPromptReq := flag.String("append-system-prompt", "", "Append this text to the final system prompt.")
	versionReq := flag.Bool("version", false, "Print the version and exit.")
	unsupervisedReq := flag.Bool("i-promise-i-have-backups-and-will-not-file-issues", false, "UNSUPPORTED: run every tool without user confirmation.")
	askForUserApprovalReq := flag.Bool("ask-for-user-approval", false, askForUserApprovalUsage)
	enableImagesReq := flag.Bool("enable-images", false, "Force-enable image attachments even if the backend does not advertise vision support.")
	continueReq := flag.Bool("continue", false, "Resume the most recently updated session, regardless of which project directory it was started in.")
	continueProjectReq := flag.Bool("continue-project", false, "Resume the most recently updated session for the current project (git repo root of the working directory, or the working directory outside a repo); mutually exclusive with -continue.")
	showCWDReq := flag.Bool("show-cwd", true, "Show the git branch / working directory in the status bar.")
	themeReq := flag.String("theme", "", "Plugin theme id ('plugin:name' or bare name); env: LATE_THEME.")
	promptReq := flag.String("prompt", "", "Start the agent immediately with this prompt.")
	logitBiasReq := flag.String("logit-bias", "", "Main-agent token bias: JSON object or comma-separated TOKEN_ID:BIAS pairs.")
	suppressThinkingWordsReq := flag.Bool("suppress-thinking-words", false, "Bias anti-overthinking tokens (requires the same model for main agent and subagents).")
	subagentLogitBiasReq := flag.String("subagent-logit-bias", "", "Subagent token bias: JSON object or comma-separated TOKEN_ID:BIAS pairs.")
	// Compaction (staged rollout of the jev-compaction port): off = no
	// scoring at all; shadow = score tool outputs + shadow log only
	// (default, no behavior change); enabled = additionally relocate
	// low-scoring segments out of oversized tool results (registers the
	// expand tool so originals stay retrievable).
	compactionModeReq := flag.String("compaction-mode", "", "Tool-output compaction stage: off, shadow (score + shadow log only), or enabled (also relocate low-scoring segments; adds the expand tool). Overrides config.json compaction-mode. Default: shadow.")
	compactionThresholdReq := flag.Float64("compaction-threshold", compaction.DefaultRelocationThreshold, "Score (0-1] below which tool-output segments are elided when -compaction-mode=enabled. Overrides config.json compaction-threshold; default 0.35.")
	replayShadowReq := flag.String("replay-shadow", "", "Replay the default shadow log at the given comma-separated thresholds (e.g. 0.10,0.35,0.50): print the kept/relocated/tokens-saved/still-missed table plus the false-negative rate, then exit. Read-only; the TUI does not start.")
	checkCompactionReq := flag.Bool("check-compaction", false, "Run the compaction preflight against the resolved System One backend — real requests checking (1) decisions answers and parse, (2) the gate relocates something from a real tool output, (3) a pointer expands back byte for byte — print the per-stage report and exit (0 pass, 1 fail; the TUI does not start). Pairs with -compaction-mode. With config.json compaction-backend \"offline\" the same three stages run against the deterministic local scripted scorer: no key, no network.")

	flag.Usage = func() {
		writeHelp(os.Stderr, flag.CommandLine)
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

	// -replay-shadow: read-only offline replay of the default shadow log —
	// one kept/relocated/tokens-saved/still-missed row per given threshold
	// (re-decided from the recorded scores, no scorer round trip) plus the
	// false-negative rate — then exit without starting the TUI.
	//
	// This branch deliberately runs BEFORE appconfig.LoadConfig: the replay
	// consumes only the shadow log, never config.json, and LoadConfig has
	// side effects a read-only diagnostic must not take — it CREATES a
	// default config.json when the file is missing and tightens the config
	// dir/file permissions. The price is that the startup config warnings
	// (invalid compaction-mode, compaction-threshold-percent, …) are not
	// printed on this path; they surface on any normal run or
	// -check-compaction (which resolves the config below). If a replay ever
	// needs to honor a config setting, move this branch below the
	// LoadConfig block and accept the side effects.
	if *replayShadowReq != "" {
		thresholds, err := parseReplayThresholds(*replayShadowReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := runReplayShadow(thresholds); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --continue and --continue-project are mutually exclusive: both select
	// the session to resume, so asking for two is ambiguous (same rule and
	// messaging style as the permission flags).
	if err := validateContinueFlags(*continueReq, *continueProjectReq); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var loadedHistoryPath string
	var resumedSessionTitle string
	var loadedSessionMeta *session.SessionMeta

	switch {
	case *continueReq:
		// --continue: resume the most recently updated session overall,
		// regardless of the project directory it was started in.
		meta, err := resolveContinueSession()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting latest session: %v\n", err)
			os.Exit(1)
		}
		if meta == nil {
			fmt.Fprintln(os.Stderr, "No sessions found to continue.")
			fmt.Fprintln(os.Stderr, "Use `late session list` to see saved sessions, or `late session load <id>` to resume one directly.")
			os.Exit(1)
		}
		loadedHistoryPath = meta.HistoryPath
		resumedSessionTitle = fmt.Sprintf("Resumed session: %s (%s)", meta.ID, meta.Title)
		loadedSessionMeta = meta
	case *continueProjectReq:
		// --continue-project: resume the most recently updated session of
		// the current project (git repo root of the working directory, or
		// the working directory outside a repo). It works from inside a
		// subdirectory because the repo root is matched, not the CWD.
		meta, err := resolveContinueProjectSession()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting latest session: %v\n", err)
			os.Exit(1)
		}
		if meta == nil {
			if projectDir, dirErr := resolveContinueProjectDir(); dirErr == nil {
				fmt.Fprintf(os.Stderr, "No sessions found to continue in project %s.\n", projectDir)
			} else {
				fmt.Fprintln(os.Stderr, "No sessions found to continue in the current project.")
			}
			fmt.Fprintln(os.Stderr, "Use `late session list` to see sessions started in other projects, or `late session load <id>` to resume one directly.")
			os.Exit(1)
		}
		loadedHistoryPath = meta.HistoryPath
		resumedSessionTitle = fmt.Sprintf("Resumed session: %s (%s)", meta.ID, meta.Title)
		loadedSessionMeta = meta
	case flag.NArg() > 0 && flag.Arg(0) == "session":
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
	// Surface an invalid compaction-threshold-percent the same way the
	// invalid permission-mode is reported: warn once and use the default.
	if _, compactionWarning := appconfig.ResolveCompactionThreshold(appConfig); compactionWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", compactionWarning)
	}
	// Same warn-and-fall-back pattern for the auto-compaction threshold.
	if _, _, autocompactWarning := appconfig.ResolveAutocompact(appConfig); autocompactWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", autocompactWarning)
	}
	// Per-model jev-autocompact-percent overrides use the same key inside
	// each models[] entry; an out-of-range per-model value warns and falls
	// back to the global threshold (it cannot fail the models[] key walk —
	// that covers key names, not value ranges).
	for _, modelWarning := range appConfig.AutocompactWarnings() {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", modelWarning)
	}
	enabledTools := make(map[string]bool)
	if appConfig != nil {
		for toolName, enabled := range appConfig.EnabledTools {
			enabledTools[toolName] = enabled
		}
	}

	// Parse explicit user logit bias overrides if provided
	var explicitUserLogitBias map[string]int
	if *logitBiasReq != "" {
		parsed, err := client.ParseLogitBias(*logitBiasReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing --logit-bias: %v\n", err)
			os.Exit(1)
		}
		explicitUserLogitBias = parsed
	}

	var explicitSubagentLogitBias map[string]int
	if *subagentLogitBiasReq != "" {
		parsed, err := client.ParseLogitBias(*subagentLogitBiasReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing --subagent-logit-bias: %v\n", err)
			os.Exit(1)
		}
		explicitSubagentLogitBias = parsed
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

	// Resolve the effective permission mode
	// (explicit CLI flag > config.json permission-mode > ask-for-user-approval).
	permissionMode, permissionModeWarning, err := appconfig.ResolvePermissionMode(appConfig, *askForUserApprovalReq, *unsupervisedReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if permissionModeWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", permissionModeWarning)
	}

	// Initialize Core Components
	resolvedOpenAIConfig := appconfig.ResolveOpenAISettings(appConfig)
	resolvedClientConfig := client.Config{
		BaseURL:      resolvedOpenAIConfig.BaseURL,
		APIKey:       resolvedOpenAIConfig.APIKey,
		Model:        resolvedOpenAIConfig.Model,
		EnableImages: *enableImagesReq,
		LogitBias:    explicitUserLogitBias,
		AppVersion:   common.Version,
	}
	if appConfig != nil {
		if setting, ok := appConfig.GetModelForAgent("orchestrator"); ok {
			resolvedClientConfig.BaseURL = setting.URL
			resolvedClientConfig.APIKey = setting.Key
			resolvedClientConfig.Model = setting.Model
		}
	}
	resolvedSubagentConfig := appconfig.ResolveSubagentSettings(appConfig, resolvedOpenAIConfig)

	// Validate --suppress-thinking-words: only allowed in homogeneous setups
	if err := validateSuppressThinkingWords(*suppressThinkingWordsReq, resolvedClientConfig.Model, resolvedSubagentConfig.Model, appConfig); err != nil {
		fmt.Fprintf(os.Stderr, "Error: --suppress-thinking-words is currently only supported when orchestrator and subagents use the same model: %v\n", err)
		os.Exit(1)
	}

	c := client.NewClient(resolvedClientConfig)

	// Initialize Subagent Client
	subagentClient := c
	if len(explicitSubagentLogitBias) > 0 || len(explicitUserLogitBias) > 0 ||
		resolvedSubagentConfig.BaseURL != resolvedClientConfig.BaseURL ||
		resolvedSubagentConfig.APIKey != resolvedClientConfig.APIKey ||
		resolvedSubagentConfig.Model != resolvedClientConfig.Model {
		subagentClient = client.NewClient(client.Config{
			BaseURL:      resolvedSubagentConfig.BaseURL,
			APIKey:       resolvedSubagentConfig.APIKey,
			Model:        resolvedSubagentConfig.Model,
			EnableImages: *enableImagesReq,
			LogitBias:    explicitSubagentLogitBias,
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
	mainTools["create_todos"] = true
	mainTools["list_todos"] = true
	mainTools["finish_todo"] = true
	mainTools["write_file"] = false
	mainTools["target_edit"] = false

	sess := session.New(c, historyPath, history, systemPrompt, *useToolsReq)
	if loadedSessionMeta != nil {
		sess.SetSubagentMetadata(loadedSessionMeta.SubagentSeq, loadedSessionMeta.SaveSubagentHistories)
		if loadedSessionMeta.WorkingDir != "" {
			sess.SetWorkingDir(loadedSessionMeta.WorkingDir)
		}
		// Restore the compaction high-water mark so the frozen prefix stays
		// append-only across restarts: resumed sessions never re-score or
		// rewrite messages a previous run already froze. Legacy sidecars
		// without the field carry zero — the count-based prefix then applies.
		sess.SetCompactionHighWater(loadedSessionMeta.CompactionHighWater)
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

	// Compaction (staged rollout stage 2 of the jev-compaction port).
	// Mode resolution: the -compaction-mode flag beats config.json
	// compaction-mode; both are validated against the same three values
	// (invalid → warn + shadow, the safe default).
	compactionMode, compactionModeWarning := appconfig.ResolveCompactionMode(appConfig)
	if *compactionModeReq != "" {
		if appconfig.IsValidCompactionMode(*compactionModeReq) {
			compactionMode = *compactionModeReq
			compactionModeWarning = ""
		} else {
			compactionMode = appconfig.DefaultCompactionMode
			compactionModeWarning = fmt.Sprintf("ignoring invalid -compaction-mode %q; using %q",
				*compactionModeReq, appconfig.DefaultCompactionMode)
		}
	}
	if compactionModeWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", compactionModeWarning)
	}

	// Retrieval read side (Step 17): compaction-retrieval scores the record
	// store's digest against the current task before every stream request
	// and stages the top-k relevant records into the outgoing request's work
	// area (ephemeral — never the frozen prefix, never persisted). Resolved
	// with the same warn-on-invalid pattern as the other compaction knobs;
	// the warning fires for the inert combinations (mode not "enabled",
	// where the store never fills).
	compactionRetrieval, compactionRetrievalWarning := appconfig.ResolveCompactionRetrieval(appConfig)
	if compactionRetrievalWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", compactionRetrievalWarning)
	}

	// Backend selection (Step 18): config.json compaction-backend points
	// scoring at a specific scorer. The only value today is "offline" — the
	// deterministic scripted scorer (no API key, no network; demos and tests
	// only, its scores are content hashes). A set value WINS over the
	// environment: JEV_API and auto-detection are consulted only when the
	// entry is absent, because the config entry is the explicit statement
	// about where scoring happens. Invalid values warn and fall back to the
	// env-based path.
	compactionBackendName, compactionBackendWarning := appconfig.ResolveCompactionBackend(appConfig)
	if compactionBackendWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", compactionBackendWarning)
	}

	// -check-compaction: run the compaction preflight (Step 16) against the
	// backend THIS run would resolve and exit — the TUI never starts. The
	// mode resolution above is deliberately shared with the normal startup
	// path (the check must vet exactly the backend the session would use),
	// and -compaction-mode pairs with the flag, so an invalid mode warns
	// here the same way it would in a real run. The check itself exercises
	// scoring, the gate, and expansion — a superset of what shadow mode does
	// — so it applies in every mode: it is the "would have caught the
	// too-small local backend before integration" tool. With the offline
	// backend selected in config.json the same stages run against the
	// scripted scorer — no key, no network, and they pass by construction.
	if *checkCompactionReq {
		os.Exit(runCompactionCheck(compactionBackendName == appconfig.CompactionBackendOffline))
	}

	// Elision threshold: segments scoring strictly below it are relocated
	// out of oversized tool results when compaction-mode is enabled
	// (default per the upstream repo's own shadow-log replay data).
	// Precedence: an explicitly passed -compaction-threshold flag >
	// config.json compaction-threshold > the 0.35 default. The resolver
	// receives the flag value only when it was explicitly passed
	// (flag.Visit — config loads after flag.Parse, so this is the only
	// reliable explicit-flag signal); 0 otherwise, so the config entry can
	// win over the flag's built-in default.
	compactionThresholdFlagValue := 0.0
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "compaction-threshold" {
			compactionThresholdFlagValue = *compactionThresholdReq
		}
	})
	compactionThreshold, compactionThresholdWarning := appconfig.ResolveCompactionScoreThreshold(appConfig, compactionThresholdFlagValue)
	if compactionThresholdWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", compactionThresholdWarning)
	}

	// Gate safety knobs (reference-parity semantics for the elide decision).
	// Max elide fraction: a scorer that wants to drop more than this share
	// of an output's tokens is distrusted and nothing is elided. Protected
	// floor: stacktrace and diff segments are only elided below this score.
	compactionMaxElidePercent, maxElideWarning := appconfig.ResolveCompactionMaxElidePercent(appConfig)
	if maxElideWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", maxElideWarning)
	}
	compactionProtectedFloorPercent, protectedFloorWarning := appconfig.ResolveCompactionProtectedFloor(appConfig)
	if protectedFloorWarning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", protectedFloorWarning)
	}

	// The TUI's /jev-compact-context command and auto-trigger reuse this
	// pipeline (its scoring client) and elide store; both stay nil when
	// compaction is off or no backend resolved, which disables them.
	var (
		compactionPipeline  *compaction.Pipeline
		compactionStore     *compaction.Store
		compactionShadowLog *compaction.ShadowLog
		// compactionBackend is the resolved backend behind the pipeline,
		// captured for the Step 16 startup probe; nil when compaction is
		// off, no backend resolved, or the offline scripted scorer is
		// selected (nothing to probe — there is no network to reach).
		compactionBackend *compaction.ResolvedBackend
	)
	if compactionMode != appconfig.CompactionModeOff {
		// Scoring source: the offline scripted scorer (compaction-backend
		// "offline") or, when the config entry is absent/invalid, the
		// env-resolved System One backend as before. The config entry wins:
		// an "offline" session never resolves a backend, never needs a key,
		// and never sends a request.
		compactionOffline := compactionBackendName == appconfig.CompactionBackendOffline
		var (
			backend   compaction.ResolvedBackend
			scoringOK bool
		)
		if compactionOffline {
			scoringOK = true
		} else {
			resolved, backendErr := compaction.ResolveBackendEnv("")
			if backendErr != nil {
				if compactionMode == appconfig.CompactionModeEnabled {
					// Relocation without a backend would fail-open every
					// oversized result (nothing ever elided): warn and drop to
					// the shadow stage per the staged rollout.
					fmt.Fprintf(os.Stderr, "Warning: compaction-mode %q needs a System One backend (%v); falling back to %q\n",
						appconfig.CompactionModeEnabled, backendErr, appconfig.CompactionModeShadow)
					compactionMode = appconfig.CompactionModeShadow
				} else {
					fmt.Fprintf(os.Stderr, "Warning: compaction-mode %q has no System One backend (%v)\n",
						appconfig.CompactionModeShadow, backendErr)
				}
			} else {
				backend = resolved
				scoringOK = true
			}
		}
		// The pipeline only exists behind usable scoring: without it every
		// decisions call would burn retries and fail-open, so this run
		// proceeds with compaction off instead (the warning above explains).
		// Offline scoring is always usable — that is the point of the demo
		// path.
		if scoringOK {
			shadowLog, shadowErr := compaction.NewShadowLog()
			if shadowErr != nil {
				// Logging is best-effort: scoring (and relocation) still
				// run without it.
				fmt.Fprintf(os.Stderr, "Warning: compaction shadow log unavailable (%v); continuing without it\n", shadowErr)
				shadowLog = nil
			}
			compactionShadowLog = shadowLog
			var pipeline *compaction.Pipeline
			if compactionOffline {
				// Step 18 demo path: the same segmentation, gate, pointers,
				// and store over the deterministic scripted scorer. Demos
				// and tests only — the scripted scores are content hashes,
				// not essentialness judgments, and must never become a
				// production default.
				pipeline = compaction.NewOfflinePipeline(compaction.PipelineOptions{Shadow: shadowLog})
			} else {
				compactionBackend = &backend
				pipeline = compaction.NewPipeline(backend, "", shadowLog, compaction.PipelineOptions{})
			}
			// GateConfig: the reference-parity elision safety semantics —
			// keep threshold, elide-fraction tripwire, and protected-kind
			// floors — threaded from config.json (defaults mirror the
			// reference pipeline.py). KeepThreshold uses the SAME resolved
			// compactionThreshold as EnableRelocation below — one source of
			// truth for the elision cutoff (flag > config > default).
			gate := compaction.DefaultGateConfig()
			gate.KeepThreshold = compactionThreshold
			gate.MaxElideFraction = float64(compactionMaxElidePercent) / 100
			protectedFloor := float64(compactionProtectedFloorPercent) / 100
			gate.ProtectedKinds = map[compaction.SegmentKind]float64{
				compaction.KindStacktrace: protectedFloor,
				compaction.KindDiff:       protectedFloor,
			}
			pipeline.ApplyGateConfig(gate)
			if compactionMode == appconfig.CompactionModeEnabled {
				// The record store persists elided originals across
				// restarts: [[elided …]] pointers saved into a session
				// history must still resolve after `late` exits, so
				// relocation is backed by the append-only JSONL store at
				// compaction.DefaultStorePath instead of a throwaway
				// in-memory map. Open failure degrades to the in-memory
				// store — compaction keeps working, pointers merely stop
				// surviving restarts (the shadow-log warning pattern).
				store := openCompactionStore()
				// Outcomes: the expand tool attributes every expand back to
				// the record and its contributing segment ids through the
				// shadow log attached here (Step 13's false-negative
				// ledger). Nil-safe — a missing shadow log simply disables
				// outcome logging.
				store = store.WithShadowLog(compactionShadowLog)
				pipeline.EnableRelocation(store, compactionThreshold)
				// The expand tool returns relocated originals. Registered on
				// the main registry before any spawn: subagents inherit it
				// (and the same store) from the parent registry.
				sess.Registry.Register(tool.ExpandTool{Store: store})
				compactionStore = store
			}
			// Shared by the root agent and every subagent: ExecuteToolCalls
			// consults it for both (shadow mode scores and logs without
			// changing results).
			executor.SetToolResultCompactor(pipeline)
			compactionPipeline = pipeline
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
	rootAgent := orchestrator.NewBaseOrchestrator(common.MainAgentID, sess, nil, 0)

	model := tui.NewModel(rootAgent, renderer, appConfig)
	model.SetActiveThemeStyles(themeBytes)
	if themeID != "" {
		model.SelectedTheme = themeID
	}
	model.ApplyOrchestratorModel = func(setting appconfig.ModelSetting) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Either both biases or none should be sent: only pass logit biases
			// if the switched model matches the configured orchestrator model.
			var bias map[string]int
			if setting.Model == resolvedClientConfig.Model {
				bias = c.LogitBias()
			}
			sess.SetClient(newModelClient(ctx, setting, *enableImagesReq, bias))
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

	// History compaction for /jev-compact-context + the auto-trigger: the
	// session's CompactContext shares the pipeline's scoring client and its
	// elide-id space (the same store the expand tool reads). Shadow mode
	// reports without mutating; enabled mode persists the compacted history.
	if compactionPipeline != nil {
		if compactionStore == nil {
			// Shadow mode: the history walk still mints pointer ids for its
			// honest report, so it needs a store even though nothing is
			// applied; a fresh one keeps those ids out of the (absent)
			// expand tool's id space.
			compactionStore = compaction.NewStore()
		}
		model.Compactor = historyCompactionRunner(sess, compactionPipeline.HistoryScorer(), compactionStore,
			compactionMode != appconfig.CompactionModeEnabled, compactionThreshold, compactionShadowLog)
		// The TUI's one-shot 413 payload-recovery compaction only fires when
		// compaction can actually shrink history: mode "enabled" (after the
		// shadow fallback above, which downgrades to shadow when the
		// backend is unavailable — compactionMode is re-read here, so the
		// fallback is honored), not shadow report-only runs.
		//
		// Ordering invariant: this assignment runs before tea.NewProgram
		// below, and the TUI can only observe a 413 after a run starts —
		// which requires a submitted message through the live program. So
		// no event can trigger the recovery before the flag is set: the
		// startup race is closed by construction, not by synchronization.
		model.CompactionApplies = compactionMode == appconfig.CompactionModeEnabled
	}

	// Retrieval hooks (Step 17): BaseOrchestrator runs the hook at every
	// turn start — right before that turn's stream request — so each agent
	// (root and every subagent, which each own a session) stages retrieved
	// context into its own request's work area. The hook owns its errors:
	// a failed retrieval warns once and the turn proceeds without it; the
	// next turns retry, so one flaky scoring round never disables the
	// feature for the session.
	var retrievalWarnOnce sync.Once
	var retrievalHookFor func(s *session.Session) func(context.Context)
	// diagSink reads the mid-session diagnostics sink at hook-run time: diag
	// (below, after the TUI program exists) assigns it, so closures created
	// here — before the program starts — route their warnings through the
	// live TUI instead of raw stderr. The write happens before p.Run() and
	// before any agent run can start (runs begin only when the TUI submits
	// a message), so the assignment happens-before every read. nil (CLI
	// flows, bootstrap) keeps the os.Stderr fallback.
	var diagSink func(msg string)
	if compactionRetrieval && compactionPipeline != nil {
		retrievalHookFor = func(s *session.Session) func(context.Context) {
			return func(ctx context.Context) {
				if _, err := s.InjectRetrieved(ctx, compactionPipeline, compactionStore,
					compaction.DefaultRetrieveK, compaction.DefaultRetrieveBudgetTokens, compaction.DefaultRetrieveThreshold); err != nil {
					retrievalWarnOnce.Do(func() {
						if diagSink != nil {
							diagSink(fmt.Sprintf("Warning: compaction retrieval skipped (%v); later turns retry\n", err))
							return
						}
						fmt.Fprintf(os.Stderr, "Warning: compaction retrieval skipped (%v); later turns retry\n", err)
					})
				}
			}
		}
		rootAgent.SetRetrievalHook(retrievalHookFor(sess))
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

	// diag is the mid-session diagnostics sink: compaction's mid-session
	// warnings — the pipeline's one-time auth-poison note and the retrieval-
	// skip notice — are delivered to the live TUI as DiagnosticMsg warning
	// toasts instead of raw fmt.Fprintf(os.Stderr, ...) writes, which paint
	// text over the alt-screen (duplicated footer rows, displaced agent-name
	// line). The trailing newline the stderr formatting carries is trimmed
	// here so the toast text is clean. Sources without a sink installed
	// (CLI flows, pre-TUI bootstrap) still fall back to os.Stderr. Every
	// diagnostic is ALSO appended to the durable critical-error log
	// (~/.local/share/late/late-errors.log): a toast disappears with the
	// terminal, the file does not — best-effort, never fails the caller.
	diag := func(msg string) {
		common.LogError("diagnostic", strings.TrimRight(msg, "\n"))
		p.Send(tui.DiagnosticMsg{Text: strings.TrimRight(msg, "\n")})
	}
	// Publish the sink to closures created before the program existed (see
	// diagSink above), and give the compaction pipeline's one-time
	// auth-poison warning the same route: it can fire mid-session (first
	// scoring call after a key is revoked) and must not paint raw stderr
	// over the alt-screen either.
	diagSink = diag
	if compactionPipeline != nil {
		compactionPipeline.SetWarningSink(diag)
	}

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
		switch permissionMode {
		case appconfig.PermissionModeUnsupervised:
			ctx = context.WithValue(ctx, common.SkipConfirmationKey, true)
		}
		ctx = context.WithValue(ctx, common.MaxStreamRetriesKey, *maxStreamRetries)
		rootAgent.SetContext(ctx)

		// Set middlewares (see buildMiddlewares for ordering rationale).
		rootAgent.SetMiddlewares(buildMiddlewares(pluginManager, p, sess.Registry))

		// Start forwarding events from the root agent to the TUI
		ForwardOrchestratorEvents(p, rootAgent)

		// Wait only in this background goroutine: the TUI remains usable while
		// connections and discovery finish, but --prompt needs their results.
		runBootstrap(p, mcpClient, config, c, subagentClient, sess, enabledTools, pluginManager, toolSync, *suppressThinkingWordsReq, explicitUserLogitBias, explicitSubagentLogitBias)

		if *promptReq != "" {
			p.Send(tui.StartPromptMsg(*promptReq))
		}
	}()

	// Startup compaction probe (Step 16): one cheap ScoreBatch with a single
	// small item against the resolved backend, in its own goroutine so the
	// first paint never waits for the backend. A failure never tears the
	// pipeline down — scoring is fail-open by contract and shadow mode is
	// harmless — it warns once on stderr, surfaces the reason in the status
	// bar, and, ONLY for a typed auth rejection, disables the session's
	// scoring through the same path a live 401 takes (the probe's client is
	// a throwaway, so without this the live pipeline would learn on its
	// first real scoring call against a backend that can only say 401). The
	// probe is deliberately NOT logged as a shadow decision: it is not a
	// scoring decision, and one probe line per launch would pollute the
	// replay ledger.
	if compactionPipeline != nil && compactionBackend != nil {
		probeBackend := *compactionBackend
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), compactionProbeTimeout)
			defer cancel()
			if err := compaction.ProbeBackend(ctx, probeBackend, ""); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: compaction backend probe failed (%v); scoring fails open this session\n", err)
				common.LogErrorf("compaction", "backend probe failed: %v", err)
				p.Send(tui.BootstrapStatusMsg{
					Text:    "compaction: backend probe failed — scoring fails open",
					Warning: true,
				})
				var ce *compaction.Error
				if errors.As(err, &ce) && ce.Kind == compaction.KindAuth {
					compactionPipeline.DisableAuth(ce.Error())
				}
				return
			}
			p.Send(tui.BootstrapStatusMsg{Text: "compaction: backend probe OK", Active: false})
		}()
	}

	if *enableSubagentsReq {
		runner := func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			var currentSubagentClient *client.Client
			if appConfig != nil {
				if setting, ok := appConfig.GetModelForAgent(agentType); ok {
					var biasForSubagent map[string]int
					if setting.Model == resolvedSubagentConfig.Model {
						biasForSubagent = subagentClient.LogitBias()
					}
					currentSubagentClient = client.NewClient(client.Config{
						BaseURL:      setting.URL,
						APIKey:       setting.Key,
						Model:        setting.Model,
						EnableImages: *enableImagesReq,
						LogitBias:    biasForSubagent,
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

			// Retrieval read side (Step 17): children get the same per-turn
			// hook as the root agent — the work-area injection is per-agent
			// session, while the record store and pipeline are shared.
			if retrievalHookFor != nil {
				if bo, ok := child.(*orchestrator.BaseOrchestrator); ok {
					bo.SetRetrievalHook(retrievalHookFor(bo.Session()))
				}
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

// compactionCheckTimeout bounds the whole -check-compaction preflight (three
// stages of real requests against the backend, one attempt each; the offline
// backend's stages are local and finish in microseconds) and
// compactionProbeTimeout bounds the light startup probe. Generous enough for
// a slow local gateway, short enough that a dead endpoint cannot hang the
// flag or the startup path.
const (
	compactionCheckTimeout = 90 * time.Second
	compactionProbeTimeout = 30 * time.Second
)

// runCompactionCheck runs the compaction preflight against the backend the
// normal startup path resolves — the same compaction.ResolveBackendEnv("")
// call the pipeline wiring makes — and returns the process exit code: 0 when
// every stage passes, 1 otherwise. A missing backend or key is stage 0's
// failure: the report then says what to configure instead of starting a run
// that cannot score anything. With offline (config.json compaction-backend
// "offline") the same three stages run against the deterministic scripted
// scorer instead: no backend is resolved, no key is consulted, no request is
// sent, and the stages pass by construction — the demo path's self-test.
func runCompactionCheck(offline bool) int {
	if offline {
		ctx, cancel := context.WithTimeout(context.Background(), compactionCheckTimeout)
		defer cancel()
		results, ok := compaction.RunPreflightOffline(ctx)
		fmt.Print(compaction.FormatCheckReport(results, ok))
		if !ok {
			return 1
		}
		return 0
	}
	backend, backendErr := compaction.ResolveBackendEnv("")
	if backendErr != nil {
		fmt.Print(compaction.FormatCheckReport(noBackendCheckResults(backendErr), false))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), compactionCheckTimeout)
	defer cancel()
	results, ok := compaction.RunPreflight(ctx, backend, "", nil)
	fmt.Print(compaction.FormatCheckReport(results, ok))
	if !ok {
		return 1
	}
	return 0
}

// noBackendCheckResults builds the stage-0 failure report for a run with no
// resolved compaction backend: the three real stages cannot run without one,
// and the detail carries the guidance plus the resolver's typed reason (which
// backend was tried and what each was missing).
func noBackendCheckResults(backendErr error) []compaction.CheckResult {
	return []compaction.CheckResult{{
		Stage:  compaction.CheckStageBackend,
		OK:     false,
		Detail: fmt.Sprintf("no compaction backend configured (set the provider key or run with -compaction-mode pointing at a gateway): %v", backendErr),
	}}
}

// openCompactionStore opens the persistent elided-record store at the
// default path (compaction.DefaultStorePath), degrading to the in-memory
// store — with a stderr warning — when the path cannot be resolved or the
// file cannot be opened. Compaction must keep working even when its
// persistence layer fails, exactly like the shadow log: the session loses
// only cross-restart pointer resolution, nothing else.
func openCompactionStore() *compaction.Store {
	path, err := compaction.DefaultStorePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: compaction record store path unavailable (%v); continuing in-memory — elided originals will not survive restarts\n", err)
		common.LogErrorf("compaction-store", "record store path unavailable: %v", err)
		return compaction.NewStore()
	}
	return openCompactionStoreAt(path)
}

// openCompactionStoreAt is openCompactionStore for an explicit path; split
// out so tests can exercise the degrade-to-in-memory fallback without
// touching the real user store.
func openCompactionStoreAt(path string) *compaction.Store {
	store, err := compaction.OpenStore(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: compaction record store unavailable (%v); continuing in-memory — elided originals will not survive restarts\n", err)
		common.LogErrorf("compaction-store", "record store unavailable at %s: %v", path, err)
		return compaction.NewStore()
	}
	return store
}

// parseReplayThresholds parses a -replay-shadow value: a comma-separated
// list of keep thresholds in (0, 1], e.g. "0.10,0.35,0.50". Surrounding
// whitespace is tolerated. Empty entries, non-numeric values, and
// out-of-range thresholds are errors — the caller asked for an explicit
// replay, so silently clamping would misrepresent it.
func parseReplayThresholds(s string) ([]float64, error) {
	var out []float64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid -replay-shadow value %q: empty threshold", s)
		}
		th, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid -replay-shadow threshold %q: %v", part, err)
		}
		if th <= 0 || th > 1 {
			return nil, fmt.Errorf("invalid -replay-shadow threshold %v: must be in (0, 1]", th)
		}
		out = append(out, th)
	}
	return out, nil
}

// runReplayShadow prints the replay table (one row per threshold, re-decided
// from the log's recorded scores without any scorer round trip) plus the
// false-negative rate line for the default shadow log, then returns; the
// caller exits. Strictly read-only: a missing log is reported as "nothing
// scored yet" and nothing is created — the only constructor reached,
// NewShadowLogAt, runs after a stat confirmed the file exists (its parent
// mkdir is then a no-op) and nothing appends to it.
func runReplayShadow(thresholds []float64) error {
	path, err := compaction.DefaultShadowPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No shadow log at %s — nothing scored yet (compaction modes shadow and enabled write it).\n", path)
			return nil
		}
		return err
	}
	shadowLog, err := compaction.NewShadowLogAt(path)
	if err != nil {
		return err
	}
	rows, err := shadowLog.ReplayTable(thresholds)
	if err != nil {
		return err
	}
	ffr, err := shadowLog.FalseNegativeRate()
	if err != nil {
		return err
	}
	fmt.Printf("Shadow log: %s\n\n", path)
	fmt.Print(formatReplayTable(rows, ffr))
	return nil
}

// formatReplayTable renders the replay rows as an aligned table — threshold,
// kept, relocated, tokens saved, still missed — followed by the
// false-negative rate line. Split from runReplayShadow so tests can pin the
// exact rendering.
func formatReplayTable(rows []compaction.ReplayRow, ffr float64) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "threshold\tkept\trelocated\ttokens saved\tstill missed")
	for _, r := range rows {
		fmt.Fprintf(tw, "%.2f\t%d\t%d\t%d\t%d\n", r.Threshold, r.Kept, r.Relocated, r.TokensSaved, r.StillMissed)
	}
	tw.Flush()
	fmt.Fprintf(&b, "\nfalse-negative rate: %.1f%%\n", ffr*100)
	return b.String()
}

// historyCompactionRunner adapts the live session for the TUI's
// /jev-compact-context command and auto-trigger: each call runs one
// session.CompactContext pass — scoring history segments against the ongoing
// task with the pipeline's decision client and relocating low scorers into
// the shared elide store — and persists the mutated history the same way the
// orchestrator's own SaveHistory call sites do. Shadow runs (compaction-mode
// "shadow") compute the honest would-save report without touching history,
// so they skip persistence. threshold mirrors the pipeline's elision
// threshold so both compaction paths make the same keep/elide calls.
// shadowLog receives one "history-run" summary line per run — shadow or
// mutating, failed or clean — so runs are auditable next to the per-segment
// decisions they produced; it may be nil (logging is unavailable), and an
// append failure is a stderr warning, never a run failure.
func historyCompactionRunner(sess *session.Session, scorer session.HistoryScorer, store session.ElideStore, shadow bool, threshold float64, shadowLog *compaction.ShadowLog) func(context.Context) (session.CompactionReport, error) {
	return func(ctx context.Context) (session.CompactionReport, error) {
		report, err := sess.CompactContext(ctx, scorer, store, session.CompactionOptions{
			Threshold:  threshold,
			ShadowOnly: shadow,
		})
		if !shadow {
			// The walk mutated (or partially mutated — a mid-walk scorer
			// failure leaves consistent pointers and stored originals)
			// history: persist it even when err != nil.
			if saveErr := session.SaveHistory(sess.HistoryPath, sess.History); saveErr != nil {
				err = errors.Join(err, fmt.Errorf("saving compacted history: %w", saveErr))
			}
		}
		if err != nil {
			// Durable record of the failure (walk aborts, save failures):
			// the toast/TUI notice is ephemeral, the error log is not.
			// Best-effort — never fails the run.
			common.LogErrorf("compaction", "history compaction run failed (shadow=%v): %v", shadow, err)
		}
		if shadowLog != nil {
			run := compaction.RunSummary{
				Shadow:       shadow,
				Scanned:      report.MessagesScanned,
				Scored:       report.MessagesScored,
				Elided:       report.SegmentsElided,
				TokensBefore: report.TokensBefore,
				TokensAfter:  report.TokensAfter,
				TokensSaved:  report.TokensSaved,
			}
			if err != nil {
				run.Err = err.Error()
			}
			// Best-effort: a logging failure must never fail the compaction
			// itself, so it only surfaces as a warning.
			if appendErr := shadowLog.AppendRun(report.TaskHash, run); appendErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: compaction run summary not logged (%v)\n", appendErr)
			}
		}
		return report, err
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
func newModelClient(ctx context.Context, setting appconfig.ModelSetting, enableImages bool, logitBias map[string]int) *client.Client {
	c := client.NewClient(client.Config{
		BaseURL:      setting.URL,
		APIKey:       setting.Key,
		Model:        setting.Model,
		EnableImages: enableImages,
		LogitBias:    logitBias,
		AppVersion:   common.Version,
	})
	c.DiscoverBackend(ctx)
	return c
}

func validateSuppressThinkingWords(suppressThinkingWords bool, orchestratorModel, subagentModel string, appConfig *appconfig.Config) error {
	if !suppressThinkingWords {
		return nil
	}
	if orchestratorModel != subagentModel {
		return fmt.Errorf("orchestrator and subagents use different models (%q vs %q)", orchestratorModel, subagentModel)
	}
	if appConfig != nil {
		for _, sub := range assets.GetSubagents() {
			if setting, ok := appConfig.GetModelForAgent(sub.Name); ok && setting.Model != orchestratorModel {
				return fmt.Errorf("subagent %q uses a different model (%q vs %q)", sub.Name, setting.Model, orchestratorModel)
			}
		}
	}
	return nil
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

// validateContinueFlags enforces that at most one of --continue and
// --continue-project is passed: both select the session to resume, so
// requesting both is ambiguous. The messaging mirrors the permission-flag
// exclusivity error.
func validateContinueFlags(continueFlag, continueProjectFlag bool) error {
	if continueFlag && continueProjectFlag {
		return fmt.Errorf("continue flags are mutually exclusive; pass at most one of -continue, -continue-project")
	}
	return nil
}

// resolveContinueSession returns the session to resume for --continue: the
// most recently updated session overall, regardless of which project
// directory it was started in. It returns (nil, nil) when no sessions exist.
func resolveContinueSession() (*session.SessionMeta, error) {
	return session.GetLatestSession()
}

// resolveContinueProjectDir returns the project directory that scopes
// --continue-project: the git repository root containing the current working
// directory (so the flag also works from inside a subdirectory), or the
// working directory itself when it is not inside a git repository.
func resolveContinueProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determining current directory: %w", err)
	}
	if root, ok := git.RepoRoot(cwd); ok {
		return root, nil
	}
	return cwd, nil
}

// resolveContinueProjectSession returns the session to resume for
// --continue-project: the most recently updated session whose recorded
// project directory is the current project. It returns (nil, nil) when no
// matching session exists.
func resolveContinueProjectSession() (*session.SessionMeta, error) {
	projectDir, err := resolveContinueProjectDir()
	if err != nil {
		return nil, err
	}
	return session.GetLatestSessionForDir(projectDir)
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
		_ = fs.Parse(args[1:])
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
		handleWorktreeCreate(path, branch)
		return true
	case "remove":
		if len(args) < 2 {
			fmt.Println("Error: path required for remove command")
			fmt.Println("Usage: late worktree remove <path>")
			return true
		}
		handleWorktreeRemove(args[1])
		return true
	case "active":
		handleWorktreeActive()
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

	fmt.Println(path)
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
func runBootstrap(p *tea.Program, mcpClient *mcp.Client, config *mcp.MCPConfig, c *client.Client, subagentClient *client.Client, sess *session.Session, enabledTools map[string]bool, pluginManager *plugin.PluginManager, toolSync *pluginToolSync, suppressThinkingWords bool, explicitUserLogitBias, explicitSubagentLogitBias map[string]int) {
	var (
		wg             sync.WaitGroup
		mu             sync.Mutex
		connected      int
		failed         []string
		logitBiasToast *tui.ToastMsg
	)

	sendMsg := func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	}

	hasMCP := config != nil && len(config.McpServers) > 0

	// Initial notification inside TUI
	if hasMCP {
		sendMsg(tui.BootstrapStatusMsg{
			Text:   "Connecting MCP servers & discovering backend...",
			Active: true,
		})
	} else {
		sendMsg(tui.BootstrapStatusMsg{
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
					sendMsg(tui.BootstrapStatusMsg{
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
				sendMsg(tui.BootstrapStatusMsg{
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
		sendMsg(tui.BootstrapStatusMsg{
			Text:        fmt.Sprintf("Backend: %s%s", b, ctxText),
			Active:      true,
			RefreshView: true,
		})

		if suppressThinkingWords {
			if c.IsLlamaCPP() {
				resolveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				resolved, err := client.ResolveThinkingBiases(resolveCtx, c.BaseURL(), c.APIKey(), c.HTTPClient())
				if err != nil {
					mu.Lock()
					logitBiasToast = &tui.ToastMsg{
						Text:    "Logit bias failed: tokenize error",
						Warning: true,
					}
					mu.Unlock()
				} else {
					c.SetLogitBias(client.MergeLogitBiases(resolved, explicitUserLogitBias))
					if subagentClient != c {
						subagentClient.SetLogitBias(client.MergeLogitBiases(resolved, explicitSubagentLogitBias))
					}
					mu.Lock()
					logitBiasToast = &tui.ToastMsg{
						Text: "Applied logit biases",
					}
					mu.Unlock()
				}
			} else {
				mu.Lock()
				logitBiasToast = &tui.ToastMsg{
					Text:    "Logit bias failed: not llama.cpp",
					Warning: true,
				}
				mu.Unlock()
			}
		} else if len(explicitUserLogitBias) > 0 || len(explicitSubagentLogitBias) > 0 {
			mu.Lock()
			logitBiasToast = &tui.ToastMsg{
				Text: "Applied logit biases",
			}
			mu.Unlock()
		}
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

	sendMsg(tui.BootstrapStatusMsg{
		Text:        summary,
		Warning:     warn,
		Active:      false,
		RefreshView: true,
		NextToast:   logitBiasToast,
	})
}
