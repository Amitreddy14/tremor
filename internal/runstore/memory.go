package runstore

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore is an in-memory Store for tests and fakes.
type MemoryStore struct {
	mu   sync.Mutex
	runs map[string]Run
}

// Save inserts or replaces a run.
func (m *MemoryStore) Save(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs == nil {
		m.runs = map[string]Run{}
	}
	m.runs[r.ID] = r
	return nil
}

// List returns matching runs, newest first.
func (m *MemoryStore) List(_ context.Context, f Filter) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Run
	for _, r := range m.runs {
		if f.Namespace != "" && r.Namespace != f.Namespace {
			continue
		}
		if f.Name != "" && r.Name != f.Name {
			continue
		}
		if f.FaultType != "" && r.FaultType != f.FaultType {
			continue
		}
		if f.Result != "" && r.Result != f.Result {
			continue
		}
		if !f.Since.IsZero() && r.StartedAt.Before(f.Since) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Get returns the run with the given ID.
func (m *MemoryStore) Get(_ context.Context, id string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return r, nil
}

// Len returns the number of stored runs.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}
