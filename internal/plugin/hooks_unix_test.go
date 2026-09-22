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

	// The hook must stay alive until the script has spawned its child and
	// written the pid file, so it is cancelled only once the pid file
	// appears — asserting the process-group kill requires the child to
	// exist first. The previous fixed 250ms budget for the spawn failed
	// deterministically on hosts whose fork/exec latency exceeded it: the
	// process group was SIGKILLed before the shell ever executed
	// `echo $! > pidFile`, so the pid file never appeared and the test
	// failed before the process-group assertion could run. runHook caps
	// hook execution at hookTimeout, so extend it for this test (same
	// pattern as the hookWaitDelay override below) and poll for the pid
	// file instead of budgeting the spawn; on loaded hosts shell startup
	// has been observed to take seconds.
	oldHookTimeout := hookTimeout
	hookTimeout = 60 * time.Second
	t.Cleanup(func() { hookTimeout = oldHookTimeout })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hookDone := make(chan error, 1)
	go func() {
		_, err := runHook(ctx, pluginDir, "group_child.sh", nil)
		hookDone <- err
	}()

	// Deadline-based poll for the pid file instead of a fixed spawn budget.
	// The limit is half the hook window so the poll can never race the
	// hook's own timeout.
	const pidFileWaitLimit = 30 * time.Second
	pidWaitStart := time.Now()
	for {
		// echo's `>` opens pidFile before writing it, so break only on non-empty content — an empty read in the open->write gap would break pid parsing.
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
			break
		}
		if elapsed := time.Since(pidWaitStart); elapsed > pidFileWaitLimit {
			cancel() // don't leak the hook if the pid file never appears
			t.Fatalf("child pid file %s never appeared within %v", pidFile, elapsed)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// The child now exists; cancelling the hook must kill the whole
	// process group (asserted below).
	cancel()

	err := <-hookDone
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
