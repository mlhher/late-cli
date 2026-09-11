package main

import (
	"context"
	"testing"

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

func BenchmarkStartupTiming(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// 1. Load MCP config
		_, _ = mcp.LoadMCPConfig()

		// 2. Load App config
		appConfig, _ := appconfig.LoadConfig()

		// 3. New Client
		c := client.NewClient(client.Config{
			BaseURL: "http://localhost:8080",
		})

		// 4. Session with real system prompt and history
		promptContent, _ := assets.PromptsFS.ReadFile("prompts/instruction-orchestrator.md")
		systemPrompt := string(promptContent)
		sess := session.New(c, "", nil, systemPrompt, true)

		// 5. RegisterTools
		executor.RegisterTools(sess.Registry, map[string]bool{"read_file": true, "bash": true})

		// 6. Glamour NewTermRenderer
		renderer, _ := glamour.NewTermRenderer(
			glamour.WithStylesFromJSONBytes(tui.LateTheme),
			glamour.WithWordWrap(80),
			glamour.WithPreservedNewLines(),
		)

		// 7. BaseOrchestrator
		rootAgent := orchestrator.NewBaseOrchestrator("main", sess, nil, 0)

		// 8. tui.NewModel
		model := tui.NewModel(rootAgent, renderer, appConfig)

		// 9. Initial View() render before WindowSizeMsg
		model.Width = 100
		model.Height = 30
		_ = model.View()

		// 10. WindowSizeMsg processing (layout + welcome markdown render)
		newModel, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		model = newModel.(tui.Model)

		// 11. View() after WindowSizeMsg
		_ = model.View()

		// 12. Calculate history tokens fast
		_ = common.CalculateHistoryTokensFast(nil, rootAgent.SystemPrompt(), rootAgent.ToolDefinitions())
	}
}

func BenchmarkFullStartupPipeline(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
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
		_ = tea.NewProgram(model, tea.WithWindowSize(120, 40))

		// Render Frame 1
		v := model.View()
		if len(v.Content) == 0 {
			b.Fatalf("Frame 1 content is empty")
		}
	}
}

func BenchmarkDiscoverBackend(b *testing.B) {
	c := client.NewClient(client.Config{
		BaseURL: "http://localhost:54321",
	})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.DiscoverBackend(ctx)
	}
}
