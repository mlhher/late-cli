package common

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestLateConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", `C:\Users\Test\AppData\Roaming`)
		t.Setenv("APPDATA", `C:\Users\Test\AppData\Roaming`)
		got, err := LateConfigDir()
		if err != nil {
			t.Fatalf("LateConfigDir() error = %v", err)
		}
		want := filepath.Join(`C:\Users\Test\AppData\Roaming`, "late")
		if got != want {
			t.Fatalf("LateConfigDir() = %q, want %q", got, want)
		}
		return
	}

	t.Setenv("HOME", "/tmp/late-home")
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := LateConfigDir()
	if err != nil {
		t.Fatalf("LateConfigDir() error = %v", err)
	}

	var want string
	if runtime.GOOS == "darwin" {
		want = filepath.Join("/tmp/late-home", "Library", "Application Support", "late")
	} else {
		want = filepath.Join("/tmp/late-home", ".config", "late")
	}

	if got != want {
		t.Fatalf("LateConfigDir() = %q, want %q", got, want)
	}
}

func TestLateSessionDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", `C:\Users\Test\AppData\Roaming`)
		t.Setenv("APPDATA", `C:\Users\Test\AppData\Roaming`)
		got, err := LateSessionDir()
		if err != nil {
			t.Fatalf("LateSessionDir() error = %v", err)
		}
		want := filepath.Join(`C:\Users\Test\AppData\Roaming`, "late", "sessions")
		if got != want {
			t.Fatalf("LateSessionDir() = %q, want %q", got, want)
		}
		return
	}

	t.Setenv("HOME", "/tmp/late-home")
	got, err := LateSessionDir()
	if err != nil {
		t.Fatalf("LateSessionDir() error = %v", err)
	}
	want := filepath.Join("/tmp/late-home", ".local", "share", "late", "sessions")
	if got != want {
		t.Fatalf("LateSessionDir() = %q, want %q", got, want)
	}
}
