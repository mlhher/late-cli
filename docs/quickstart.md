# Late Quickstart Guide

[English](quickstart.md) | [简体中文](quickstart.zh-CN.md)

Get from install to your first autonomous coding task in a couple of minutes.

## Install

### Homebrew — Linux / macOS

```bash
brew tap mlhher/late && brew install late
```

### Universal installer — Linux / macOS / Windows WSL

```bash
curl -sfL https://raw.githubusercontent.com/mlhher/late-cli/main/install.sh | bash
```

Manual binaries for Linux, macOS, and native Windows are available from the [GitHub Releases](https://github.com/mlhher/late-cli/releases).

---

## Local Models: Zero Configuration

Late automatically looks for an OpenAI-compatible `llama-server` on `localhost:8080`.

Start `llama-server` with your GGUF model as usual, then launch Late from your project:

```bash
cd your-project
late
```

That's it. If `llama-server` is already running on `:8080` (its default port) Late will connect with no configuration required.

---

## Cloud Models

Late works with any OpenAI-compatible APIs including DeepSeek, Claude, GPT, Kimi, GLM, OpenRouter, and others.

Set:

```bash
# DeepSeek
export OPENAI_BASE_URL="https://api.deepseek.com" # your-api-url
export OPENAI_API_KEY="sk-123" # your-api-key
export OPENAI_MODEL="deepseek-flash" # your-model-name
```

Then run:

```bash
cd your-project
late
```

You can later persist model settings in Late's `config.json` instead of exporting environment variables every time.

---

## Configuration

Persistent configuration lives at:

* **Linux:** `~/.config/late/config.json`
* **macOS:** `~/Library/Application Support/late/config.json`
* **Windows:** `%APPDATA%\late\config.json`

Configuration precedence is:

1. Environment variables
2. `config.json`
3. Late defaults

For the standard local `llama-server` setup on `localhost:8080`, you do not need to create a configuration file.

### Tool Approval Mode (`permission-mode`)

You can choose how much supervision Late applies to dangerous commands by adding a `permission-mode` entry to the `config.json` file for your platform (see the locations above):

```json
{
  "permission-mode": "ask-for-user-approval"
}
```

The two allowed values are:

* `ask-for-user-approval` — the default. Potentially dangerous commands require your approval.
* `i-promise-i-have-backups-and-will-not-file-issues` — run every tool without user confirmation.

Notes:

* The two CLI flags of the same names (`--ask-for-user-approval`, `--i-promise-i-have-backups-and-will-not-file-issues`) are mutually exclusive and override the `config.json` value.
* Omitting the entry (and any flag) defaults to `ask-for-user-approval`.
* An invalid value is ignored with a warning and the safe default applies.

### Advanced Model Configuration (`models` and `agent_models`)

By default, Late uses the same model for the orchestrator and its subagents. However, you can map different models to specific agent roles (e.g., using a massive frontier model for planning, and a faster local model for execution).

You can change models interactively using `/model` inside the TUI, or persist your hybrid routing in `config.json` via the `models` registry:

```json
{
  "models": [
    {
      "id": "deepseek",
      "url": "https://api.deepseek.com",
      "key": "sk-your-key",
      "model": "deepseek-flash"
    },
    {
      "id": "local-qwen",
      "url": "http://localhost:8080",
      "key": "",
      "model": "qwen3.6-35b-a3b"
    },
    {
      "id": "local-gemma",
      "url": "http://localhost:8080",
      "key": "",
      "model": "gemma-4-e4b"
    }
  ],
  "agent_models": {
    "orchestrator": "deepseek",
    "researcher": "local-qwen",
    "coder": "local-gemma"
  }
}
```

> Note: For backward compatibility, the older flat format using `openai_base_url`, `late_subagent_model`, etc., is also still supported.

---

## The TUI

Late keeps the orchestrator and active subagents visible inside the same terminal interface.

The essentials:

| Key / Command       | Action                                               |
| ------------------- | ---------------------------------------------------- |
| `Tab`               | Switch between the orchestrator and active subagents |
| `Ctrl+O`            | Attach a file                                        |
| `Esc` / `Ctrl+G`    | Stop the currently running agent                     |
| `/model`            | Change orchestrator or worker models                 |
| `/rewind`           | Rewind to an earlier point in the conversation       |
| `/themes`           | Change the TUI theme                                 |
| `/compose`          | Draft a longer instruction in your `$EDITOR`         |
| `Ctrl+D` / `Ctrl+C` | Quit                                                 |

Type `/` at any time to open the command picker.

When Late creates subagents, each appears in its own tab while it works and disappears after completing its task.

---

## Context Compaction

Late can keep oversized tool outputs out of the orchestrator's context. The port follows [jev-compaction](https://github.com/Waxmell114514/jev-compaction) (MIT) and its staged shadow → enabled rollout. Pick a stage with `--compaction-mode` (or `compaction-mode` in `config.json`):

* `off` — no scoring, no logging.
* `shadow` (default) — tool outputs are segmented and Jev-scored, and what *would* be elided is recorded to the shadow log (`~/.local/share/late/compaction-shadow.jsonl`). No behavior change.
* `enabled` — tool outputs over 4000 characters are segmented and Jev-scored; low-scoring segments are elided into `[[elided …]]` pointers. The `expand` tool retrieves the original text on demand.

Scoring is fail-open: any backend error keeps the original tool output. Segments scoring strictly below the elision threshold are elided. That threshold resolves as: explicit `-compaction-threshold` flag > `compaction-threshold` in `config.json` (valid range (0,1]) > the default `0.35`. Don't confuse the three similarly named knobs: `compaction-threshold` is the per-segment SCORE cutoff, `compaction-threshold-percent` sets the context level the info bar reports headroom for, and `jev-autocompact-percent` sets the context level that fires the auto-trigger.

The scorer is keep-biased by design, so real scores on dense, code-heavy sessions cluster well above the default — 0.4–0.9 is normal for good content. If a compaction run "saved only a handful of tokens", that is usually the threshold, not a broken scorer: see the 413 playbook below for how to pick a better one from your own shadow log.

### If you hit 413 / context too large

A provider that caps the request body (many proxies reject bodies over ~2 MB) answers every turn with `API error (413)` — Late renders it as *"request body exceeds this provider's limit (413): compact the context with /jev-compact-context (consider raising compaction-threshold in config.json) or start a new session with /new"*. With `compaction-mode: enabled`, the TUI also automatically runs ONE recovery compaction pass (never a loop — at most once per conversation, until `/new`). Work through this playbook:

1. **Measure first**: `late -replay-shadow=0.35,0.5,0.65,0.8` re-decides your existing shadow log at each threshold (read-only, no scorer round trips) and prints the kept/relocated/tokens-saved/still-missed table per threshold, so you can see which cutoff would actually free the space you need.
2. **Set the threshold**: put the winning value in `config.json` as `"compaction-threshold": 0.65` (or pass `-compaction-threshold=0.65` for a single run) and restart `late`.
3. **If nothing relocates even at a high threshold**, the session is genuinely dense: start a new session with `/new` and re-seed only the context that matters.

### Pointers and the record store

An elided run of segments is replaced by one pointer line standing exactly where the run stood:

```
[[elided id=r:1a2b3c4d lines=12-18 tokens=214 "first line of the elided run, cut at 120 chars…"]]
```

* `id` is content-addressed: the first 8 hex chars of a SHA-256 of the run text, so the same run always maps to the same id and the same record (re-compacting identical text never duplicates records).
* `lines` is the run's 1-based line range in the original output, `tokens` its token count, and the quoted summary is a 120-char preview so the agent can decide whether the `expand` tool is worth calling.

Under `compaction-mode: enabled` the originals are persisted to an append-only JSONL store at `~/.local/share/late/compaction-store.jsonl`, so pointers saved into a session history still resolve — and `expand` still works — after `late` exits. If the store cannot be opened, compaction degrades to in-memory (with a warning): the session keeps working, only cross-restart pointer resolution is lost.

### Gate safety knobs

The elide decision runs through the reference pipeline's gate (defaults mirror `pipeline.py`):

* `compaction-max-elide-percent` (default `70`, range 1–100) — the tripwire: when the scorer wants to elide more than this share of a tool output's tokens, it is distrusted and NOTHING is elided for that output (recorded in the result and the shadow log).
* `compaction-protected-floor` (default `5`, range 1–100) — stacktrace and diff segments are only elided below this score, whatever the normal threshold: a dropped hunk or trace silently corrupts everything built on top of it.
* The token min-gate (fixed 400 tokens) skips scoring entirely for outputs below the floor — the round trip costs more than any possible elision saves.

### Preflight and replay (`-check-compaction`, `-replay-shadow`)

```bash
late -check-compaction            # three-stage preflight against the resolved backend
late -replay-shadow=0.10,0.35,0.50   # replay the shadow log at other thresholds
```

* `-check-compaction` runs the three stages the reference `check.py` runs — (1) decisions answers parse, (2) the gate actually relocates something from a real ~2KB tool output, (3) a pointer expands back byte for byte — prints a per-stage report with latencies, and exits 0/1 without starting the TUI. Run it after configuring a backend and before trusting the feature; the first failing stage names what broke (bad key, malformed request, unreachable server).
* `-replay-shadow=<thresholds>` is read-only offline replay: it re-decides every logged score at each comma-separated threshold (no scorer round trips) and prints the kept/relocated/tokens-saved/still-missed table plus the false-negative rate, so you can pick the elision threshold from your own traffic instead of the default (see the 413 playbook above).

### Offline demo (`compaction-backend: "offline"`)

```json
{
  "compaction-mode": "enabled",
  "compaction-backend": "offline"
}
```

`compaction-backend: "offline"` swaps the System One scoring backend for a deterministic local scripted scorer: scores are a SHA-256 of the task and segment text mapped into [0,1), so the whole flow — shadow scoring, the gate, pointers, `expand`, `/jev-compact-context`, `-check-compaction` — runs with NO API key, NO network, and identical results every run. It exists for demos and tests only: the scores measure nothing about essentialness, so never ship it as a default (the value wins over `JEV_API`/auto-detection when set, and `-check-compaction` passes by construction on this backend).

### Full-History Compaction (`/jev-compact-context`)

`/jev-compact-context` in the TUI compacts the whole conversation, not just tool outputs: every message after a frozen prefix (the first quarter of the history, so the system prompt and earliest exchanges stay byte-identical for prompt caching) is segmented and Jev-scored against the ongoing task. Low-scoring segments are replaced in place with `[[elided …]]` pointer lines and their originals move to the store, where the `expand` tool retrieves them on demand; user messages are never compacted. The command requires `compaction-mode` ≠ `off`; under `shadow` it runs report-only, showing what a real run would save without touching history.

The frozen prefix is append-only across runs AND restarts: every completed run advances a persisted high-water mark (session meta `CompactionHighWater`), and messages below the mark are never re-scored or rewritten — resumed sessions never spend tokens re-compacting what an earlier run already froze. A run that would mutate a message below the mark fails loudly instead of corrupting the cache anchor. Pointer-bearing messages are final (never re-scored, never nested).

### Auto-Compaction (`jev-autocompact`)

```json
{
  "jev-autocompact": true,
  "jev-autocompact-percent": 99
}
```

* `jev-autocompact` (bool, default `false`) — when enabled and the context usage crosses the percent, the same compaction runs automatically.
* `jev-autocompact-percent` (default `99`, valid range 1–100) — the context-usage percentage that fires it. The trigger runs once per crossing and re-arms after compaction shrinks usage back down (or when `/new` starts a fresh conversation).

### Retrieved Context (`compaction-retrieval`)

```json
{
  "compaction-mode": "enabled",
  "compaction-retrieval": true
}
```

`compaction-retrieval` (bool, default `false`) turns on the read side of the compaction store: before every stream request, the store's per-record summaries are scored against the current task and the top matches (up to 5 records scoring ≥ 0.5, capped at a 24k-token digest budget) are appended to the END of the outgoing request as a "Retrieved context" block. That is the work area by construction — it never touches the frozen prefix, never lands in history, and never shows in the transcript, so it costs tokens only for the request that carries it and is re-scored fresh every turn. Each scored record is logged to the shadow log as a `kind=retrieve` decision (`injected`/`skipped`), so `-replay-shadow` tooling can audit what retrieval would have injected at other thresholds. The switch only does something under `compaction-mode: enabled` — that is the only mode whose record store ever fills (the resolver warns about the inert combinations) — and a scoring failure stages nothing for that turn rather than injecting unranked records.

> Credits: the scoring protocol, the gate, the pointer format, the shadow log, and the staged shadow → enabled rollout are a Go port of [jev-compaction](https://github.com/Waxmell114514/jev-compaction) (MIT). The offline scripted scorer mirrors that repo's testing.py demo, which drives the same flow against a scripted stand-in scorer with no backend.

---

## Tool Approval

Potentially destructive commands and file changes require approval unless you have already granted permission for that scope.

When prompted, you can approve:

* once;
* for the current session;
* for the current project;
* globally.

Read-only operations are generally handled automatically.

Approvals decay over time rather than becoming permanent trust forever.

---

## Run Fully Autonomously with Podman

For unattended work, large refactors, or overnight runs, use `late-podman`.

It runs Late inside an isolated rootless Podman container rather than giving the agent unrestricted access to your host.

From inside your project run:

```bash
late-podman
```

Late automatically looks for container configuration in the project, including `.devcontainer/devcontainer.json`. For an example check Late's own [devcontainer.json](../.devcontainer/devcontainer.json).

You can also specify an image explicitly:

```bash
late-podman --image your-development-image
```

Arguments after `--` are forwarded to Late:

```bash
late-podman -- --continue
```

Your current workspace is mounted read-write at `/workspace`. Late keeps its own session and cache volumes, forwards your SSH agent when available, and mounts your Late configuration read-only if present. Your host home directory and container socket are not exposed by default.

> **Note:** `late-podman` requires Linux with Podman installed. Includes additional SELinux support. Works out of the box with Silverblue and Universal Blue images.

> **Note:** Devcontainer configurations may declare additional mounts. Late respects those when constructing the sandbox.

---

## Start with a Prompt

For scripts or unattended workflows:

```bash
late --prompt "Run the test suite, diagnose the failures, and fix them."
```

If using `late-podman`:

```bash
late-podman -- --prompt "Refactor this package and verify all tests."
```

---

## Resume Previous Work

Late automatically saves sessions.

Resume the most recently updated session, no matter which project it belongs to:

```bash
late --continue
```

Resume the most recently updated session for the **current project**. The project is resolved as the git repository root containing your working directory (falling back to the working directory itself outside a repository), so this also works from inside a subdirectory:

```bash
late --continue-project
```

The two flags are mutually exclusive: pass at most one. Sessions from other projects — or ones created before the project directory was recorded — can be found with `late session list` (use `-v` to see each session's project folder) and resumed with `late session load <id>`:

```bash
late session list -v
late session load <id>
```

---

## MCP Integration

Late supports the Model Context Protocol (MCP) to let you bring your own external tools. Add your MCP servers to one of the following locations:

* **Linux:** `~/.config/late/mcp_config.json`
* **macOS:** `~/Library/Application Support/late/mcp_config.json`
* **Windows:** `%APPDATA%\late\mcp_config.json`
* **Project-local:** `.late/mcp_config.json`

```json
{
  "mcpServers": {
    "my-server": {
      "command": "npx",
      "args": ["-y", "my-mcp-server"]
    }
  }
}
```

---

## Agent Skills

[Skills](https://agentskills.io/) are reusable sets of markdown instructions. They are discovered automatically from:

* **Linux:** `~/.config/late/skills/`
* **macOS:** `~/Library/Application Support/late/skills/`
* **Windows:** `%APPDATA%\late\skills\`
* **Project-local:** `.late/skills/`

No setup required. Just drop your skills into the respective folders, and Late will automatically load them.

---

## Plugins

Plugins bundle **skills**, **slash commands**, **MCP servers**, **hooks**, **themes**, and **inline tools** into one installable unit.

You can find the default registry at https://github.com/mlhher/late-plugins.

```bash
# Install from the default registry or npm fallback
late plugin install notify-tool-approval

# Install from a Git repo
late plugin install https://github.com/you/late-plugin.git

# Install from a local path for development
late plugin install ./my-plugin
```

For plugin development and manifest formatting, see the [Plugin SDK](plugin-sdk.md).

---

## File Exclusions

Late's native search tool respects your project's `.gitignore` automatically, saving LLM context by excluding vendor and build directories. 

You can also create an `.llmignore` file alongside your `.gitignore` to specifically hide files from the agent (e.g., secrets, large binaries, test fixtures, or generated code) without affecting your git tracking.

---


## Stream Retries

Transient LLM API failures are retried automatically, so a flaky gateway rarely interrupts a run:

* Transport errors — connection refused/reset, timeouts, and mid-stream disconnects (the server accepted the request, then the body died) — are retried.
* HTTP 408, 429, and 5xx responses are retried; HTTP 400 gets a few quick retries.
* Each retry waits a jittered exponential backoff (500 ms base, capped at 30 s). A server `Retry-After` header is honored as a floor, capped at 5 minutes.
* Failures that retrying cannot fix fail fast: TLS/certificate errors, unsupported URL schemes, and plain HTTP on an HTTPS endpoint.

The retry budget is resolved as CLI flag > environment variable > built-in default:

* `--max-stream-retries <n>` — maximum retries per stream call (default: 10)
* `LATE_MAX_STREAM_RETRIES` — environment variable, used when the flag is not passed

Setting `0` (or a negative value) disables stream retrying entirely. Run `late -h` to see all flags.

---

## Common Flags

| Flag | Description |
| --- | --- |
| `--help` | Show all flags and commands |
| `--version` | Show version information |
| `--continue` | Resume the latest session, regardless of project directory |
| `--continue-project` | Resume the latest session in the current project (git repo root of the working directory; falls back to the working directory outside a repo) |
| `--prompt "..."` | Start the agent immediately with the given prompt |
| `--suppress-thinking-words` | Apply a default Logit bias map of overthinking words (`llama.cpp` only) |
| `--logit-bias` and `--subagent-logit-bias` | Manually set the logit biases for specific models (`llama.cpp` only) |
| `--gemma-thinking` | Inject thinking tokens for Gemma 4 models |
| `--subagent-max-turns <n>` | Set max turns per subagent (default: 500) |
| `--append-system-prompt "..."` | Append text to the system prompt (e.g. further instructions) |
| `--enable-images` | Treat models as supporting images (for non llama.cpp servers) |
| `--save-subagent-histories` | Persist subagent conversation histories to disk |
| `--max-stream-retries <n>` | Max retries per LLM stream call with jittered exponential backoff (default: 10); `0` disables stream retrying. Env: `LATE_MAX_STREAM_RETRIES` |
| `--ask-for-user-approval` | Require user approval for dangerous commands (default; overrides `config.json` `permission-mode`) |
| `--i-promise-i-have-backups-and-will-not-file-issues` | Run every tool without user confirmation (overrides `config.json` `permission-mode`) |

The two permission flags above are mutually exclusive: pass at most one of `--ask-for-user-approval` or `--i-promise-i-have-backups-and-will-not-file-issues`.

