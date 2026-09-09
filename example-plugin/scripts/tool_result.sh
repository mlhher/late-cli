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

