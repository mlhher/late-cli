package common

import (
	"encoding/json"
	"strings"
)

// ExternalTool is one entry of the enabled_external_tools config: the
// binary to run plus an optional description shown to the model so it
// knows what the tool does and which arguments it takes.
type ExternalTool struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// MarshalJSON writes the entry back as a plain path string when there is
// no description, or {"path": "...", "description": "..."} otherwise.
// Value receiver so it also applies to map values.
func (t ExternalTool) MarshalJSON() ([]byte, error) {
	path := strings.TrimSpace(t.Path)
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		return json.Marshal(path)
	}
	return json.Marshal(struct {
		Path        string `json:"path"`
		Description string `json:"description"`
	}{
		Path:        path,
		Description: desc,
	})
}

// UnmarshalJSON accepts either a plain string (the binary path, the form
// every existing config.json uses) or an object of the shape
// {"path": "...", "description": "..."}.
func (t *ExternalTool) UnmarshalJSON(data []byte) error {
	var path string
	if err := json.Unmarshal(data, &path); err == nil {
		t.Path = strings.TrimSpace(path)
		return nil
	}
	var object struct {
		Path        string `json:"path"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	t.Path = strings.TrimSpace(object.Path)
	t.Description = strings.TrimSpace(object.Description)
	return nil
}
