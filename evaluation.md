# Late Project Evaluations

## Plugin System Evaluation

**Overall Verdict: functional with caveats.** The system has a clean architecture and solid unit-test coverage for happy paths and common failure modes, but lacks sandboxing entirely and has several gaping holes in the hot-reload path, MCP server arg validation, and silent-override semantics that will cause real incidents in multi-plugin deployments.

---

### Install / Bootstrap

| Risk | Code Reference | Mechanism | Severity |
|------|---------------|-----------|----------|
| **No plugin signing or integrity verification** | Entire `installer.go`, no call to checksum/signature anywhere | npm, git clone, local symlink, marketplace — all paths install arbitrary code with zero verification. A compromised npm package or MITM on git clone plants a plugin that runs arbitrary scripts as the user. | **CRITICAL** |
| **Source classifier heuristic can misroute** | `installer.go:218-253` `Install()`, `looksLikeGitSource:259-268`, `looksLikeLocalPath:273-278`, `looksLikeNpmPackage:284-294` | Classifier checks substring patterns, not actual content. A git URL missing `://` or shorthand prefix falls through to marketplace, then npm as bare name. An npm package named `@scope/subdir` with a `/` could be misclassified. Edge: `source == "github:user/repo"` is a valid shorthand, but `source == "git@evil.com:repo"` also passes. | **MEDIUM** |
| **npm install is unfiltered** | `installer.go:28-89` `InstallFromNpm` — direct `exec.Command("npm", "install", "--prefix", targetDir, pkgName)` | `pkgName` comes from user input or marketplace. No validation that it's a real npm package name. `npm install` can run pre/postinstall scripts from the package — arbitrary code execution on install. | **HIGH** |
| **Git clone is unfiltered** | `installer.go:94-141` `InstallFromGit` — `exec.Command("git", "clone", "--depth", "1", gitURL, targetDir)` | `gitURL` is expanded from shorthand by `expandGitURL` but never validated as a real git URL. Clone hooks can execute code. | **HIGH** |
| **Local symlink bypasses all path checks** | `installer.go:143-200` `InstallFromLocal` — `isSuspiciousPluginPath:613-625` | Symlink is created from any local path to plugin store. `isSuspiciousPluginPath` prints a **warning** but never blocks. A plugin can be linked from `/tmp/evil-plugin` or anywhere. | **HIGH** |
| **Project-local silently overrides global** | `manager.go:90-96` `Discover()` — project-local plugins loaded after global, same `pm.plugins` map | Project-local plugin with same name as global completely replaces it. No warning, no diff, no user choice. A project `.late/plugins/` checked into a public repo could shadow a trusted global plugin with a malicious version. | **HIGH** |
| **Discover ignores load errors silently** | `manager.go:124-131` — `LoadPluginMeta` errors → `fmt.Fprintf(stderr, ...); continue` | A plugin directory with a corrupted `.late-plugin.json` is silently skipped. The user sees no error, no health indicator. | **LOW** |
| **Watcher does not re-discover on change** | `cmd/late/main.go:502-516` — watcher callback sends `PluginChangeMsg{Commands, Themes}` | Only slash-command list and theme list are refreshed. New MCP servers, skills, inline tools from a newly installed plugin are NOT registered until restart. User must restart Late to activate any non-command/non-theme surface. | **MEDIUM** |

---

### Hook Execution

| Risk | Code Reference | Mechanism | Severity |
|------|---------------|-----------|----------|
| **No sandbox for hook scripts at all** | `hooks.go:52-99` `runHook` — direct `exec.CommandContext` with the resolved script path | Hook scripts run as the `late` process user. They can read/write any file, spawn network connections, access secrets in env vars (API keys, etc.). The only protection is path-containment (`resolveHookPath`), which only ensures the script lives inside the plugin directory — it does nothing to limit what the script can *do*. | **CRITICAL** |
| **Hook stdin payload capped at 256 bytes** | `hooks.go:69-70` — `hookCommandMax = 256` | Stdin payloads >256 bytes are rejected with error. Tool arguments JSON can exceed this trivially (e.g., a file-read tool with a long path). The hook silently fails to fire, and the error message is logged to stderr only. | **MEDIUM** |
| **Hook timeout is 15s, hardcoded** | `hooks.go:22` — `hookTimeout = 15 * time.Second` | A slow or deadlocked hook script is killed after 15s. Multiple sequential hooks can compound: 3 plugins × 3 scripts × 15s = 135s worst-case delay before a tool call completes. Not configurable. | **MEDIUM** |
| **Stderr truncated to 4096 bytes** | `hooks.go:23,82-85` — `maxStderrBytes = 4096` | Error output beyond 4KB is silently dropped. Plugin authors may not see full error messages. | **LOW** |
| **Fanout hooks run in parallel with no error propagation** | `hooks.go:168-194` `fanout` — uses `sync.WaitGroup`, logs errors to stderr | `CallOnSessionStartHooks`, `CallOnTurnStartHooks`, `CallOnTurnEndHooks` all use `fanout`. A failing hook prints to stderr but never surfaces to the user. If one plugin's hook panics or deadlocks, it's invisible. | **LOW-MEDIUM** |
| **`onSessionStart`/`onTurnStart`/`onTurnEnd` hooks receive nil stdin** | `hooks.go:249-263` — `fanout(ctx, "session-start", nil)` (3rd arg = nil), and `fanout:179-182` — `stdinFor` is nil → payload stays nil | These hooks receive empty stdin (nil), not an empty JSON object `{}` as the docs promise. The `manifest.go:112-127` docs say "They receive an empty JSON object on stdin". A script that does `cat | jq .` will hang waiting for JSON. | **LOW** |

---

### Hook Semantics

| Risk | Code Reference | Mechanism | Severity |
|------|---------------|-----------|----------|
| **Sequential transform hooks degrade silently on error** | `hooks.go:344-366` `CallOnInputHooks`, `hooks.go:373-396` `HookedMessage` | Error in one hook → logged to stderr, skipped, next hook runs with unmodified input. The user never sees that a hook failed. A critical security or formatting hook could silently stop working. | **MEDIUM** |
| **`onToolCall` per-plugin middleware order is alphabetical by plugin name** | `hooks.go:114-163` `snapshotHooks` sorts by `pluginName` | If plugin "alpha" blocks calls and plugin "beta" expects to see them, "beta" never runs because "alpha" vetoes before "beta" can inspect. The ordering is deterministic but arbitrary from the user's perspective. | **LOW** |
| **`onToolCall` middleware always calls `next()` even after script errors** | `hooks.go:218-244` — on script error, logs stderr and `continue`s, then calls `next()` | A crashing plugin's `onToolCall` can't block execution. Good for resilience, but means a security plugin that encounters a bug still passes the call through. | **LOW** (intentional) |
| **Veto token is a literal string "blocked" — no namespace/contract** | `hooks.go:232` — `if out == "blocked"` | A script that accidentally outputs "blocked\n" (with newline stripped by `strings.TrimSpace`) vetoes the call. There's no structured veto mechanism, no reason code. | **LOW-MEDIUM** |
| **`onToolCall` mutation replaces ALL arguments with raw JSON** | `hooks.go:235-236` — `if out != "" && json.Valid([]byte(out)) { call.Function.Arguments = out }` | No merge, no validation of the mutated schema. A hook that returns `{"command": "rm -rf /"}` while the original call was `{"path": "/safe"}` completely replaces the arguments. The next hook in the chain sees only the mutated version. | **HIGH** |
| **`onToolResult` can replace result bytes with arbitrary JSON** | `hooks.go:328-333` — same pattern, `result = []byte(out)` | A post-execution hook can replace a tool's entire output with fake data. The agent never sees the real result. | **HIGH** |

---

### Tool Middleware

| Risk | Code Reference | Mechanism | Severity |
|------|---------------|-----------|----------|
| **MCP server args NOT validated for path containment** | `manifest.go:209-219` `resolveArgs` — only converts `./` and `../` prefixes; other args pass through verbatim | A plugin manifest with `"args": ["/etc/passwd"]` or `"args": ["../../evil.sh"]` passes args directly to `exec.Command` when the MCP server starts. There is NO containment check for MCP server command arguments. The `resolveHookPath` guard used for hooks/skills doesn't apply. | **CRITICAL** |
| **TUI confirmation middleware runs before plugin onToolCall hooks** | `cmd/late/main.go:532-536` — order: `tui.TUIConfirmMiddleware` first, then `BuildHookMiddlewares()`, then `BuildToolResultMiddlewares()` | User confirmation gates happen before plugin hooks can veto. A plugin's "block dangerous commands" hook never fires if the user already approved the call in the TUI confirmation. However, the confirmation prompt already acts as a safety layer — the ordering just means plugin hooks can't short-circuit user approval. | **LOW** (design choice) |
| **Inline tools run via `runHook` with NO additional sandboxing** | `tools.go:59-69` — the `Runner` closure calls `runHook()` directly | Same `exec.CommandContext` call used for hooks. Inline tools are shell scripts running as the `late` user, with no sandbox, no seccomp, no capability drop, no filesystem namespace. | **CRITICAL** (shared with hook execution) |
| **`resolveArgs` for MCP servers does NOT sanitize `./` or `../` args in all code paths** | `manifest.go:209-219` — only transforms `./` and `../` prefixed args but doesn't clean them | An arg like `"./../../etc/passwd"` would become `<pluginDir>/./../../etc/passwd` which, after path resolution, could escape the plugin dir. `filepath.Clean` on the join result would collapse the `..`, but `resolveArgs` doesn't call `filepath.Clean`. The parent caller `ResolveSurfaces` stores the result as-is. | **MEDIUM** |

---

### Watcher / Rescan

| Risk | Code Reference | Mechanism | Severity |
|------|---------------|-----------|----------|
| **Hot-reload only refreshes commands and themes, not MCP/tools/skills** | `cmd/late/main.go:502-516` — `PluginChangeMsg` contains only `Commands` and `Themes` | Adding/removing a plugin while Late is running updates slash commands and theme picker, but MCP servers, inline tools, and skill symlinks are NOT registered/unregistered. The watcher is effectively a partial hot-reload. | **HIGH** |
| **Watcher never re-discoveries after initial bootstrap** | `watcher.go:73-101` `Start` callback sends `PluginChangeMsg`, but `Discover()` is NOT called | The watcher tick detects filesystem changes and fires a callback, but `Discover()` is never called again. The plugin manager's `plugins` map is not refreshed. This means the "file changed" detection works, but the actual plugin data in memory is stale. | **HIGH** |
| **Polling interval is 2s with no configurable backoff** | `watcher.go:13` — `defaultPollInterval = 2 * time.Second` | Every 2 seconds, `takeSnapshot` walks all plugin directories and stats every subdirectory. On a filesystem with many plugins or slow disk (NFS, network mounts), this creates continuous I/O. `SetInterval` exists but is never called from production code. | **LOW** |
| **`takeSnapshot` holds RLock while doing I/O** | `watcher.go:112-114` — `pm.mu.RLock()` before `snapshotDir`, which does `os.ReadDir`, `os.Stat`, `os.ReadFile` | The RLock prevents any write-lock operation (Discover, SetProjectDir, Add) for the duration of snapshot I/O. On slow filesystems, this creates a window where plugin operations block. | **LOW-MEDIUM** |
| **Snapshot dedup relies on string path equality** | `watcher.go:107-119` `takeSnapshot` — `seen` map of `filepath.Clean(dir)` | If the same directory is reachable via two different paths (symlinks, bind mounts), both are scanned. No dedup by device+inode. | **LOW** |

---

### Safety / Sandboxing

| Risk | Code Reference | Mechanism | Severity |
|------|---------------|-----------|----------|
| **ZERO sandboxing for all plugin scripts** | `hooks.go:52-99` `runHook`, `tools.go:59-69` inline tool runner | Every hook script, inline tool, and MCP server child process runs as the `late` user with full user privileges. No seccomp, no Landlock, no containers, no capability dropping, no filesystem namespace. Plugin = arbitrary code execution as the user. | **CRITICAL** |
| **No network or filesystem restrictions** | No reference to any sandbox API anywhere in the plugin package | A plugin's hook script can `curl` sensitive data to an external server, read `~/.ssh/id_rsa`, write to `~/.bashrc`, or install cryptominers. The only defense is "don't install untrusted plugins". | **CRITICAL** |
| **MCP server env supports `${VAR}` expansion — but also leaks env** | `manifest.go` — `MCPServerConfig.Env` used directly; expansion happens at MCP layer | Plugin-authored MCP servers receive env vars. A plugin could read `OPENAI_API_KEY`, `AWS_SECRET_ACCESS_KEY`, or any secret the `late` process has access to. | **HIGH** |
| **No plugin signing or attestation at any layer** | Entire plugin package — no signature verification | Every install path (npm, git, local, marketplace) trusts the source completely. There is no key signing, no checksum verification, no plugin attestation. | **HIGH** |
| **Plugin manifest can declare arbitrary executables for MCP servers** | `manifest.go:99-107` `MCPServerConfig` — `Command string` with no validation | A plugin's MCP server declaration can set `"command": "/usr/bin/python3"` or `"command": "curl"` or `"command": "bash"`. The command is not constrained to the plugin directory or an allowlist. This runs when MCP servers are connected at startup. | **CRITICAL** |
| **Skill symlink target is not validated for path containment in `ResolveSurfaces` (only in `RegisterPluginSkills`)** | `manifest.go:177-181` — `ResolveSurfaces` joins paths without checking. Check happens later in `manager.go:219-225` `RegisterPluginSkills`. | The guard is in the right place (registration), but `ResolveSurfaces` is callable independently and returns uncleaned paths. If a new code path calls `ResolveSurfaces` and uses the paths without going through `RegisterPluginSkills`, the guard is bypassed. | **LOW** (defense in depth) |

---

### Smallest Validation Test Loop

For each risk category, run these exact commands from repo root to confirm/disconfirm on this machine:

```bash
# 1. Install/Bootstrap — verify heuristic misclassification
# Test a local path that looks like a git URL
mkdir -p /tmp/evil-plugin/ && echo '{"name":"evil","late":{}}' > /tmp/evil-plugin/package.json
cd /mnt/storage/Projects/late
go run ./cmd/late plugin link /tmp/evil-plugin  # should succeed — verifies local path link
go run ./cmd/late plugin list                    # should show evil

# 2. Install/Bootstrap — local link outside $HOME
mkdir -p /opt/test-escape && echo '{"name":"escape","late":{}}' > /opt/test-escape/package.json
go run ./cmd/late plugin link /opt/test-escape   # should warn but succeed

# 3. Hook execution — runHook no sandboxing
# Deploy a test plugin that reads /etc/shadow
mkdir -p /tmp/test-hook-plugin/hooks
cat > /tmp/test-hook-plugin/package.json << 'EOF'
{"name":"test-hook","late":{"hooks":{"onSessionStart":["hooks/leak.sh"]}}}
EOF
cat > /tmp/test-hook-plugin/hooks/leak.sh << 'SCRIPT'
#!/bin/bash
id > /tmp/plugin-eval-proof-$$ 2>&1
SCRIPT
chmod +x /tmp/test-hook-plugin/hooks/leak.sh
go run ./cmd/late plugin link /tmp/test-hook-plugin
go run ./cmd/late                                                 # starts TUI — onSessionStart fires
cat /tmp/plugin-eval-proof-*                                      # should show uid — proof of arbitrary code exec

# 4. Hook semantics — stdin cap at 256 bytes
cat > /tmp/test-hook-plugin/hooks/onToolCall.sh << 'SCRIPT'
#!/bin/bash
len=$(wc -c)
echo "toolcall stdin length: $len" > /tmp/hook-stdin-debug
SCRIPT
chmod +x /tmp/test-hook-plugin/hooks/onToolCall.sh
# Update manifest to add onToolCall, then run a tool call with long args

# 5. Tool middleware — MCP server args not validated
cat > /tmp/test-mcp-plugin/package.json << 'EOF'
{"name":"test-mcp","late":{"mcp":{"servers":{"evil":{"command":"bash","args":["-c","touch /tmp/mcp-escaped"]}}}}}
EOF
go run ./cmd/late plugin link /tmp/test-mcp-plugin
ls -la /tmp/mcp-escaped 2>&1 || echo "MCP not auto-started (expected: MCP only starts when server connects)"

# 6. Watcher — partial hot-reload proof
# Start late, then in another terminal:
mkdir -p /tmp/live-plugin/ && echo '{"name":"live-plugin","late":{"commands":["/live-test"]}}' > /tmp/live-plugin/package.json
go run ./cmd/late plugin link /tmp/live-plugin
# Check if /live-test appears in TUI command list without restart

# 7. Safety — no signature verification proof
# Verify no code path calls crypto/sign, crypto/x509, or similar
grep -rn "Sign\|Verify\|x509\|crypto" internal/plugin/ || echo "No crypto verification found — confirmed"
```

**Cleanup:**
```bash
go run ./cmd/late plugin remove test-hook  2>/dev/null || true
go run ./cmd/late plugin remove test-mcp   2>/dev/null || true
go run ./cmd/late plugin remove live-plugin 2>/dev/null || true
go run ./cmd/late plugin remove evil       2>/dev/null || true
go run ./cmd/late plugin remove escape     2>/dev/null || true
rm -rf /tmp/evil-plugin /tmp/test-hook-plugin /tmp/test-mcp-plugin /tmp/live-plugin /tmp/plugin-eval-proof-* /tmp/hook-stdin-debug 2>/dev/null || true
```

---

### Fallback Recommendations (Prioritized by Likelihood × Impact)

1. **Add sandboxing for all script execution** (HIGHEST IMPACT)
   - Use Landlock on Linux (available since kernel 5.13) via `unix.Syscall(SYS_LANDLOCK_CREATE_RULESET)` in Go to restrict filesystem access to the plugin directory. Golang.org/x/sys/unix has the constants.
   - Alternatively: `prctl(PR_SET_NO_NEW_PRIVS, 1)` + seccomp allowlist for read/write syscalls constrained to plugin dir.
   - Fallback: chroot-jail or at minimum set `cmd.SysProcAttr{Unshareflags: CLONE_NEWNS}` on hook scripts.
   - **Interim patch** (1-2 days): Add `cmd.SysProcAttr{NoNewPrivs: true}` to `runHook` immediately — prevents privilege escalation via setuid binaries.

2. **Validate MCP server command and args** (HIGH IMPACT, easy fix)
   - Add `resolveMCPPath` that runs `filepath.Clean` + `filepath.Rel` containment check on the `Command` and every `Args` entry, same pattern as `resolveHookPath`.
   - Reject any command or arg that escapes the plugin directory. This prevents MCP server manifests from specifying `/bin/sh` or other system executables.
   - **Quick fix** (hours): Add containment check in `ResolveSurfaces` or when connecting MCP servers.

3. **Full hot-reload instead of partial** (HIGH IMPACT)
   - Watcher callback should call `pm.Discover()` + `pm.RegisterPluginSkills()` + rebuild MCP config + re-register inline tools.
   - Currently only commands and themes are refreshed on change. Everything else requires a restart.

4. **Plugin signature verification** (MEDIUM-HIGH IMPACT, longer term)
   - Marketplace registry should return a signed manifest digest.
   - Add optional `"signature"` field to `.late-plugin.json` verified against a built-in or user-trusted public key.
   - For git-sourced plugins: verify commit signature with `git verify-commit`.

5. **Fix silent-override semantics** (MEDIUM IMPACT)
   - Log a warning when a project-local plugin overrides a global one, showing both names and versions.
   - Add `--allow-override` flag or user confirmation when overrides happen.

6. **Add stdin payload to lifecycle hooks** (LOW IMPACT)
   - `CallOnSessionStartHooks` should pass `{}` on stdin, not nil, to match documented contract.
   - Same for `onTurnStart` and `onTurnEnd`.

7. **Make `hookCommandMax` configurable or increase it** (LOW IMPACT)
   - 256 bytes is too tight for tool argument payloads. Raise to 4096 or make it per-plugin configurable.

8. **Consider adding `resolveArgs` path cleaning** (LOW IMPACT)
   - `filepath.Clean` the result of `filepath.Join(pluginDir, arg)` in `resolveArgs` to prevent `./../../` traversal tricks in MCP server args.
