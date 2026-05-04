package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dong-qiu/agent-lens/internal/auth"
	"github.com/dong-qiu/agent-lens/internal/ingest"
	"github.com/dong-qiu/agent-lens/internal/linking"
	"github.com/dong-qiu/agent-lens/internal/migrate"
	"github.com/dong-qiu/agent-lens/internal/query"
	"github.com/dong-qiu/agent-lens/internal/store"
	"github.com/dong-qiu/agent-lens/internal/webui"
	deploywh "github.com/dong-qiu/agent-lens/internal/webhooks/deploy"
	githubwh "github.com/dong-qiu/agent-lens/internal/webhooks/github"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	addr := envOr("AGENT_LENS_ADDR", ":8787")
	pgDSN := envOr("AGENT_LENS_PG_DSN", "postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable")
	storeKind := envOr("AGENT_LENS_STORE", "postgres")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// AGENT_LENS_STORE=memory enables an in-process store for local
	// dogfood / kicking the tyres without a Postgres dependency. Events
	// vanish on restart — fine for §17 self-observation runs and demo
	// flows, NOT a production option (no durability, no concurrent
	// readers across processes).
	var st store.Store
	switch storeKind {
	case "memory":
		st = store.NewMemory()
		slog.Info("store: memory (ephemeral; events lost on restart)")
	case "postgres", "":
		// Apply embedded migrations on startup so personal-mode users
		// don't need a separate `golang-migrate` install (issue #12).
		// AGENT_LENS_SKIP_MIGRATE=1 lets ops with externally-managed
		// schemas opt out (e.g. Helm + Atlas / sqitch).
		if os.Getenv("AGENT_LENS_SKIP_MIGRATE") == "" {
			if err := migrate.Up(pgDSN); err != nil {
				slog.Error("migrate up", "err", err)
				os.Exit(1)
			}
			slog.Info("migrations applied")
		} else {
			slog.Info("AGENT_LENS_SKIP_MIGRATE set; skipping embedded migrations")
		}

		pg, err := store.OpenPostgres(ctx, pgDSN)
		if err != nil {
			slog.Error("open store", "err", err)
			os.Exit(1)
		}
		st = pg
	default:
		slog.Error("unknown AGENT_LENS_STORE", "value", storeKind, "want", "postgres|memory")
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

	// Linking worker observes successful appends and writes shared-ref
	// links asynchronously so it never blocks ingest.
	linker := linking.New(st, 1024)
	ingestH.AfterAppend(func(_ context.Context, ev *ingest.WireEvent) {
		linker.Notify(ev)
	})
	var linkerWG sync.WaitGroup
	linkerWG.Add(1)
	go func() {
		defer linkerWG.Done()
		linker.Run(ctx)
	}()

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

	// /webhooks/deploy uses bearer-token auth (most deploy systems
	// don't sign webhook bodies). Token is separate from /v1's
	// AGENT_LENS_TOKEN so a deploy system gets write-only credentials.
	if deployToken := os.Getenv("AGENT_LENS_DEPLOY_WEBHOOK_TOKEN"); deployToken != "" {
		r.Group(func(authed chi.Router) {
			authed.Use(auth.RequireToken(deployToken))
			authed.Post("/webhooks/deploy", deploywh.NewHandler(ingestH).ServeHTTP)
		})
		slog.Info("deploy webhook enabled", "path", "/webhooks/deploy")
	} else {
		r.Post("/webhooks/deploy", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "deploy webhook receiver disabled (AGENT_LENS_DEPLOY_WEBHOOK_TOKEN unset)", http.StatusServiceUnavailable)
		})
		slog.Info("AGENT_LENS_DEPLOY_WEBHOOK_TOKEN unset; /webhooks/deploy returns 503")
	}

	// Static UI handler at /. Registered last so /healthz, /v1/*,
	// /webhooks/* take precedence (chi matches in registration order
	// only for explicit routes; the catch-all serves what's left).
	// When the embedded UI is missing (no `make embed-webui` run),
	// the handler returns a helpful 503 stub at /.
	if webui.Available() {
		slog.Info("ui: embedded bundle available; serving at /")
	} else {
		slog.Info("ui: not embedded; / returns dev-mode stub. Run `make embed-webui` for prod.")
	}
	r.Mount("/", webui.Handler())

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
	// Linker.Run returns once ctx is cancelled; wait so any in-flight
	// AppendLink finishes (or errors out cleanly) before we exit and
	// the process tears down the DB connection pool.
	linkerWG.Wait()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
