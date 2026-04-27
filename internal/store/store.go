package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("event not found")
	ErrDuplicate = errors.New("event id already exists")
)

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

// Link is a causal or semantic relation between two events. Links are
// stored separately from the append-only event log so they can be
// re-derived without rewriting history.
type Link struct {
	FromEvent  string
	ToEvent    string
	Relation   string
	Confidence float32
	InferredBy string
}

// Store is the persistence interface used by ingest, query, and linking
// layers.
//
// ListBySession returns events for sessionID in append order. A limit of
// zero or negative means "no limit"; both implementations honor this.
//
// EventsByRef returns events whose refs array contains ref, in append
// order. Used by the linking worker to find peers for a given git/PR ref.
//
// AppendLink inserts a link; ErrDuplicate is returned if (from, to,
// relation) already exists. LinksForEvent returns all links touching
// eventID in either direction.
type Store interface {
	AppendEvent(ctx context.Context, e *Event) error
	GetEvent(ctx context.Context, id string) (*Event, error)
	ListBySession(ctx context.Context, sessionID string, limit int) ([]*Event, error)
	HeadHash(ctx context.Context, sessionID string) (string, error)
	EventsByRef(ctx context.Context, ref string) ([]*Event, error)
	AppendLink(ctx context.Context, l *Link) error
	LinksForEvent(ctx context.Context, eventID string) ([]*Link, error)
	Close() error
}
