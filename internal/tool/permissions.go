package tool

import (
	"encoding/json"
	"late/internal/pathutil"
	"os"
	"path/filepath"
	"strings"
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
	commandsFileName        = "allowed_commands.json"
	toolsFileName           = "allowed_tools.json"
)

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

	var list map[string][]string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}

	for cmd, flags := range list {
		allowed[cmd] = make(map[string]bool)
		for _, flag := range flags {
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

	serializable := make(map[string][]string)
	for cmd, flagMap := range allowed {
		var flagList []string
		for flag := range flagMap {
			flagList = append(flagList, flag)
		}
		serializable[cmd] = flagList
	}

	data, err := json.MarshalIndent(serializable, "", "  ")
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

	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}

	for _, tool := range list {
		allowed[tool] = true
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

	return merged, nil
}

// SaveAllowedTool adds a tool name to the specified always-allowed list (local or global).
func SaveAllowedTool(name string, global bool) error {
	allowed, err := LoadAllowedTools(global)
	if err != nil {
		return err
	}

	allowed[name] = true

	var list []string
	for tool := range allowed {
		list = append(list, tool)
	}

	data, err := json.MarshalIndent(list, "", "  ")
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
