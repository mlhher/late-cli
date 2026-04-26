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

type persistedCommandEntry struct {
	Flags     []string `json:"flags"`
	SavedAt   string   `json:"saved_at,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Version   string   `json:"version,omitempty"`
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

func parseRFC3339OrZero(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
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
		if !isEntryValid(parseRFC3339OrZero(entry.ExpiresAt), entryVersion) {
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
			merged[cmd][flag] = true
		}
	}

	return merged, nil
}

// SaveAllowedCommand adds a command string to the specified allow-list (local or global).
func SaveAllowedCommand(command string, global bool) error {
	commands := ParseCommandsForAllowList(command)
	if len(commands) == 0 {
		return nil
	}

	allowed, err := LoadAllowedCommands(global)
	if err != nil {
		return err
	}

	for key, flags := range commands {
		if _, exists := allowed[key]; !exists {
			allowed[key] = make(map[string]bool)
		}
		for _, flag := range flags {
			allowed[key][flag] = true
		}
	}

	file := persistedCommandsFile{
		Version: common.Version,
		Entries: make(map[string]persistedCommandEntry),
	}
	expiresAt := nowFunc().Add(projectApprovalTTL)
	if global {
		expiresAt = nowFunc().Add(globalApprovalTTL)
	}
	for cmd, flagMap := range allowed {
		var flagList []string
		for flag := range flagMap {
			flagList = append(flagList, flag)
		}
		file.Entries[cmd] = persistedCommandEntry{
			Flags:     flagList,
			SavedAt:   nowFunc().UTC().Format(time.RFC3339),
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
			Version:   common.Version,
		}
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	path := getFilePath(localAllowedCommandsFile, commandsFileName, global)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
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
		if !isEntryValid(parseRFC3339OrZero(entry.ExpiresAt), entryVersion) {
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
		file.Entries[toolName] = persistedToolEntry{
			SavedAt:   nowFunc().UTC().Format(time.RFC3339),
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
			Version:   common.Version,
		}
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	path := getFilePath(localAllowedToolsFile, toolsFileName, global)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// NormalizeCommandForAllowList is now a legacy helper that returns the first command key found.
func NormalizeCommandForAllowList(command string) string {
	commands := ParseCommandsForAllowList(command)
	for key := range commands {
		return key
	}
	return ""
}
