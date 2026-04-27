// Package ingest accepts NDJSON event streams over HTTP and persists them
// via store.Store. Each line in the request body is one JSON event.
//
// Concurrency model: a single handler-wide mutex serializes the
// "load head → compute hash → append → update cache" sequence so
// concurrent appends to the same session never fork the chain. The
// trade-off is that all sessions share one writer; for v1 single-node
// throughput this is acceptable. Per-session locking is a future
// optimization, tracked separately.
package ingest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/dongqiu/agent-lens/internal/hashchain"
	"github.com/dongqiu/agent-lens/internal/store"
)

var (
	errMissingField = errors.New("missing required field (session_id, kind, actor.type)")
	errInvalidKind  = errors.New("invalid kind")
)

// validKinds is the canonical lowercase set accepted on the wire. New
// kinds must also be added to proto/event.proto and the GraphQL enum.
var validKinds = map[string]struct{}{
	"prompt":      {},
	"thought":     {},
	"tool_call":   {},
	"tool_result": {},
	"code_change": {},
	"commit":      {},
	"pr":          {},
	"test_run":    {},
	"build":       {},
	"deploy":      {},
	"review":      {},
	"decision":    {},
}

// RegisterRoutes wires the ingest endpoints onto r. It is the primary
// entry point used by the main server. NewRouter wraps it for tests and
// standalone embedding.
func RegisterRoutes(r chi.Router, st store.Store) {
	h := &handler{st: st, heads: map[string]string{}}
	r.Post("/events", h.ingest)
}

func NewRouter(st store.Store) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, st)
	return r
}

type handler struct {
	st    store.Store
	mu    sync.Mutex
	heads map[string]string // session_id -> last appended hash; cache backed by store.HeadHash
}

// wireEvent is the JSON shape accepted on the wire. It mirrors
// proto/event.proto but uses json.RawMessage for payload so we don't
// re-marshal user content before hashing.
type wireEvent struct {
	ID        string          `json:"id,omitempty"`
	TS        time.Time       `json:"ts"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	Actor     wireActor       `json:"actor"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Parents   []string        `json:"parents,omitempty"`
	Refs      []string        `json:"refs,omitempty"`
}

type wireActor struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

func (h *handler) ingest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 1<<20), 8<<20) // 8 MiB max line

	var accepted int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev wireEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			http.Error(w, "bad ndjson line: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.appendOne(r.Context(), &ev); err != nil {
			h.writeAppendError(w, err)
			return
		}
		accepted++
	}
	if err := scanner.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": accepted})
}

// appendOne validates an event, computes its hash chain entry, persists
// it, and advances the in-memory head cache. The lock spans only the
// load-prev → compute → append → cache update sequence; canonical
// marshaling happens before the lock since it does not depend on prev.
func (h *handler) appendOne(ctx context.Context, in *wireEvent) error {
	if err := validateWireEvent(in); err != nil {
		return err
	}
	if in.ID == "" {
		in.ID = ulid.Make().String()
	}
	if in.TS.IsZero() {
		in.TS = time.Now().UTC()
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	prev, hit := h.heads[in.SessionID]
	if !hit {
		loaded, err := h.st.HeadHash(ctx, in.SessionID)
		if err != nil {
			return fmt.Errorf("load head: %w", err)
		}
		prev = loaded
	}
	hash := hashchain.Compute(prev, canonical)

	ev := &store.Event{
		ID:         in.ID,
		TS:         in.TS,
		SessionID:  in.SessionID,
		TurnID:     in.TurnID,
		ActorType:  in.Actor.Type,
		ActorID:    in.Actor.ID,
		ActorModel: in.Actor.Model,
		Kind:       in.Kind,
		Payload:    in.Payload,
		Parents:    in.Parents,
		Refs:       in.Refs,
		Hash:       hash,
		PrevHash:   prev,
	}
	if err := h.st.AppendEvent(ctx, ev); err != nil {
		// Cache is intentionally not updated on append failure so the
		// next attempt re-reads the actual head.
		return err
	}
	h.heads[in.SessionID] = hash
	return nil
}

func validateWireEvent(in *wireEvent) error {
	if in.SessionID == "" || in.Kind == "" || in.Actor.Type == "" {
		return errMissingField
	}
	if _, ok := validKinds[in.Kind]; !ok {
		return errInvalidKind
	}
	return nil
}

func (h *handler) writeAppendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMissingField), errors.Is(err, errInvalidKind):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrDuplicate):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		slog.Error("ingest append", "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
	}
}
