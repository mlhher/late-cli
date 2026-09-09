//go:build unix

package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunHook_ProcessGroupKillsChildrenOnCancel(t *testing.T) {
	pluginDir := t.TempDir()
	pidFile := filepath.Join(pluginDir, "child.pid")
	script := filepath.Join(pluginDir, "group_child.sh")

	// Script starts a child in background, writes its pid, and waits
	body := "sleep 30 &\necho $! > " + pidFile + "\nwait"
	writeExecutableShell(t, script, body)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, err := runHook(ctx, pluginDir, "group_child.sh", nil)
	if err == nil {
		t.Fatal("expected hook to fail on context deadline")
	}

	// Read child pid
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("reading child pid: %v", err)
	}
	pidStr := strings.TrimSpace(string(pidBytes))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		t.Fatalf("parsing child pid %q: %v", pidStr, err)
	}

	// Poll until process is dead
	dead := false
	for i := 0; i < 50; i++ {
		err := syscall.Kill(pid, 0)
		if err != nil {
			dead = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !dead {
		// Clean up runaway process if test failed
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child process %d in process group was not killed upon hook cancellation", pid)
	}
}

func TestRunHook_WaitDelayBoundsInheritedPipeWait(t *testing.T) {
	pluginDir := t.TempDir()
	pidFile := filepath.Join(pluginDir, "child.pid")
	script := filepath.Join(pluginDir, "spawn_pipe_child.sh")

	// Child sleeps holding inherited stdout; parent records its PID and exits
	// immediately. WaitDelay must both bound the pipe wait and trigger cleanup
	// of the now-leaderless process group.
	body := "sleep 30 &\necho $! > " + pidFile + "\nexit 0"
	writeExecutableShell(t, script, body)

	oldWaitDelay := hookWaitDelay
	hookWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { hookWaitDelay = oldWaitDelay })

	start := time.Now()
	_, err := runHook(context.Background(), pluginDir, "spawn_pipe_child.sh", nil)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("hook execution took %v; WaitDelay did not bound execution time", elapsed)
	}
	if err == nil {
		t.Fatal("expected error due to wait delay timeout, got nil")
	}
	if !strings.Contains(err.Error(), "process I/O completion") {
		t.Fatalf("expected process I/O completion error, got: %v", err)
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("reading child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parsing child pid %q: %v", strings.TrimSpace(string(pidBytes)), err)
	}

	dead := false
	for i := 0; i < 50; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			dead = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !dead {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child process %d was not killed after WaitDelay", pid)
	}
}
