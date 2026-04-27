// Package github receives GitHub webhook deliveries, verifies the
// shared-secret HMAC, maps known event types into wire events, and
// forwards them through the ingest pipeline.
//
// Supported events: pull_request, pull_request_review, push, ping.
// Unrecognized event types are politely 204-acked so GitHub stops
// retrying without surfacing as a webhook failure.
package github

import (
	"encoding/json"
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

// mapper turns a webhook body into a wire event. Returning (nil, nil)
// from a mapper means "ignore this delivery" (e.g. a branch-delete
// push that we don't want to record).
type mapper func(body json.RawMessage, deliveryID string) (*ingest.WireEvent, error)

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
	event := r.Header.Get("X-GitHub-Event")

	if event == "ping" {
		// GitHub sends this on webhook creation; just ack.
		w.WriteHeader(http.StatusOK)
		return
	}

	var m mapper
	switch event {
	case "pull_request":
		m = mapPullRequest
	case "pull_request_review":
		m = mapPullRequestReview
	case "push":
		m = mapPush
	default:
		// Unrecognized event types are not an error; GitHub may send
		// many we don't (yet) care about. Just ack so it doesn't retry.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.forward(w, r, body, deliveryID, event, m)
}

func (h *Handler) forward(
	w http.ResponseWriter,
	r *http.Request,
	body json.RawMessage,
	deliveryID, event string,
	m mapper,
) {
	ev, err := m(body, deliveryID)
	if err != nil {
		slog.Warn("github webhook map", "event", event, "delivery", deliveryID, "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ev == nil {
		// Mapper chose to ignore this delivery.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch err := h.ingest.Append(r.Context(), ev); {
	case err == nil:
		slog.Info("github webhook accepted", "event", event, "delivery", deliveryID, "session", ev.SessionID)
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, store.ErrDuplicate):
		// GitHub redelivery: the delivery UUID is our event ID, so this
		// is a true duplicate. Ack 200 so GitHub stops retrying without
		// the operator seeing it as a webhook failure.
		slog.Info("github webhook duplicate", "event", event, "delivery", deliveryID, "session", ev.SessionID)
		w.WriteHeader(http.StatusOK)
	default:
		slog.Error("github webhook append", "event", event, "delivery", deliveryID, "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
	}
}
