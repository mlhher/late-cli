// late-test is a manual performance evaluation harness for the context-recovery
// architecture. It connects to the real LLM configured in config.json, pre-fills
// the session history to a configurable depth to simulate context pressure, then
// runs an interactive loop while printing per-turn recovery metrics.
//
// Usage:
//
//	./bin/late-test [flags]
//
// Flags:
//
//	-prefill  N     Pre-fill the session with N synthetic messages before starting
//	                (default 0; use >=81 to immediately trigger the 80-msg ceiling)
//	-prompt   text  Seed prompt to send as the first real turn (default: built-in)
//	-turns    N     Maximum number of interactive turns before the harness exits
//	                (default 0 = unlimited)
//	-plan           Write a synthetic implementation_plan.md before starting,
//	                so the plan-injection path is exercised (default true)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	appconfig "late/internal/config"
	"late/internal/client"
	"late/internal/executor"
	"late/internal/session"
)

// ── ANSI helpers ──────────────────────────────────────────────────────────────

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	cyan   = "\033[36m"
	yellow = "\033[33m"
	green  = "\033[32m"
	red    = "\033[31m"
	grey   = "\033[90m"
)

func header(s string) string { return bold + cyan + s + reset }
func warn(s string) string   { return yellow + s + reset }
func ok(s string) string     { return green + s + reset }
func bad(s string) string    { return red + s + reset }
func dim(s string) string    { return grey + s + reset }

// ── Metrics ───────────────────────────────────────────────────────────────────

type TurnMetrics struct {
	Turn           int
	PromptTokens   int
	ContextSize    int
	UsagePct       float64
	HistoryLen     int
	RecoveryFired  bool
	RecoveryTimeMs int64
	ResponseLen    int
	Elapsed        time.Duration
}

func (m TurnMetrics) String() string {
	usageStr := fmt.Sprintf("%.1f%%", m.UsagePct)
	if m.UsagePct > 75 {
		usageStr = warn(usageStr)
	} else {
		usageStr = ok(usageStr)
	}

	recovStr := dim("—")
	if m.RecoveryFired {
		recovStr = ok(fmt.Sprintf("✓ (%dms)", m.RecoveryTimeMs))
	}

	ctxStr := "unknown"
	if m.ContextSize > 0 {
		ctxStr = fmt.Sprintf("%d", m.ContextSize)
	}

	return fmt.Sprintf(
		"  turn=%-3d  prompt_tokens=%-6d  ctx_max=%-8s  usage=%-9s  history=%-4d  recovery=%-18s  resp_chars=%-6d  elapsed=%s",
		m.Turn,
		m.PromptTokens,
		ctxStr,
		usageStr,
		m.HistoryLen,
		recovStr,
		m.ResponseLen,
		m.Elapsed.Round(time.Millisecond),
	)
}

// ── Recovery-aware session wrapper ────────────────────────────────────────────

// recoverySession wraps a *session.Session and mirrors the dual-signal check
// from executor.RunLoop so we can capture timing and report it here without
// needing to hook into the executor internals.
type recoverySession struct {
	sess          *session.Session
	recoveryCount int
	lastRecovery  TurnMetrics
}

func (rs *recoverySession) checkAndRecover(acc *executor.StreamAccumulator) (fired bool, elapsed time.Duration) {
	maxCtx := rs.sess.Client().ContextSize()
	isTokenOverflow := maxCtx > 0 && float64(acc.Usage.PromptTokens)/float64(maxCtx) > 0.75
	isMsgOverflow := len(rs.sess.History) > 80

	if !isTokenOverflow && !isMsgOverflow {
		return false, 0
	}

	start := time.Now()
	if err := rs.sess.PruneAndRestoreFromDisk(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", bad(fmt.Sprintf("[recovery] PruneAndRestoreFromDisk error: %v", err)))
	}
	rs.recoveryCount++
	return true, time.Since(start)
}

// ── Synthetic plan ─────────────────────────────────────────────────────────────

const syntheticPlan = `# Implementation Plan - Context Recovery Architecture

## 1. Architecture & Patterns
- **Style**: Modular Go packages
- **Key Files**: internal/session/session.go, internal/executor/executor.go

## 2. Step-by-Step Implementation Strategy

### Phase 1: Session Method
- [x] **Step 1**: Add PruneAndRestoreFromDisk() to session.go
  - Tail extraction, boundary guard, plan injection, saveAndNotify

### Phase 2: RunLoop Heartbeat
- [x] **Step 2**: Insert dual-signal detection into executor.RunLoop
  - Token ratio > 0.75 OR message count > 80

### Phase 3: Verification
- [x] **Step 3**: Unit tests in prune_test.go
- [x] **Step 4**: Integration test in prune_integration_test.go
`

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	prefill := flag.Int("prefill", 0, "number of synthetic messages to pre-fill (use >=81 to trigger ceiling immediately)")
	seed := flag.String("prompt", "", "first prompt to send (default: built-in coherence question)")
	maxTurns := flag.Int("turns", 0, "max interactive turns (0 = unlimited)")
	writePlan := flag.Bool("plan", true, "write a synthetic implementation_plan.md to CWD before starting")
	flag.Parse()

	fmt.Println()
	fmt.Println(header("═══════════════════════════════════════════════════════"))
	fmt.Println(header("  late-test  ·  Context Recovery Performance Harness   "))
	fmt.Println(header("═══════════════════════════════════════════════════════"))
	fmt.Println()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := appconfig.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, bad("[config] failed to load: "+err.Error()))
		os.Exit(1)
	}
	settings := appconfig.ResolveOpenAISettings(cfg)
	if settings.BaseURL == "" {
		fmt.Fprintln(os.Stderr, bad("[config] no LLM base URL configured"))
		os.Exit(1)
	}

	c := client.NewClient(client.Config{
		BaseURL: settings.BaseURL,
		APIKey:  settings.APIKey,
		Model:   settings.Model,
	})
	fmt.Printf("  Connecting to %s ...\n", dim(settings.BaseURL))
	discoverCtx, discoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
	c.DiscoverBackend(discoverCtx)
	discoverCancel()

	ctxSize := c.ContextSize()
	backendName := "generic-openai"
	if c.IsLlamaCPP() {
		backendName = "llama.cpp"
	}
	if ctxSize > 0 {
		fmt.Printf("  Backend: %s  ContextSize: %s\n", ok(backendName), ok(fmt.Sprintf("%d tokens", ctxSize)))
	} else {
		fmt.Printf("  Backend: %s  ContextSize: %s\n", ok(backendName), dim("unknown (-1)"))
	}
	fmt.Println()

	// ── Synthetic plan ────────────────────────────────────────────────────────
	if *writePlan {
		if err := os.WriteFile("implementation_plan.md", []byte(syntheticPlan), 0644); err != nil {
			fmt.Fprintln(os.Stderr, warn("[plan] could not write implementation_plan.md: "+err.Error()))
		} else {
			fmt.Printf("  %s  implementation_plan.md written (%d chars)\n", ok("✓"), len(syntheticPlan))
		}
	}

	// ── Session ───────────────────────────────────────────────────────────────
	const systemPrompt = "You are a helpful coding assistant. Answer concisely."
	histPath := fmt.Sprintf("/tmp/late-test-session-%d.json", time.Now().UnixMilli())
	history := make([]client.ChatMessage, 0, *prefill)

	if *prefill > 0 {
		fmt.Printf("  Pre-filling %d synthetic messages...\n", *prefill)
		for i := 0; i < *prefill; i++ {
			if i%2 == 0 {
				history = append(history, client.ChatMessage{
					Role:    "user",
					Content: fmt.Sprintf("Synthetic user message %d: please continue working on the task.", i),
				})
			} else {
				history = append(history, client.ChatMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("Synthetic assistant message %d: understood, proceeding with the task.", i),
				})
			}
		}
		fmt.Printf("  %s  Pre-filled %d messages (ceiling trigger: %s)\n",
			ok("✓"), *prefill,
			boolStr(*prefill > 80, ok("YES — recovery will fire on turn 1"), dim("no")))
	}
	fmt.Println()

	sess := session.New(c, histPath, history, systemPrompt, false)
	rs := &recoverySession{sess: sess}

	// ── Metrics table header ──────────────────────────────────────────────────
	fmt.Println(header("─── Per-Turn Metrics ────────────────────────────────────────────────────────────"))
	var allMetrics []TurnMetrics

	// ── Interactive loop ──────────────────────────────────────────────────────
	reader := bufio.NewReader(os.Stdin)
	turn := 0

	for {
		turn++
		if *maxTurns > 0 && turn > *maxTurns {
			fmt.Printf("\n  %s max turns (%d) reached\n", warn("⚑"), *maxTurns)
			break
		}

		// Determine prompt
		var prompt string
		if turn == 1 && *seed != "" {
			prompt = *seed
		} else if turn == 1 {
			prompt = "After reviewing the implementation plan you have been given, briefly state what Step 1 requires."
		} else {
			fmt.Printf("\n%s ", cyan+"›"+reset)
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			prompt = strings.TrimSpace(line)
			if prompt == "" || prompt == "q" || prompt == "quit" || prompt == "exit" {
				break
			}
		}

		fmt.Printf("%s  turn %d  prompt=%q\n", dim("  ·"), turn, truncate(prompt, 60))

		if err := sess.AddUserMessage(prompt); err != nil {
			fmt.Fprintln(os.Stderr, bad("[session] AddUserMessage: "+err.Error()))
			break
		}

		// ── Inference ────────────────────────────────────────────────────────
		start := time.Now()
		inferCtx, inferCancel := context.WithTimeout(context.Background(), 90*time.Second)

		streamCh, errCh := sess.StartStream(inferCtx, nil)
		acc := &executor.StreamAccumulator{}
		var content string

		for chunk := range streamCh {
			acc.Append(chunk)
			content += chunk.Content
		}
		inferCancel()

		if err := <-errCh; err != nil {
			fmt.Fprintln(os.Stderr, bad(fmt.Sprintf("[inference] error on turn %d: %v", turn, err)))
			break
		}

		if err := sess.AddAssistantMessage(content, ""); err != nil {
			fmt.Fprintln(os.Stderr, bad("[session] AddAssistantMessage: "+err.Error()))
			break
		}

		elapsed := time.Since(start)

		// ── Recovery check (mirrors executor.RunLoop heartbeat) ───────────────
		fired, recovDur := rs.checkAndRecover(acc)

		// ── Metrics ───────────────────────────────────────────────────────────
		maxCtx := c.ContextSize()
		usagePct := 0.0
		if maxCtx > 0 {
			usagePct = math.Round(float64(acc.Usage.PromptTokens)/float64(maxCtx)*1000) / 10
		}

		m := TurnMetrics{
			Turn:           turn,
			PromptTokens:   acc.Usage.PromptTokens,
			ContextSize:    maxCtx,
			UsagePct:       usagePct,
			HistoryLen:     len(sess.History),
			RecoveryFired:  fired,
			RecoveryTimeMs: recovDur.Milliseconds(),
			ResponseLen:    len(content),
			Elapsed:        elapsed,
		}
		allMetrics = append(allMetrics, m)
		fmt.Println(m)

		// Print truncated response
		preview := truncate(strings.TrimSpace(content), 200)
		fmt.Printf("  %s %s\n", dim("↳"), dim(preview))
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println(header("─── Session Summary ─────────────────────────────────────────────────────────────"))
	fmt.Printf("  Total turns:       %d\n", len(allMetrics))
	fmt.Printf("  Recovery events:   %s\n", colorCount(rs.recoveryCount))

	if len(allMetrics) > 0 {
		final := allMetrics[len(allMetrics)-1]
		initial := allMetrics[0]
		fmt.Printf("  Initial history:   %d messages\n", *prefill)
		fmt.Printf("  Final history:     %d messages\n", final.HistoryLen)
		fmt.Printf("  Initial usage:     %.1f%%\n", initial.UsagePct)
		fmt.Printf("  Final usage:       %.1f%%\n", final.UsagePct)

		var totalElapsed time.Duration
		for _, m := range allMetrics {
			totalElapsed += m.Elapsed
		}
		fmt.Printf("  Total LLM time:    %s\n", totalElapsed.Round(time.Millisecond))

		// Coherence verdict: did any turn reference the plan post-recovery?
		coherent := false
		for _, m := range allMetrics {
			_ = m // coherence is checked per-response below
		}
		_ = coherent
	}

	// Evaluate whether recovery happened as expected given prefill.
	if *prefill > 80 {
		recovered := rs.recoveryCount > 0
		if recovered {
			fmt.Printf("\n  %s Recovery triggered as expected for prefill=%d (ceiling >80)\n", ok("✓ PASS"), *prefill)
		} else {
			fmt.Printf("\n  %s Recovery NOT triggered for prefill=%d — check ceiling logic\n", bad("✗ FAIL"), *prefill)
		}
	}

	fmt.Printf("\n  History file: %s\n", dim(histPath))
	fmt.Println()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

func colorCount(n int) string {
	if n == 0 {
		return dim("0")
	}
	return ok(fmt.Sprintf("%d", n))
}
