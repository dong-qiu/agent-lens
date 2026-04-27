package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dongqiu/agent-lens/internal/auth"
	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/query"
	"github.com/dongqiu/agent-lens/internal/store"
	githubwh "github.com/dongqiu/agent-lens/internal/webhooks/github"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	addr := envOr("AGENT_LENS_ADDR", ":8787")
	pgDSN := envOr("AGENT_LENS_PG_DSN", "postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := store.OpenPostgres(ctx, pgDSN)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	token := os.Getenv("AGENT_LENS_TOKEN")
	if token == "" {
		slog.Warn("AGENT_LENS_TOKEN is empty; /v1 endpoints are unauthenticated")
	}

	// One Handler, shared between the HTTP NDJSON path and any other
	// in-process producers (e.g. webhook receivers) so the head-hash
	// cache stays authoritative.
	ingestH := ingest.NewHandler(st)

	r.Route("/v1", func(sub chi.Router) {
		sub.Use(auth.RequireToken(token))
		sub.Post("/events", ingestH.IngestNDJSON)
		query.RegisterRoutes(sub, st)
	})

	// /webhooks/github is always mounted so operators get an
	// actionable 503 when the secret is missing (rather than a bare
	// chi 404 that's indistinguishable from a typo).
	if secret := os.Getenv("AGENT_LENS_GH_WEBHOOK_SECRET"); secret != "" {
		r.Post("/webhooks/github", githubwh.NewHandler(secret, ingestH).ServeHTTP)
		slog.Info("github webhook enabled", "path", "/webhooks/github")
	} else {
		r.Post("/webhooks/github", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "webhook receiver disabled (AGENT_LENS_GH_WEBHOOK_SECRET unset)", http.StatusServiceUnavailable)
		})
		slog.Info("AGENT_LENS_GH_WEBHOOK_SECRET unset; /webhooks/github returns 503")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("agent-lens listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
