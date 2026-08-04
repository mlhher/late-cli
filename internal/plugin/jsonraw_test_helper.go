package plugin

import "encoding/json"

// jsonRaw wraps a JSON string literal as json.RawMessage for use in tests.
// Kept tiny so production code is not affected.
func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }
