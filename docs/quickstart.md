# Late Quickstart Guide

This guide gets you productive in Late in under 5 minutes.

## Setup

**1. Set your endpoint** (any OpenAI-compatible API, e.g. llama.cpp, [Google](https://ai.google.dev/gemini-api/docs/openai), [Anthropic](https://platform.claude.com/docs/en/api/openai-sdk), [OpenRouter](https://openrouter.ai/docs/quickstart)): 

```bash
# Local (e.g. llama.cpp)
export OPENAI_BASE_URL="http://localhost:8080"

# Cloud (e.g. Google)
export OPENAI_BASE_URL="https://generativelanguage.googleapis.com/v1beta/openai/"
export OPENAI_API_KEY="your-api-key"
export OPENAI_MODEL="your-model"
```

> **Windows:** Use your preferred shell's syntax for all environment variables for example `$env:OPENAI_BASE_URL="http://localhost:8080"` in PowerShell.

**2. Launch Late from your project directory:**

```bash
cd your-project
late
```

> **macOS:** If macOS blocks the binary, run this command in your terminal (adjust the path if needed): `xattr -d com.apple.quarantine ~/.local/bin/late`

**3. Hybrid Routing (Optional):**
By default, Late uses the same model for both the Lead Architect (orchestrator) and the ephemeral workers (subagents). You can mix and match models by setting separate environment variables.
Check the [Configuration](#configuration) section to find out how to persist these settings.

This is useful for using a large, smart model for planning and a fast, cheap model for execution:

```bash
export LATE_SUBAGENT_MODEL="gemma-4-e4b"
export LATE_SUBAGENT_BASE_URL="http://10.8.0.2:8080" # (Optional) falls back to OPENAI_BASE_URL
export LATE_SUBAGENT_API_KEY="your-other-key"        # (Optional) falls back to OPENAI_API_KEY
```

## Interface

Late is a terminal UI with three areas: the **chat viewport** (scrollable history), the **input box** (bottom), and the **status bar** (shows mode, status, token count, and available keybindings).

### Keybindings

| Key | Action |
| --- | --- |
| `Enter` | Send your message (`Alt+Enter` for a new line) |
| `↑` `↓` `PgUp` `PgDn` | Scroll chat viewport |
| `Home` / `End` | Move cursor to line start/end (scrolls chat viewport if input is empty) |
| `Shift+Home/End` | Scroll chat viewport to top/bottom |
| `Tab` | Switch between agent tabs (orchestrator ↔ subagents) |
| `Esc` / `Ctrl+G` | Stop the current agent (cancel generation) |
| `Ctrl+D` / `Ctrl+C` | Quit Late |

> **Tip:** Late supports standard terminal editing like `Alt+Arrows` (word jump), `Ctrl+A/E` (start/end), and `Alt+Backspace/Del` (delete word).

### Agent Tabs

When Late spawns subagents, each one gets its own tab. Use `Tab` to cycle through them:

- **Main** — the orchestrator (Lead Architect). It plans and delegates.
- **Subagent tabs** — ephemeral workers executing isolated tasks. They appear when spawned and disappear when finished.

The status bar at the bottom shows which agent you're currently viewing and its state (Idle, Thinking, Streaming, etc.).

> **Tip:** If a subagent seems stuck, switch to it with `Tab` to see what it's doing. You can stop it with `Esc` or `Ctrl+G` without affecting the orchestrator.

## How to Give Good Instructions

Late works best with clear, specific instructions. Some examples:

```
# Good
Add input validation to the CreateUser handler in api/users.go.
Check for empty email and name fields, return 400 with a JSON error.

# Good
Refactor the database package to use connection pooling.
The pool config should come from environment variables.

# Bad
Make the code better.
```

Late will read your codebase, plan the implementation, and ask you for approval. Make sure to read the generated implementation plan (`./implementation_plan.md`) and the intended changes before approving.

## Tool Approval

When the agent wants to run a command or edit a file, you'll see a confirmation prompt:

```
The agent wants to execute a bash command.
   {"command":"npm run build"}
> Press [y] Allow once | [s] Allow always (session) | [p] Allow always (project) | [g] Allow always (global) | [n] Deny
```

- **Read-only commands** (`ls`, `cat`, `grep`, etc.) are auto-approved for speed (Note: the listed commands can still require permission if Late deems the agents activity suspicious)
- **Everything else** requires explicit approval.
- Use **`[y] Allow once`** to approve only this single tool call.
- Use **`[s] Allow always (session)`** to auto-approve matching requests for the rest of the current session.
- Use **`[p] Allow always (project)`** to remember approval for this project.
- Use **`[g] Allow always (global)`** to remember approval across all projects on this machine.
- Use **`[n] Deny`** to block the request.

This keeps one-off actions safe while reducing repetitive prompts when you trust a tool in a broader scope.

### Permission Decay (TTL)

"Always" approvals are not permanent. Late uses TTL (time-to-live) so trust decays over time:

- **Session scope** (`[s]`) lasts **30 minutes**.
- **Project scope** (`[p]`) lasts **30 days**.
- **Global scope** (`[g]`) lasts **30 days**.

When a TTL expires, the approval is automatically ignored and Late will prompt you again. This is intentional: it reduces long-lived stale permissions while keeping day-to-day workflows smooth.

Notes:

- Re-approving a tool/command in the same scope refreshes its TTL.
- Session approvals are in-memory and expire quickly by design.
- Project/global approvals are persisted with an expiry timestamp and checked at load time.

## Configuration

You can set your preferred model selection (orchestrator, subagents) and their respective configuration (host, keys) permanently inside the `config.json`.

**File Locations:**
* **Linux:** `~/.config/late/config.json`
* **macOS:** `~/Library/Application Support/late/config.json`
* **Windows:** `%APPDATA%\late\config.json`

**Setting Precedence:**
1. Non-empty environment variables
2. `config.json`
3. Defaults


```json
{
  "openai_base_url": "http://localhost:8080",
  "openai_api_key": "your-api-key",
  "openai_model": "qwen3.6-35b-a3b",
  "late_subagent_base_url": "http://10.8.0.2:8080",
  "late_subagent_api_key": "your-other-api-key",
  "late_subagent_model": "gemma-4-e4b"
}
```

## MCP Integration

Late supports the Model Context Protocol. Add your MCP servers to one of the following locations:

* **Global (Linux):** `~/.config/late/mcp_config.json`
* **Global (macOS):** `~/Library/Application Support/late/mcp_config.json`
* **Global (Windows):** `%APPDATA%\late\mcp_config.json`
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

## Agent Skills

[Skills](https://agentskills.io/) are reusable sets of instructions. They are discovered automatically from:
* **Global (Linux):** `~/.config/late/skills/`
* **Global (macOS):** `~/Library/Application Support/late/skills/`
* **Global (Windows):** `%APPDATA%\late\skills\`
* **Project:** `.late/skills/`

There is no further setup required. Just add your skills to the directories and they will be discovered automatically.

## Common Flags

| Flag | Description |
| --- | --- |
| `--help` | Show all flags and commands |
| `--version` | Show version information |
| `--gemma-thinking` | Inject thinking tokens for Gemma 4 models |
| `--subagent-max-turns <n>` | Set max turns per subagent (default: 500) |
| `--append-system-prompt "..."` | Append text to the system prompt (e.g. further instructions) |

## Sessions

Late automatically saves your session history. Resume or manage sessions:

```bash
late session list          # List all saved sessions
late session list -v       # Verbose listing with details
late session load <id>     # Resume a previous session
late session delete <id>   # Delete a session
```

## Git Worktrees

Late is designed for parallel development. You can manage Git worktrees directly to run separate agent instances in isolated environments:

```bash
late worktree list               # List all worktrees
late worktree active             # Show current worktree
late worktree create <path> [br] # Create a new worktree at <path>
late worktree remove <path>      # Remove a worktree
```

> **Tip:** Use worktrees when you want Late to work on a feature in the background while you continue working on another branch.
