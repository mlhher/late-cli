package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"late/internal/client"
	"late/internal/orchestrator"
	"late/internal/session"
)

// TestNewSubagentOrchestratorWithGemmaThinking verifies that the gemmaThinking
// parameter correctly prepends the <|think|> token to the system prompt
func TestNewSubagentOrchestratorWithGemmaThinking(t *testing.T) {
	// Create a mock client
	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	// Create a mock parent session
	mockHistoryPath := "/tmp/mock-session.json"
	mockHistory := []client.ChatMessage{}
	mockSession := session.New(c, mockHistoryPath, mockHistory, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 100)

	// Test with gemmaThinking = true
	enabledTools := map[string]bool{"bash": true}
	child, err := NewSubagentOrchestrator(
		c,
		"test goal",
		[]string{},
		"coder",
		enabledTools,
		false, // injectCWD
		true,  // gemmaThinking
		100,   // maxTurns
		"",    // parentSessionID
		false, // saveSubagentHistory
		parent,
		nil, // messenger
	)

	if err != nil {
		t.Fatalf("Failed to create subagent orchestrator: %v", err)
	}

	// Get the session from the child orchestrator
	childBase, ok := child.(*orchestrator.BaseOrchestrator)
	if !ok {
		t.Fatalf("Expected BaseOrchestrator, got %T", child)
	}

	sess := childBase.Session()

	// Check that the system prompt has the <|think|> prefix
	systemPrompt := sess.SystemPrompt()
	if !strings.HasPrefix(systemPrompt, "<|think|>") {
		t.Errorf("Expected system prompt to start with '<|think|>', got: %s", systemPrompt[:min(50, len(systemPrompt))]+"...")
	}

	// Test with gemmaThinking = false
	child2, err := NewSubagentOrchestrator(
		c,
		"test goal",
		[]string{},
		"coder",
		enabledTools,
		false, // injectCWD
		false, // gemmaThinking
		100,   // maxTurns
		"",    // parentSessionID
		false, // saveSubagentHistory
		parent,
		nil, // messenger
	)

	if err != nil {
		t.Fatalf("Failed to create subagent orchestrator: %v", err)
	}

	childBase2, ok := child2.(*orchestrator.BaseOrchestrator)
	if !ok {
		t.Fatalf("Expected BaseOrchestrator, got %T", child2)
	}

	sess2 := childBase2.Session()

	// Check that the system prompt does NOT have the <|think|> prefix
	systemPrompt2 := sess2.SystemPrompt()
	if strings.HasPrefix(systemPrompt2, "<|think|>") {
		t.Errorf("Expected system prompt NOT to start with '<|think|>', got: %s", systemPrompt2[:min(50, len(systemPrompt2))]+"...")
	}
}

// TestNewSubagentOrchestratorGemmaThinkingWithCWD verifies that gemmaThinking
// works correctly together with injectCWD
func TestNewSubagentOrchestratorGemmaThinkingWithCWD(t *testing.T) {
	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	// Create a mock parent session
	mockHistoryPath := "/tmp/mock-session.json"
	mockHistory := []client.ChatMessage{}
	mockSession := session.New(c, mockHistoryPath, mockHistory, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 100)

	enabledTools := map[string]bool{"bash": true}
	child, err := NewSubagentOrchestrator(
		c,
		"test goal",
		[]string{},
		"coder",
		enabledTools,
		true,  // injectCWD
		true,  // gemmaThinking
		100,   // maxTurns
		"",    // parentSessionID
		false, // saveSubagentHistory
		parent,
		nil, // messenger
	)

	if err != nil {
		t.Fatalf("Failed to create subagent orchestrator: %v", err)
	}

	childBase, ok := child.(*orchestrator.BaseOrchestrator)
	if !ok {
		t.Fatalf("Expected BaseOrchestrator, got %T", child)
	}

	sess := childBase.Session()
	systemPrompt := sess.SystemPrompt()

	// Verify <|think|> is at the very beginning
	if !strings.HasPrefix(systemPrompt, "<|think|>") {
		t.Errorf("Expected system prompt to start with '<|think|>'")
	}

	// Verify ${{CWD}} was replaced with actual CWD
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get CWD: %v", err)
	}

	if !strings.Contains(systemPrompt, cwd) {
		t.Errorf("Expected system prompt to contain CWD '%s'", cwd)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestNewSubagentOrchestratorID verifies that the created subagent ID contains its agent type
func TestNewSubagentOrchestratorID(t *testing.T) {
	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)
	mockSession := session.New(c, "/tmp/mock-session.json", []client.ChatMessage{}, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 100)

	child, err := NewSubagentOrchestrator(
		c,
		"test goal",
		[]string{},
		"coder",
		map[string]bool{"bash": true},
		false,
		false,
		100,
		"",
		false,
		parent,
		nil,
	)
	if err != nil {
		t.Fatalf("Failed to create subagent: %v", err)
	}

	if !strings.Contains(child.ID(), "coder") {
		t.Errorf("Expected child ID to contain 'coder', got %s", child.ID())
	}
}

// TestNewSubagentOrchestrator_ConcurrentSpawn is the FR2 regression test:
// concurrent spawns against a shared parent must never mint duplicate child IDs.
func TestNewSubagentOrchestrator_ConcurrentSpawn(t *testing.T) {
	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	// Create a shared mock parent session and orchestrator
	mockSession := session.New(c, "/tmp/mock-session.json", []client.ChatMessage{}, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 10)

	const numSpawn = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := make([]string, 0, numSpawn)

	for i := 0; i < numSpawn; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			child, err := NewSubagentOrchestrator(
				c,
				"goal",
				nil,
				"researcher",
				map[string]bool{}, // enabledTools
				false,             // injectCWD
				false,             // gemmaThinking
				10,                // maxTurns
				"",                // parentSessionID
				false,             // saveSubagentHistory
				parent,
				nil, // messenger
			)
			if err != nil {
				t.Errorf("Failed to create subagent orchestrator: %v", err)
				return
			}

			mu.Lock()
			ids = append(ids, child.ID())
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(ids) != numSpawn {
		t.Fatalf("Expected %d spawned orchestrators, got %d", numSpawn, len(ids))
	}

	// Assert all IDs are unique (no collisions)
	seen := make(map[string]bool, numSpawn)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("Duplicate child ID minted by concurrent spawns: %s", id)
		}
		seen[id] = true
	}

	if n := len(parent.Children()); n != numSpawn {
		t.Errorf("Expected parent to have %d children, got %d", numSpawn, n)
	}
}

// setSessionDirForTest points session.SessionDir at dir for the duration of
// the test, restoring the previous value on cleanup.
func setSessionDirForTest(t *testing.T, dir string) {
	t.Helper()
	oldDir := session.SessionDir
	session.SessionDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { session.SessionDir = oldDir })
}

// walkRegularFiles returns the paths of all regular files under root.
func walkRegularFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk %s: %v", root, err)
	}
	return files
}

// TestNewSubagentOrchestrator_PersistsWhenOptedIn verifies that with
// saveSubagentHistory=true the subagent's initial goal message is persisted to
// <SessionDir()>/<parentSessionID>/subagents/<childID>.json, and that subagent
// sessions never write a .meta.json sidecar.
func TestNewSubagentOrchestrator_PersistsWhenOptedIn(t *testing.T) {
	tmp := t.TempDir()
	setSessionDirForTest(t, tmp)

	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	mockSession := session.New(c, "/tmp/mock-session.json", []client.ChatMessage{}, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 10)

	const goal = "persist me"
	_, err := NewSubagentOrchestrator(
		c,
		goal,
		[]string{},
		"researcher",
		map[string]bool{}, // enabledTools
		false,             // injectCWD
		false,             // gemmaThinking
		10,                // maxTurns
		"session-test",    // parentSessionID
		true,              // saveSubagentHistory
		parent,
		nil, // messenger
	)
	if err != nil {
		t.Fatalf("Failed to create subagent orchestrator: %v", err)
	}

	historyPath := filepath.Join(tmp, "session-test", "subagents", "researcher-subagent-0.json")
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("Expected subagent history file %s to exist: %v", historyPath, err)
	}

	data, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("Failed to read subagent history file: %v", err)
	}

	var msgs []client.ChatMessage
	if err := json.Unmarshal(data, &msgs); err != nil {
		t.Fatalf("Failed to unmarshal subagent history file: %v", err)
	}
	if len(msgs) < 1 {
		t.Fatalf("Expected at least 1 message in subagent history, got %d", len(msgs))
	}

	foundGoal := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content.String(), goal) {
			foundGoal = true
			break
		}
	}
	if !foundGoal {
		t.Errorf("Expected a message containing goal %q, got %d messages", goal, len(msgs))
	}

	// The parent sequence reservation writes its root metadata sidecar. Subagent
	// sessions themselves must never write nested .meta.json sidecars.
	rootMetaPath := filepath.Join(tmp, "mock-session.meta.json")
	for _, f := range walkRegularFiles(t, tmp) {
		if strings.HasSuffix(f, ".meta.json") && f != rootMetaPath {
			t.Errorf("Unexpected .meta.json file written by subagent session: %s", f)
		}
	}
}

// TestNewSubagentOrchestrator_RejectsUnsafeParentSessionID verifies that an
// unsafe parentSessionID (e.g. "..") is rejected at history path resolution
// before anything is written to the session directory (defense in depth;
// Phase 2 makes this unreachable from production).
func TestNewSubagentOrchestrator_RejectsUnsafeParentSessionID(t *testing.T) {
	tmp := t.TempDir()
	setSessionDirForTest(t, tmp)

	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	mockSession := session.New(c, "/tmp/mock-session.json", []client.ChatMessage{}, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 10)

	const goal = "persist me"
	_, err := NewSubagentOrchestrator(
		c,
		goal,
		[]string{},
		"coder",
		map[string]bool{}, // enabledTools
		false,             // injectCWD
		false,             // gemmaThinking
		10,                // maxTurns
		"..",              // parentSessionID
		true,              // saveSubagentHistory
		parent,
		nil, // messenger
	)
	if err == nil {
		t.Fatalf("Expected error for unsafe parentSessionID, got nil")
	}
	if !strings.Contains(err.Error(), "failed to resolve subagent history path") {
		t.Errorf("Expected error to contain %q, got: %v", "failed to resolve subagent history path", err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("Failed to read temp sessions dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected zero entries in temp sessions dir, got %d: %v", len(entries), entries)
	}
}

// TestNewSubagentOrchestrator_InMemoryByDefault verifies that with
// saveSubagentHistory=false the subagent session is fully in-memory: nothing
// (not even a directory artifact) is written to the session directory.
func TestNewSubagentOrchestrator_InMemoryByDefault(t *testing.T) {
	tmp := t.TempDir()
	setSessionDirForTest(t, tmp)

	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	mockSession := session.New(c, "/tmp/mock-session.json", []client.ChatMessage{}, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 10)

	_, err := NewSubagentOrchestrator(
		c,
		"test goal",
		[]string{},
		"researcher",
		map[string]bool{}, // enabledTools
		false,             // injectCWD
		false,             // gemmaThinking
		10,                // maxTurns
		"session-test",    // parentSessionID
		false,             // saveSubagentHistory
		parent,
		nil, // messenger
	)
	if err != nil {
		t.Fatalf("Failed to create subagent orchestrator: %v", err)
	}

	rootMetaPath := filepath.Join(tmp, "mock-session.meta.json")
	for _, f := range walkRegularFiles(t, tmp) {
		if f != rootMetaPath {
			t.Errorf("Expected only root metadata when saveSubagentHistory=false, found: %s", f)
		}
	}
}

// TestNewSubagentOrchestrator_SecondSpawnGetsNextID verifies that two spawns
// against the same parent mint distinct child IDs, producing two separate
// history files (no overwrite/collision).
func TestNewSubagentOrchestrator_SecondSpawnGetsNextID(t *testing.T) {
	tmp := t.TempDir()
	setSessionDirForTest(t, tmp)

	cfg := client.Config{BaseURL: "http://localhost:8080"}
	c := client.NewClient(cfg)

	mockSession := session.New(c, "/tmp/mock-session.json", []client.ChatMessage{}, "mock system prompt", true)
	parent := orchestrator.NewBaseOrchestrator("parent", mockSession, nil, 10)

	const goal = "persist me"
	spawn := func() {
		_, err := NewSubagentOrchestrator(
			c,
			goal,
			[]string{},
			"researcher",
			map[string]bool{}, // enabledTools
			false,             // injectCWD
			false,             // gemmaThinking
			10,                // maxTurns
			"session-test",    // parentSessionID
			true,              // saveSubagentHistory
			parent,
			nil, // messenger
		)
		if err != nil {
			t.Fatalf("Failed to create subagent orchestrator: %v", err)
		}
	}

	spawn()
	spawn()

	subagentsDir := filepath.Join(tmp, "session-test", "subagents")
	for _, name := range []string{"researcher-subagent-0.json", "researcher-subagent-1.json"} {
		p := filepath.Join(subagentsDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Expected subagent history file %s to exist: %v", p, err)
		}
	}
}

func TestNewSubagentOrchestrator_ResumedParentUsesNextHistoryPath(t *testing.T) {
	tmp := t.TempDir()
	setSessionDirForTest(t, tmp)

	c := client.NewClient(client.Config{BaseURL: "http://localhost:8080"})
	historyPath := filepath.Join(tmp, "session-test.json")
	saveHistories := true
	parentSession := session.New(c, historyPath, nil, "mock system prompt", true)
	parentSession.SetSubagentMetadata(0, &saveHistories)
	parent := orchestrator.NewBaseOrchestrator("parent", parentSession, nil, 10)

	first, err := NewSubagentOrchestrator(c, "first goal", nil, "researcher", map[string]bool{}, false, false, 10, "session-test", true, parent, nil)
	if err != nil {
		t.Fatalf("first NewSubagentOrchestrator() error = %v", err)
	}
	if first.ID() != "researcher-subagent-0" {
		t.Fatalf("first child ID = %q, want researcher-subagent-0", first.ID())
	}

	firstHistoryPath := filepath.Join(tmp, "session-test", "subagents", "researcher-subagent-0.json")
	firstHistory, err := os.ReadFile(firstHistoryPath)
	if err != nil {
		t.Fatalf("ReadFile(first child history) error = %v", err)
	}

	meta, err := session.LoadSessionMeta("session-test")
	if err != nil {
		t.Fatalf("LoadSessionMeta() error = %v", err)
	}
	resumedSession := session.New(c, historyPath, nil, "mock system prompt", true)
	resumedSession.SetSubagentMetadata(meta.SubagentSeq, meta.SaveSubagentHistories)
	resumedParent := orchestrator.NewBaseOrchestrator("parent", resumedSession, nil, 10)

	second, err := NewSubagentOrchestrator(c, "second goal", nil, "coder", map[string]bool{}, false, false, 10, "session-test", true, resumedParent, nil)
	if err != nil {
		t.Fatalf("resumed NewSubagentOrchestrator() error = %v", err)
	}
	if second.ID() != "coder-subagent-1" {
		t.Errorf("resumed child ID = %q, want coder-subagent-1", second.ID())
	}
	if _, err := os.Stat(filepath.Join(tmp, "session-test", "subagents", "coder-subagent-1.json")); err != nil {
		t.Errorf("second child history was not created: %v", err)
	}
	updatedFirstHistory, err := os.ReadFile(firstHistoryPath)
	if err != nil {
		t.Fatalf("ReadFile(first child history after resume) error = %v", err)
	}
	if string(updatedFirstHistory) != string(firstHistory) {
		t.Error("resumed child creation overwrote the original child history")
	}
}
