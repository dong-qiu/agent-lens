package store

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-memory Store used by tests and ephemeral workloads.
// It preserves append order and is safe for concurrent use.
type Memory struct {
	mu     sync.Mutex
	events []*Event
	byID   map[string]*Event
	links  map[string]Link // key: from|to|relation
}

func NewMemory() *Memory {
	return &Memory{
		byID:  map[string]*Event{},
		links: map[string]Link{},
	}
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

// HeadHash returns the hash of the latest event for sessionID, where
// "latest" matches Postgres's `ORDER BY ts DESC, id DESC LIMIT 1`:
// max ts wins, ULID lexical max breaks ties. This keeps the two impls
// in lockstep when clients send out-of-order timestamps.
func (m *Memory) HeadHash(_ context.Context, sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		maxTS time.Time
		maxID string
		head  string
	)
	for _, e := range m.events {
		if e.SessionID != sessionID {
			continue
		}
		if e.TS.After(maxTS) || (e.TS.Equal(maxTS) && e.ID > maxID) {
			maxTS = e.TS
			maxID = e.ID
			head = e.Hash
		}
	}
	return head, nil
}

func (m *Memory) ListSessions(_ context.Context, limit int, since time.Time) ([]*SessionSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[string]*SessionSummary{}
	for _, e := range m.events {
		s, ok := agg[e.SessionID]
		if !ok {
			s = &SessionSummary{ID: e.SessionID, FirstEventAt: e.TS, LastEventAt: e.TS}
			agg[e.SessionID] = s
		}
		s.EventCount++
		if e.TS.Before(s.FirstEventAt) {
			s.FirstEventAt = e.TS
		}
		if e.TS.After(s.LastEventAt) {
			s.LastEventAt = e.TS
		}
	}
	out := make([]*SessionSummary, 0, len(agg))
	for _, s := range agg {
		if !since.IsZero() && s.LastEventAt.Before(since) {
			continue
		}
		cp := *s
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastEventAt.Equal(out[j].LastEventAt) {
			return out[i].LastEventAt.After(out[j].LastEventAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) EventsByRef(_ context.Context, ref string) ([]*Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Event
	for _, e := range m.events {
		for _, r := range e.Refs {
			if r == ref {
				cp := *e
				out = append(out, &cp)
				break
			}
		}
	}
	return out, nil
}

func (m *Memory) AppendLink(_ context.Context, l *Link) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := l.FromEvent + "|" + l.ToEvent + "|" + l.Relation
	if _, exists := m.links[key]; exists {
		return ErrDuplicate
	}
	m.links[key] = *l
	return nil
}

func (m *Memory) LinksForEvent(_ context.Context, eventID string) ([]*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Link
	for _, l := range m.links {
		if l.FromEvent == eventID || l.ToEvent == eventID {
			cp := l
			out = append(out, &cp)
		}
	}
	return out, nil
}
