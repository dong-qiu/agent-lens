package store

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("event not found")

// Event is the storage-layer representation. It mirrors proto/event.proto but
// keeps payload as raw JSON so the store does not depend on generated pb.
type Event struct {
	ID        string
	TS        time.Time
	SessionID string
	TurnID    string
	ActorType string
	ActorID   string
	ActorModel string
	Kind      string
	Payload   []byte // canonical JSON
	Parents   []string
	Refs      []string
	Hash      string
	PrevHash  string
	Sig       []byte
}

// Store is the persistence interface used by ingest and query layers.
//
// ListBySession returns events for sessionID in append order. A limit of
// zero or negative means "no limit"; both implementations honor this.
type Store interface {
	AppendEvent(ctx context.Context, e *Event) error
	GetEvent(ctx context.Context, id string) (*Event, error)
	ListBySession(ctx context.Context, sessionID string, limit int) ([]*Event, error)
	HeadHash(ctx context.Context, sessionID string) (string, error)
	Close() error
}
