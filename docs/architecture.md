# Architecture

[English](architecture.md) | [简体中文](architecture.zh-CN.md)

## Design Goal

Late treats **central context as the scarce resource**.

Standard coding agents accumulate codebase scans, compiler traces, file dumps, and failed diffs into a single expanding trajectory. As execution noise pollutes the context window, model reasoning degrades significantly.

Late splits planning from execution: the **Lead Orchestrator** retains only high-level plans, architectural decisions, and verified results. All intermediate execution noise is absorbed and discarded inside isolated, disposable worker contexts.

<div align="center">
  <br/>
  <img src="../assets/workflow.jpg" alt="Late Architecture: Main Orchestrator routing to ephemeral subagents with automatic context destruction">
  <br/>
</div>

---

## Core Components

### Lead Orchestrator
- **Role:** High-level system architect, planner, and verifier.
- **Capabilities:** Explores high-level project structure, writes implementation plans, tracks progress via todos, and coordinates execution.
- **Hard Constraints:**
  - Cannot directly edit files (`write_file` and `target_edit` are physically unregistered).
  - Destructive shell operations are blocked (including output redirection `>`).
  - Instructed not to perform broad direct codebase scans; delegates discovery to the Researcher.
- **Subagent Spawning:** Dispatches atomic implementation steps to specialized workers sequentially via `spawn_subagent` or concurrently via `batch_spawn_subagents`.

### Researcher
- **Role:** Read-only codebase explorer and contextual investigator.
- **Capabilities:** Uses native search tools (`read_file`, `search_content`, `find_files`) and safe shell commands to trace logic, inspect dependencies, and map repository geography.
- **Context:** Operates in a fresh, isolated context per invocation.
- **Output:** Returns structured, high-signal findings directly to the orchestrator.
- **Restrictions:** Cannot edit files, orchestrate, create implementation plans, manage session todos, or spawn further agents.

### Coder
- **Role:** Focused implementation worker for atomic code modifications.
- **Capabilities:** Uses precise edit tools (`target_edit`, `write_file`) and shell execution (`bash`) to apply diffs, run builds, and execute test suites.
- **Context:** Operates in a fresh, isolated scratchpad for a single assigned task.
- **Output:** Returns concise diagnostics (exact changes made, test outcomes, or encountered ambiguities).
- **Restrictions:** Cannot orchestrate, create implementation plans, manage session todos, or spawn further agents.

---

## Execution Flow

1. **User Request:** User submits a feature request, refactor, or bugfix.
2. **Context Discovery (Optional):** For non-trivial tasks, the Orchestrator invokes `spawn_subagent` (type: `researcher`) to inspect the codebase and report constraints, relevant files, and patterns.
3. **Plan Formulation:** The Orchestrator synthesizes findings and writes a formal plan to `./implementation_plan.md` via `write_implementation_plan`.
4. **Milestone Tracking:** The Orchestrator registers atomic phases using `create_todos`.
5. **Worker Delegation:** (If approved:) The Orchestrator invokes `spawn_subagent` (type: `coder`) for an individual atomic step, or `batch_spawn_subagents` for concurrent execution of independent steps.
6. **Isolated Execution:** The Coder inspects designated files, applies modifications, and runs validation commands within its private context.
7. **Structured Handoff:** The Coder returns a structured summary of applied changes, test results, or blocking issues. Its ephemeral scratchpad is terminated.
8. **Verification & Advancement:** The Orchestrator evaluates the result, marks the task complete via `finish_todo`, and proceeds to the next step.

---

## Why Context Isolation Matters

- **Execution Noise Accumulation:** Full file reads, compiler diagnostics, linters, and failed patch attempts rapidly saturate context windows.
- **Cognitive Degradation:** Empirical benchmarks show long-context models suffer up to a ~45% drop in reasoning accuracy once context utilization crosses 40–50% (arXiv:2601.15300).
- **Disposable Scratchpads:** Failed attempts, test logs, and intermediate grep queries are useful only during execution. Discarding them after task completion prevents low-value execution history from tainting the model's reasoning ability.
- **Focused Central Context:** The Orchestrator's context window contains only architectural intent, milestone status, and verified outcomes.
- **Exceeding Single-Window Limits:** In local testing, models with a 64k context budget have completed 200k+ tokens of aggregate engineering work because execution was distributed across fresh, disposable contexts.

---

## Architectural Enforcement

Unlike systems where subagent delegation is merely prompt-recommended or optional, Late enforces separation at runtime:

- **Physical Tool Namespace Pruning:**
  - Write tools (`write_file`, `target_edit`) are not registered in the Orchestrator's tool registry.
  - Planning and delegation tools (`spawn_subagent`, `batch_spawn_subagents`, `write_implementation_plan`, `create_todos`, `list_todos`, `finish_todo`) are omitted when constructing subagent registries.
- **Shell-Level Enforcement:**
  - Shell commands pass through an AST and policy engine. Shell output redirection (`>`) is blocked, preventing orchestrator workarounds to write files via shell scripts.
  - Search commands (`grep`, `find`, `rg`) are gated with directions to use native `.gitignore`-aware search tools.
- **Non-Recursive Leaf Workers:** Subagents cannot spawn other subagents. Orchestration hierarchy is strictly flat (Depth = 1).
- **Mandatory Delegation Pipeline:** Because the orchestrator lacks modification primitives, all codebase mutations must pass through worker delegation.

---

## Context & Cache Strategy

- **Deterministic & Cache-Stable Prefixes:** Late avoids toggling between global "Plan" and "Build" modes, preserving KV-cache prompt prefixes across interaction turns. In Late the orchestrator is always planning.
- **Disposable Compute vs. Central Context:** Late intentionally expends more aggregate inference across ephemeral workers to protect the central orchestrator's decision-making context.
- **Optimization Metric:** Late optimizes for **maximum useful work per unit of central context**, rather than minimizing raw aggregate token counts. Cheap, disposable compute is traded for high-fidelity architectural reasoning.

---

## Planning and Todos

- **Plan Initialization:** Plans are committed to disk (`implementation_plan.md`) through `write_implementation_plan` before code modifications begin.
- **Todo Lifecycle:**
  1. `create_todos`: Called by the Orchestrator immediately after the plan is saved to initialize sequential milestones in session memory.
  2. `list_todos`: Consulted by the Orchestrator to monitor overall progress across turns.
  3. `finish_todo`: Invoked by the Orchestrator as each worker successfully delivers and verifies an atomic step.
- **Subagent Protection:** Todo management is restricted to the main orchestrator agent (`id == common.MainAgentID`). Subagents are blocked from modifying session milestones.
- **Resilience Across Turns:** The todo state lives in root orchestrator memory and survives subagent termination, providing a stable backbone throughout multi-step refactors.

---

## Model Routing

- **Default Configuration:** The Lead Orchestrator and all worker subagents share the same configured model by default.
- **Hybrid / Heterogeneous Routing:** Independent models and endpoints can be assigned per agent type (e.g., routing planning to a high-capacity frontier reasoning model, while routing atomic code edits and search to fast, cost-effective models).
- **Model-Agnostic Backend:** Interacts with any standard OpenAI-compatible API endpoint (local or remote).

---

## llama.cpp Integration

- **Automatic Discovery:** Defaults to `http://localhost:8080`. When connected to `llama-server`, Late automatically queries `/props` or `/models` to configure context lengths without manual user setup.
- **Empirical Logit Biasing:** Late implements model-dependent logit suppression inspired by EMNLP 2025 research showing 27%–51% reductions in redundant reasoning tokens without accuracy loss in the evaluated settings. It penalizes repetitive self-reflection tokens ("Wait...", "Hmm") at the sampling layer.
- **Model-Dependent Tuning:** Logit bias maps adapt based on detected model architecture and tokenizers.

---

## Persistence

- **Root Session History:** Orchestrator conversation history is persisted to disk under `<sessionsDir>/<sessionID>.json` alongside a `.meta.json` sidecar for state resumption.
- **Optional Subagent Histories:** Active subagent contexts are ephemeral in memory during execution. When enabled, subagent transcripts are persisted to `<sessionsDir>/<sessionID>/subagents/<childID>.json` for auditing and debugging.
- **Ephemeral Context vs. Disk Audit:** Workers do not leak their raw context into the orchestrator; debugging and post-mortem analysis rely on on-disk transcripts rather than an overloaded central KV cache.

---

## Design Tradeoffs

- **Higher Aggregate Inference:** Multi-agent decomposition consumes more total tokens than single-context execution due to separate system prompts, context loading, and handoffs.
- **Wall-Clock Overhead:** Multi-stage research, delegation, and verification can materially increase wall-clock time compared with direct execution. Late intentionally trades additional time and inference for stronger isolation and sustained decision quality on complex tasks.
- **Handoff Compression:** Summarizing worker execution carries a risk of omitting fine-grained details if a worker returns an incomplete report.
- **Target Workloads:** The architecture is specifically optimized for complex, multi-file, or long-horizon tasks. For trivial single-line changes, the orchestration overhead is unnecessary.
