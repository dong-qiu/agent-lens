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

// Ping always succeeds: the in-memory store has no external dependency to
// fail. Keeps /healthz honest for memory-mode dogfood runs.
func (m *Memory) Ping(context.Context) error { return nil }

// UsageEventsBySessions returns each requested session's events in append
// order (issue #65). Unlike Postgres it does not pre-filter to usage-bearing
// events — the events are already in memory, so the caller's aggregator
// skipping non-usage events costs nothing and the totals are identical.
func (m *Memory) UsageEventsBySessions(_ context.Context, ids []string) (map[string][]*Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := map[string][]*Event{}
	for _, e := range m.events {
		if _, ok := want[e.SessionID]; ok {
			out[e.SessionID] = append(out[e.SessionID], e)
		}
	}
	return out, nil
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

// HeadHash returns the hash of the last-appended event for sessionID,
// identified by max id (ULIDs are monotonic at insert) — matches
// Postgres's `ORDER BY id DESC LIMIT 1`. ts is set on the hook's wall
// clock and can be skewed across concurrent hooks, so it cannot be used
// to identify the head. See issue #38.
func (m *Memory) HeadHash(_ context.Context, sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		maxID string
		head  string
	)
	for _, e := range m.events {
		if e.SessionID != sessionID {
			continue
		}
		if e.ID > maxID {
			maxID = e.ID
			head = e.Hash
		}
	}
	return head, nil
}

func (m *Memory) EventsBeforeID(_ context.Context, sessionID, eventID string, limit int) ([]*Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// m.events is in append order, which equals id-asc per ADR / store
	// invariant. Walk it and collect everything in the same session
	// with id < eventID. Tail-trim to `limit` so we keep the closest
	// (largest id, i.e. most recent) ancestors.
	var matched []*Event
	for _, e := range m.events {
		if e.SessionID != sessionID {
			continue
		}
		if e.ID >= eventID {
			continue
		}
		cp := *e
		matched = append(matched, &cp)
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched, nil
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

// LinksForEvents is the batched form of LinksForEvent (issue #20). Each link
// is filed under whichever requested endpoint it touches; self-links file once.
func (m *Memory) LinksForEvents(_ context.Context, ids []string) (map[string][]*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := map[string][]*Link{}
	for _, l := range m.links {
		cp := l
		if _, ok := want[l.FromEvent]; ok {
			out[l.FromEvent] = append(out[l.FromEvent], &cp)
		}
		if l.ToEvent != l.FromEvent {
			if _, ok := want[l.ToEvent]; ok {
				out[l.ToEvent] = append(out[l.ToEvent], &cp)
			}
		}
	}
	return out, nil
}

func (m *Memory) LinksForSession(_ context.Context, sessionID string) ([]*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Index event id → session id for O(1) endpoint lookup.
	idToSession := make(map[string]string, len(m.events))
	for _, e := range m.events {
		idToSession[e.ID] = e.SessionID
	}
	var out []*Link
	for _, l := range m.links {
		if idToSession[l.FromEvent] == sessionID || idToSession[l.ToEvent] == sessionID {
			cp := l
			out = append(out, &cp)
		}
	}
	return out, nil
}
