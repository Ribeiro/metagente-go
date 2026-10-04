package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/value"
)

// StateStore keeps the memory of agents. Each agent has one memory per
// context (requirement S7): calls in the same context share it, and another
// context never sees it.
type StateStore struct {
	mu     sync.Mutex
	limits config.Limits
	maps   map[string]*StateMap
}

// NewStateStore creates an empty store whose memories obey the limits.
func NewStateStore(limits config.Limits) *StateStore {
	return &StateStore{limits: limits, maps: map[string]*StateMap{}}
}

// For returns the memory of an agent in a context, creating it when needed.
func (s *StateStore) For(agent, contextID string) *StateMap {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := agent + "\x00" + contextID
	m, ok := s.maps[key]
	if !ok {
		m = &StateMap{
			items:      map[string]value.Value{},
			sizes:      map[string]int64{},
			maxEntries: s.limits.MaxStateEntries,
			maxBytes:   s.limits.MaxStateBytes,
		}
		s.maps[key] = m
	}
	return m
}

// Forget drops the memories of a context, of every agent. A server calls it when
// a conversation expires, or the memory of a long running server would only grow.
func (s *StateStore) Forget(contextID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	suffix := "\x00" + contextID
	for key := range s.maps {
		if strings.HasSuffix(key, suffix) {
			delete(s.maps, key)
		}
	}
}

// StateMap is one memory, with a ceiling on how much it holds (requirement D3).
type StateMap struct {
	mu         sync.Mutex
	items      map[string]value.Value
	sizes      map[string]int64
	total      int64
	maxEntries int
	maxBytes   int64
}

func sizeOf(key string, v value.Value) int64 {
	encoded, err := json.Marshal(v.ToJSON())
	if err != nil {
		return int64(len(key))
	}
	return int64(len(key) + len(encoded))
}

// Set remembers a value, or says that the memory is full.
func (m *StateMap) Set(key string, v value.Value) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	size := sizeOf(key, v)
	_, exists := m.items[key]
	if !exists && len(m.items) >= m.maxEntries {
		return memoryFull("it already holds %d things", m.maxEntries)
	}
	if next := m.total - m.sizes[key] + size; next > m.maxBytes {
		return memoryFull("it would hold more than %d bytes", m.maxBytes)
	}
	m.total += size - m.sizes[key]
	m.sizes[key] = size
	m.items[key] = v
	return nil
}

func memoryFull(why string, n any) error {
	return diag.Newf("the agent's memory is full: "+why, n).
		Fix("forget something first, or raise max_state_entries or max_state_bytes in the [limits] section of metagente.toml")
}

// Get recalls a value, or nothing.
func (m *StateMap) Get(key string) value.Value {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items[key]
}

// State is `tool state`: the agent's short term memory.
type State struct {
	decl *lang.ToolDecl
	mem  *StateMap
}

// NewState creates the tool over a memory.
func NewState(decl *lang.ToolDecl, mem *StateMap) *State {
	return &State{decl: decl, mem: mem}
}

func (s *State) Name() string { return s.decl.Name }

func (s *State) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(s.decl), nil
}

func (s *State) Call(_ context.Context, action string, args Args) (value.Value, error) {
	switch action {
	case "set":
		key, err := NeedText("state", "set", args, "key")
		if err != nil {
			return value.Nothing, err
		}
		if err := s.mem.Set(key, args["value"]); err != nil {
			return value.Nothing, err
		}
		return value.Nothing, nil
	case "get":
		key, err := NeedText("state", "get", args, "key")
		if err != nil {
			return value.Nothing, err
		}
		return s.mem.Get(key), nil
	}
	return value.Nothing, UnknownAction("state", action, []string{"set", "get"})
}
