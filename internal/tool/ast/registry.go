package ast

import "runtime"

// NewParser returns the platform-appropriate parser adapter for the given
// platform. Passing an explicit platform overrides runtime detection, which is
// useful for cross-platform unit tests.
//
// cwd is the working-directory context passed to adapters that need path
// resolution (currently only WindowsParser).
func NewParser(platform Platform, cwd string) Parser {
	switch platform {
	case PlatformWindows:
		return &WindowsParser{Cwd: cwd}
	default:
		return &UnixParser{}
	}
}

// CurrentPlatform returns the detected Platform for the running OS.
func CurrentPlatform() Platform {
	if runtime.GOOS == "windows" {
		return PlatformWindows
	}
	return PlatformUnix
}
