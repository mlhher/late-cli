package assets

import (
	"embed"
	"encoding/json"
)

//go:embed prompts/*.md subagents/*.json
var PromptsFS embed.FS

type SubagentConfig struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	PromptFile   string   `json:"prompt_file"`
	AllowedTools []string `json:"allowed_tools"`
	Async        bool     `json:"async,omitempty"`
}

func GetSubagents() []SubagentConfig {
	entries, err := PromptsFS.ReadDir("subagents")
	if err != nil {
		return nil
	}
	var configs []SubagentConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			data, err := PromptsFS.ReadFile("subagents/" + entry.Name())
			if err == nil {
				var config SubagentConfig
				if err := json.Unmarshal(data, &config); err == nil {
					configs = append(configs, config)
				}
			}
		}
	}
	return configs
}
