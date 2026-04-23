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
export OPENAI_MODEL="your-model-name"
```
> **Windows:** Use your preferred shell's syntax for all environment variables for example `$env:OPENAI_BASE_URL="http://localhost:8080"` in PowerShell.

**2. Launch Late:**
Late operates within your current working directory. Always launch it from the root of the project you want to work on.

```bash
cd your-project
late
```

> **macOS:** If macOS blocks the binary, run this command in your terminal (adjust the path if needed): `xattr -d com.apple.quarantine ~/.local/bin/late`

> **Windows (Experimental):** Native Windows binaries (`.exe`) are currently experimental. If you hit a bug, please check the issue tracker.

**3. Hybrid Routing (Optional):**
By default, Late uses the same model for both the Lead Architect (orchestrator) and the ephemeral workers (subagents). You can mix and match models by setting separate environment variables.

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
| `Enter` | Send your message |
| `↑` `↓` `PgUp` `PgDn` | Scroll the chat viewport |
| `Tab` | Switch between agent tabs (orchestrator ↔ subagents) |
| `y` / `n` | Approve or deny a tool call when prompted |
| `Ctrl+G` | Stop the current agent (cancel generation) |
| `Esc` / `Ctrl+C` | Quit Late |

### Agent Tabs

When Late spawns subagents, each one gets its own tab. Use `Tab` to cycle through them:

- **Main** — the orchestrator (Lead Architect). It plans and delegates.
- **Subagent tabs** — ephemeral workers executing isolated tasks. They appear when spawned and disappear when finished.

The status bar at the bottom shows which agent you're currently viewing and its state (Idle, Thinking, Streaming, etc.).

> **Tip:** If a subagent seems stuck, switch to it with `Tab` to see what it's doing. You can stop it with `Ctrl+G` without affecting the orchestrator.

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
The agent wants to execute bash.
  {"command": "go test ./..."}
> Press [ y ] to Approve  |  [ n ] to Deny
```

- **Read-only commands** (`ls`, `cat`, `grep`, etc.) are auto-approved for speed (Note: the listed commands can still require permission if Late deems the agents activity suspicious)
- **Everything else** requires your explicit `y` / `n`.

> **Note:** On Windows currently, every command will require your explicit `y` / `n` approval. This will be resolved in a future release.

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

## MCP Integration

Late supports the Model Context Protocol. Add your MCP servers to `~/.config/late/mcp_config.json` (global) or `.late/mcp_config.json` (project-local):

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

MCP tools are automatically available to the agent after connecting.