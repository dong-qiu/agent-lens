//go:build integration

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const migrationPath = "../../migrations/0001_init.up.sql"

func loadSchema(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return string(b)
}

func TestPostgresAppendAndList(t *testing.T) {
	ctx := context.Background()

	pgC, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("agentlens"),
		postgres.WithUsername("agentlens"),
		postgres.WithPassword("agentlens"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	// Apply schema.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := pool.Exec(ctx, loadSchema(t)); err != nil {
		t.Fatalf("schema: %v", err)
	}
	pool.Close()

	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	events := []*Event{
		{ID: "01H1", TS: now, SessionID: "s1", ActorType: "human", ActorID: "alice", Kind: "prompt", Hash: "h1"},
		{ID: "01H2", TS: now.Add(time.Second), SessionID: "s1", ActorType: "agent", ActorID: "claude", Kind: "tool_call", Hash: "h2", PrevHash: "h1"},
	}
	for _, e := range events {
		if err := st.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}

	got, err := st.ListBySession(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[1].PrevHash != got[0].Hash {
		t.Errorf("chain broken: got[1].prev = %q, want %q", got[1].PrevHash, got[0].Hash)
	}

	head, err := st.HeadHash(ctx, "s1")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head != "h2" {
		t.Errorf("head = %q, want h2", head)
	}
}

// TestPostgresAcceptsNilParentsAndRefs guards against the regression caught
// during the M1 smoke test: pgx serializes a Go nil []string as SQL NULL,
// which violates the NOT NULL constraint on parents/refs. The store layer
// must normalize nil to an empty slice before inserting.
func TestPostgresAcceptsNilParentsAndRefs(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openPostgresWithSchema(ctx, t)
	defer cleanup()

	e := &Event{
		ID:        "01HNILSLICES",
		TS:        time.Now().UTC(),
		SessionID: "s-nil",
		ActorType: "human",
		ActorID:   "alice",
		Kind:      "prompt",
		Hash:      "h1",
		Parents:   nil,
		Refs:      nil,
	}
	if err := st.AppendEvent(ctx, e); err != nil {
		t.Fatalf("AppendEvent with nil slices: %v", err)
	}

	got, err := st.GetEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Parents == nil || got.Refs == nil {
		t.Errorf("expected non-nil slices after round-trip, got Parents=%v Refs=%v", got.Parents, got.Refs)
	}
	if len(got.Parents) != 0 || len(got.Refs) != 0 {
		t.Errorf("expected empty slices, got Parents=%v Refs=%v", got.Parents, got.Refs)
	}
}

// openPostgresWithSchema is the shared setup used by the integration tests.
// It spins up a fresh container, applies the embedded schema, and returns
// the store along with a teardown.
func openPostgresWithSchema(ctx context.Context, t *testing.T) (*Postgres, func()) {
	t.Helper()
	pgC, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("agentlens"),
		postgres.WithUsername("agentlens"),
		postgres.WithPassword("agentlens"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := pool.Exec(ctx, loadSchema(t)); err != nil {
		t.Fatalf("schema: %v", err)
	}
	pool.Close()

	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st, func() {
		st.Close()
		_ = pgC.Terminate(ctx)
	}
}
