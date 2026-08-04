package main

import (
	"testing"
)

// TestPluginInlineTool_RequiresConfirmation guards the documented contract
// that plugin inline tools (arbitrary scripts) go through the normal user
// confirmation flow. plugin-example.md: "user confirmation still prompts
// the user"; plugin-sdk.md: "plugin tools respect user confirmation".
func TestPluginInlineTool_RequiresConfirmation(t *testing.T) {
	tool := pluginInlineTool{name: "example:lookup"}
	if !tool.RequiresConfirmation(nil) {
		t.Error("plugin inline tool must require confirmation before running its script")
	}
}
