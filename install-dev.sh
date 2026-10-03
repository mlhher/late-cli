#!/usr/bin/env bash
# install-dev.sh — smart multi-source installer for the `late` command.
#
# Sources (each idempotent — every run converges to the chosen state):
#   local-dev      build the current branch of this repo and make the
#                  `late` command a SYMLINK to <repo>/bin/late, so every
#                  `make build` updates the command in place.
#   pinned         build the current branch and install a fixed COPY
#                  (rebuilds no longer update the command until re-run).
#   fork-main      build origin/main (Emasoft/late-cli) from a codeload
#                  tarball and install a COPY.
#   upstream-main  build upstream/main (mlhher/late-cli) from a codeload
#                  tarball and install a COPY.
#   official       run upstream's own installer (latest stable release).
#                  Upstream owns placement — `--target` is not applicable.
#   check          print the detection report only (read-only).
#   uninstall      remove what this installer manages (name-invoked only;
#                  see UNINSTALL below — never offered in the menu).
#
# local-dev/pinned/fork-main/upstream-main also install the `late-podman`
# launcher into the target dir with the same parity as `late` (symlink for
# local-dev, copy otherwise — matching `make install`). late-podman's
# runtime requires a Linux host and podman; it refuses to run elsewhere.
#
# Every run prints a detection report first (platform, current install,
# package managers, podman, late-podman, remotes, brew, target) and a
# shadowing + version verification after each install. Displaced binaries
# are archived as <target>.bak-YYYYmmddHHMMSS (never overwritten) with a
# printed revert hint, so switching between dev/symlink and pinned/copy
# modes is safe.
#
# Usage:
#   ./install-dev.sh                     # interactive menu (needs a TTY)
#   ./install-dev.sh <source> [flags]    # non-interactive
#   ./install-dev.sh --choice N [flags]  # headless: menu entry N, implies --yes
#   ./install-dev.sh --dry-run pinned    # print the plan, mutate nothing
#   ./install-dev.sh uninstall [--purge] [--with-deps] [--yes] [--dry-run]
#
# Flags (any position): --yes  --dry-run  --target DIR  --choice N
#                       --purge  --with-deps  --help
#
# Headless mode (--choice N, 1..6 = the menu entries) implies --yes and
# never shows the menu or prompts; running as root is refused (set
# LATE_INSTALL_ALLOW_ROOT=1 to override). The 'official' source may still
# prompt — the upstream script owns it.
#
# Go back to a brew-managed `late` at any time:
#   brew uninstall late 2>/dev/null; brew install late
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$SCRIPT_DIR"
DEV_LINK="$REPO/bin/late"

FORK_REPO="Emasoft/late-cli"
UPSTREAM_REPO="mlhher/late-cli"
OFFICIAL_URL="https://raw.githubusercontent.com/${UPSTREAM_REPO}/main/install.sh"

ASSUME_YES=0
DRY_RUN=0
TARGET_OVERRIDE=""
SOURCE=""
CHOICE=""
PURGE=0
WITH_DEPS=0
LATE_TMP=""
BAK_HINTS=""
UNI_PLAN=""

# Detection state
GOOS=""
GOARCH=""
CUR_PATH=""
CUR_CLASS="not installed"
CUR_VERSION=""
REPO_BRANCH="unknown"
REPO_SHA="unknown"
REPO_DIRTY="clean"
FORK_SHA="unknown"
UPSTREAM_SHA="unknown"
RELEASE_TAG="unknown"
BREW_PRESENT="no"
BREW_LATE="no"
BREW_PREFIX=""
TARGET_DIR=""
TARGET_NOTE=""
TARGET_ON_PATH="no"

# Package managers / podman / late-podman detection state
PM_BREW="no"
PM_APT="no"
PM_DNF="no"
PM_PACMAN="no"
PM_ZYPPER="no"
PM_APK="no"
PM_NPM="no"
PM_LINE="none found"
PODMAN_PRESENT="no"
PODMAN_PATH=""
PODMAN_VERSION=""
LATE_PODMAN_REPORT="not installed"

err()  { echo "Error: $*" >&2; }
warn() { echo "⚠️  $*" >&2; }
info() { echo "=> $*"; }
die()  { err "$*"; exit 1; }

# usage_error — bad usage: print the error plus the full usage, exit 2.
usage_error() {
  err "$*"
  usage >&2
  exit 2
}

# arg_error — bad usage without the full usage block, exit 2.
arg_error() {
  err "$*"
  exit 2
}

# refuse_root — the installer is user-level; refuse accidental root runs
# (dev boxes and containers often run as root by default).
refuse_root() {
  if [ "$(id -u)" -eq 0 ] && [ "${LATE_INSTALL_ALLOW_ROOT:-0}" != "1" ]; then
    err "late installer is user-level; set LATE_INSTALL_ALLOW_ROOT=1 to override"
    exit 2
  fi
}

usage() {
  cat <<EOF
install-dev.sh — smart multi-source installer for the \`late\` command

Usage:
  ./install-dev.sh [SOURCE] [FLAGS]

Sources:
  local-dev      build current branch, install as SYMLINK to <repo>/bin/late
  pinned         build current branch, install a fixed COPY
  fork-main      build ${FORK_REPO}@main from a tarball, install a COPY
  upstream-main  build ${UPSTREAM_REPO}@main from a tarball, install a COPY
  official       run upstream's installer (latest stable release);
                 upstream owns placement — --target is not applicable
  check          print the detection report and exit (read-only)
  uninstall      remove what this installer manages (see UNINSTALL below)
  help           show this help

Flags (any position):
  --yes          skip confirmations
  --dry-run      print the plan, mutate nothing (the repo build still runs;
                 it writes only bin/late inside the repo)
  --target DIR   override the install directory (not applicable to 'official')
  --choice N     headless mode: select menu entry N (1 local-dev, 2 pinned,
                 3 fork-main, 4 upstream-main, 5 official, 6 check);
                 implies --yes, never shows the menu or prompts. 'official'
                 may still prompt — the upstream script owns it.
  --purge        (uninstall only) also delete late user data: the config dir
                 (config.json, mcp_config.json, plugins/ and skills/) and the
                 data dir (~/.local/share/late). Every path is printed first.
                 Interactive: asks y/N (default N); headless: requires --yes.
  --with-deps    (uninstall only) print the exact package-manager removal
                 commands for late-relevant dependencies (podman, go).
                 Commands are PRINTED, never executed — they may be shared
                 with other projects.
  --help         show this help

With no SOURCE and a TTY on stdin an interactive menu is shown; without a
TTY one choice is read from a single stdin line. --choice N skips the menu
entirely (headless mode).

Running as root is refused (set LATE_INSTALL_ALLOW_ROOT=1 to override).

Every install archives a displaced binary as <target>.bak-<timestamp>
(never overwritten) and prints a revert hint.

UNINSTALL
  ./install-dev.sh uninstall [--purge] [--with-deps] [--yes] [--dry-run] [--target DIR]

  Removes what this installer manages:
    - the installed \`late\` in the target dir (symlink or regular file);
      brew-managed files are NEVER deleted — 'brew uninstall late' is printed
      as advice instead
    - \`late-podman\` in the target dir / ~/.local/bin, but only our own copies
      (a symlink into this repo, or a copy matching this repo's launcher);
      brew-managed files are never deleted
    - all late.bak-<timestamp> / late-podman.bak-<timestamp> archives
    - a symlink into a DIFFERENT repo is removed, that repo itself is kept

  --purge additionally deletes user data (each path is printed first):
    - the config dir — on macOS: ~/Library/Application Support/late
      (on Linux: ~/.config/late, honoring XDG_CONFIG_HOME); contains
      config.json, mcp_config.json, plugins/ and skills/
    - the data dir — ~/.local/share/late (session history)
  Interactive runs confirm with y/N (default N); headless runs require --yes.

  --with-deps prints package-manager removal commands for podman and go
  (e.g. 'brew uninstall podman', 'sudo apt remove podman'). It NEVER runs
  them.

  Idempotent: uninstalling an already-uninstalled system prints
  "nothing to uninstall" and exits 0. 'uninstall' is name-invoked only —
  it is NOT part of the --choice menu (destructive).
EOF
}

# --- detection helpers -----------------------------------------------------

detect_goos() {
  case "$(uname -s)" in
    Darwin)                printf '%s\n' "darwin" ;;
    Linux)                 printf '%s\n' "linux" ;;
    MINGW*|MSYS*|CYGWIN*)  printf '%s\n' "windows" ;;
    *)                     printf '%s\n' "unknown" ;;
  esac
}

detect_goarch() {
  case "$(uname -m)" in
    arm64|aarch64)   printf '%s\n' "arm64" ;;
    x86_64|amd64)    printf '%s\n' "amd64" ;;
    *)               printf '%s\n' "unknown" ;;
  esac
}

# resolve_path PATH — follow a symlink chain (best effort, max 10 hops)
# and print the final path.
resolve_path() {
  local p="$1" t n=0 d
  while [ -L "$p" ] && [ "$n" -lt 10 ]; do
    t="$(readlink "$p")" || return 1
    case "$t" in
      /*) p="$t" ;;
      *)  p="$(dirname "$p")/$t" ;;
    esac
    n=$((n + 1))
  done
  case "$p" in
    *..*)
      if d="$(cd "$(dirname "$p")" 2>/dev/null && pwd -P)"; then
        p="${d}/$(basename "$p")"
      fi
      ;;
  esac
  printf '%s\n' "$p"
}

binary_sha() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  fi
}

# --- package managers / podman detection ------------------------------------

# detect_package_managers — report EVERY package manager found (brew,
# apt-get, dnf, pacman, zypper, apk, npm) as a comma list in PM_LINE.
detect_package_managers() {
  PM_BREW="no"
  PM_APT="no"
  PM_DNF="no"
  PM_PACMAN="no"
  PM_ZYPPER="no"
  PM_APK="no"
  PM_NPM="no"
  if command -v brew >/dev/null 2>&1;    then PM_BREW="yes";    fi
  if command -v apt-get >/dev/null 2>&1; then PM_APT="yes";     fi
  if command -v dnf >/dev/null 2>&1;     then PM_DNF="yes";     fi
  if command -v pacman >/dev/null 2>&1;  then PM_PACMAN="yes";  fi
  if command -v zypper >/dev/null 2>&1;  then PM_ZYPPER="yes";  fi
  if command -v apk >/dev/null 2>&1;     then PM_APK="yes";     fi
  if command -v npm >/dev/null 2>&1;     then PM_NPM="yes";     fi
  local out=""
  if [ "$PM_BREW" = "yes" ];   then out="${out:+${out}, }brew";    fi
  if [ "$PM_APT" = "yes" ];    then out="${out:+${out}, }apt-get"; fi
  if [ "$PM_DNF" = "yes" ];    then out="${out:+${out}, }dnf";     fi
  if [ "$PM_PACMAN" = "yes" ]; then out="${out:+${out}, }pacman";  fi
  if [ "$PM_ZYPPER" = "yes" ]; then out="${out:+${out}, }zypper";  fi
  if [ "$PM_APK" = "yes" ];    then out="${out:+${out}, }apk";     fi
  if [ "$PM_NPM" = "yes" ];    then out="${out:+${out}, }npm";     fi
  if [ -n "$out" ]; then
    PM_LINE="$out"
  else
    PM_LINE="none found"
  fi
}

# detect_podman — binary present? version?
detect_podman() {
  PODMAN_PRESENT="no"
  PODMAN_PATH=""
  PODMAN_VERSION=""
  PODMAN_PATH="$(command -v podman 2>/dev/null || true)"
  if [ -n "$PODMAN_PATH" ]; then
    PODMAN_PRESENT="yes"
    PODMAN_VERSION="$(podman --version 2>/dev/null | head -n 1 || true)"
  fi
}

# --- detection report ------------------------------------------------------

# pick_target_dir — choose TARGET_DIR (report only; never mutates).
pick_target_dir() {
  if [ -n "$TARGET_OVERRIDE" ]; then
    TARGET_DIR="$TARGET_OVERRIDE"
    TARGET_NOTE="(from --target)"
    if [ ! -d "$TARGET_DIR" ]; then
      TARGET_NOTE="(from --target; will be created)"
    fi
    return 0
  fi
  local candidate
  for candidate in /opt/homebrew/bin "$HOME/.local/bin" /usr/local/bin; do
    if [ "$candidate" = "/opt/homebrew/bin" ] && [ ! -d "$candidate" ]; then
      continue
    fi
    if [ -d "$candidate" ]; then
      if [ -w "$candidate" ]; then
        TARGET_DIR="$candidate"
        TARGET_NOTE=""
        return 0
      fi
      continue
    fi
    if [ "$candidate" = "$HOME/.local/bin" ]; then
      TARGET_DIR="$candidate"
      TARGET_NOTE="(will be created)"
      return 0
    fi
  done
  die "no usable target directory found (tried /opt/homebrew/bin, ~/.local/bin, /usr/local/bin) — use --target DIR"
}

# ensure_target_dir — create TARGET_DIR when an install really needs it.
ensure_target_dir() {
  if [ "$DRY_RUN" -eq 1 ]; then
    return 0
  fi
  if [ -d "$TARGET_DIR" ]; then
    return 0
  fi
  if ! mkdir -p "$TARGET_DIR"; then
    die "cannot create target directory ${TARGET_DIR}"
  fi
  info "Created target directory ${TARGET_DIR}"
}

run_detection() {
  GOOS="$(detect_goos)"
  GOARCH="$(detect_goarch)"

  REPO_BRANCH="$(git -C "$REPO" branch --show-current 2>/dev/null || true)"
  if [ -z "$REPO_BRANCH" ]; then REPO_BRANCH="unknown"; fi
  REPO_SHA="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || true)"
  if [ -z "$REPO_SHA" ]; then REPO_SHA="unknown"; fi
  if [ -n "$(git -C "$REPO" status --porcelain 2>/dev/null || true)" ]; then
    REPO_DIRTY="dirty"
  else
    REPO_DIRTY="clean"
  fi

  FORK_SHA="$(git -C "$REPO" ls-remote origin main 2>/dev/null \
    | awk 'NR==1 {print substr($1, 1, 7)}' || true)"
  if [ -z "$FORK_SHA" ]; then FORK_SHA="unknown"; fi
  UPSTREAM_SHA="$(git -C "$REPO" ls-remote upstream main 2>/dev/null \
    | awk 'NR==1 {print substr($1, 1, 7)}' || true)"
  if [ -z "$UPSTREAM_SHA" ]; then UPSTREAM_SHA="unknown"; fi

  if command -v curl >/dev/null 2>&1; then
    RELEASE_TAG="$(curl -sfL --max-time 10 \
      "https://api.github.com/repos/${UPSTREAM_REPO}/releases/latest" 2>/dev/null \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
      | head -n 1 || true)"
  fi
  if [ -z "$RELEASE_TAG" ]; then RELEASE_TAG="unknown"; fi

  if command -v brew >/dev/null 2>&1; then
    BREW_PRESENT="yes"
    BREW_PREFIX="$(brew --prefix 2>/dev/null || true)"
    if brew list --formula 2>/dev/null | grep -qx late; then
      BREW_LATE="yes"
    fi
  fi

  detect_package_managers
  detect_podman

  CUR_PATH="$(command -v late 2>/dev/null || true)"
  CUR_CLASS="not installed"
  CUR_VERSION=""
  if [ -n "$CUR_PATH" ]; then
    if [ -L "$CUR_PATH" ]; then
      local real
      real="$(resolve_path "$CUR_PATH" || true)"
      if [ -z "$real" ]; then real="?"; fi
      case "$real" in
        "$REPO"|"$REPO"/*) CUR_CLASS="symlink into THIS repo (branch ${REPO_BRANCH})" ;;
        *)                 CUR_CLASS="symlink elsewhere -> ${real}" ;;
      esac
    else
      if [ -n "$BREW_PREFIX" ]; then
        case "$CUR_PATH" in
          "${BREW_PREFIX}"/*) CUR_CLASS="regular file in brew prefix" ;;
          *)                  CUR_CLASS="regular file elsewhere" ;;
        esac
      else
        CUR_CLASS="regular file elsewhere"
      fi
    fi
    if [ -x "$CUR_PATH" ]; then
      CUR_VERSION="$("$CUR_PATH" -version 2>/dev/null | head -n 1 || true)"
    fi
  fi

  pick_target_dir
  detect_late_podman
  case ":$PATH:" in
    *":$TARGET_DIR:"*) TARGET_ON_PATH="yes" ;;
    *)                 TARGET_ON_PATH="no" ;;
  esac

  echo "== late installer — detection =="
  printf "Platform:          %s %s  ->  GOOS=%s GOARCH=%s\n" "$(uname -s)" "$(uname -m)" "$GOOS" "$GOARCH"
  if [ -n "$CUR_PATH" ]; then
    printf "Current install:   %s\n" "$CUR_PATH"
    printf "                   %s\n" "$CUR_CLASS"
    if [ -n "$CUR_VERSION" ]; then
      printf "                   version: %s\n" "$CUR_VERSION"
    fi
  else
    echo "Current install:   not installed"
  fi
  printf "Repo:              %s (branch %s, %s @ %s)\n" "$REPO" "$REPO_BRANCH" "$REPO_DIRTY" "$REPO_SHA"
  printf "Remotes:           fork %s@main = %s | upstream %s@main = %s\n" "$FORK_REPO" "$FORK_SHA" "$UPSTREAM_REPO" "$UPSTREAM_SHA"
  printf "Upstream release:  latest stable = %s\n" "$RELEASE_TAG"
  if [ "$BREW_PRESENT" = "yes" ]; then
    if [ "$BREW_LATE" = "yes" ]; then
      echo "Brew:              late IS brew-managed ('brew upgrade late' may overwrite this install)"
    else
      echo "Brew:              late not brew-managed (prefix ${BREW_PREFIX})"
    fi
  else
    echo "Brew:              not installed"
  fi
  printf "Package managers:  %s\n" "$PM_LINE"
  if [ "$PODMAN_PRESENT" = "yes" ]; then
    printf "Podman:            %s (%s)\n" "${PODMAN_VERSION:-present}" "$PODMAN_PATH"
  else
    echo "Podman:            not installed"
  fi
  printf "late-podman:       %s\n" "$LATE_PODMAN_REPORT"
  if [ -n "$TARGET_NOTE" ]; then
    printf "Target dir:        %s %s\n" "$TARGET_DIR" "$TARGET_NOTE"
  else
    printf "Target dir:        %s\n" "$TARGET_DIR"
  fi
  if [ "$TARGET_ON_PATH" = "yes" ]; then
    echo "PATH:              target dir is on PATH"
  else
    echo "PATH:              target dir is NOT on PATH (add it or 'late' will not be found)"
  fi
  echo "================================"
}

# --- build / install helpers ----------------------------------------------

require_go() {
  if ! command -v go >/dev/null 2>&1; then
    die "go is required for this source but was not found in PATH (the 'official' source needs no local build)"
  fi
}

build_repo() {
  if [ "$DRY_RUN" -eq 1 ]; then
    info "Building (dry-run allows the build — it writes only bin/late inside the repo)..."
  else
    info "Building late from ${REPO}..."
  fi
  if command -v make >/dev/null 2>&1 && [ -f "$REPO/Makefile" ]; then
    if [ -n "${LATE_DEV_VERSION:-}" ]; then
      if ! make -C "$REPO" build VERSION="$LATE_DEV_VERSION"; then
        die "make build failed"
      fi
    else
      if ! make -C "$REPO" build; then
        die "make build failed"
      fi
    fi
  else
    if ! (cd "$REPO" && go build -o bin/late ./cmd/late); then
      die "go build failed"
    fi
  fi
  if [ ! -f "$DEV_LINK" ]; then
    die "build did not produce ${DEV_LINK}"
  fi
}

backup_path() {
  local p="$1" ts cand n=2
  ts="$(date +%Y%m%d%H%M%S)"
  cand="${p}.bak-${ts}"
  while [ -e "$cand" ] || [ -L "$cand" ]; do
    cand="${p}.bak-${ts}-${n}"
    n=$((n + 1))
  done
  printf '%s\n' "$cand"
}

# archive_displaced PATH — move an existing file/symlink aside to a
# timestamped .bak (never overwrites) and print a revert hint.
archive_displaced() {
  local p="$1" bak
  bak="$(backup_path "$p")"
  mv "$p" "$bak"
  warn "Archived ${p} -> ${bak}"
  echo "   Revert hint: mv \"${bak}\" \"${p}\""
  BAK_HINTS="${BAK_HINTS}${BAK_HINTS:+; }${bak}"
}

install_symlink() {
  local dest="$TARGET_DIR/late"
  if [ -d "$dest" ] && [ ! -L "$dest" ]; then
    die "refusing to replace directory ${dest}"
  fi
  if [ -e "$dest" ] && [ ! -L "$dest" ]; then
    archive_displaced "$dest"
    info "The command tracks this repo's rebuilds again (dev mode)."
  fi
  ln -sfn "$DEV_LINK" "$dest"
  info "Installed symlink ${dest} -> ${DEV_LINK}"
}

install_copy() {
  local src="$1" note="$2" dest="$TARGET_DIR/late"
  if [ -d "$dest" ] && [ ! -L "$dest" ]; then
    die "refusing to replace directory ${dest}"
  fi
  if [ -L "$dest" ]; then
    archive_displaced "$dest"
    warn "$note"
  fi
  cp -f "$src" "$dest"
  chmod 0755 "$dest"
  info "Installed copy ${dest}"
}

# install_podman_helper — install the `late-podman` launcher into the target
# dir with the same parity rules as `late`: symlink for local-dev, copy for
# the pinned/tarball sources (mirrors `make install`, which ships both).
# Same .bak-<timestamp> archiving on mode transitions. A missing source
# (e.g. an old tarball without the launcher) is skipped with a warning.
install_podman_helper() {
  local mode="$1" src="$2" dest="$TARGET_DIR/late-podman"
  if [ ! -f "$src" ]; then
    warn "no late-podman launcher at ${src} — skipping the late-podman install"
    return 0
  fi
  if [ -d "$dest" ] && [ ! -L "$dest" ]; then
    die "refusing to replace directory ${dest}"
  fi
  if [ "$mode" = "symlink" ]; then
    if [ -e "$dest" ] && [ ! -L "$dest" ]; then
      archive_displaced "$dest"
    fi
    ln -sfn "$src" "$dest"
    info "Installed symlink ${dest} -> ${src}"
  else
    if [ -L "$dest" ]; then
      archive_displaced "$dest"
    fi
    cp -f "$src" "$dest"
    chmod 0755 "$dest"
    info "Installed copy ${dest}"
  fi
}

# podman_runtime_note — late-podman's runtime requirements, printed with
# every late-podman install.
podman_runtime_note() {
  info "Note: late-podman runs Late in a rootless Podman container — it requires a Linux host and podman."
  if [ "$GOOS" != "linux" ]; then
    warn "This host is $(uname -s): late-podman refuses to run here; it is meant for Linux hosts (or Linux containers)."
  fi
  if [ "$PODMAN_PRESENT" != "yes" ]; then
    warn "podman was not detected in PATH — late-podman needs it on the Linux host it runs on."
  fi
}

# target_state_desc — human description of what TARGET_DIR/late currently is.
target_state_desc() {
  local dest="$TARGET_DIR/late" real
  if [ -L "$dest" ]; then
    real="$(resolve_path "$dest" 2>/dev/null || true)"
    if [ -z "$real" ]; then real="?"; fi
    case "$real" in
      "$REPO"|"$REPO"/*) printf '%s\n' "symlink into this repo" ;;
      *)                 printf '%s\n' "symlink -> ${real}" ;;
    esac
  elif [ -d "$dest" ]; then
    printf '%s\n' "a directory (refusing to touch)"
  elif [ -e "$dest" ]; then
    if [ -n "$BREW_PREFIX" ]; then
      case "$dest" in
        "${BREW_PREFIX}"/*) printf '%s\n' "a regular file (in brew prefix)" ;;
        *)                  printf '%s\n' "a regular file" ;;
      esac
    else
      printf '%s\n' "a regular file"
    fi
  else
    printf '%s\n' "absent"
  fi
}

plan_header() {
  echo ""
  echo "PLAN (dry-run — nothing will be changed) — source: $1"
}

plan_target_line() {
  echo "  - target ${TARGET_DIR}/late: currently $(target_state_desc)"
}

plan_backup_line() {
  local mode="$1" dest="$TARGET_DIR/late" real
  if [ -f "$dest" ] && [ ! -L "$dest" ]; then
    echo "  - would archive it to $(backup_path "$dest") and print a revert hint"
    return 0
  fi
  if [ -L "$dest" ]; then
    real="$(resolve_path "$dest" 2>/dev/null || true)"
    if [ -z "$real" ]; then real="?"; fi
    case "$real" in
      "$REPO"|"$REPO"/*)
        if [ "$mode" = "copy" ]; then
          echo "  - would archive the dev symlink to $(backup_path "$dest") and print a revert hint"
        else
          echo "  - already a symlink into this repo — re-pointing is a no-op"
        fi
        ;;
      *)
        if [ "$mode" = "copy" ]; then
          echo "  - would archive the symlink (-> ${real}) to $(backup_path "$dest")"
        else
          echo "  - would replace the symlink (currently -> ${real})"
        fi
        ;;
    esac
  fi
}

plan_brew_line() {
  if [ "$BREW_LATE" = "yes" ]; then
    echo "  - WARNING: late is brew-managed; a future 'brew upgrade late' may overwrite this install"
  fi
}

plan_podman_line() {
  local mode="$1" dest="$TARGET_DIR/late-podman"
  if [ "$mode" = "symlink" ]; then
    echo "  - would also install late-podman as a SYMLINK -> ${REPO}/late-podman at ${dest}"
  else
    echo "  - would also install late-podman as a COPY at ${dest}"
  fi
  echo "  - late-podman's runtime requires a Linux host + podman (it refuses to run on $(uname -s))"
}

# guard_brew — warn and gate installs when late is brew-managed.
guard_brew() {
  local answer
  if [ "$BREW_LATE" != "yes" ]; then
    return 0
  fi
  echo "" >&2
  warn "late is currently installed via brew — a future 'brew upgrade late' may overwrite this install." >&2
  if [ "$ASSUME_YES" -eq 1 ]; then
    return 0
  fi
  if [ -t 0 ]; then
    printf "   Proceed anyway? [y/N] " >&2
    IFS= read -r answer || answer=""
    case "$answer" in
      y|Y|yes|Yes|YES) return 0 ;;
      *) die "aborted by user (use --yes to skip this prompt)" ;;
    esac
  else
    die "late is brew-managed — re-run with --yes to proceed anyway, or 'brew uninstall late' first"
  fi
}

post_verify() {
  local mode="$1" dest="$TARGET_DIR/late" resolved v
  echo ""
  echo "== verification =="
  if [ "$mode" = "official" ]; then
    resolved="$(command -v late 2>/dev/null || true)"
    if [ -z "$resolved" ]; then
      warn "'late' is not on your PATH after the official installer — check the installer output above."
      return 0
    fi
    echo "late resolves to: ${resolved}"
    v="$(late -version 2>/dev/null | head -n 1 || true)"
    if [ -n "$v" ]; then
      echo "version: ${v}"
    fi
    return 0
  fi
  if [ "$mode" = "symlink" ]; then
    if [ ! -L "$dest" ]; then
      err "verification failed: expected a symlink at ${dest}"
      return 1
    fi
    echo "symlink: ${dest} -> $(readlink "$dest")"
  else
    if [ ! -f "$dest" ] || [ -L "$dest" ]; then
      err "verification failed: expected a regular file at ${dest}"
      return 1
    fi
    echo "copy: ${dest} (sha256 $(binary_sha "$dest" || true))"
  fi
  if [ -x "$dest" ]; then
    v="$("$dest" -version 2>/dev/null | head -n 1 || true)"
    if [ -n "$v" ]; then
      echo "version: ${v}"
    fi
  fi
  if [ -L "$TARGET_DIR/late-podman" ]; then
    echo "late-podman: ${TARGET_DIR}/late-podman -> $(readlink "$TARGET_DIR/late-podman")"
  elif [ -e "$TARGET_DIR/late-podman" ]; then
    echo "late-podman: copy at ${TARGET_DIR}/late-podman"
  else
    warn "late-podman was not installed (launcher missing in the source?)"
  fi
  resolved="$(command -v late 2>/dev/null || true)"
  if [ -z "$resolved" ]; then
    warn "'late' is not on your PATH — add this to your ~/.zshrc or ~/.bashrc:"
    echo "    export PATH=\"\$PATH:${TARGET_DIR}\""
  elif [ "$resolved" != "$dest" ]; then
    warn "'late' currently resolves to: ${resolved}"
    warn "it shadows ${dest} — adjust your PATH order or remove that binary."
  else
    echo "late resolves to: ${resolved} (no shadowing)"
  fi
}

# --- install options -------------------------------------------------------

opt_local_dev() {
  require_go
  echo ""
  info "Source: local-dev — build ${REPO_BRANCH} @ ${REPO_SHA}, install as symlink"
  build_repo
  if [ "$DRY_RUN" -eq 1 ]; then
    plan_header "local-dev"
    plan_target_line
    echo "  - would replace it with a SYMLINK -> ${DEV_LINK}"
    plan_backup_line "symlink"
    plan_podman_line "symlink"
    plan_brew_line
    return 0
  fi
  guard_brew
  install_symlink
  install_podman_helper "symlink" "$REPO/late-podman"
  podman_runtime_note
  post_verify "symlink"
}

opt_pinned() {
  require_go
  echo ""
  info "Source: pinned — build ${REPO_BRANCH} @ ${REPO_SHA}, install as fixed copy"
  build_repo
  if [ "$DRY_RUN" -eq 1 ]; then
    plan_header "pinned"
    plan_target_line
    echo "  - would replace it with a COPY of ${DEV_LINK}"
    plan_backup_line "copy"
    plan_podman_line "copy"
    plan_brew_line
    return 0
  fi
  guard_brew
  install_copy "$DEV_LINK" \
    "Dev tracking stops: rebuilds of this repo no longer update the command; re-run the installer to update."
  install_podman_helper "copy" "$REPO/late-podman"
  podman_runtime_note
  post_verify "copy"
}

opt_tarball() {
  local slug="$1" label="$2" note="$3" url root d
  require_go
  echo ""
  info "Source: ${label} — build ${slug}@main, install as copy"
  if [ "$DRY_RUN" -eq 1 ]; then
    plan_header "$label"
    echo "  - would download https://codeload.github.com/${slug}/tar.gz/refs/heads/main to a temp dir"
    echo "  - would extract it and build ./cmd/late with go (GOOS=${GOOS} GOARCH=${GOARCH})"
    plan_target_line
    echo "  - would install the built binary as a COPY to ${TARGET_DIR}/late"
    plan_backup_line "copy"
    plan_podman_line "copy"
    plan_brew_line
    return 0
  fi
  guard_brew
  url="https://codeload.github.com/${slug}/tar.gz/refs/heads/main"
  LATE_TMP="$(mktemp -d "${TMPDIR:-/tmp}/late-install.XXXXXX")"
  info "Downloading ${url}"
  if ! curl -sfL "$url" -o "${LATE_TMP}/src.tar.gz"; then
    die "download failed: ${url}"
  fi
  if ! tar -xzf "${LATE_TMP}/src.tar.gz" -C "$LATE_TMP"; then
    die "failed to extract the ${slug} tarball"
  fi
  root=""
  if [ -d "${LATE_TMP}/cmd/late" ]; then
    root="$LATE_TMP"
  else
    for d in "$LATE_TMP"/*/; do
      if [ -d "${d}cmd/late" ]; then
        root="${d%/}"
        break
      fi
    done
  fi
  if [ -z "$root" ]; then
    die "tarball from ${slug} contains no cmd/late — refusing to install"
  fi
  info "Building ${slug}@main in $(basename "$root") (GOOS=${GOOS} GOARCH=${GOARCH})"
  if ! (
    cd "$root" || exit 1
    # Tarballs carry no .git (no VCS stamping needed). If go.sum is absent
    # (it is normally tracked), let go resolve checksums into the temp copy.
    if [ -f go.mod ] && [ ! -f go.sum ]; then
      export GOFLAGS="-mod=mod"
    fi
    # Pass the platform to go via env-prefix (never mutating the parent
    # script's GOOS/GOARCH, which stay the detected host values).
    # -trimpath strips the random mktemp dir from the binary so two builds
    # of the same commit are byte-identical (idempotent reinstalls).
    if [ "$GOOS" != "unknown" ] && [ "$GOARCH" != "unknown" ]; then
      env GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -o "${LATE_TMP}/late" ./cmd/late
    else
      go build -trimpath -o "${LATE_TMP}/late" ./cmd/late
    fi
  ); then
    die "build of ${slug}@main failed — refusing to install"
  fi
  install_copy "${LATE_TMP}/late" "$note"
  install_podman_helper "copy" "${root}/late-podman"
  podman_runtime_note
  post_verify "copy"
}

opt_official() {
  echo ""
  info "Source: official — upstream installer (${OFFICIAL_URL})"
  if [ -n "$TARGET_OVERRIDE" ]; then
    die "--target is not applicable to 'official' (upstream owns placement)"
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    plan_header "official"
    echo "  - would fetch and execute: ${OFFICIAL_URL}"
    echo "  - upstream owns placement and conflict logic; --target is not applicable"
    plan_brew_line
    return 0
  fi
  info "Running upstream installer (latest stable ${RELEASE_TAG})..."
  if ! curl -sfL "$OFFICIAL_URL" | bash -s --; then
    die "official installer failed"
  fi
  post_verify "official"
}

# --- uninstall ---------------------------------------------------------------
# Name-invoked only (never in the --choice menu — destructive). Removes what
# THIS installer manages: the target-dir `late`, our own `late-podman` copies
# (target dir / ~/.local/bin), and all .bak-<timestamp> archives. Never
# deletes anything inside the brew prefix (prints 'brew uninstall late' as
# advice instead) and never touches package-manager-installed dependencies.

# late_config_dir — the exact dir the Go code uses (pathutil.LateConfigDir =
# os.UserConfigDir()/late). Go ignores XDG_CONFIG_HOME on darwin and honors
# it on Linux — replicate that faithfully here.
late_config_dir() {
  case "$GOOS" in
    darwin) printf '%s\n' "${HOME}/Library/Application Support/late" ;;
    *)      printf '%s\n' "${XDG_CONFIG_HOME:-${HOME}/.config}/late" ;;
  esac
}

# late_data_dir — parent of pathutil.LateSessionDir (~/.local/share/late).
late_data_dir() {
  printf '%s\n' "${HOME}/.local/share/late"
}

# uni_purge_guard — fool-proofing for rm -rf on user-data dirs: the basename
# must be exactly "late" and the path must never be $HOME or /.
uni_purge_guard() {
  local base
  case "$1" in
    ""|"/"|"$HOME"|"$HOME"/) die "refusing suspicious purge path: ${1}" ;;
  esac
  base="$(basename "$1")"
  case "$base" in
    late) return 0 ;;
    *)    die "refusing suspicious purge path (unexpected basename): ${1}" ;;
  esac
}

# uni_confirm DESC — y/N confirm (default N) for destructive uninstall steps.
# --yes short-circuits to yes; a non-TTY stdin yields "no" (the caller turns
# that into a clear refusal instead of deleting anything silently).
uni_confirm() {
  local answer
  if [ "$ASSUME_YES" -eq 1 ]; then
    return 0
  fi
  if [ -t 0 ]; then
    printf "Proceed with %s? [y/N] " "$1"
    IFS= read -r answer || answer=""
    case "$answer" in
      y|Y|yes|Yes|YES) return 0 ;;
      *)               return 1 ;;
    esac
  fi
  return 1
}

# uni_late_kind — classify TARGET_DIR/late (same taxonomy as the detection
# report). Prints one of: absent | directory | regular-brew | regular |
# symlink-this-repo | symlink-other | symlink-broken. Sets UNI_LATE_REAL.
UNI_LATE_REAL=""
uni_late_kind() {
  UNI_LATE_REAL=""
  local dest="$TARGET_DIR/late" real
  if [ -L "$dest" ]; then
    real="$(resolve_path "$dest" 2>/dev/null || true)"
    UNI_LATE_REAL="$real"
    if [ -z "$real" ]; then
      printf '%s\n' "symlink-broken"
      return 0
    fi
    case "$real" in
      "$REPO"|"$REPO"/*) printf '%s\n' "symlink-this-repo" ;;
      *)                 printf '%s\n' "symlink-other" ;;
    esac
    return 0
  fi
  if [ -d "$dest" ]; then printf '%s\n' "directory"; return 0; fi
  if [ -e "$dest" ]; then
    if [ -n "$BREW_PREFIX" ]; then
      case "$dest" in
        "${BREW_PREFIX}"/*) printf '%s\n' "regular-brew"; return 0 ;;
      esac
    fi
    printf '%s\n' "regular"
    return 0
  fi
  printf '%s\n' "absent"
}

# uni_podman_kind PATH — classify a late-podman candidate. Prints one of:
# absent | directory | brew | ours-symlink | foreign-symlink | ours-copy |
# foreign. "Ours" = a symlink into this repo, or a regular file whose bytes
# match this repo's launcher (or an older launcher revision, recognized by
# its usage header).
uni_podman_kind() {
  local p="$1" real a b
  if [ -L "$p" ]; then
    real="$(resolve_path "$p" 2>/dev/null || true)"
    case "$real" in
      "$REPO"/late-podman) printf '%s\n' "ours-symlink" ;;
      *)                   printf '%s\n' "foreign-symlink" ;;
    esac
    return 0
  fi
  if [ -d "$p" ]; then printf '%s\n' "directory"; return 0; fi
  if [ -e "$p" ]; then
    if [ -n "$BREW_PREFIX" ]; then
      case "$p" in
        "${BREW_PREFIX}"/*) printf '%s\n' "brew"; return 0 ;;
      esac
    fi
    a="$(binary_sha "$p" 2>/dev/null || true)"
    b="$(binary_sha "$REPO/late-podman" 2>/dev/null || true)"
    if [ -n "$a" ] && [ -n "$b" ] && [ "$a" = "$b" ]; then
      printf '%s\n' "ours-copy"
      return 0
    fi
    if grep -q 'Usage: late-podman' "$p" 2>/dev/null; then
      printf '%s\n' "ours-copy"
      return 0
    fi
    printf '%s\n' "foreign"
    return 0
  fi
  printf '%s\n' "absent"
}

# describe_late_podman PATH — one-line description for the detection report.
describe_late_podman() {
  local p="$1" kind real
  kind="$(uni_podman_kind "$p")"
  case "$kind" in
    ours-symlink)
      real="$(resolve_path "$p" 2>/dev/null || true)"
      printf '%s\n' "${p} (symlink -> ${real:-?}, our symlink)"
      ;;
    foreign-symlink)
      real="$(resolve_path "$p" 2>/dev/null || true)"
      printf '%s\n' "${p} (symlink -> ${real:-?})"
      ;;
    directory)
      printf '%s\n' "${p} (a directory — unexpected)"
      ;;
    brew)
      printf '%s\n' "${p} (regular file, brew-managed)"
      ;;
    ours-copy)
      printf '%s\n' "${p} (regular file, our copy)"
      ;;
    foreign)
      printf '%s\n' "${p} (regular file)"
      ;;
  esac
}

# detect_late_podman — is late-podman installed? Check the target dir and
# ~/.local/bin (used by the detection report).
detect_late_podman() {
  LATE_PODMAN_REPORT="not installed"
  local report="" p desc
  local p1="$TARGET_DIR/late-podman" p2="$HOME/.local/bin/late-podman"
  local paths=("$p1")
  if [ "$p2" != "$p1" ]; then paths+=("$p2"); fi
  for p in "${paths[@]}"; do
    if [ ! -L "$p" ] && [ ! -e "$p" ]; then continue; fi
    desc="$(describe_late_podman "$p")"
    if [ -z "$report" ]; then
      report="$desc"
    else
      report="${report}"$'\n'"                   ${desc}"
    fi
  done
  if [ -n "$report" ]; then
    LATE_PODMAN_REPORT="$report"
  fi
}

# pm_owns PM PKG — exit 0 when the package manager owns the package.
# (Read-only queries only; nothing is ever removed here.)
pm_owns() {
  local pm="$1" pkg="$2"
  case "$pm" in
    brew)
      if brew list --formula 2>/dev/null | grep -qx "$pkg"; then return 0; fi
      ;;
    apt-get)
      if command -v dpkg-query >/dev/null 2>&1; then
        if dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q "install ok installed"; then return 0; fi
      fi
      ;;
    dnf|zypper)
      if command -v rpm >/dev/null 2>&1; then
        if rpm -q "$pkg" >/dev/null 2>&1; then return 0; fi
      fi
      ;;
    pacman)
      if pacman -Q "$pkg" >/dev/null 2>&1; then return 0; fi
      ;;
    apk)
      if apk info -e "$pkg" >/dev/null 2>&1; then return 0; fi
      ;;
  esac
  return 1
}

# uni_dep_hint NAME — print the exact removal command per detected package
# manager that owns NAME (npm is skipped: neither podman nor go are npm
# packages). Prints a fallback note when no detected PM owns it.
uni_dep_hint() {
  local name="$1" found=0 pm pkg
  for pm in brew apt-get dnf pacman zypper apk; do
    case "$pm" in
      brew)    if [ "$PM_BREW" != "yes" ];    then continue; fi ;;
      apt-get) if [ "$PM_APT" != "yes" ];     then continue; fi ;;
      dnf)     if [ "$PM_DNF" != "yes" ];     then continue; fi ;;
      pacman)  if [ "$PM_PACMAN" != "yes" ];  then continue; fi ;;
      zypper)  if [ "$PM_ZYPPER" != "yes" ];  then continue; fi ;;
      apk)     if [ "$PM_APK" != "yes" ];     then continue; fi ;;
    esac
    case "$pm" in
      brew)    pkg="$name" ;;
      apt-get) case "$name" in go) pkg="golang-go" ;; *) pkg="$name" ;; esac ;;
      dnf)     case "$name" in go) pkg="golang" ;;    *) pkg="$name" ;; esac ;;
      *)       pkg="$name" ;;
    esac
    if pm_owns "$pm" "$pkg"; then
      case "$pm" in
        brew)    echo "    brew-managed:     brew uninstall ${name}" ;;
        apt-get) echo "    apt-get-managed: sudo apt remove ${pkg}" ;;
        dnf)     echo "    dnf-managed:     sudo dnf remove ${pkg}" ;;
        pacman)  echo "    pacman-managed:  sudo pacman -Rns ${pkg}" ;;
        zypper)  echo "    zypper-managed:  sudo zypper remove ${pkg}" ;;
        apk)     echo "    apk-managed:     sudo apk del ${pkg}" ;;
      esac
      found=1
    fi
  done
  if [ "$found" -eq 0 ]; then
    echo "    managed outside the detected package managers — remove manually if desired"
  fi
}

# uni_dep_hints — --with-deps: PRINT (never execute) removal commands for
# late-relevant dependencies (podman, go). They may be shared with other
# projects, so the script never runs them.
uni_dep_hints() {
  local go_path
  echo ""
  echo "== dependencies (NOT removed — commands printed only; they may be shared with other projects) =="
  if [ "$PODMAN_PRESENT" = "yes" ]; then
    echo "  podman: ${PODMAN_PATH} (${PODMAN_VERSION:-version unknown})"
    uni_dep_hint "podman"
  else
    echo "  podman: not installed — nothing to remove"
  fi
  go_path="$(command -v go 2>/dev/null || true)"
  if [ -n "$go_path" ]; then
    echo "  go: ${go_path}"
    uni_dep_hint "go"
  else
    echo "  go: not installed — nothing to remove"
  fi
  echo "== end of dependency report =="
}

# uni_print_plan VERB — print the tab-separated plan entries.
uni_print_plan() {
  local verb="$1" kind path detail
  while IFS=$'\t' read -r kind path detail; do
    if [ -z "$kind" ]; then continue; fi
    case "$kind" in
      rm)    echo "  - ${verb} remove ${path} (${detail})" ;;
      rmdir) echo "  - ${verb} delete ${path} (${detail})" ;;
      brew)  echo "  - KEEP ${path} — ${detail}" ;;
      note)  echo "  - ${path}: ${detail}" ;;
    esac
  done <<PLAN_EOF
${UNI_PLAN}
PLAN_EOF
}

# opt_uninstall — the uninstall flow.
opt_uninstall() {
  local plan="" dest="$TARGET_DIR/late" p kind removed=0
  local p1="$TARGET_DIR/late-podman" p2="$HOME/.local/bin/late-podman"
  local podman_paths=("$p1")
  if [ "$p2" != "$p1" ]; then podman_paths+=("$p2"); fi

  echo ""
  info "Uninstall — classifying the current install"

  # (a) the installed `late` in the target dir
  kind="$(uni_late_kind)"
  case "$kind" in
    absent)            : ;;
    directory)         plan="${plan}note"$'\t'"${dest}"$'\t'"is a directory — refusing to touch it"$'\n' ;;
    regular-brew)      plan="${plan}brew"$'\t'"${dest}"$'\t'"brew-managed — run: brew uninstall late"$'\n' ;;
    regular)           plan="${plan}rm"$'\t'"${dest}"$'\t'"remove installed late (regular file)"$'\n' ;;
    symlink-this-repo) plan="${plan}rm"$'\t'"${dest}"$'\t'"remove symlink (points into this repo; ${REPO}/bin/late is kept)"$'\n' ;;
    symlink-other)     plan="${plan}rm"$'\t'"${dest}"$'\t'"remove symlink (points to ${UNI_LATE_REAL} — that target is KEPT)"$'\n' ;;
    symlink-broken)    plan="${plan}rm"$'\t'"${dest}"$'\t'"remove broken symlink"$'\n' ;;
  esac

  # (b) late-podman in the target dir / ~/.local/bin (our copies only)
  for p in "${podman_paths[@]}"; do
    kind="$(uni_podman_kind "$p")"
    case "$kind" in
      absent)          : ;;
      directory)       plan="${plan}note"$'\t'"${p}"$'\t'"is a directory — refusing to touch it"$'\n' ;;
      brew)            plan="${plan}brew"$'\t'"${p}"$'\t'"brew-managed (shipped by the late formula) — run: brew uninstall late"$'\n' ;;
      ours-symlink)    plan="${plan}rm"$'\t'"${p}"$'\t'"remove our late-podman symlink"$'\n' ;;
      ours-copy)       plan="${plan}rm"$'\t'"${p}"$'\t'"remove our late-podman copy"$'\n' ;;
      foreign-symlink) plan="${plan}note"$'\t'"${p}"$'\t'"symlink does not point into this repo — left untouched"$'\n' ;;
      foreign)         plan="${plan}note"$'\t'"${p}"$'\t'"not recognized as our copy — left untouched"$'\n' ;;
    esac
  done

  # (c) all .bak archives of this installer in the target dir
  for p in "$TARGET_DIR"/late.bak-* "$TARGET_DIR"/late-podman.bak-*; do
    if [ ! -e "$p" ] && [ ! -L "$p" ]; then continue; fi
    plan="${plan}rm"$'\t'"${p}"$'\t'"remove archived backup"$'\n'
  done

  # (d) --purge: user-data dirs (each path printed; guarded basenames)
  if [ "$PURGE" -eq 1 ]; then
    case "$GOOS" in
      darwin|linux)
        local cdir ddir
        cdir="$(late_config_dir)"
        ddir="$(late_data_dir)"
        echo ""
        echo "PURGE plan (user data):"
        echo "  - ${cdir}          (late config dir: config.json, mcp_config.json, plugins/)"
        echo "  - ${cdir}/skills   (agent skills — inside the config dir, removed with it)"
        echo "  - ${ddir}          (late data dir: session history)"
        if [ -d "$cdir" ] || [ -L "$cdir" ]; then
          uni_purge_guard "$cdir"
          plan="${plan}rmdir"$'\t'"${cdir}"$'\t'"delete late config dir (config.json, mcp_config.json, plugins/, skills/)"$'\n'
        fi
        if [ -d "$ddir" ] || [ -L "$ddir" ]; then
          uni_purge_guard "$ddir"
          plan="${plan}rmdir"$'\t'"${ddir}"$'\t'"delete late data dir (sessions)"$'\n'
        fi
        ;;
      *)
        plan="${plan}note"$'\t'"user-data purge"$'\t'"unsupported platform for purge-path detection — skipped"$'\n'
        ;;
    esac
  fi

  UNI_PLAN="$plan"
  if [ -z "$plan" ]; then
    if [ "$WITH_DEPS" -eq 1 ]; then
      uni_dep_hints
    fi
    echo "nothing to uninstall"
    return 0
  fi

  echo ""
  echo "Uninstall plan:"
  uni_print_plan "would"

  if [ "$WITH_DEPS" -eq 1 ]; then
    uni_dep_hints
  fi

  if [ "$DRY_RUN" -eq 1 ]; then
    echo ""
    echo "PLAN (dry-run — nothing was changed)"
    return 0
  fi

  if ! uni_confirm "removing late from ${TARGET_DIR} (binaries + backups)"; then
    if [ -t 0 ]; then
      die "aborted by user — nothing was removed"
    fi
    die "uninstall is destructive — re-run with --yes for non-interactive use"
  fi
  if [ "$PURGE" -eq 1 ]; then
    if ! uni_confirm "PURGING late user data (config dir, skills, session history)"; then
      if [ -t 0 ]; then
        die "aborted by user — user data was NOT deleted (use --yes to purge)"
      fi
      die "--purge deletes user data — headless mode requires --yes"
    fi
  fi

  local kind2 path2 detail2
  while IFS=$'\t' read -r kind2 path2 detail2; do
    if [ -z "$kind2" ]; then continue; fi
    case "$kind2" in
      rm)
        rm -f -- "$path2"
        echo "removed ${path2}"
        removed=$((removed + 1))
        ;;
      rmdir)
        rm -rf -- "$path2"
        echo "deleted ${path2}"
        removed=$((removed + 1))
        ;;
      brew) echo "kept ${path2} — ${detail2}" ;;
      note) echo "${path2}: ${detail2}" ;;
    esac
  done <<UNI_PLAN_EOF
${UNI_PLAN}
UNI_PLAN_EOF

  echo ""
  echo "Uninstall complete: ${removed} path(s) removed."
}

# --- menu ------------------------------------------------------------------

print_menu() {
  local cur=""
  case "$CUR_CLASS" in
    "symlink into THIS repo"*) cur=" [CURRENT]" ;;
  esac
  echo ""
  echo "Select an install source for \`late\`:"
  printf "  [1] local unstable dev       — build current branch (%s @ %s), symlink%s\n" "$REPO_BRANCH" "$REPO_SHA" "$cur"
  printf "  [2] pinned stable            — build current branch, fixed copy\n"
  printf "  [3] fork main (unstable)     — build %s@main (%s), copy\n" "$FORK_REPO" "$FORK_SHA"
  printf "  [4] upstream main (unstable) — build %s@main (%s), copy\n" "$UPSTREAM_REPO" "$UPSTREAM_SHA"
  printf "  [5] official installer       — upstream script (latest stable %s)\n" "$RELEASE_TAG"
  echo "  [6] check only"
  echo "  q   quit"
}

choice_to_source() {
  case "$1" in
    1|local-dev)      printf '%s\n' "local-dev" ;;
    2|pinned)         printf '%s\n' "pinned" ;;
    3|fork-main)      printf '%s\n' "fork-main" ;;
    4|upstream-main)  printf '%s\n' "upstream-main" ;;
    5|official)       printf '%s\n' "official" ;;
    6|check)          printf '%s\n' "check" ;;
    *)                printf '%s\n' "" ;;
  esac
}

is_quit() {
  case "$1" in
    q|Q|quit|Quit|QUIT) return 0 ;;
    *)                  return 1 ;;
  esac
}

# menu_pick — print the menu and set SOURCE; re-prompts on a TTY, reads a
# single line from stdin otherwise.
menu_pick() {
  local choice source
  print_menu
  if [ -t 0 ]; then
    while :; do
      printf "Choice: "
      if ! IFS= read -r choice; then
        echo ""
        die "no selection made"
      fi
      if is_quit "$choice"; then
        echo "Bye."
        exit 0
      fi
      source="$(choice_to_source "$choice")"
      if [ -n "$source" ]; then
        SOURCE="$source"
        return 0
      fi
      echo "Invalid choice: ${choice} — enter 1-6 (or q to quit)."
    done
  fi
  IFS= read -r choice || choice=""
  if [ -z "$choice" ]; then
    die "no selection made on stdin — pass a source (local-dev|pinned|fork-main|upstream-main|official|check) as an argument"
  fi
  if is_quit "$choice"; then
    exit 0
  fi
  source="$(choice_to_source "$choice")"
  if [ -z "$source" ]; then
    die "invalid choice: ${choice}"
  fi
  SOURCE="$source"
}

confirm_or_die() {
  local answer
  if [ "$ASSUME_YES" -eq 1 ]; then
    return 0
  fi
  if [ ! -t 0 ]; then
    return 0
  fi
  printf "Proceed with '%s' on %s/late? [y/N] " "$SOURCE" "$TARGET_DIR"
  IFS= read -r answer || answer=""
  case "$answer" in
    y|Y|yes|Yes|YES) return 0 ;;
    *) die "aborted by user (use --yes to skip confirmations)" ;;
  esac
}

cleanup() {
  if [ -n "$LATE_TMP" ] && [ -d "$LATE_TMP" ]; then
    rm -rf -- "$LATE_TMP"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# validate_choice N — --choice must be an integer in 1..6 (menu entries).
validate_choice() {
  case "$1" in
    ''|*[!0-9]*)
      usage_error "--choice expects an integer 1-6 (got: '$1')" ;;
  esac
  if [ "$1" -lt 1 ] || [ "$1" -gt 6 ]; then
    usage_error "--choice expects an integer 1-6 (got: $1)"
  fi
}

# validate_target_override — fool-proofing for an explicit --target: it must
# be an absolute path and must not name a system directory (/usr/local/bin
# stays allowed).
validate_target_override() {
  [ -n "$TARGET_OVERRIDE" ] || return 0
  case "$TARGET_OVERRIDE" in
    /*) ;;
    *)  arg_error "--target must be an absolute path (got: ${TARGET_OVERRIDE})" ;;
  esac
  local norm="$TARGET_OVERRIDE"
  while [ "$norm" != "/" ] && [ "${norm%/}" != "$norm" ]; do
    norm="${norm%/}"
  done
  case "$norm" in
    /|/bin|/sbin|/etc|/usr)
      arg_error "refusing --target ${TARGET_OVERRIDE}: system directory (use e.g. /usr/local/bin or ~/.local/bin)" ;;
  esac
}

parse_args() {
  local positional_count=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --yes)
        ASSUME_YES=1
        ;;
      --dry-run)
        DRY_RUN=1
        ;;
      --purge)
        PURGE=1
        ;;
      --with-deps)
        WITH_DEPS=1
        ;;
      --target)
        if [ $# -lt 2 ]; then
          die "--target requires a directory argument"
        fi
        TARGET_OVERRIDE="$2"
        shift
        ;;
      --target=*)
        TARGET_OVERRIDE="${1#--target=}"
        if [ -z "$TARGET_OVERRIDE" ]; then
          die "--target requires a directory argument"
        fi
        ;;
      --choice)
        if [ "$SOURCE" = "uninstall" ]; then
          arg_error "uninstall is destructive and name-invoked only; it cannot be combined with --choice"
        fi
        if [ "$positional_count" -gt 0 ]; then
          arg_error "pass either an option name or --choice N"
        fi
        if [ $# -lt 2 ]; then
          usage_error "--choice requires a number argument (1-6)"
        fi
        validate_choice "$2"
        CHOICE="$2"
        shift
        ;;
      --choice=*)
        if [ "$SOURCE" = "uninstall" ]; then
          arg_error "uninstall is destructive and name-invoked only; it cannot be combined with --choice"
        fi
        if [ "$positional_count" -gt 0 ]; then
          arg_error "pass either an option name or --choice N"
        fi
        CHOICE="${1#--choice=}"
        if [ -z "$CHOICE" ]; then
          usage_error "--choice requires a number argument (1-6)"
        fi
        validate_choice "$CHOICE"
        ;;
      --help|-h)
        SOURCE="help"
        ;;
      help)
        if [ -n "$CHOICE" ]; then
          arg_error "pass either an option name or --choice N"
        fi
        SOURCE="help"
        ;;
      check|local-dev|pinned|fork-main|upstream-main|official|uninstall)
        positional_count=$((positional_count + 1))
        if [ "$positional_count" -gt 1 ]; then
          die "only one SOURCE argument is allowed (got another: $1)"
        fi
        if [ -n "$CHOICE" ]; then
          if [ "$1" = "uninstall" ]; then
            arg_error "uninstall is destructive and name-invoked only; it cannot be combined with --choice"
          fi
          arg_error "pass either an option name or --choice N"
        fi
        SOURCE="$1"
        ;;
      *)
        die "unknown argument: $1 (try --help)"
        ;;
    esac
    shift
  done
}

main() {
  refuse_root
  parse_args "$@"
  validate_target_override
  if [ "$SOURCE" = "help" ]; then
    usage
    return 0
  fi
  if [ -n "$CHOICE" ]; then
    # Headless mode: --choice N selects the menu entry, implies --yes and
    # never shows the menu or prompts. 'official' may still prompt because
    # the upstream script owns its interaction.
    ASSUME_YES=1
    SOURCE="$(choice_to_source "$CHOICE")"
  fi
  run_detection
  if [ -z "$SOURCE" ]; then
    menu_pick
  fi
  case "$SOURCE" in
    check)
      return 0
      ;;
    uninstall)
      # Destructive and name-invoked only; it must NOT create the target dir
      # and uses its own confirmations.
      opt_uninstall
      ;;
    *)
      run_install_flow
      ;;
  esac
}

# run_install_flow — the shared confirm/verify/dispatch path for the
# install sources (everything except check/uninstall/help).
run_install_flow() {
  confirm_or_die
  ensure_target_dir
  case "$SOURCE" in
    local-dev)     opt_local_dev ;;
    pinned)        opt_pinned ;;
    fork-main)     opt_tarball "$FORK_REPO" "fork-main" \
                     "Dev tracking stops: this snapshot of ${FORK_REPO}@main does not follow local rebuilds." ;;
    upstream-main) opt_tarball "$UPSTREAM_REPO" "upstream-main" \
                     "Dev tracking stops: this snapshot of ${UPSTREAM_REPO}@main does not follow local rebuilds." ;;
    official)      opt_official ;;
    *)             die "unhandled source: ${SOURCE}" ;;
  esac
  if [ -n "$BAK_HINTS" ]; then
    echo ""
    echo "Backups created this run: ${BAK_HINTS}"
  fi
}

main "$@"
