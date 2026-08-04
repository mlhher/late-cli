# Late Plugin SDK Guide

Late's plugin system lets you bundle extension surfaces — skills, slash commands, MCP servers, themes, and hooks — into a single installable unit. Plugins can be installed from npm, a Git repository, a local directory, or a marketplace catalog.

---

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Manifest Format](#manifest-format)
- [Extension Surfaces](#extension-surfaces)
  - [Skills](#skills)
  - [Slash Commands](#slash-commands)
  - [MCP Servers](#mcp-servers)
  - [Themes](#themes)
  - [Hooks](#hooks)
- [Installation Methods](#installation-methods)
  - [From npm](#from-npm)
  - [From Git](#from-git)
  - [From local path](#from-local-path-development)
  - [Project-local (per-project)](#project-local-per-project)
- [CLI Commands](#cli-commands)
- [Development Workflow](#development-workflow)
- [Sample Plugin: `git-pull`](#sample-plugin-git-pull)

---

## Architecture Overview

A plugin is a directory with a `package.json` that contains a `"late"` field. The `"late"` field declares which extension surfaces the plugin provides.

Plugins can be installed in two scopes:

- **Global** — `~/.config/late/plugins/` (Linux) or `~/Library/Application Support/late/plugins/` (macOS). Available in every project.
- **Project-local** — `.late/plugins/` relative to your project root. Only available when Late is running from that project. Overrides a global plugin with the same name.

At startup, Late discovers plugins from both locations, registers their surfaces, and makes them available in the TUI. Project-local plugins take priority over global ones with the same name. A background filesystem watcher polls for changes every 2 seconds so plugin installs and enables/disables are picked up without restarting.

```
~/.config/late/plugins/               # Global plugins (all projects)
├── @late/plugin-graph-rag/           # npm scoped plugin
│   ├── package.json
│   └── skills/
├── my-git-plugin/                    # local or git plugin
│   ├── package.json
│   └── skills/
└── node_modules/                     # npm-managed dependencies (ignored)

.my-project/.late/plugins/            # Project-local plugins (this project only)
├── project-helper/                   # overrides any global "project-helper"
│   ├── package.json
│   └── skills/
└── node_modules/
```

> **Override behavior:** If a plugin with the same name exists in both the global and project-local directories, the project-local version takes precedence. This lets you pin a specific version per project without affecting others.

---

## Manifest Format

Every plugin must have a `package.json` at its root. The `"late"` field is the plugin manifest. All sub-fields are optional.

```json
{
  "name": "my-plugin",
  "version": "1.0.0",
  "description": "A short description of what the plugin does",
  "late": {
    "skills": ["skills/"],
    "commands": ["/my-command"],
    "mcp": {
      "servers": {
        "my-server": {
          "command": "node",
          "args": ["server.js"],
          "env": {
            "API_KEY": "${API_KEY}"
          }
        }
      }
    },
    "themes": ["themes/my-theme.json"],
    "hooks": {
      "onToolCall": ["hooks/log-tool.sh"],
      "onSessionStart": ["hooks/on-start.sh"]
    }
  }
}
```

| Field | Type | Description |
| --- | --- | --- |
| `name` | `string` | **Required.** Plugin name (must match the directory name unless it's an npm scoped package). |
| `version` | `string` | Plugin version (semver). |
| `description` | `string` | Short description shown in `late plugin list`. |
| `late.skills` | `string[]` | Relative paths to [Skill](#skills) directories. |
| `late.commands` | `string[]` | Slash command names (with or without leading `/`). |
| `late.mcp.servers` | `object` | Map of MCP server names to [MCP Server](#mcp-servers) configs. |
| `late.themes` | `string[]` | Relative paths to Glamour theme JSON files. |
| `late.hooks` | `object` | Hook type → script path mappings. |

---

## Extension Surfaces

### Skills

[Agent Skills](https://agentskills.io/) are reusable sets of instructions that Late's orchestrator injects into the system prompt. Skills are discovered automatically from skill directories.

Plugin skills are symlinked into `~/.config/late/skills/<name>` at startup so the existing skill loader discovers them.

A skill directory contains a `SKILL.md` with YAML frontmatter:

```markdown
---
name: git-helpers
description: Helper instructions for git operations
scripts:
  - pull.sh
---

## Instructions

When the user asks about git operations, use the `pull.sh` script
to perform safe git pulls with conflict handling.
```

Each script in a skill becomes a `ScriptTool` that the agent can invoke.

**Directory structure:**
```
my-plugin/
├── package.json
└── skills/
    ├── SKILL.md
    └── pull.sh
```

### Slash Commands

Plugin slash commands appear in the TUI's autocomplete dropdown when the user types `/`. They are also shown in the `/help` overlay and the status bar.

Commands are listed in the manifest as strings. When the user types a plugin slash command and presses Enter, it is dispatched as a regular user prompt to the agent. The plugin's skills, MCP servers, and tools handle the actual execution.

```json
{
  "late": {
    "commands": ["/pull", "/fetch", "/status"]
  }
}
```

The leading `/` is optional — Late normalizes it automatically.

### MCP Servers

Model Context Protocol servers provide tools that agents can call. Plugin MCP servers are merged into Late's MCP configuration at startup.

Server names are automatically namespaced as `plugin-name:server-name` to prevent collisions.

```json
{
  "late": {
    "mcp": {
      "servers": {
        "git-server": {
          "command": "node",
          "args": ["./mcp/server.js"],
          "env": {
            "GIT_REPO_PATH": "${GIT_REPO_PATH}"
          }
        }
      }
    }
  }
}
```

| Field | Type | Description |
| --- | --- | --- |
| `command` | `string` | Executable path or name. |
| `args` | `string[]` | Command-line arguments. |
| `env` | `object` | Environment variables (supports `${VAR}` expansion). |
| `url` | `string` | URL for SSE/HTTP transport. |
| `transportType` | `string` | `"stdio"` (default), `"sse"`, or `"streamable-http"`. |
| `disabled` | `bool` | If `true`, the server is not started automatically. |

### Themes

Themes are [Glamour](https://github.com/charmbracelet/glamour) style JSON files that customize the TUI's markdown rendering. Plugin themes are resolved to absolute paths at registration time.

```json
{
  "late": {
    "themes": ["themes/dark-glow.json"]
  }
}
```

### Hooks

Hooks are scripts that run at specific lifecycle events. Each hook type accepts an array of paths to executable scripts.

```json
{
  "late": {
    "hooks": {
      "onToolCall": ["hooks/log-tool.sh"],
      "onSessionStart": ["hooks/on-start.sh"],
      "onMessageSend": ["hooks/on-send.sh"]
    }
  }
}
```

| Hook | When it fires | Script receives |
| --- | --- | --- |
| `onToolCall` | Before a tool is executed | Tool name and arguments via stdin (JSON) |
| `onSessionStart` | When a new session begins | Session metadata via stdin (JSON) |
| `onMessageSend` | When a user sends a message | The message content via stdin |

---

## Installation Methods

### From npm

```bash
late plugin install @late/plugin-git-helper
```

Runs `npm install` in the plugins directory and symlinks the installed package.

### From Git

```bash
late plugin install https://github.com/user/late-plugin-git.git
late plugin install github:user/late-plugin-git
```

Clones the repository with `--depth 1`, removes the `.git` directory, and loads the plugin.

### From local path (development)

```bash
late plugin link ./my-plugin
# or
late plugin install ./my-plugin
```

Creates a symlink from `~/.config/late/plugins/<name>` to your local directory. Any changes you make are available immediately (the watcher picks them up within 2 seconds).

### Project-local (per-project)

```bash
# Install into .late/plugins/ (create it first if it doesn't exist)
mkdir -p .late/plugins

late plugin install --project ./my-plugin
late plugin install --project github:user/late-plugin
late plugin install --project @late/plugin-git-helper

# Link a local directory into the project
late plugin link --project ./my-plugin

# Remove from project
late plugin remove --project my-plugin
```

The `--project` flag (or `--local`) installs the plugin into `.late/plugins/` instead of the global directory. Project-local plugins are scoped to the current project and are only active when Late is launched from that project directory.

This is useful for:
- **Team-shared configurations** — check `.late/` into version control so everyone gets the same plugins
- **Pinning plugin versions** — override a global plugin with a specific version for a project
- **Isolation** — keep experimental plugins from affecting other projects

Project-local plugins override global plugins with the same name. If both `~/.config/late/plugins/my-plugin` and `.late/plugins/my-plugin` exist, the project-local one wins.

---

## CLI Commands

```
late plugin list, ls                      List installed plugins
late plugin install [--project] <src>     Install a plugin (npm, git, or local path)
late plugin remove [--project] <name>     Remove a plugin (use --project for project-local)
late plugin link [--project] <path>       Symlink a local directory for development
late plugin update [name]                 Update all or a specific plugin
late plugin enable <name>                 Enable a plugin
late plugin disable <name>                Disable a plugin
```

The `--project` (or `--local`) flag can be used with `install`, `link`, and `remove`
to operate on the project-local `.late/plugins/` directory instead of the global store.
When omitted, these commands default to the global directory.

---

## Development Workflow

1. **Create your plugin directory** with a `package.json`:

```bash
mkdir my-plugin && cd my-plugin
```

2. **Write your manifest:**

```json
{
  "name": "my-plugin",
  "version": "0.1.0",
  "description": "My first Late plugin",
  "late": {
    "commands": ["/hello"],
    "skills": ["skills/"]
  }
}
```

3. **Add a skill** (optional):

```bash
mkdir -p skills
```

Create `skills/SKILL.md` with instructions for the agent.

4. **Link it for development:**

```bash
late plugin link ./my-plugin
```

5. **Verify it's loaded:**

```bash
late plugin list
```

You should see your plugin listed. The slash commands appear in autocomplete and the status bar shows the plugin count.

6. **Iterate:** Edit your plugin files. The background watcher picks up changes within ~2 seconds. Run `late plugin list` again or toggle the help overlay to see your updates.

7. **Use project-local scope (optional):** For plugins that should only apply to the current project, create `.late/plugins/` and use the `--project` flag:

```bash
mkdir -p .late/plugins
late plugin link --project ./my-plugin
```

Project-local plugins override global ones. This is great for team repos where you want to check in plugin configurations.

8. **Package for distribution:** Publish to npm or push to a Git repository.

---

## Sample Plugin: `git-pull`

This sample plugin adds a `/pull` slash command and a `git-helpers` skill that teaches the agent how to perform safe git pulls.

### Directory structure

```
git-pull/
├── package.json
└── skills/
    ├── SKILL.md
    └── safe-pull.sh
```

### `package.json`

```json
{
  "name": "git-pull",
  "version": "1.0.0",
  "description": "Safe git pull command and helpers",
  "late": {
    "commands": ["/pull"],
    "skills": ["skills/"]
  }
}
```

### `skills/SKILL.md`

```markdown
---
name: git-helpers
description: Safe git pull instructions
scripts:
  - safe-pull.sh
---

## Safe Git Pull Procedure

When the user asks to pull changes or uses the `/pull` command:

1. First check for uncommitted changes with `git status --porcelain`
2. If there are uncommitted changes, stash them with `git stash push -m "auto-stash before pull"`
3. Run `git pull --ff-only` to ensure a fast-forward merge
4. If the pull succeeds and stashed changes exist, pop them with `git stash pop`
5. If the pull fails, restore the stash with `git stash pop` and report the error

Use the `safe-pull.sh` script tool for automated execution.
```

### `skills/safe-pull.sh`

```bash
#!/bin/bash
set -e

echo "=== Safe Git Pull ==="

# Check for uncommitted changes
if [ -n "$(git status --porcelain)" ]; then
    echo "Uncommitted changes detected. Stashing..."
    git stash push -m "auto-stash before pull"
    STASHED=true
fi

# Pull with --ff-only for safety
echo "Pulling..."
if git pull --ff-only; then
    echo "Pull successful."
    if [ "$STASHED" = true ]; then
        echo "Restoring stashed changes..."
        git stash pop
    fi
else
    echo "Pull failed. Restoring stashed changes..."
    if [ "$STASHED" = true ]; then
        git stash pop
    fi
    exit 1
fi
```

Make the script executable:

```bash
chmod +x skills/safe-pull.sh
```

### Install and test

```bash
# Link locally for development
late plugin link ./git-pull

# Verify
late plugin list
# Expected output:
# Name       Version  Source  Enabled  Path
# ----       -------  ------  -------  ----
# git-pull   1.0.0    local   ✓        ~/.config/late/plugins/git-pull

# Type /pull in the chat and press Enter
# The agent uses the git-helpers skill to run a safe git pull
```

### Extending the plugin

Add more commands, skills, or MCP servers:

```json
{
  "name": "git-pull",
  "version": "2.0.0",
  "description": "Git tools for Late",
  "late": {
    "commands": ["/pull", "/fetch", "/status", "/log"],
    "skills": ["skills/"],
    "mcp": {
      "servers": {
        "git-server": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-github"]
        }
      }
    }
  }
}
```

---

> **Tip:** Plugins are discovered at startup and watched for changes every 2 seconds. You can enable/disable plugins with `late plugin enable <name>` and `late plugin disable <name>` without restarting Late.

---

## Plugin Surfaces (Reference)

A plugin manifest's `late` field can declare any of the following surfaces.
All surfaces are optional and may be combined in any package.json.

### `late.commands` — slash commands

`late.commands` accepts either a flat array of strings (legacy form, the
command falls through to plain-prompt dispatch) or an array of objects that
can attach a `handler` script (messages are dispatched to your script, stdout
becomes a toast, errors become error toasts).

```json
{
  "late": {
    "commands": [
      "/weather",
      { "name": "/git-pull", "handler": "scripts/pull.sh" }
    ]
  }
}
```

When the handler returns non-empty stdout, the TUI shows a toast like:
`/git-pull → "Already up to date."`. When it exits non-zero, the toast
reads: `/git-pull failed: <message>`.

### `late.tools` — inline agent-callable tools

Tools declared this way are exposed to the model without an MCP wrapper.
The script receives the tool's argument JSON on stdin and must return the
result on stdout.

```json
{
  "late": {
    "tools": [
      {
        "name": "summarize",
        "description": "Summarize a file the user references.",
        "script": "scripts/summarize.sh",
        "parameters": {
          "type": "object",
          "properties": { "path": { "type": "string" } },
          "required": ["path"]
        }
      }
    ]
  }
}
```

The tool is registered under the namespaced name `<plugin>:<tool>` (e.g.
`weather:summarize`). All the usual onToolCall / middleware pipeline rules
apply, so plugin tools respect user confirmation, hook mutation, and
enabledTools gating.

### `late.hooks` — lifecycle hooks

| Hook            | Trigger                                                       | Input (stdin)                                          |
| --------------- | ------------------------------------------------------------- | ------------------------------------------------------ |
| `onSessionStart` | Once, when Late starts.                                      | empty                                                   |
| `onToolCall`     | Before every tool runs. May mutate or veto (return `"blocked"`). | `{ "tool": "...", "arguments": {...}, "timestamp": "..." }` |
| `onToolResult`   | After every tool runs. Observation only by default; can **mutate** the result before the LLM sees it (see [Tool-result mutation](#tool-result-mutation) below). | `{ "tool": "...", "result": "..." }`                    |
| `onMessageSend`  | Sequential transform of outgoing user messages.              | the current message text                                 |

Hooks are run inside the plugin's directory, so relative paths in the
manifest are resolved against the package root. Paths that escape the
plugin directory are rejected.

### `--project` — project-local plugins

Install into `~/.config/late/.late/plugins/` (your project's local copy)
instead of the global plugins directory:

```bash
late plugin install --project ./my-plugin
late plugin link    --project ./my-plugin
```

Project-local plugins override global plugins with the same `name`, so
teams can ship a plugin with their repo without forcing every developer
to install it. Path: `$CWD/.late/plugins/`.

### From the marketplace registry

```bash
# Bare name (no @, no /, no `.git`, no path prefix) → marketplace lookup.
late plugin install git-helper
# `github:user/repo` shorthand is also accepted.
late plugin install github:my-org/git-helper
```

Bare names hit the marketplace first (`LATE_PLUGIN_REGISTRY` overrides
`https://registry.late.dev/v1`). The registry returns either an npm target
or a git URL. If the registry is unreachable or returns 404, install
falls back to trying the bare name as an npm package. Override the
registry from the environment when self-hosting:

```bash
export LATE_PLUGIN_REGISTRY="https://registry.example.com/v1"
```

## Updating Plugins

```bash
# Re-fetch every installed npm/git plugin in place; skips local devlinks.
late plugin update

# Update one plugin by name. Marketplace-source plugins re-resolve first.
late plugin update git-helper
```

Update flow:

- **npm** — `npm install --prefix <plugins-dir> --no-save --quiet <pkg>@latest`
  then recreates the `plugins/<name>` symlink if it was missing.
- **git** — clones to a sibling temp directory, strips `.git`, then atomically
  `rename`s over the existing plugin directory (no half-writes).
- **marketplace** — re-resolves the registry entry, then proceeds as npm or git.
- **local** — skipped with a hint to edit the source directory directly.

## Tool-result Mutation

`onToolResult` scripts can rewrite the tool result before the LLM sees it.
The hook runs sequentially across plugin scripts (deterministic order
matches the snapshot from `PluginChangeMsg`). A script's stdout is
interpreted like this:

| Script stdout           | Effect                                                          |
| ----------------------- | --------------------------------------------------------------- |
| empty / non-JSON        | Result passes through to the next script / LLM unchanged.        |
| valid JSON              | Replaces the result bytes for the next script / LLM.            |
| literal `blocked`       | Vetoes the result; the calling tool returns an error.            |
| stderr / nonzero exit   | Logged, the rest of the chain continues with the prior result.   |

The public entry point is `(*PluginManager).CallOnToolResultHooks(ctx, tool, result) ([]byte, error)`.
The orchestrator's tool-execution pipeline does not call it yet —
plugin authors can invoke it from a custom tool shim or an add-on
process while the agent-side integration is being finalized.

---

## See also

A worked example that exercises **every surface** described above
(skills, MCP, commands in both shapes, themes, hooks including veto and
mutate, inline tools, and the `--project` flag) lives at
[`plugin-example.md`](./plugin-example.md) — it's the recommended
starting point for plugin authors who want a copy-paste-able skeleton.
