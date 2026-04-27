package store

import (
	"context"
	"sync"
)

// Memory is an in-memory Store used by tests and ephemeral workloads.
// It preserves append order and is safe for concurrent use.
type Memory struct {
	mu     sync.Mutex
	events []*Event
	byID   map[string]*Event
}

func NewMemory() *Memory {
	return &Memory{byID: map[string]*Event{}}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) AppendEvent(_ context.Context, e *Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byID[e.ID]; exists {
		return ErrDuplicate
	}
	cp := *e
	m.events = append(m.events, &cp)
	m.byID[cp.ID] = &cp
	return nil
}

func (m *Memory) GetEvent(_ context.Context, id string) (*Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (m *Memory) ListBySession(_ context.Context, sessionID string, limit int) ([]*Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Event
	for _, e := range m.events {
		if e.SessionID != sessionID {
			continue
		}
		cp := *e
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *Memory) HeadHash(_ context.Context, sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var head string
	for _, e := range m.events {
		if e.SessionID == sessionID {
			head = e.Hash
		}
	}
	return head, nil
}
