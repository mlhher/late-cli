package main

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"late/internal/assets"
	"late/internal/client"
	"late/internal/common"
	appconfig "late/internal/config"
	"late/internal/executor"
	"late/internal/mcp"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tui"
)

func TestStartupTiming(t *testing.T) {
	t0 := time.Now()
	mark := func(name string) {
		t1 := time.Now()
		fmt.Printf("[TIMING] %-35s: %v\n", name, t1.Sub(t0))
		t0 = t1
	}

	// 1. Load MCP config
	mcpConfig, _ := mcp.LoadMCPConfig()
	mark("mcp.LoadMCPConfig")

	// 2. Load App config
	appConfig, _ := appconfig.LoadConfig()
	mark("appconfig.LoadConfig")

	// 3. New Client
	c := client.NewClient(client.Config{
		BaseURL: "http://localhost:8080",
	})
	mark("client.NewClient")

	// 4. Session with real system prompt and history
	promptContent, _ := assets.PromptsFS.ReadFile("prompts/instruction-orchestrator.md")
	systemPrompt := string(promptContent)
	history := []client.ChatMessage{}
	sess := session.New(c, "", history, systemPrompt, true)
	mark("session.New (with real prompt)")

	// 5. RegisterTools
	executor.RegisterTools(sess.Registry, map[string]bool{"read_file": true, "bash": true})
	mark("executor.RegisterTools")

	// 6. Glamour NewTermRenderer
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(tui.LateTheme),
		glamour.WithWordWrap(80),
		glamour.WithPreservedNewLines(),
	)
	mark("glamour.NewTermRenderer")

	// 7. BaseOrchestrator
	rootAgent := orchestrator.NewBaseOrchestrator("main", sess, nil, 0)
	mark("NewBaseOrchestrator")

	// 8. tui.NewModel
	model := tui.NewModel(rootAgent, renderer, appConfig)
	mark("tui.NewModel")

	// 9. tea.NewProgram
	p := tea.NewProgram(model)
	_ = p
	mark("tea.NewProgram")

	// 10. Model.Init()
	initCmd := model.Init()
	mark("model.Init()")
	_ = initCmd

	// 11. Initial View() render before WindowSizeMsg
	model.Width = 100
	model.Height = 30
	_ = model.View()
	mark("model.View() (before WindowSizeMsg)")

	// 11b. WindowSizeMsg processing (layout + welcome markdown render)
	newModel, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = newModel.(tui.Model)
	mark("model.Update(WindowSizeMsg)")

	// 11c. View() after WindowSizeMsg
	_ = model.View()
	mark("model.View() (after WindowSizeMsg)")

	// 12. DiscoverBackend with backend running (localhost:8080)
	c.DiscoverBackend(t.Context())
	mark("DiscoverBackend (running)")

	// 13. DiscoverBackend with dead port (localhost:54321)
	cDead := client.NewClient(client.Config{
		BaseURL: "http://localhost:54321",
	})
	cDead.DiscoverBackend(t.Context())
	mark("DiscoverBackend (dead port)")

	// 14. BPE vocab load and CalculateHistoryTokens
	tBpe := time.Now()
	_ = common.CalculateHistoryTokens(nil, rootAgent.SystemPrompt(), rootAgent.ToolDefinitions())
	fmt.Printf("[TIMING] %-35s: %v\n", "CalculateHistoryTokens (cold BPE)", time.Since(tBpe))

	_ = mcpConfig
}

func TestFullStartupPipeline(t *testing.T) {
	start := time.Now()

	// Simulate main() exact execution path up to and including frame 1 rendering
	appConfig, _ := appconfig.LoadConfig()
	c := client.NewClient(client.Config{
		BaseURL: "http://localhost:8080",
	})
	promptContent, _ := assets.PromptsFS.ReadFile("prompts/instruction-orchestrator.md")
	systemPrompt := string(promptContent)
	sess := session.New(c, "", nil, systemPrompt, true)
	executor.RegisterTools(sess.Registry, map[string]bool{"write_implementation_plan": true})
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(tui.LateTheme),
		glamour.WithWordWrap(80),
		glamour.WithPreservedNewLines(),
	)
	rootAgent := orchestrator.NewBaseOrchestrator("main", sess, nil, 0)
	model := tui.NewModel(rootAgent, renderer, appConfig)
	model.SetSize(120, 40)
	p := tea.NewProgram(model, tea.WithWindowSize(120, 40))
	_ = p

	// Render Frame 1
	v := model.View()
	elapsed := time.Since(start)

	if len(v.Content) == 0 {
		t.Fatalf("Frame 1 content is empty")
	}

	fmt.Printf("[BENCHMARK] Total time from process start to full Frame 1 render: %v (content length: %d bytes)\n", elapsed, len(v.Content))
}
