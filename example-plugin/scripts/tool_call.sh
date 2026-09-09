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

