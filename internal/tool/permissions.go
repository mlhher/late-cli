package tool

import (
	"encoding/json"
	"late/internal/common"
	"late/internal/pathutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// canonicalizePath resolves symlinks for the nearest existing ancestor of absPath
// and then reapplies the non-existing suffix. This gives a canonical target path
// even when the leaf does not exist yet.
func canonicalizePath(absPath string) (string, error) {
	absPath = filepath.Clean(absPath)
	current := absPath

	for {
		if _, err := os.Lstat(current); err == nil {
			resolvedCurrent, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}

			suffix, err := filepath.Rel(current, absPath)
			if err != nil {
				return "", err
			}
			if suffix == "." {
				return filepath.Clean(resolvedCurrent), nil
			}

			return filepath.Clean(filepath.Join(resolvedCurrent, suffix)), nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return filepath.Clean(absPath), nil
}

// isNewPath returns true when the resolved target path does not yet exist,
// falls within the project root, and stays within the provided session cwd.
// Creation outside the project root or outside the active cwd always prompts.
func isNewPath(path string, cwd string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}

	baseDir := strings.TrimSpace(cwd)
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return false
		}
	}

	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return false
	}
	if evalBaseDir, err := filepath.EvalSymlinks(absBaseDir); err == nil {
		absBaseDir = evalBaseDir
	}

	resolvedPath := path
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(absBaseDir, resolvedPath)
	}
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return false
	}
	canonicalPath, err := canonicalizePath(absPath)
	if err != nil {
		return false
	}

	if !IsSafePath(canonicalPath) {
		return false
	}

	relToBase, err := filepath.Rel(absBaseDir, canonicalPath)
	if err != nil {
		return false
	}
	if relToBase == ".." || strings.HasPrefix(relToBase, ".."+string(filepath.Separator)) {
		return false
	}

	// Safety check uses the symlink-resolved canonical path to prevent
	// symlink-escape attacks.  Existence check intentionally uses the
	// pre-resolved absPath: os.Stat follows symlinks, so if absPath IS a
	// symlink, Stat reflects the link target's existence—which is correct for
	// "does this path already exist" semantics.
	_, err = os.Stat(absPath)
	return os.IsNotExist(err)
}

// IsSafePath checks if a path is within the current working directory.
func IsSafePath(path string) bool {
	// Shortcut: If the path is relative and does not contain ".." components,
	// it is guaranteed to be within the CWD (unless it follows a malicious symlink,
	// but we assume the agent stays within the provided tree).
	if !filepath.IsAbs(path) && !strings.Contains(path, "..") {
		return true
	}

	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	// Resolve symlinks to get canonical CWD
	if evalCwd, err := filepath.EvalSymlinks(absCwd); err == nil {
		absCwd = evalCwd
	}

	// Resolve symlinks for path by climbing up until an existing directory is found
	current := absPath
	var suffix string
	for {
		if eval, err := filepath.EvalSymlinks(current); err == nil {
			if suffix != "" {
				absPath = filepath.Join(eval, suffix)
			} else {
				absPath = eval
			}
			break
		}
		// Move up to the parent directory
		dir := filepath.Dir(current)
		if dir == current {
			break // Reached root
		}
		rel, _ := filepath.Rel(dir, absPath)
		suffix = rel
		current = dir
	}

	// Ensure absCwd ends with path separator for proper prefix matching
	if !strings.HasSuffix(absCwd, string(filepath.Separator)) {
		absCwd += string(filepath.Separator)
	}

	// Handle root path case
	if absCwd == string(filepath.Separator) {
		return true
	}

	// Also ensure absPath has a trailing separator so that an exact match
	// with the CWD returns true
	if !strings.HasSuffix(absPath, string(filepath.Separator)) {
		absPath += string(filepath.Separator)
	}

	return strings.HasPrefix(absPath, absCwd)
}

const (
	localAllowedCommandsFile = ".late/allowed_commands.json"
	localAllowedToolsFile    = ".late/allowed_tools.json"
	commandsFileName         = "allowed_commands.json"
	toolsFileName            = "allowed_tools.json"
	projectApprovalTTL       = 30 * 24 * time.Hour
	globalApprovalTTL        = 30 * 24 * time.Hour
	sessionApprovalTTL       = 30 * time.Minute
	sessionBaseMarker        = "__base__"
)

// Exact-string approval entries (issue #1): once a command has been executed
// with a valid OTP, that exact command string must never be gated again in
// ANY late instance, so the approval is persisted into the allow-list store
// every process reloads.
//
// Exact entries share the allowed_commands.json file and entry shape with
// flag-scoped approvals, but they live under a reserved key namespace and
// carry a reserved flag marker so they can never widen (or be widened by)
// the per-command/per-flag allowances produced by SaveAllowedCommand:
//
//   - key   = exactCommandKeyPrefix + <cwd> + exactCommandKeySeparator +
//     <command string>. Parsed command keys ("rm", "git log") can never
//     start with ":", so the namespaces are disjoint in both directions.
//     The cwd component scopes the approval to the directory the command
//     actually executes in (the shell tool's resolved cmd.Dir): relative
//     paths in the command resolve against it, so "rm -rf ./bin" run in
//     /tmp is a DIFFERENT approval than the same string run in the home
//     directory. The separator is NUL because a directory path can never
//     contain NUL on any supported platform, and encoding/json escapes NUL
//     in object keys as \u0000, so the JSON store stays valid; split keys
//     with splitExactCommandKey (first-NUL cut after the prefix). Keys
//     written before cwd scoping (no separator) simply no longer match any
//     lookup and re-gate.
//   - flags = [exactCommandFlagMarker]. allCommandsAllowlisted only ever
//     looks up parsed keys, so the marker is never consulted as a flag.
//   - cwd   = the canonical cwd from the key, mirrored into the entry's
//     "cwd" field so humans reading allowed_commands.json see what the
//     approval is scoped to.
//
// Normalization: the command string is trimmed of leading/trailing whitespace
// before storage AND at lookup time (JSON object keys must not carry invisible
// boundary whitespace). Interior whitespace stays significant — "rm  -rf x"
// and "rm -rf x" remain different commands, matching the byte-for-byte rule
// of the pending-OTP registry. The cwd is canonicalized by ExactCommandKey
// (absolute, symlink-resolved, cleaned) so equivalent spellings of the same
// directory share one entry. Path SPELLING inside the command stays
// significant by design: "rm -rf ./bin" and "rm -rf /abs/bin" are separate
// entries even in the same cwd — the raw argv is what was approved.
const (
	exactCommandKeyPrefix  = "::exact::"
	exactCommandFlagMarker = "__exact__"
	// exactCommandKeySeparator joins the cwd and command components of an
	// exact-entry key. NUL cannot occur in a directory path, so the split
	// is unambiguous.
	exactCommandKeySeparator = "\x00"
)

type persistedCommandEntry struct {
	Flags     []string `json:"flags"`
	SavedAt   string   `json:"saved_at,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Version   string   `json:"version,omitempty"`
	// Cwd records the canonical working directory an EXACT-string approval
	// is scoped to (parsed from the entry key on write). It is informational
	// — the key is authoritative — and exists so humans reading
	// allowed_commands.json can see which directory the approval covers.
	// Optional and omitted for flag-scoped entries, so pre-existing files
	// stay byte-compatible.
	Cwd string `json:"cwd,omitempty"`
	// TimesApproved counts how many times this entry's command string was
	// approved and (re)persisted. Only exact-string approvals increment it
	// today; the force-revaluate gate message reports it so the agent can
	// see how often the command was approved before. Optional and omitted
	// when zero, so pre-existing files stay byte-compatible.
	TimesApproved int `json:"times_approved,omitempty"`
}

type persistedCommandsFile struct {
	Version string                           `json:"version,omitempty"`
	Entries map[string]persistedCommandEntry `json:"entries"`
}

type persistedToolEntry struct {
	SavedAt   string `json:"saved_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Version   string `json:"version,omitempty"`
}

type persistedToolsFile struct {
	Version string                        `json:"version,omitempty"`
	Entries map[string]persistedToolEntry `json:"entries"`
}

type sessionApproval struct {
	expiresAt time.Time
}

var (
	sessionApprovalsMu       sync.Mutex
	sessionAllowedTools      = make(map[string]sessionApproval)
	sessionAllowedCommandMap = make(map[string]map[string]sessionApproval)
	nowFunc                  = time.Now
)

func parseRFC3339OrZero(s string) (time.Time, bool) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func isEntryValid(expiresAt time.Time, version string) bool {
	if !expiresAt.IsZero() && nowFunc().After(expiresAt) {
		return false
	}
	if strings.TrimSpace(version) != "" && version != common.Version {
		return false
	}
	return true
}

func cleanupSessionAllowListLocked() {
	now := nowFunc()
	for toolName, entry := range sessionAllowedTools {
		if now.After(entry.expiresAt) {
			delete(sessionAllowedTools, toolName)
		}
	}

	for cmd, flags := range sessionAllowedCommandMap {
		for flag, entry := range flags {
			if now.After(entry.expiresAt) {
				delete(flags, flag)
			}
		}
		if len(flags) == 0 {
			delete(sessionAllowedCommandMap, cmd)
		}
	}
}

// SaveSessionAllowedCommand stores a command in session scope with auto-expiry.
func SaveSessionAllowedCommand(command string) {
	commands := ParseCommandsForAllowList(command)
	if len(commands) == 0 {
		return
	}

	sessionApprovalsMu.Lock()
	defer sessionApprovalsMu.Unlock()
	cleanupSessionAllowListLocked()

	expiresAt := nowFunc().Add(sessionApprovalTTL)
	for cmd, flags := range commands {
		if _, ok := sessionAllowedCommandMap[cmd]; !ok {
			sessionAllowedCommandMap[cmd] = make(map[string]sessionApproval)
		}
		if len(flags) == 0 {
			sessionAllowedCommandMap[cmd][sessionBaseMarker] = sessionApproval{expiresAt: expiresAt}
			continue
		}
		for _, flag := range flags {
			sessionAllowedCommandMap[cmd][flag] = sessionApproval{expiresAt: expiresAt}
		}
	}
}

// SaveSessionAllowedTool stores a tool in session scope with auto-expiry.
func SaveSessionAllowedTool(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}

	sessionApprovalsMu.Lock()
	defer sessionApprovalsMu.Unlock()
	cleanupSessionAllowListLocked()
	sessionAllowedTools[name] = sessionApproval{expiresAt: nowFunc().Add(sessionApprovalTTL)}
}

func getGlobalConfigPath(fileName string) string {
	configDir, err := pathutil.LateConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, fileName)
}

func getFilePath(localPath string, fileName string, global bool) string {
	if global {
		return getGlobalConfigPath(fileName)
	}
	return localPath
}

func loadPersistedCommandsFile(path string) (persistedCommandsFile, error) {
	file := persistedCommandsFile{Entries: make(map[string]persistedCommandEntry)}
	if path == "" {
		return file, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return file, err
	}

	if err := json.Unmarshal(data, &file); err != nil || file.Entries == nil {
		return persistedCommandsFile{Entries: make(map[string]persistedCommandEntry)}, nil
	}

	return file, nil
}

func loadPersistedToolsFile(path string) (persistedToolsFile, error) {
	file := persistedToolsFile{Entries: make(map[string]persistedToolEntry)}
	if path == "" {
		return file, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return file, err
	}

	if err := json.Unmarshal(data, &file); err != nil || file.Entries == nil {
		return persistedToolsFile{Entries: make(map[string]persistedToolEntry)}, nil
	}

	return file, nil
}

// LoadAllowedCommands loads allowed commands from either local or global allow-list.
func LoadAllowedCommands(global bool) (map[string]map[string]bool, error) {
	allowed := make(map[string]map[string]bool)
	path := getFilePath(localAllowedCommandsFile, commandsFileName, global)
	if path == "" {
		return allowed, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return allowed, nil
		}
		return nil, err
	}

	// Backward-compatible format: map[string][]string
	var list map[string][]string
	if err := json.Unmarshal(data, &list); err == nil {
		for cmd, flags := range list {
			allowed[cmd] = make(map[string]bool)
			for _, flag := range flags {
				allowed[cmd][flag] = true
			}
		}
		return allowed, nil
	}

	// New format with metadata and decay.
	var file persistedCommandsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}

	for cmd, entry := range file.Entries {
		entryVersion := entry.Version
		if entryVersion == "" {
			entryVersion = file.Version
		}
		expiresAt, ok := parseRFC3339OrZero(entry.ExpiresAt)
		if !ok || !isEntryValid(expiresAt, entryVersion) {
			continue
		}
		if _, ok := allowed[cmd]; !ok {
			allowed[cmd] = make(map[string]bool)
		}
		for _, flag := range entry.Flags {
			allowed[cmd][flag] = true
		}
	}

	return allowed, nil
}

// LoadAllAllowedCommands loads both local and global allowed commands and merges them.
func LoadAllAllowedCommands() (map[string]map[string]bool, error) {
	merged := make(map[string]map[string]bool)

	// Load global first
	global, err := LoadAllowedCommands(true)
	if err == nil {
		for cmd, flags := range global {
			merged[cmd] = flags
		}
	}

	// Load local and override/merge
	local, err := LoadAllowedCommands(false)
	if err == nil {
		for cmd, flags := range local {
			if _, exists := merged[cmd]; !exists {
				merged[cmd] = make(map[string]bool)
			}
			for flag := range flags {
				merged[cmd][flag] = true
			}
		}
	}

	sessionApprovalsMu.Lock()
	defer sessionApprovalsMu.Unlock()
	cleanupSessionAllowListLocked()
	for cmd, flags := range sessionAllowedCommandMap {
		if _, exists := merged[cmd]; !exists {
			merged[cmd] = make(map[string]bool)
		}
		for flag := range flags {
			if flag == sessionBaseMarker {
				continue
			}
			merged[cmd][flag] = true
		}
	}

	return merged, nil
}

// writeAllowedCommandsFile rebuilds and persists the allow-list file at path
// from the merged allowed flag-map. Touched entries get a fresh TTL and
// metadata (an explicit approval always refreshes expiry); every other entry
// preserves its stored metadata (saved_at, expires_at, version and
// times_approved) so unrelated approvals are not refreshed. countBumps (may
// be nil) increments TimesApproved for the given keys — used by exact-string
// approvals to record how often a command has been approved. This is the
// single write path for both flag-scoped approvals (SaveAllowedCommand) and
// exact-string approvals (SaveExactAllowedCommand).
func writeAllowedCommandsFile(path string, allowed map[string]map[string]bool, touched map[string]bool, existingFile persistedCommandsFile, expiresAt time.Time, countBumps map[string]int) error {
	file := persistedCommandsFile{
		Version: common.Version,
		Entries: make(map[string]persistedCommandEntry),
	}
	for cmd, flagMap := range allowed {
		var flagList []string
		for flag := range flagMap {
			flagList = append(flagList, flag)
		}
		entry := persistedCommandEntry{
			Flags:         flagList,
			SavedAt:       nowFunc().UTC().Format(time.RFC3339),
			ExpiresAt:     expiresAt.UTC().Format(time.RFC3339),
			Version:       common.Version,
			TimesApproved: existingFile.Entries[cmd].TimesApproved,
		}
		// Exact entries record their scoping cwd visibly: the key is
		// authoritative, the field is for humans reading the store.
		if cwd, _, isExact := splitExactCommandKey(cmd); isExact {
			entry.Cwd = cwd
		}
		if bump := countBumps[cmd]; bump != 0 {
			entry.TimesApproved += bump
		}
		if existingEntry, ok := existingFile.Entries[cmd]; ok && !touched[cmd] {
			entry.SavedAt = existingEntry.SavedAt
			entry.ExpiresAt = existingEntry.ExpiresAt
			entry.Version = existingEntry.Version
		}
		file.Entries[cmd] = entry
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// SaveAllowedCommand adds a command string to the specified allow-list (local or global).
func SaveAllowedCommand(command string, global bool) error {
	commands := ParseCommandsForAllowList(command)
	if len(commands) == 0 {
		return nil
	}

	path := getFilePath(localAllowedCommandsFile, commandsFileName, global)
	existingFile, err := loadPersistedCommandsFile(path)
	if err != nil {
		return err
	}

	allowed, err := LoadAllowedCommands(global)
	if err != nil {
		return err
	}
	touched := make(map[string]bool)

	for key, flags := range commands {
		_, exists := allowed[key]
		if !exists {
			allowed[key] = make(map[string]bool)
		}
		// Always mark as touched so the TTL is refreshed on any explicit approval,
		// including re-approving a command that was already in the allow-list.
		touched[key] = true
		for _, flag := range flags {
			allowed[key][flag] = true
		}
	}

	expiresAt := nowFunc().Add(projectApprovalTTL)
	if global {
		expiresAt = nowFunc().Add(globalApprovalTTL)
	}
	return writeAllowedCommandsFile(path, allowed, touched, existingFile, expiresAt, nil)
}

// canonicalExecCwd normalizes an execution cwd for the exact-allowlist key.
// "" means the process working directory, relative paths resolve against it
// (mirroring os/exec's handling of a relative cmd.Dir), and symlinks in the
// nearest existing ancestor are resolved so /tmp and /private/tmp (macOS)
// land on the same key as os.Getwd's result. Best-effort: when resolution
// fails (e.g. a cwd that vanished mid-run), the cleaned absolute input is
// used so keys remain stable within the process.
func canonicalExecCwd(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		abs, err := filepath.Abs(".")
		if err != nil {
			return ""
		}
		cwd = abs
	} else if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	if canonical, err := canonicalizePath(cwd); err == nil {
		return canonical
	}
	return filepath.Clean(cwd)
}

// ExactCommandKey returns the allow-list store key under which the exact
// command string (trimmed of leading/trailing whitespace) is persisted by
// SaveExactAllowedCommand, scoped to the directory the command executes in:
// the key is prefix + canonical cwd + NUL + command. The cwd scoping exists
// because relative paths in the command resolve against it — the same argv
// in a different cwd is a different command. The cwd is canonicalized so
// equivalent spellings of the same directory (os.Getwd's symlink-resolved
// result, a caller-supplied path through a symlink, a relative path, or ""
// meaning the process working directory) all collapse to one entry.
// Exposed so other packages can inspect or fixture the store without
// duplicating the reserved-key format.
func ExactCommandKey(cwd, command string) string {
	return exactCommandKeyPrefix + canonicalExecCwd(cwd) + exactCommandKeySeparator + strings.TrimSpace(command)
}

// splitExactCommandKey splits a stored exact-entry key into its canonical
// cwd and command components. The cwd can never contain the NUL separator,
// so the first separator byte after the reserved prefix is the boundary.
func splitExactCommandKey(key string) (cwd, command string, ok bool) {
	rest, ok := strings.CutPrefix(key, exactCommandKeyPrefix)
	if !ok {
		return "", "", false
	}
	cwd, command, found := strings.Cut(rest, exactCommandKeySeparator)
	if !found {
		return "", "", false
	}
	return cwd, command, true
}

// SaveExactAllowedCommand persists an EXACT command-string approval (issue #1)
// into the allow-list store. It is called when a gated command is re-run with
// a valid OTP and executes: from then on the exact command string must be
// auto-allowed by every future late instance, in any session, so the approval
// is written to the persistent GLOBAL store (global=true) that every process
// reloads on startup.
//
// The approval is scoped to cwd (the execution cwd as resolved by
// ResolveShellExecCwd — explicit cwd param > worktree > process CWD): the
// stored key pairs the canonical cwd with the trimmed command string, so the
// same argv executed in a different directory gates again and needs its own
// OTP approval. cwd may be "" (the process working directory); it is
// canonicalized by ExactCommandKey.
//
// Storage format: see the exactCommandKeyPrefix/exactCommandFlagMarker
// constants — the trimmed command string becomes a reserved-key entry with
// the single "__exact__" flag marker (plus a human-readable "cwd" field),
// which the AST analyzer turns into an exact-(cwd, string) bypass that never
// touches flag-level allowances. Each save refreshes the TTL and increments
// the entry's times_approved counter so the gate message can report how many
// times the command was approved before.
func SaveExactAllowedCommand(cwd, command string, global bool) error {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return nil
	}

	path := getFilePath(localAllowedCommandsFile, commandsFileName, global)
	existingFile, err := loadPersistedCommandsFile(path)
	if err != nil {
		return err
	}

	allowed, err := LoadAllowedCommands(global)
	if err != nil {
		return err
	}

	key := ExactCommandKey(cwd, cmd)
	if _, ok := allowed[key]; !ok {
		allowed[key] = make(map[string]bool)
	}
	allowed[key][exactCommandFlagMarker] = true

	expiresAt := nowFunc().Add(projectApprovalTTL)
	if global {
		expiresAt = nowFunc().Add(globalApprovalTTL)
	}
	touched := map[string]bool{key: true}
	bumps := map[string]int{key: 1}
	return writeAllowedCommandsFile(path, allowed, touched, existingFile, expiresAt, bumps)
}

// CountExactCommandApprovals returns how many times the exact command string
// (trimmed of leading/trailing whitespace), executed in the given cwd, has
// been approved and persisted via SaveExactAllowedCommand, summed across the
// global and local stores. The cwd participates in the lookup exactly as it
// does in the stored key, so counters are per (cwd, command). Entries that
// have since expired or were invalidated by a version change still count:
// they represent past approvals, even though they no longer grant an
// allowance (the rebuild-on-write drops them only at the next save). A
// non-zero count therefore means "gated again after previous approvals",
// which is exactly what the force-revaluate block message reports.
func CountExactCommandApprovals(cwd, command string) int {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return 0
	}
	key := ExactCommandKey(cwd, cmd)
	total := 0
	for _, global := range []bool{true, false} {
		file, err := loadPersistedCommandsFile(getFilePath(localAllowedCommandsFile, commandsFileName, global))
		if err != nil {
			continue
		}
		total += file.Entries[key].TimesApproved
	}
	return total
}

// LoadAllowedTools loads the list of tools that are always allowed (local or global).
func LoadAllowedTools(global bool) (map[string]bool, error) {
	allowed := make(map[string]bool)
	path := getFilePath(localAllowedToolsFile, toolsFileName, global)
	if path == "" {
		return allowed, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return allowed, nil
		}
		return nil, err
	}

	// Backward-compatible format: []string
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		for _, tool := range list {
			allowed[tool] = true
		}
		return allowed, nil
	}

	// New format with metadata and decay.
	var file persistedToolsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}

	for toolName, entry := range file.Entries {
		entryVersion := entry.Version
		if entryVersion == "" {
			entryVersion = file.Version
		}
		expiresAt, ok := parseRFC3339OrZero(entry.ExpiresAt)
		if !ok || !isEntryValid(expiresAt, entryVersion) {
			continue
		}
		allowed[toolName] = true
	}

	return allowed, nil
}

// LoadAllAllowedTools loads both local and global allowed tools and merges them.
func LoadAllAllowedTools() (map[string]bool, error) {
	merged := make(map[string]bool)

	global, err := LoadAllowedTools(true)
	if err == nil {
		for t := range global {
			merged[t] = true
		}
	}

	local, err := LoadAllowedTools(false)
	if err == nil {
		for t := range local {
			merged[t] = true
		}
	}

	sessionApprovalsMu.Lock()
	defer sessionApprovalsMu.Unlock()
	cleanupSessionAllowListLocked()
	for t := range sessionAllowedTools {
		merged[t] = true
	}

	return merged, nil
}

// SaveAllowedTool adds a tool name to the specified always-allowed list (local or global).
func SaveAllowedTool(name string, global bool) error {
	path := getFilePath(localAllowedToolsFile, toolsFileName, global)
	existingFile, err := loadPersistedToolsFile(path)
	if err != nil {
		return err
	}

	allowed, err := LoadAllowedTools(global)
	if err != nil {
		return err
	}

	allowed[name] = true

	file := persistedToolsFile{
		Version: common.Version,
		Entries: make(map[string]persistedToolEntry),
	}
	expiresAt := nowFunc().Add(projectApprovalTTL)
	if global {
		expiresAt = nowFunc().Add(globalApprovalTTL)
	}
	for toolName := range allowed {
		entry := persistedToolEntry{
			SavedAt:   nowFunc().UTC().Format(time.RFC3339),
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
			Version:   common.Version,
		}
		// For tools other than the one being newly approved, preserve existing
		// timestamps so their TTL is not accidentally reset by an unrelated save.
		if toolName != name {
			if existingEntry, ok := existingFile.Entries[toolName]; ok {
				entry.SavedAt = existingEntry.SavedAt
				entry.ExpiresAt = existingEntry.ExpiresAt
				entry.Version = existingEntry.Version
			}
		}
		file.Entries[toolName] = entry
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
