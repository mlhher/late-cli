# Late: High-Leverage AI Agent Orchestration

**Late** (Lightweight AI Terminal Environment) is a deterministic coding agent orchestrator designed to give a solo developer the execution throughput of an entire engineering team.

Standard AI coding assistants dump massive contexts into a single window, leading to token bloat, amnesia, hallucinations and degraded ability. **Late solves this by mirroring real engineering teams:** a Lead Architect orchestrator maps the codebase and spawns ephemeral, isolated subagents to execute perfect, exact-match code edits.

![Late Orchestrator planning a 4-phase implementation and spawning the first subagent](assets/late-subagent-handoff.png)
*Late acting as Lead Architect: Orchestrating a 4-phase plan and autonomously spawning atomic subagents.*

> **Built with Late:** As of today, the vast majority of Late is being built *inside* Late.

## 🔥 Why Late?

### 1. Delegation Over Context Bloat
**Zero Prompt Bloat:** Standard terminal agents eat 10,000+ tokens just for their system prompt, exhausting your VRAM or burning your money through API usage before you even start working. Late's core system prompt is ruthlessly optimized to ~1,000 tokens, leaving your context window open for what actually matters: your code. Throwing larger models at a problem doesn't solve context degradation. As context pollutes, models suffer massive performance drops.
* **The Orchestrator:** Holds the master plan, reads the codebase, and delegates.
* **Atomic Subagents:** Receive fresh, empty context windows containing *only* the exact instructions for a single task.

### 2. Zero Silently Broken Code (Exact-Match Diffs)
Standard agents use fragile diff formats that frequently hallucinate and corrupt files. Late forces subagents to use strict exact-match `search`/`replace` string blocks. If the model fails the match, the edit fails loudly, and the Agent initiates an **autonomous self-healing loop** until it gets it right.

### 3. Sandboxed Execution & Deterministic Safety
You shouldn't have to give an LLM blanket `sudo` access to your machine. Late introduces strict terminal guardrails:
* **Command Whitelisting:** Dangerous operations are blocked by default. Standard writes outside the CWD require explicit permission, while safe reads (`find`, `cat`) are securely parsed to block malicious pipes and redirects.
* **CWD Jailing:** The agent is locked to the current working directory (`cd` is blocked). It cannot wander into your system files.
* **Turn Limits:** Configurable limits prevent runaway LLM loops.

### 4. Pure Go & No Dependencies
A statically compiled engine. No `node_modules`, no virtual environments. Drop the binary in your path and go.

### 5. Local-First & Model Agnostic
Requires any OpenAI-compatible endpoint. Late's ephemeral subagent architecture is designed for consumer hardware: subagent contexts are destroyed on completion and never pollute the planner's window, keeping VRAM and context usage flat regardless of task complexity. Late orchestrates its own codebase development on **5GB VRAM** using a local `Qwen3.5-35B-A3B` (~25-30 tokens/sec through `llama.cpp`, 65k context, remaining layers offloaded to system RAM). Two simultaneous agent instances run comfortably at ~15-20 t/s.
Natively supports both thinking and non-thinking models (including `Gemma 4`), or can be pointed at heavy-compute cloud endpoints for complex architectural tasks.

### How Is This Different?

Tools like Cursor, Claude Code or OpenCode feed your entire session into a single, growing context window. Late takes the opposite approach: a lean orchestrator delegates to ephemeral subagents with fresh, minimal context. Subagent history is destroyed on completion and never pollutes the planner's context. This mirrors how real engineering teams operate — and it runs on 5GB VRAM with local models.

## 🚀 Quick Start (Zero Dependencies)

**1. Download the Binary**
Grab the latest single-binary release for your OS (Linux/macOS) from the [Releases](https://github.com/mlhher/late/releases) page.

```bash
chmod +x late-linux-amd64  # (Adjust for your downloaded filename)
mv late-linux-amd64 ~/.local/bin/late # Ensure ~/.local/bin is in your system's $PATH
```

**2. Point to Your Model**
Point Late to any OpenAI-compatible API endpoint (local or cloud).

```bash
export OPENAI_BASE_URL="http://localhost:8080"
# Optional: Set your API key if using a cloud provider
# export OPENAI_API_KEY="your-key"
```

**3. Execute**

```bash
late
```

## 🔨 Build from Source

If you prefer to compile Late yourself (requires Go):

```bash
git clone https://github.com/mlhher/late.git
cd late
make build
make install
```

## 🛠️ Advanced Features

* **Native MCP Integration:** Dynamically map external MCP servers (databases, APIs) directly into Late's tool interface via standard I/O, bypassing massive token bloat.
* **Stateful Resilience:** The Orchestrator maintains continuous, newest-first session history on disk (`~/.local/share/Late`), ensuring perfect context retention across runs.
* **Git Worktree Support:** Run independent, parallel Late instances across multiple Git worktrees for isolated feature development without context switching.

## 📜 License: BSL 1.1

We built this to generate real engineering leverage, not to supply free backend infrastructure for AI startups.

* **Free for Builders:** You may use Late freely to write code for any project, including your own commercial startups. We do not restrict your output.
* **Commercial Restrictions:** You may not monetize Late itself (e.g., wrapping our orchestration engine into a paid AI service), nor deploy Late as internal infrastructure within enterprise environments without a commercial agreement.

*Late safely converts to an open-source GPLv2 license on February 21, 2030.*
