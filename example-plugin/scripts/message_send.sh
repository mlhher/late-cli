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
