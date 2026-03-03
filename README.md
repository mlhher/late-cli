# Late: High-Leverage AI Agent Orchestration

**Late** (Lightweight AI Terminal Environment) is a deterministic coding agent orchestrator designed to give a solo developer the execution throughput of an entire engineering team.

Whether you are mapping undocumented APIs, digesting complex repositories, or generating boilerplate, **Late** acts as a highly autonomous engineering partner instead of burning API credits, time, and money on bloated, hallucinated contexts.

> **Built with Late:** As of today, the vast majority of Late is being built inside Late.

![Late Orchestrator planning a 4-phase implementation and spawning the first subagent](assets/late-subagent-handoff.png)
*Late acting as Lead Architect: Orchestrating a 4-phase plan to fix an async state-management bug (CTRL+G interrupts), autonomously spawning the Phase 1 subagent to edit the core engine.*

## The Paradigm: Delegation Over Bloat

The current industry standard for AI coding tools (Claude Code, Cursor, OpenCode) is fundamentally flawed: they dump 10k+ tokens of raw instructions, tool schemas, and massive `grep` dumps into a single context window. This leads to severe "token waste" and session amnesia.

**This actively degrades model reasoning.** Research shows as context pollutes, models suffer a [39% performance drop](https://arxiv.org/abs/2512.13914) and frequently [lose 60-80% of their effectiveness within just 2-3 attempts](https://arxiv.org/abs/2506.18403) at solving a problem.

This is why throwing **larger models at the problem does not always solve the issue**.

**Late solves this by mirroring how actual engineering teams work:**

1. **The Orchestrator (Global Context):** The main agent acts as the Lead Architect. It manages the global session, reads the codebase, maps the APIs, and holds the master plan in its context window.
2. **The Subagents (Ephemeral Context):** When code needs to be written, the Orchestrator does not write it directly. It spawns a localized subagent with a fresh, empty context window containing *only* the specific instructions and files needed for that single isolated task.
3. **Deterministic Execution:** Standard agents use fragile diff formats that frequently hallucinate line numbers and corrupt files. Late forces subagents to use strict exact-match `search`/`replace` blocks. If the AI's output doesn't perfectly match the local file state, the edit fails loudly and the Orchestrator forces a self-healing loop. Zero silently broken code.

## Core Capabilities

* **Model Agnostic & Local-First:** Requires any OpenAI-compatible endpoint. Highly optimized to run locally on consumer hardware (e.g., Qwen3.5), or can be pointed at heavy-compute cloud endpoints (e.g., [Gemini 3.1 Pro](https://ai.google.dev/gemini-api/docs/openai)) for complex architectural tasks.
* **Native MCP Integration:** Implements the Model Context Protocol (MCP) client directly via standard I/O. Dynamically map external MCP servers (databases, external APIs) transparently into Late's tool interface without the massive token bloat seen in other clients.
* **Stateful Resilience:** The Orchestrator maintains continuous session history on disk (`~/.local/share/Late`), ensuring perfect context retention across multiple runs.
* **Pure Go:** A bare-metal, high-performance engine. Statically compiled. Zero JavaScript framework bloat. No arbitrary node packages. No Python virtual environment shenanigans.

## Quick Start (Zero Dependencies)

**1. Download the Binary**
Grab the latest release for your OS (Linux/macOS) from the [Releases](https://github.com/mlhher/late/releases) page.

**2. Point to Your Model**
Point Late to any OpenAI-compatible API endpoint (e.g., local `llama.cpp` / LM Studio, or a cloud provider).

```bash
export OPENAI_BASE_URL="http://localhost:8080"
```

**3. Execute**

```bash
late
```

*(Note for llama.cpp users: Upstream contains a bug causing crashes during context shifts with slots. You **MUST** use the build patched with [PR #18675](https://github.com/mlhher/llama.cpp) for stability).*

## Operational Notes

Late is a raw, high-performance execution engine. It does not hold your hand.

* **Halts:** If a local model drops a stop token and the UI stalls, type `continue` to resume.
* **Deadlocks:** If an ephemeral subagent enters an infinite argument with the Orchestrator, press `Ctrl-C`. Your main session state is preserved. Restart and instruct the Orchestrator to re-evaluate the task.

## License: BSL 1.1

We built this to generate real engineering leverage, not to supply free backend infrastructure for corporations or AI startups.

* **Free for Builders:** You may use Late freely to write code for any project, including your own commercial startups. We do not restrict your output.
* **Commercial Restrictions:** You may not monetize Late itself (e.g., wrapping our orchestration engine into a paid AI service or IDE), nor may you deploy Late as internal infrastructure within enterprise environments without a commercial agreement.

*Late safely converts to an open-source GPLv2 license on February 21, 2030.*