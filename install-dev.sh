#!/usr/bin/env bash
# install-dev.sh — replace the installed `late` command with a symlink into
# this repository's locally built binary, so every `make build` updates the
# command in place. Nothing is copied.
#
# Link target:
#   - with Homebrew present (and late not brew-managed): brew's bin dir
#   - without Homebrew: the first bin dir that is already on PATH and that we
#     can write to or create, in this order: ~/.local/bin, ~/bin,
#     /usr/local/bin. If none is on PATH, the script falls back to
#     ~/.local/bin and tells you to add it to your PATH.
#
# Usage:
#   brew uninstall late           # only when late is brew-managed
#   ./install-dev.sh              # optionally: LATE_DEV_VERSION=x ./install-dev.sh
#
# Go back to brew:
#   rm "$(brew --prefix)/bin/late" && brew install late
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$SCRIPT_DIR"
DEV_LINK="$REPO/bin/late"

# Refuse to fight an active brew install: replacing brew's symlink out from
# under it would leave brew's link tracking desynced.
if command -v brew >/dev/null 2>&1; then
    if brew list --formula 2>/dev/null | grep -qx 'late' || brew list --cask 2>/dev/null | grep -qx 'late'; then
        echo "Error: late is still installed via brew — run: brew uninstall late" >&2
        exit 1
    fi
    BIN_DIR="$(brew --prefix)/bin"
else
    # No Homebrew: pick the first bin dir that is already on PATH and that we
    # can write to (or create), so `late` can be invoked from anywhere.
    BIN_DIR=""
    for candidate in "$HOME/.local/bin" "$HOME/bin" /usr/local/bin; do
        if [[ ":$PATH:" != *":$candidate:"* ]]; then
            continue
        fi
        if [[ -d "$candidate" && -w "$candidate" ]]; then
            BIN_DIR="$candidate"
            break
        fi
        if [[ ! -e "$candidate" ]]; then
            mkdir -p "$candidate"
            BIN_DIR="$candidate"
            break
        fi
    done
    if [[ -z "$BIN_DIR" ]]; then
        # Nothing suitable on PATH: default to ~/.local/bin (XDG convention)
        # and remind the user to add it below.
        BIN_DIR="$HOME/.local/bin"
        mkdir -p "$BIN_DIR"
    fi
fi

echo "=> Building late from $REPO..."
if [[ -n "${LATE_DEV_VERSION:-}" ]]; then
    make -C "$REPO" build VERSION="$LATE_DEV_VERSION"
else
    make -C "$REPO" build
fi

# Never clobber a real binary: the destination must be a symlink (a previous
# dev install) or absent. Anything else gets removed by hand, deliberately.
if [[ -e "$BIN_DIR/late" && ! -L "$BIN_DIR/late" ]]; then
    echo "Error: $BIN_DIR/late already exists and is not a symlink." >&2
    echo "Remove or rename it manually, then re-run this script." >&2
    exit 1
fi

echo "=> Symlinking $BIN_DIR/late -> $DEV_LINK"
ln -sfn "$DEV_LINK" "$BIN_DIR/late"

# Friction checks: the link must be reachable and must not be shadowed by
# another `late` sitting earlier on PATH.
resolved="$(command -v late 2>/dev/null || true)"
if [[ -z "$resolved" ]]; then
    echo "" >&2
    echo "⚠️  'late' is not on your PATH — add this to your ~/.zshrc or ~/.bashrc:" >&2
    echo "    export PATH=\"\$PATH:$BIN_DIR\"" >&2
    exit 1
fi
if [[ "$resolved" != "$BIN_DIR/late" ]]; then
    echo "" >&2
    echo "⚠️  'late' currently resolves to: $resolved" >&2
    echo "    which shadows $BIN_DIR/late — adjust your PATH order or remove" >&2
    echo "    that binary, then re-run this script." >&2
    exit 1
fi

echo "=> Success! 'late' now resolves to:"
command -v late
late -version
