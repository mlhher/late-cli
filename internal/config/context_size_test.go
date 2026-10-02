package config

import (
	"encoding/json"
	"testing"
)

// TestModelSettingContextSizeOverride pins the context-size-tokens resolver:
// positive values override, zero/unset means "unknown — discovery only",
// and negative values are ignored (they cannot describe a real window).
func TestModelSettingContextSizeOverride(t *testing.T) {
	tests := []struct {
		name    string
		setting ModelSetting
		wantVal int
		wantOK  bool
	}{
		{"declared", ModelSetting{ContextSizeTokens: 32768}, 32768, true},
		{"unset", ModelSetting{}, 0, false},
		{"negative ignored", ModelSetting{ContextSizeTokens: -5}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.setting.ContextSizeOverride()
			if got != tt.wantVal || ok != tt.wantOK {
				t.Errorf("ContextSizeOverride() = (%d, %v), want (%d, %v)", got, ok, tt.wantVal, tt.wantOK)
			}
		})
	}
}

// TestUnmarshalConfig_ContextSizeTokensAccepted pins that the per-model
// key decodes from config.json and survives into the struct.
func TestUnmarshalConfig_ContextSizeTokensAccepted(t *testing.T) {
	content := []byte(`{"models": [{"id": "local", "url": "http://a:8080", "key": "", "model": "m", "context-size-tokens": 32768}]}`)
	var cfg Config
	if err := json.Unmarshal(content, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("got %d models entries, want 1", len(cfg.Models))
	}
	if got := cfg.Models[0].ContextSizeTokens; got != 32768 {
		t.Errorf("ContextSizeTokens = %d, want 32768", got)
	}
}

// TestUnmarshalConfig_TopLevelContextSizeTokensAccepted pins the
// single-model fallback: a config without models[]/agent_models can
// declare the window at the top level and it survives into the struct
// (main.go applies it when no agent_models entry exists).
func TestUnmarshalConfig_TopLevelContextSizeTokensAccepted(t *testing.T) {
	content := []byte(`{"openai_base_url": "http://a:8080", "openai_model": "m", "context-size-tokens": 16384}`)
	var cfg Config
	if err := json.Unmarshal(content, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := cfg.ContextSizeTokens; got != 16384 {
		t.Errorf("top-level ContextSizeTokens = %d, want 16384", got)
	}
}
