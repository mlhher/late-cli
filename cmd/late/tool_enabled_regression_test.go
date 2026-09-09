package main

import "testing"

func TestToolEnabled_PreservesDisabledMCPNameWithPunctuation(t *testing.T) {
	// The MCP adapter sanitizes query:docs.v1 to query_docs_v1.
	if toolEnabled(map[string]bool{"query:docs.v1": false}, "context_7__query_docs_v1") {
		t.Fatal("previously disabled MCP tool was re-enabled after name sanitization")
	}
}
