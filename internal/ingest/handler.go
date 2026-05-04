// Package ingest accepts NDJSON event streams over HTTP and persists them
// via store.Store. Each line in the request body is one JSON event.
//
// The package also exposes a programmatic Append entry point so other
// in-process producers (webhook receivers, background workers) can enter
// the same hash-chain pipeline without re-marshaling through the HTTP path.
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

	"github.com/dong-qiu/agent-lens/internal/hashchain"
	"github.com/dong-qiu/agent-lens/internal/store"
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
	"push":        {},
}

// Handler owns the per-session head-hash cache and is the single writer
// into the store. Construct one per process and share it between every
// ingest path (HTTP NDJSON, webhook, programmatic) so the cache stays
// authoritative.
//
// AfterAppend, when set, is invoked outside the per-handler lock with
// every successfully-stored event. It is the hook used by linkers,
// metrics, and any other observer that should not be on the write path.
type Handler struct {
	st    store.Store
	mu    sync.Mutex
	heads map[string]string
	after func(context.Context, *WireEvent)
}

func NewHandler(st store.Store) *Handler {
	return &Handler{st: st, heads: map[string]string{}}
}

// AfterAppend registers a hook called after every successful Append.
// The hook runs outside the handler's lock so it can do its own I/O
// without blocking other ingest paths.
//
// Concurrency contract: must be called during process startup, before
// any goroutine that calls Append (HTTP server, webhook, etc.) is
// started. The field is not protected for concurrent assignment;
// happens-before is established only via the goroutine spawn that
// later reads it. Multiple observers should be combined into a single
// callback by the caller.
func (h *Handler) AfterAppend(fn func(context.Context, *WireEvent)) {
	h.after = fn
}

// WireEvent is the JSON shape accepted on the wire. It mirrors
// proto/event.proto but uses json.RawMessage for payload so we don't
// re-marshal user content before hashing.
type WireEvent struct {
	ID        string          `json:"id,omitempty"`
	TS        time.Time       `json:"ts"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	Actor     WireActor       `json:"actor"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Parents   []string        `json:"parents,omitempty"`
	Refs      []string        `json:"refs,omitempty"`
}

type WireActor struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

// IngestNDJSON is the chi-compatible HTTP handler for POST /v1/events.
func (h *Handler) IngestNDJSON(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 1<<20), 8<<20) // 8 MiB max line

	var accepted int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev WireEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			http.Error(w, "bad ndjson line: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.Append(r.Context(), &ev); err != nil {
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

// Append validates an event, computes its hash chain entry, persists
// it, and advances the in-memory head cache. The lock spans only the
// load-prev → compute → append → cache update sequence; canonical
// marshaling happens before the lock, and the AfterAppend hook (if
// any) runs after the lock is released.
func (h *Handler) Append(ctx context.Context, in *WireEvent) error {
	if err := h.appendLocked(ctx, in); err != nil {
		return err
	}
	if h.after != nil {
		h.after(ctx, in)
	}
	return nil
}

func (h *Handler) appendLocked(ctx context.Context, in *WireEvent) error {
	if err := validateWireEvent(in); err != nil {
		return err
	}

	// ID assignment, ts default, and canonical marshal must happen under
	// the same lock that orders the append. Otherwise, two concurrent
	// goroutines can call ulid.Make() in one order and acquire h.mu in
	// the other — the resulting ids no longer reflect append order, and
	// the store's id-asc read order (see issue #38) drifts from the hash
	// chain. Marshal is also moved inside because canonical includes the
	// id; doing it outside before id assignment would race.
	h.mu.Lock()
	defer h.mu.Unlock()

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

func validateWireEvent(in *WireEvent) error {
	if in.SessionID == "" || in.Kind == "" || in.Actor.Type == "" {
		return errMissingField
	}
	if _, ok := validKinds[in.Kind]; !ok {
		return errInvalidKind
	}
	return nil
}

func (h *Handler) writeAppendError(w http.ResponseWriter, err error) {
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

// RegisterRoutes wires the ingest endpoints onto r. Used by
// `internal/ingest`'s own tests via NewRouter; production callers
// (cmd/agent-lens) construct a Handler explicitly so they can share
// it with other producers.
func RegisterRoutes(r chi.Router, st store.Store) {
	h := NewHandler(st)
	r.Post("/events", h.IngestNDJSON)
}

func NewRouter(st store.Store) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, st)
	return r
}
