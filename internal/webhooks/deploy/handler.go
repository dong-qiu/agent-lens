// Package deploy receives deploy notifications and forwards them as
// kind=deploy events into the ingest pipeline.
//
// Auth: this package contains no auth logic; the route is gated by
// internal/auth.RequireToken middleware in cmd/agent-lens (using a
// dedicated AGENT_LENS_DEPLOY_WEBHOOK_TOKEN so a deploy system can be
// granted write access without any /v1 read permissions).
//
// Idempotency: clients may set the `Idempotency-Key` header; the value
// becomes the wire event id, so a redelivery hits store.ErrDuplicate
// at the store layer and the handler responds 200 OK. Without the
// header each delivery is a fresh ULID and dedup is the client's
// responsibility.
package deploy

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/dong-qiu/agent-lens/internal/ingest"
	"github.com/dong-qiu/agent-lens/internal/store"
)

// Handler is the http.Handler for POST /webhooks/deploy.
type Handler struct {
	ingest  *ingest.Handler
	maxBody int64
}

func NewHandler(h *ingest.Handler) *Handler {
	return &Handler{
		ingest:  h,
		maxBody: 1 << 20, // 1 MiB; deploy payloads are small
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	idempotencyKey := r.Header.Get("Idempotency-Key")

	ev, err := mapDeploy(body, idempotencyKey)
	if err != nil {
		slog.Warn("deploy webhook map", "idempotency_key", idempotencyKey, "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch err := h.ingest.Append(r.Context(), ev); {
	case err == nil:
		slog.Info("deploy webhook accepted", "session", ev.SessionID, "idempotency_key", idempotencyKey)
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, store.ErrDuplicate):
		// Idempotency-Key matched a prior delivery; just ack.
		slog.Info("deploy webhook duplicate", "session", ev.SessionID, "idempotency_key", idempotencyKey)
		w.WriteHeader(http.StatusOK)
	default:
		slog.Error("deploy webhook append", "idempotency_key", idempotencyKey, "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
	}
}
