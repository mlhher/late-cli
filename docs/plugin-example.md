# Worked Example: `example-plugin`

This page is an end-to-end walkthrough of the reference plugin shipped directly in the repository at [`example-plugin/`](../example-plugin). It exercises **every extension surface** Late supports: skills, MCP servers, slash commands (both prompt dispatch and script handlers), themes, inline tools, and all four lifecycle hooks (`onSessionStart`, `onMessageSend`, `onToolCall`, and `onToolResult`).

See [plugin-sdk.md](./plugin-sdk.md) for the canonical field-by-field reference.

---

## Directory Layout

The plugin lives at [`example-plugin/`](../example-plugin) with the following structure:

```
example-plugin/
├── package.json
├── skills/
│   └── demo/
│       └── SKILL.md
├── themes/
│   └── demo.json
└── scripts/
    ├── command.sh          # /echo-cmd handler script
    ├── tool.sh             # inline greet tool script
    ├── mcp_server.py       # stdio MCP server (provides mcp_ping tool)
    ├── session_start.sh    # onSessionStart hook
    ├── message_send.sh     # onMessageSend hook
    ├── tool_call.sh        # onToolCall hook (mutate or veto)
    └── tool_result.sh      # onToolResult hook (mutate or veto)
```

Ensure scripts are executable before testing:

```bash
chmod +x example-plugin/scripts/*.sh example-plugin/scripts/mcp_server.py
```

---

## The Manifest (`package.json`)

The manifest declares all plugin surfaces in the `"late"` field:

```json
{
  "name": "example-plugin",
  "version": "1.0.0",
  "description": "Comprehensive plugin consuming all available Late plugin APIs",
  "late": {
    "skills": [
      "skills"
    ],
    "commands": [
      "/bare-cmd",
      {
        "name": "/echo-cmd",
        "handler": "scripts/command.sh"
      }
    ],
    "tools": [
      {
        "name": "greet",
        "description": "Returns a greeting for the given name",
        "script": "scripts/tool.sh",
        "parameters": {
          "type": "object",
          "properties": {
            "name": {
              "type": "string",
              "description": "Name to greet"
            }
          },
          "required": [
            "name"
          ]
        }
      }
    ],
    "themes": [
      "themes/demo.json"
    ],
    "mcp": {
      "servers": {
        "mcp-demo": {
          "command": "python3",
          "args": [
            "./scripts/mcp_server.py"
          ],
          "env": {
            "MCP_ENV_TEST": "active"
          }
        }
      }
    },
    "hooks": {
      "onSessionStart": [
        "scripts/session_start.sh"
      ],
      "onMessageSend": [
        "scripts/message_send.sh"
      ],
      "onToolCall": [
        "scripts/tool_call.sh"
      ],
      "onToolResult": [
        "scripts/tool_result.sh"
      ]
    }
  }
}
```

Key points on each surface:

- **Commands**: Demonstrates both command shapes. `/bare-cmd` is a bare string that dispatches as a regular user prompt to the agent (legacy dispatch). `/echo-cmd` carries a `handler` script (`scripts/command.sh`) executed directly by Late; stdout appears as a toast without model orchestration.
- **Skills**: Points to the `skills` directory. Late symlinks the skill into `~/.config/late/skills/example-plugin:demo` and discovers `skills/demo/SKILL.md`.
- **Inline Tools**: `greet` exposes a script-backed tool (`scripts/tool.sh`) directly to the LLM as `example-plugin__greet`.
- **Themes**: `themes/demo.json` provides Glamour styling overrides under the theme ID `example-plugin:demo`.
- **MCP Server**: `mcp-demo` launches a Python stdio MCP server exposing the `mcp_ping` tool as `example-plugin_mcp-demo__mcp_ping`.
- **Hooks**: Implements all four lifecycle hooks: `onSessionStart`, `onMessageSend`, `onToolCall`, and `onToolResult`.

---

## Skills — `skills/demo/SKILL.md`

Skills are exposed to the agent through the `activate_skill` tool. When the agent activates a skill, its instructions are returned to guide subsequent actions:

```markdown
---
name: demo
description: A demo skill provided by example-plugin
---

# Demo Skill Instructions

When the user asks for demo assistance, follow these guidelines:
1. Greet the user courteously.
2. Demonstrate calling the example-plugin tools.
```

---

## Slash Commands

### `/bare-cmd` — prompt dispatch (legacy)

Declared as a bare string in `late.commands`. When entered, Late submits `/bare-cmd ...` as a user message to the orchestrator, letting the agent use available tools and skills to respond.

### `/echo-cmd` — explicit script handler

Declared as `{ "name": "/echo-cmd", "handler": "scripts/command.sh" }`. When entered, Late executes the script and shows stdout as a toast notification.

#### `scripts/command.sh`

```bash
#!/bin/sh
# Command handler script for /echo-cmd
# Receives JSON array of arguments on stdin
sleep 2
args=$(cat)
echo "echo-cmd received arguments: $args"
```

---

## Inline Tool — `greet`

`late.tools[*]` exposes a script to the LLM as a tool without requiring an MCP server. The tool is namespaced as `example-plugin__greet`. The script receives arguments JSON on stdin and writes its output to stdout.

#### `scripts/tool.sh`

```bash
#!/bin/sh
# Inline tool script for "greet" tool
# Receives JSON arguments object on stdin
input=$(cat)
name=$(echo "$input" | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [ -z "$name" ]; then
  name="world"
fi
echo "Hello, $name! Greetings from example-plugin inline tool."
```

---

## MCP Server — `mcp-demo`

The plugin manifest starts `scripts/mcp_server.py` as an MCP server over stdio. The server is declared under the key `mcp-demo`, and its exposed `mcp_ping` tool is registered in Late as `example-plugin_mcp-demo__mcp_ping`.

#### `scripts/mcp_server.py`

```python
#!/usr/bin/env python3
"""
Minimal stdio MCP server for example-plugin.
Speaks Model Context Protocol (JSON-RPC 2.0 over newline-delimited stdio).
"""
import sys
import json

def send_response(response):
    sys.stdout.write(json.dumps(response) + "\n")
    sys.stdout.flush()

def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception:
            continue

        req_id = req.get("id")
        method = req.get("method")
        params = req.get("params", {})

        if req_id is None:
            continue

        if method == "initialize":
            send_response({
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {
                    "protocolVersion": "2024-11-05",
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "mcp-demo", "version": "1.0.0"}
                }
            })
        elif method == "ping":
            send_response({"jsonrpc": "2.0", "id": req_id, "result": {}})
        elif method == "tools/list":
            send_response({
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {
                    "tools": [{
                        "name": "mcp_ping",
                        "description": "MCP demo tool that replies to a ping",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "message": {"type": "string", "description": "Ping message"}
                            },
                            "required": ["message"]
                        }
                    }]
                }
            })
        elif method == "tools/call":
            args = params.get("arguments", {})
            msg = args.get("message", "hello")
            send_response({
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {
                    "content": [{
                        "type": "text",
                        "text": f"MCP pong from example-plugin: {msg}"
                    }]
                }
            })
        else:
            send_response({
                "jsonrpc": "2.0",
                "id": req_id,
                "error": {"code": -32601, "message": f"Method {method} not found"}
            })

if __name__ == "__main__":
    main()
```

---

## Themes — `themes/demo.json`

Themes customize markdown rendering in the TUI using Glamour style rules.

```json
{
  "name": "demo",
  "glamour": {
    "document": {
      "margin": 2
    },
    "heading": {
      "color": "#33ccff",
      "bold": true
    },
    "code_block": {
      "color": "#aaffaa"
    }
  }
}
```

Apply via the TUI command `/themes demo` or by namespaced ID `/themes example-plugin:demo`.

---

## Hooks

Late supports four plugin lifecycle hooks:

| Hook | Read from stdin | Write to stdout |
| --- | --- | --- |
| `onSessionStart` | `{}` (empty JSON object) | non-empty stdout is logged to stderr |
| `onMessageSend` | user message string | replacement string for user message |
| `onToolCall` | `{ "tool", "arguments", "timestamp", "requires_approval" }` JSON | valid JSON mutates arguments; literal `"blocked"` vetoes call; empty stdout passes through |
| `onToolResult` | `{ "tool", "result" }` JSON | valid JSON replaces result LLM sees; literal `"blocked"` vetoes result; empty stdout passes through |

### `scripts/session_start.sh` — `onSessionStart`

Fires once when Late starts. Non-empty stdout is logged to stderr.

```bash
#!/bin/sh
# onSessionStart hook script
# Receives empty JSON object {} on stdin
cat > /dev/null
echo "example-plugin session initialized"
```

### `scripts/message_send.sh` — `onMessageSend`

Intercepts and can transform outgoing user messages.

```bash
#!/bin/sh
# onMessageSend hook script
# Receives message text on stdin
# If non-empty stdout is produced, it replaces the message content.
msg=$(cat)
case "$msg" in
  *"[TEST_TRANSFORM]"*)
    echo "$msg (transformed by example-plugin)"
    ;;
  *)
    # Return message as-is
    echo "$msg"
    ;;
esac
```

### `scripts/tool_call.sh` — `onToolCall`

Runs before every tool invocation. Can mutate arguments or veto execution.

```bash
#!/bin/sh
# onToolCall hook script
# Receives JSON payload: {"tool": "...", "arguments": {...}, "timestamp": "...", "requires_approval": true/false}
# Return "blocked" to veto the tool call.
# Return valid JSON to mutate arguments.
# Return empty to pass through unchanged.
payload=$(cat)

# Extract tool name and requires_approval flag
if command -v jq >/dev/null 2>&1; then
  tool_name=$(echo "$payload" | jq -r '.tool // empty')
  requires_approval=$(echo "$payload" | jq -r '.requires_approval')
else
  tool_name=$(echo "$payload" | sed -n 's/.*"tool"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  case "$payload" in
    *"\"requires_approval\":true"*|*"\"requires_approval\": true"*)
      requires_approval=true
      ;;
    *)
      requires_approval=false
      ;;
  esac
fi

# Notify based on approval requirement (no-op demonstration; uncomment notify-send to enable desktop notifications)
if command -v notify-send >/dev/null 2>&1; then
  target_tool="${tool_name:-Tool}"
  if [ "$requires_approval" = "true" ]; then
    # notify-send "$target_tool requires approval" 2>/dev/null || true
    : "$target_tool requires approval"
  else
    # notify-send "Running $target_tool without approval" 2>/dev/null || true
    : "Running $target_tool without approval"
  fi
fi

case "$payload" in
  *"\"block_me\":true"*|*"\"block_me\": true"*)
    echo "blocked"
    ;;
  *)
    # Pass through unchanged
    ;;
esac
```

### `scripts/tool_result.sh` — `onToolResult`

Runs after successful tool execution. Can mutate the result seen by the LLM or veto it.

```bash
#!/bin/sh
# onToolResult hook script
# Receives JSON payload: {"tool": "...", "result": "..."}
# Return "blocked" to veto the result.
# Return valid JSON to replace the result.
# Return empty to pass through unchanged.
payload=$(cat)
# Extract tool name from payload (no-op demonstration; avoid echoing to stderr to prevent TUI leaks)
tool_name=$(echo "$payload" | sed -n 's/.*"tool"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
: "$tool_name"

case "$payload" in
  *block_result*)
    echo "blocked"
    ;;
  *)
    # Pass through unchanged
    ;;
esac
```

---

## Install & Test

### Global link (recommended for development)

From the root of the repository:

```bash
late plugin link ./example-plugin
late plugin list
```

You should see `example-plugin 1.0.0 local ✓` in the list.

### Project-local link

```bash
mkdir -p .late/plugins
late plugin link --project ./example-plugin
```

> **Note on linking:** `late plugin link` creates an absolute symlink pointing to your local source directory for iteration. For committed team configurations, copy the plugin folder into `.late/plugins/` or install via `late plugin install --project <source>`.

### Smoke tests in the TUI

1. `/echo-cmd hello world` — produces a toast displaying `echo-cmd received arguments: ["hello","world"]`.
2. `/themes` — open the theme picker, choose `example-plugin:demo`, and observe custom cyan headings.
3. In chat, ask *"Use the greet tool to say hello to Alice"* — the agent invokes `example-plugin__greet`.
4. In chat, ask *"Call mcp_ping with message test"* — the agent invokes `example-plugin_mcp-demo__mcp_ping`.
5. Send a chat message containing `[TEST_TRANSFORM]` — the `onMessageSend` hook appends `(transformed by example-plugin)`.
6. Ask the agent to call a tool with argument `"block_me": true` — `onToolCall` returns `blocked` and prevents execution.
7. To disable without uninstalling: `late plugin disable example-plugin`.
