package plugin

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr runs fn while redirecting os.Stderr to a buffer and
// returns (returnValue, capturedStderr). Used by the help-flag tests
// to assert that HandlePluginCommand prints expected usage text.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- buf
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	bb := <-done
	return string(bb)
}

// WithHandlePluginCommandHelp exercises every help entry point of
// HandlePluginCommand: top-level `-h`/`--help`/`help`, plus the
// per-subcommand `--help` short-circuit for install, link, list,
// remove, update, enable, disable.
//
// The regression cases are:
//   - `install --help` previously fell through to npm install's own
//     docs because Install() classified "--help" as a bare name.
//   - `link --help` previously crashed with "local path does not
//     exist: --help" because InstallFromLocal was given the literal
//     string "--help" as the source path.
func TestHandlePluginCommandHelp(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	cases := []struct {
		name    string
		args    []string
		wantSub string // substring that must appear in the printed usage
	}{
		{"top-level -h", []string{"-h"}, "Usage: late plugin <command>"},
		{"top-level --help", []string{"--help"}, "Usage: late plugin <command>"},
		{"top-level 'help' word", []string{"help"}, "Usage: late plugin <command>"},

		{"install -h", []string{"install", "-h"}, "Usage: late plugin install"},
		{"install --help", []string{"install", "--help"}, "Usage: late plugin install"},
		{"install --help after source", []string{"install", "@late/foo", "--help"}, "Usage: late plugin install"},
		{"install help word", []string{"install", "help"}, "Usage: late plugin install"},

		{"link -h", []string{"link", "-h"}, "Usage: late plugin link"},
		{"link --help", []string{"link", "--help"}, "Usage: late plugin link"},
		{"link --project --help", []string{"link", "--project", "--help"}, "Usage: late plugin link"},

		{"list -h", []string{"list", "-h"}, "Usage: late plugin list"},
		{"remove -h", []string{"remove", "-h"}, "Usage: late plugin remove"},
		{"update -h", []string{"update", "-h"}, "Usage: late plugin update"},
		{"enable -h", []string{"enable", "-h"}, "Usage: late plugin enable"},
		{"disable -h", []string{"disable", "-h"}, "Usage: late plugin disable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stderr := captureStderr(t, func() {
				_ = HandlePluginCommand(pm, c.args)
			})
			if !strings.Contains(stderr, c.wantSub) {
				t.Errorf("HandlePluginCommand(%v) stderr missing %q\ngot: %s",
					c.args, c.wantSub, stderr)
			}
		})
	}
}

// TestHandlePluginCommandHelpShortCircuits verifies that the help
// path returns true (telling main to exit) without ever needing the
// pm argument to be valid. We hand in a freshly-constructed pm with
// no plugins and call install --help; if the install branch were
// reached, the call would attempt an HTTP marketplace lookup and
// very likely take seconds before returning. This test pins the
// sub-second behavior.
func TestHandlePluginCommandHelpShortCircuits(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	if !HandlePluginCommand(pm, []string{"install", "--help"}) {
		t.Error("expected HandlePluginCommand(install, --help) to return true")
	}
	if !HandlePluginCommand(pm, []string{"link", "--help"}) {
		t.Error("expected HandlePluginCommand(link, --help) to return true")
	}
}

// ensure we don't break the no-args usage path
func TestHandlePluginCommandNoArgs(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	stderr := captureStderr(t, func() {
		_ = HandlePluginCommand(pm, nil)
	})
	if !strings.Contains(stderr, "Usage: late plugin <command>") {
		t.Errorf("expected top-level usage on empty args, got: %s", stderr)
	}
}
