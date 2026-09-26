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

## Subagent Control

Subagents run under wall-clock budgets and an idle watchdog:

| Flag | Default | Behavior |
| --- | --- | --- |
| `--subagent-timeout <dur>` | 24h | Max wall-clock time for one subagent run (`0` = unlimited). Also settable as `subagent_timeout` in `config.json`; the flag wins over the config value. |
| `--subagent-idle-timeout <dur>` | 15m | Notify when a subagent has been truly idle — no stream progress, no in-flight tool, no nested spawn (`0` = off). |
| `--subagent-idle-kill-after <dur>` | 0 | Kill a subagent that stays truly idle past this duration (`0` = notify only). |

The orchestrator can also budget a single run: `spawn_subagent` accepts an optional `timeout` argument (e.g. `"45m"`, `"2h"`; `"0"` = unlimited; omitted = the global value).

Every `bash` call accepts an optional per-call `timeout` argument (e.g. `"30m"`; `"0"` = unlimited; omitted = the global default of 10m). Timed-out processes are killed with their whole process group, so runaway pipes and grandchildren cannot hang the session.

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

