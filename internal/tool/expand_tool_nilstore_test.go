package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// TestExpandTool_NilStore: a tool wired without a store must fail with a
// clear error result instead of panicking on the nil interface — the agent
// sees an error, never a crash.
func TestExpandTool_NilStore(t *testing.T) {
	e := ExpandTool{Store: nil}
	_, err := e.Execute(context.Background(), json.RawMessage(`{"id":"r:1a2b3c4d"}`))
	if err == nil {
		t.Fatal("Execute with a nil store succeeded; want an error result")
	}
}
