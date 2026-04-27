// Package github receives GitHub webhook deliveries, verifies the
// shared-secret HMAC, maps known event types into wire events, and
// forwards them through the ingest pipeline. M2-A handles
// `pull_request`; later slices add review and push events.
package github

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/store"
)

// Handler is the http.Handler for POST /webhooks/github.
type Handler struct {
	secret  []byte
	ingest  *ingest.Handler
	maxBody int64
}

func NewHandler(secret string, h *ingest.Handler) *Handler {
	return &Handler{
		secret:  []byte(secret),
		ingest:  h,
		maxBody: 5 << 20, // 5 MiB; GitHub caps at 25 MiB but our PR events are small
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

	if !verifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")

	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		// GitHub sends this on webhook creation; just ack.
		w.WriteHeader(http.StatusOK)
		return
	case "pull_request":
		ev, err := mapPullRequest(body, deliveryID)
		if err != nil {
			slog.Warn("github pull_request map", "delivery", deliveryID, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch err := h.ingest.Append(r.Context(), ev); {
		case err == nil:
			slog.Info("github pull_request accepted", "delivery", deliveryID, "session", ev.SessionID)
			w.WriteHeader(http.StatusAccepted)
		case errors.Is(err, store.ErrDuplicate):
			// GitHub redeliveries land here. The delivery UUID is the
			// event ID, so this is a true duplicate; ack 200 so GitHub
			// stops retrying without surfacing as a webhook failure.
			slog.Info("github pull_request duplicate", "delivery", deliveryID, "session", ev.SessionID)
			w.WriteHeader(http.StatusOK)
		default:
			slog.Error("github pull_request append", "delivery", deliveryID, "err", err)
			http.Error(w, "store error", http.StatusInternalServerError)
		}
	default:
		// Unrecognized event types are not an error; GitHub may send
		// many we don't (yet) care about. Just ack so it doesn't retry.
		w.WriteHeader(http.StatusNoContent)
	}
}
