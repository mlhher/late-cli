package common

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// Tool represents a primitive tool that the agent can execute.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	RequiresConfirmation(args json.RawMessage) bool
	CallString(args json.RawMessage) string
}

// ToolRegistry stores available tools.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

func (r *ToolRegistry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		all = append(all, t)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Name() < all[j].Name()
	})
	return all
}
