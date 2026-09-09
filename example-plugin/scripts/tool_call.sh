#!/bin/sh
# onToolCall hook script
# Receives JSON payload: {"tool": "...", "arguments": {...}, "timestamp": "..."}
# Return "blocked" to veto the tool call.
# Return valid JSON to mutate arguments.
# Return empty to pass through unchanged.
payload=$(cat)
# Extract tool name from payload (no-op demonstration; avoid echoing to stderr to prevent TUI leaks)
tool_name=$(echo "$payload" | sed -n 's/.*"tool"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
: "$tool_name"

case "$payload" in
  *"\"block_me\":true"*|*"\"block_me\": true"*)
    echo "blocked"
    ;;
  *)
    # Pass through unchanged
    ;;
esac

