// Package ingest accepts NDJSON event streams over HTTP and persists them
// via store.Store. Each line in the request body is one JSON event.
package ingest

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/dongqiu/agent-lens/internal/hashchain"
	"github.com/dongqiu/agent-lens/internal/store"
)

var errMissingField = errors.New("missing required field (session_id, kind, actor.type)")

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
	heads map[string]string // session_id -> head hash, in-memory cache
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
		stored, err := h.toStoreEvent(&ev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.st.AppendEvent(r.Context(), stored); err != nil {
			slog.Error("append event", "err", err)
			http.Error(w, "store error", http.StatusInternalServerError)
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

func (h *handler) toStoreEvent(in *wireEvent) (*store.Event, error) {
	if in.SessionID == "" || in.Kind == "" || in.Actor.Type == "" {
		return nil, errMissingField
	}
	if in.ID == "" {
		in.ID = ulid.Make().String()
	}
	if in.TS.IsZero() {
		in.TS = time.Now().UTC()
	}

	h.mu.Lock()
	prev := h.heads[in.SessionID]
	h.mu.Unlock()

	canonical, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	hash := hashchain.Compute(prev, canonical)

	h.mu.Lock()
	h.heads[in.SessionID] = hash
	h.mu.Unlock()

	return &store.Event{
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
	}, nil
}

