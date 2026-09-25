<h1 align="center">Late</h1>

<p align="center">
  <a href="README.md">English</a> | <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <b>The AI agent that always stays sharp.</b><br><br>
  <b>64k context window. 200k+ tokens of work.</b><br>
  Late isolates execution steps to keep your model's context clean during long workflows.<br>
  Drop it into any project. Works with any cloud provider or local model.<br>
</p>

<p align="center">
  <a href="https://github.com/mlhher/late-cli/releases"><img src="https://img.shields.io/github/v/release/mlhher/late-cli?style=flat&color=3fb950" alt="Release"></a>
  <a href="https://github.com/mlhher/homebrew-late"><img src="https://img.shields.io/badge/Homebrew-tap-blue.svg?style=flat" alt="Homebrew"></a>
  <a href="https://github.com/mlhher/late-cli/"><img alt="GitHub Repo stars" src="https://img.shields.io/github/stars/mlhher/late-cli?style=flat&color=8a5cf5"></a>
  <a href="https://deepwiki.com/mlhher/late-cli"><img src="https://img.shields.io/badge/DeepWiki-docs-blue.svg?style=flat" alt="DeepWiki"></a>
</p>

> [Outperforming Claude Code and Codex for Local LLM Workflows](https://agentnativedev.medium.com/outperforming-claude-code-and-codex-for-local-llm-workflows-5de0e2b1add5) — Agent Native
>
> *"You solved local AI coding for me."* — Reddit
>
> *"Late-CLI is mindblowing... it's a true hidden gem."* — GitHub Discussions
>
> *"The same model feels smarter with Late."* — Reddit
>
> **Built with Late:** Late is primarily developed inside Late itself.

<div align="center">
  <br/>
  <img src="assets/late-subagent-handoff.png" alt="Late Orchestrator planning a multi-phase implementation and spawning the first subagent">
  <br/>
    <i>Late autonomously planning, delegating, and resolving a complex multi-step merge conflict.</i>
  <br/><br/>
</div>

## 10-Second Quickstart

A single, statically compiled binary. Zero dependencies. No Python venvs, no Node.js.

```bash
# Linux / macOS (Homebrew)
brew tap mlhher/late && brew install late
```

```bash
# Universal Fallback (Linux / macOS / Windows WSL)
curl -sfL https://raw.githubusercontent.com/mlhher/late-cli/main/install.sh | bash
```

```bash
# Launch interactively in any project
cd your-project
late
```
*Manual Binaries: [Linux, macOS, native Windows](https://github.com/mlhher/late-cli/releases)*

> **Pre-release Notice:** This README documents features in **[v2.0.0-rc.1](https://github.com/mlhher/late-cli/releases/tag/v2.0.0-rc.1)** (pre-release). The latest stable release is **[v1.5.1](https://github.com/mlhher/late-cli/releases/tag/v1.5.1)**. Binaries for both versions are available on [GitHub Releases](https://github.com/mlhher/late-cli/releases).

One binary. Zero configuration. If `llama-server` is already running, Late finds it automatically.

📖 [**Read the Quickstart Guide**](./docs/quickstart.md) for setup details on persistent settings, fully autonomous containerized workflows, MCP and Skills setup, Git worktrees, keybindings, and more.


## The Architectural Bottleneck

**The Problem:** Standard coding agents still let the primary agent directly absorb codebase scans, compiler errors, file reads, failed diffs, and retries into one growing trajectory. As that execution noise accumulates in the KV cache, model reasoning quality degrades severely. You blame the model, but it's an architectural failure.

> **1. The 40% Collapse:** Long-context LLMs suffer up to a **~45% collapse in reasoning accuracy** once context utilization crosses 40–50%, even when all tokens are technically relevant ([Weiwei Wang et al., arXiv 2026: Intelligence Degradation in Long-Context LLMs](https://arxiv.org/abs/2601.15300)).
>
> **2. The Overthinking Tax:** Reasoning models waste **27%–51% of their trajectory** on redundant self-reflection loops ("Wait...", "Hmm") without accuracy gains ([Chenlong Wang et al., EMNLP 2025: Wait, We Don't Need to "Wait"! Removing Thinking Tokens Improves Reasoning Efficiency](https://arxiv.org/abs/2506.08343)).

<br/>

**The Late Solution:** Late splits the brain and treats **central context as the scarce resource**:
1. **Architecturally Enforced Isolation:** The Lead Orchestrator strictly plans and verifies. It spawns ephemeral Coder and Researcher subagents into isolated contexts. Once a subagent finishes its atomic task, its noisy scratchpad is wiped. Only structured, high-signal diagnostics return to the Lead Architect.
2. **Disposable Test-Time Compute:** Workers can spend far more aggregate inference than would ever fit cleanly into one useful reasoning trajectory. Late trades cheap compute for a bounded, high-signal orchestrator context.
3. **Empirical Logit Biasing:** Late implements real-time logit biasing for `llama.cpp`/`llama-server`. It suppresses redundant thinking tokens on the fly, reclaiming wasted CoT compute while leaving room for useful reasoning (varies by model).
4. **Physical Tool Registry Pruning:** The Orchestrator has no file-writing tools, has repo-mutating bash commands blocked, and cannot bypass delegation. Subagents have no orchestration tools and cannot recursively spawn agents.

See the [Feature Matrix](#the-feature-matrix) and [FAQ](#faq) for a direct comparison and real world examples.

<div align="center">
  <br/>
  <img src="assets/workflow.jpg" alt="Late Architecture: Main Orchestrator routing to ephemeral subagents with automatic context destruction">
  <br/>
</div>

The orchestrator’s context grows primarily from what actually matters: your instructions, plans, and verifiable results—not every grep, compiler trace, failed edit, and discarded hypothesis used to get there.

**A model with a 64k context window is no longer limited to a 64k task.** The cumulative job can grow into hundreds of thousands of tokens while the orchestrator stays inside its useful context budget, delegating fresh work instead of carrying the entire execution history forward.

**The same model feels smarter in Late because the architecture protects the context in which its important decisions are made.**

---

## The Feature Matrix

|  | Late | Standard Monolithic Agents |
| :--- | :--- | :--- |
| **Workflow** | **Autonomous orchestration: Always Planning** | Manual Build / Plan mode switching |
| **Implementations** | **Strictly enforced ephemeral coder subagents (Wiped)** | Delegation optional or mixed with primary-context execution |
| **Explorations** | **Strictly enforced ephemeral researcher subagents (Wiped)** | Exploration may still accumulate in the primary trajectory |
| **Tool Enforcement** | **Physical tool namespace pruning (Hard boundaries)** | Commonly policy/prompt driven |
| **KV-Cache** | **Ruthless KV-cache preservation (Deterministic prefixes)** | Brute-force dumping & cache-busting mode switches |
| **Logit Biasing** | **Native EMNLP 2025 token suppression (Culls CoT bloat)** | None (Full overthinking tax paid on every turn) |
| **Startup Time** | **Instant (<10ms native Go, feels like `htop`)** | 1s–3s+ (Node.js / Python runtimes) |
| **System Prompt** | **~1,000 tokens (Lean & focused)** | 3,000–10,000+ tokens (From No-Workflow to Over-Constrained Bloat) |
| **Sandboxing** | **Native rootless devcontainers (`late-podman`)** | No equivalent built-in workflow |
| **Setup Required** | **None (Automatic `llama-server` on `:8080`)** | Requires provider/config setup |
| **Telemetry** | **None** | Telemetry by default |

<p align="center"><b>If Late makes your model feel smarter, <a href="https://github.com/mlhher/late-cli">give it a ⭐ on GitHub</a></b></p>

---

## FAQ

**Why not conventional agents that can just edit the repository directly?**

Because optional delegation is not the same thing as an architectural boundary. If the primary agent can keep reading files, executing commands, editing code, retrying patches, and absorbing tool output directly, its central trajectory still grows with the work.

Late makes that impossible. The orchestrator plans and verifies; isolated workers execute. This structural discipline ensures the model doesn't get dragged down in execution noise and converge on a partial solution. Late keeps decomposing the task in clean environments until all objectives are closed.

**Don't other tools already have subagents?**

Many modern tools have subagents. The distinction is enforcement: in Late, workers are not an optional side-path that the primary agent may bypass. Late **architecturally enforces** isolation:
* **Forced Plan-First Decomposition:** Tasks are decomposed into atomic, verifiable steps before code is touched.
* **Actionable Diagnostic Reporting:** Subagents return structured, high-signal reports rather than lossy summaries, letting the orchestrator do what it's best at: **Planning** (and nothing else).
* **Tool Registry Pruning:** The Orchestrator literally has no file-writing tools and cannot make hasty edits. Subagents have no orchestration tools and cannot spawn recursive agents.

**Can this workflow not just be rebuilt with plugins?**

The underlying orchestration loop of agents not written with the specific architecture in mind isn't built to enforce isolation, even if you write plugins or extensions to try and add it. Instead of just offering subagents, Late architecturally enforces them. The orchestrator is physically incapable of making file edits (whether through tools or Bash), and workers are architecturally separated from orchestration. You can't replicate that structural discipline with a third-party plugin.

**Can small or quantized local models actually handle complex, real-world tasks?**

Yes. This is specifically what Late was made for.

Repeated local testing with **35B-A3B models at 3-bit quantization** running via `llama-server` has shown a consistent qualitative pattern: Late can spend far more aggregate inference than the orchestrator itself ever has to retain.

In one representative run, the agents collectively executed **200,000+ tokens** while the orchestrator stayed below **64k tokens**. The model autonomously resolved an interwoven 6-file merge conflict with cross-file refactors, duplicated logic, and subtle regressions, compiled cleanly, and passed all tests inside a disposable `late-podman` container. The same model repeatedly failed on the same task in monolithic harnesses as its working trajectory grew.

This is the practical reason Late targets local models: **it turns context capacity into a compute problem.** If inference is local or cheap, you can spend more worker compute instead of asking one increasingly polluted context to remember everything. In local testing, that has enabled smaller models to keep working on tasks far larger and longer-lived than their useful single-trajectory context would normally permit.

**Doesn't this create a "telephone game" where information gets lost between agents?**

No. In real-world tests, the exact opposite is true.

The subagents are explicitly instructed to return concise summaries of what they did, what worked, what changed from the original plan, and which architectural assumptions may have been wrong. This lets the orchestrator focus on the important failures and pivot quickly, instead of inheriting every file read, git operation, test result, lint error, and build log produced along the way.

In testing, models like Qwen3.6-35B-A3B and its finetunes return structured summaries that let the orchestrator immediately adjust its trajectory without inheriting the worker's entire reasoning history. This keeps the central context focused on the overall architecture and lets Late spend substantially more disposable worker compute without degrading the context making the important decisions.

The model didn't change. The architecture did.

**What else does Late do besides subagents?**

Late is engineered as an end-to-end harness:
* **Full Plugin System:** Install plugins from the default registry, npm, Git repositories, or local directories with full support for custom skills, slash commands, themes, and lifecycle hooks, in any language.
* **Sub-10ms Startup:** Native Go binary with zero runtime dependencies. It launches instantly like `htop` or `nvim`, avoiding the sluggish startup latency and RAM footprint of Node.js or Python.
* **Active Cognitive Anchoring:** Instead of leaving the model to blindly guess tool semantics, Late injects targeted contextual cues and sentinel feedback when relevant—freeing model compute from tool mechanics so it can focus purely on solving your code.
* **Zero Prompt-Reprocessing:** Unlike tools that manually toggle between "Plan" and "Build" modes (invalidating prompt caches and driving up latency/costs), Late maintains deterministic, cache-stable prompt prefixes.
* **Surgical Scoping Over AST Bloat:** Instead of dumping thousands of static AST tokens into the prompt on every turn, Late's orchestrator provides precise line ranges and instructions directly to the subagent.

**Does Late work with local models?**

Zero config. Point `llama-server` at any GGUF and Late connects on `:8080` automatically.

---

## Model Connectivity

Late is completely model-agnostic.

**Local Models (Zero Config):**
No configuration required. Late targets `llama.cpp` on port `:8080` (the default for `llama-server`).

**Cloud Providers (DeepSeek, Claude, GPT, Kimi, GLM, OpenRouter):**

```bash
export OPENAI_BASE_URL="your-api-url"
export OPENAI_API_KEY="your-api-key"
export OPENAI_MODEL="model-name"
```

---

## Features

* **Universal Plugin System:** Extend Late with custom slash commands, MCP servers, themes, and lifecycle hooks (`onSessionStart`, `onMessageSend`, `onToolCall`, `onToolResult`). Install via `late plugin install <package>` directly. Subagents automatically inherit active plugins.
* **Empirical Logit Biasing:** Built on [Chenlong Wang et al., Findings of EMNLP 2025](https://arxiv.org/abs/2506.08343), Late dynamically suppresses repetitive self-reflection tokens ("Wait...", "Hmm") in reasoning models via `llama-server`, reducing CoT token bloat by 27%–51% with zero loss in accuracy (may vary depending on the model).
* **Overnight Autopilot via Podman (`late-podman`):** Run the agent in an isolated, rootless container sandbox with devcontainer support and `yolo mode`. Turn Late loose on large-scale refactors overnight without risking your host machine.
* **Interactive Model Switching & Hybrid Routing:** Press `/model` to reconfigure orchestrator and worker models on the fly. Route planning to frontier reasoning models while delegating execution to fast, cost-efficient workers.
* **Developer Ergonomics:**
  * `/compose`: Pop open your preferred `$EDITOR` (Neovim, Vim, Helix, VS Code) to draft complex, multi-line instructions.
  * `/rewind`: Visual history scrubber to roll back turns and branch conversational states.
  * `/infobar` / `/timestamps`: Toggle the info footer (version, model, context, subagents, skills, uptime) and `[HH:MM:SS]` message timestamps; both persist to `config.json`.
  * `late --prompt "..."`: Start a session with a pre-given prompt, useful for running it from other scripts.
* **Auditable Subagent History:** Full subagent conversation transcripts and metadata are persisted to disk for total transparency and debugging—without poisoning the orchestrator's active context window. Opt-in persistence protects your disk space while ensuring you can debug overnight autopilot runs.
* **Exact-Match Diffs & Autonomous Healing:** Strict `search`/`replace` editing with automatic self-healing on mismatch. Edits fail loud; files are never silently corrupted.
* **Pre-emptive Sentinel Feedback:** Built-in tool guards detect common agent failure patterns and inject immediate corrective context before the model stumbles into hallucinatory loops.
* **True cl100k BPE Offline Tokenizer:** Embedded tokenizer calculates real BPE token counts offline with zero heuristic guesswork.
* **Native Context-Aware Search:** High-performance codebase search with globster filtering that respects `.gitignore` and `.llmignore`.
* **Agent Skills & MCP Support:** Natively consume external Model Context Protocol (MCP) servers and third-party Agent Skills with zero configuration overhead.
* **Git Worktree Support:** Run independent, parallel agent instances across multiple branches simultaneously with zero context bleeding.

---

## License

Built to create engineering leverage, not to supply free infrastructure for AI startups.

* **Free for Builders:** Use Late freely to write code for any project, including commercial ones. Your generated output is yours.
* **Commercial Infrastructure:** You may not monetize Late itself. Wrapping the orchestration engine into a paid service requires a commercial agreement. *(Converts to GPLv2 on Feb 21, 2030).*
