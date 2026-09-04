# Worked Example: `late-codestyle`

This page is an end-to-end example of a plugin that exercises **every
extension surface** Late supports today: skills, MCP servers, slash
commands (both shapes), themes, lifecycle hooks (mutation + veto),
inline tools, and project-local install.

The example is `late-codestyle`, a small developer-tooling plugin that
formats, lints, and looks up CLI syntax. It is intentionally tiny so
the contracts stay visible — every script is short.

> See [plugin-sdk.md](./plugin-sdk.md) for the canonical field-by-field
> reference. This page is the "show me the whole thing" companion.

---

## Directory Layout

```
late-codestyle/
├── package.json
├── skills/
│   └── codestyle.md
├── themes/
│   └── ocean.json
├── hooks/
│   ├── veto.sh          # onToolCall — mutate or block dangerous calls
│   ├── welcome.sh       # onSessionStart
│   └── log-result.sh    # onToolResult — observation (may also mutate/veto)
└── scripts/
    ├── lint.sh          # /lint handler
    ├── lint-server.sh   # MCP stdio server (mock for demo)
    └── lookup.sh        # inline `lookup` tool + /lookup command fallback
```

Run `chmod +x hooks/*.sh scripts/*.sh` after creating these files.

---

## The Manifest (`package.json`)

Every `late.*` field declared, with realistic values. This is the
complete `late` object — the surrounding `package.json` is the usual
`name`/`version`/`description` triple and otherwise vanilla.

```json
{
  "name": "late-codestyle",
  "version": "0.1.0",
  "description": "Format, lint, lookup, and ocean theme for Late.",
  "late": {
    "skills": ["skills/"],

    "mcp": {
      "servers": {
        "live-lint": {
          "command": "bash",
          "args": ["scripts/lint-server.sh"]
        }
      }
    },

    "commands": [
      "/format",
      { "name": "/lint", "handler": "scripts/lint.sh" }
    ],

    "themes": ["themes/ocean.json"],

    "hooks": {
      "onToolCall":     ["hooks/veto.sh"],
      "onSessionStart": ["hooks/welcome.sh"],
      "onToolResult":   ["hooks/log-result.sh"]
    },

    "tools": [
      {
        "name": "lookup",
        "description": "Look up CLI command syntax.",
        "script": "scripts/lookup.sh",
        "parameters": {
          "type": "object",
          "properties": {
            "query": { "type": "string", "description": "The command or flag to look up." }
          },
          "required": ["query"]
        }
      }
    ]
  }
}
```

Notes on the choices below:

- **Commands**: declared in **both** shapes on purpose. `/format` is a
  bare string — Late submits the input as a plain prompt and the agent
  uses the plugin's skills/tools to handle it (legacy path). `/lint`
  carries an explicit `handler` script — Late runs the script and shows
  its stdout as a toast, no orchestration needed.
- **Hooks**: each one demonstrates a different part of the hook
  contract (mutate/veto, sequential transform, lifecycle, observation).
- **Tools**: declared inline so the model can call `lookup` directly,
  no MCP sandbox needed. Names are namespaced to
  `late-codestyle__lookup` (sanitized — `:` is not accepted by
  OpenAI-compatible endpoints).

---

## Skills — `skills/codestyle.md`

Skills are injected into the system prompt as instructions. The
`scripts:` list is informational — agent scripts called from skills
become `ScriptTool`s.

```markdown
---
name: codestyle
description: When the user asks about code formatting, lint summaries, or CLI syntax, prefer the codestyle plugin's tools and scripts over hand-rolled shell.
scripts:
  - scripts/lookup.sh
  - scripts/lint.sh
---

## Instructions

- When the user asks about formatting, prefer invoking `format` via the
  `bash` tool with the project's formatter, but first describe the change
  to the user.
- When the user asks "what flags does X have?" or requests a CLI lookup,
  prefer the `late-codestyle__lookup` tool so the result lands as a real
  tool call trace.
- The plugin's `onToolCall` hook may redact or reject your proposed
  command — if a veto comes back as an error, surface it to the user
  rather than retrying with a workaround.
```

---

## Slash Commands

### `/format` — legacy (plain-prompt fall-through)

`/format` is declared as a bare string. When the user presses Enter,
Late submits `/format ...` as a regular user prompt and the agent
leans on the `codestyle` skill + generic `bash` tool to actually run
the formatter. There is no `handler` script.

### `/lint` — explicit handler

`/lint <file>` runs `scripts/lint.sh` with the trailing args (a JSON
string array) on stdin. Late surfaces the script's stdout as a toast
or surfaces errors as an error toast.

#### `scripts/lint.sh`

```bash
#!/usr/bin/env bash
# /lint handler — receives args JSON on stdin, e.g. ["README.md"]
set -e
args=$(cat)
file=$(printf '%s' "$args" | sed -n 's/.*"\([^"]*\)".*/\1/p' | head -1)

if [ -z "$file" ]; then
  echo "usage: /lint <file>"
  exit 2
fi

echo "linting $file"
if [ ! -f "$file" ]; then
  echo "error: file not found: $file"
  exit 1
fi
wc -l "$file"
```

---

## MCP Server

Servers must speak Model Context Protocol over stdio. The example is a
mock that always replies with a single tool — replace it with a real
MCP server (Python `@modelcontextprotocol/sdk`, Node
`@modelcontextprotocol/sdk`, etc.) for production.

#### `scripts/lint-server.sh`

```bash
#!/usr/bin/env bash
# Minimal mock MCP stdio server — one tool: live_lint
while IFS= read -r _; do
  echo '{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"live_lint","description":"Lint the file at path.","inputSchema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}}'
done
```

The server is registered in the agent as the namespaced tool
`late-codestyle__live-lint` (sanitized; `:` becomes `_`). The MCP `env`
field supports `${VAR}` expansion at launch time — wire secrets in via
`env`, not args.

---

## Inline Tool — `lookup`

`late.tools[*]` registers a script-backed tool **without** an MCP
wrapper. The model calls `late-codestyle:lookup` and the script
receives the JSON-encoded arguments on stdin.

#### `scripts/lookup.sh`

```bash
#!/usr/bin/env bash
# Tool stdin is the arguments JSON, e.g. {"query":"grep -E"}
set -e
payload=$(cat)
query=$(printf '%s' "$payload" \
  | sed -n 's/.*"query"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ -z "$query" ]; then
  echo "error: lookup requires a 'query' argument"
  exit 1
fi

echo "lookup results for: $query"
echo "- ${query} --help → standard help text applies"
echo "- ${query} -V      → version output"
echo "- ${query}(1)     → man page (if installed)"
```

The agent sees this as a normal tool. All the usual middleware
applies — `onToolCall` can still veto it, `enabledTools` still gates
it, and user confirmation still prompts the user. The name is
sanitized for OpenAI-compatible endpoints: `late-codestyle:lookup`
becomes `late-codestyle__lookup` (only `[A-Za-z0-9_-]`, max 64 chars).

---

## Themes

Themes are [Glamour](https://github.com/charmbracelet/glamour) JSON
files. Plug them in by listing their path under `late.themes`. The
TUI's `/themes` command exposes them in the picker.

#### `themes/ocean.json`

```json
{
  "name": "ocean",
  "glamour": {
    "document":  { "color": "#bce0ff" },
    "heading":   { "color": "#5fc8ff", "bold": true },
    "link":      { "color": "#5fc8ff", "underline": true },
    "strong":    { "bold": true, "color": "#ffffff" },
    "bullet":    { "foreground": "#5fc8ff" },
    "code":      { "color": "#cdf7ff", "background": "#002b48" },
    "code_block":{ "color": "#cdf7ff", "background": "#002b48" }
  }
}
```

Apply from the TUI: `/themes ocean` (by bare name) or
`/themes late-codestyle:ocean` (by namespaced ID).

---

## Hooks

Hooks are the must-tiny part of every plugin — they show whether you
understand the contract. Each of the four hooks below tests a
different part of it.

### Hook contract recap

| Hook            | Read from stdin                                       | Write to stdout                                          |
| --------------- | ----------------------------------------------------- | -------------------------------------------------------- |
| `onSessionStart` | empty JSON object `{}`                                 | ignored (fire-and-forget)                                |
| `onToolCall`     | `{ "tool", "arguments", "timestamp" }`                | JSON → mutate `arguments` · literal `"blocked"` → veto the call · empty/non-JSON → pass-through |
| `onToolResult`   | `{ "tool", "result" }`                                | JSON → replace the result the LLM sees · literal `"blocked"` → veto · empty/non-JSON → pass-through |
| `onMessageSend`  | the current user message                              | replacement text (sequential)                             |

### `hooks/veto.sh` — `onToolCall` (mutate OR veto)

```bash
#!/usr/bin/env bash
# Read the ToolCall payload and either:
#   1. Block dangerous commands by returning the literal string "blocked".
#   2. Redact arguments by returning replacement JSON.
#   3. Pass through unchanged by returning empty.
set -e
payload=$(cat)

# Block any bash call that asks for `rm -rf` of root or $HOME.
if printf '%s' "$payload" | grep -q 'rm[[:space:]]*-r[fR]\?[[:space:]]\+/\(\|[[:space:]]\)\|$HOME'; then
  echo "blocked"
  exit 0
fi

# Otherwise, redact long filesystem paths in bash arguments.
if printf '%s' "$payload" | grep -q '"tool"[[:space:]]*:[[:space:]]*"bash"'; then
  redacted=$(printf '%s' "$payload" \
    | sed 's#/Users/[a-z0-9_]\+/<REDACTED>#g' \
    | sed 's#/home/[a-z0-9_]\+/<REDACTED>#g')
  if [ "$redacted" != "$payload" ]; then
    printf '%s' "$redacted"
    exit 0
  fi
fi

# Empty stdout → pass-through.
```

Returning the literal string `"blocked"` aborts the chain (next()
is skipped, the call returns an error to the agent). Returning any
other JSON-valued stdout replaces `call.Function.Arguments` and
continues the chain.

### `hooks/welcome.sh` — `onSessionStart` (lifecycle)

```bash
#!/usr/bin/env bash
# Fires once, when Late boots. Stdin is empty.
echo "[late-codestyle] loaded — /format, /lint, lookup, ocean theme ready" >&2
```

Errors and stderr are forwarded; the user sees the message in the
TUI.

### `hooks/log-result.sh` — `onToolResult` (observation)

```bash
#!/usr/bin/env bash
# Best-effort one-liner per tool result, logged to stderr.
cat \
  | grep -o '"tool"[[:space:]]*:[[:space:]]*"[^"]*"' \
  | head -1 \
  | sed 's/^/[late-codestyle][result] /' >&2
```

This hook logs to stderr, but `onToolResult` is **not** observation-only:
hooks that print valid JSON to stdout replace the result the agent sees,
and a literal `"blocked"` vetoes it. Keeping the script stderr-only is a
choice, not a limitation.

---

## Install & Test

### Global dev install (recommended while iterating)

```bash
# from inside the late-codestyle directory
chmod +x hooks/*.sh scripts/*.sh
late plugin link ./late-codestyle
late plugin list
```

You should see `late-codestyle 0.1.0 local ✓` in the output.

### Project-local install (recommended for team repos)

```bash
mkdir -p .late/plugins
chmod +x hooks/*.sh scripts/*.sh
late plugin link --project ./late-codestyle
```

Now commit `.late/plugins/late-codestyle` (it's a symlink; the real
files live in the repo at `./late-codestyle`) so the whole team gets
the same plugin.

### Smoke tests in the TUI

1. `/help` — confirm `/format` and `/lint` appear in the command list
   alongside the built-ins.
2. `/lint README.md` — should produce a "linting README.md" toast with
   `wc -l` output.
3. `/themes` — open the picker, navigate to `late-codestyle:ocean`,
   press Enter to apply.
4. In the chat, ask *"look up the flags for `grep -E`"* — the agent
   should call `late-codestyle:lookup`.
5. Ask the agent to *"run `bash` with `rm -rf /tmp/cache`"* — the
   hook should let it through (no leading slash, no `$HOME`). Try
   `rm -rf /` on a sandbox and watch the hook veto the call.

If something doesn't work, `late plugin disable late-codestyle` flips
it off without removing the install.

---

## Why each shape?

A short rationale for the design choices that aren't obvious.

| Choice                                  | Why                                                                  |
| --------------------------------------- | -------------------------------------------------------------------- |
| Two commands, two shapes                  | Demonstrates both wire-level dispatch modes.                          |
| `onToolCall` **returns** JSON             | Demonstrates the gate-via-mutate contract — the most powerful hook.   |
| `onToolResult` only writes to stderr      | Demonstrates the observation use case; the hook can also mutate the result by printing JSON to stdout. |
| `late.tools[*]` instead of MCP for `lookup` | Demonstrates the simpler "no-server" path inline tools support.       |
| `themes`                                  | Shows a plugin theme's Glamour style overrides.                       |
| One global install, one `--project` install | Both scopes are first-class; pick whichever matches the rollout.    |

---

> **Tip:** once you're done iterating, run
> `late plugin disable late-codestyle` to turn it off, or
> `late plugin remove late-codestyle` to uninstall. The filesystem
> watcher picks up source edits within about two seconds — no restart
> needed.
