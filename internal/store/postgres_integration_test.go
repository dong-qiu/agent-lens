//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const migrationsDir = "../../migrations"

// loadSchema concatenates every migrations/*.up.sql in lexical order so
// the integration tests run against the same schema a fresh deployment
// gets after `make migrate-up`.
func loadSchema(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no migrations found under %s", migrationsDir)
	}
	sort.Strings(files)
	var b strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
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

// TestPostgresRejectsDuplicateID ensures Memory and Postgres agree on
// duplicate-ID semantics so ingest can rely on a single sentinel.
func TestPostgresRejectsDuplicateID(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openPostgresWithSchema(ctx, t)
	defer cleanup()

	e := &Event{
		ID:        "01HDUPETEST",
		TS:        time.Now().UTC(),
		SessionID: "s-dup",
		ActorType: "human",
		ActorID:   "alice",
		Kind:      "prompt",
		Hash:      "h1",
	}
	if err := st.AppendEvent(ctx, e); err != nil {
		t.Fatalf("first append: %v", err)
	}
	err := st.AppendEvent(ctx, e)
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("second append err = %v, want ErrDuplicate", err)
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

// TestPostgresListSessions checks that ListSessions aggregates events
// correctly and matches the Memory implementation's ordering: most
// recently active session first, with eventCount and first/last
// timestamps from MIN/MAX(ts). Also exercises the `since` filter.
func TestPostgresListSessions(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openPostgresWithSchema(ctx, t)
	defer cleanup()

	t0 := time.Now().UTC().Truncate(time.Microsecond)
	mustAppend := func(id, sid string, ts time.Time) {
		t.Helper()
		if err := st.AppendEvent(ctx, &Event{
			ID: id, SessionID: sid, TS: ts,
			ActorType: "human", ActorID: "alice", Kind: "prompt", Hash: id,
		}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}
	mustAppend("01HSOLD1", "s-old", t0)
	mustAppend("01HSMID1", "s-mid", t0.Add(1*time.Hour))
	mustAppend("01HSMID2", "s-mid", t0.Add(2*time.Hour))
	mustAppend("01HSNEW1", "s-new", t0.Add(5*time.Hour))

	all, err := st.ListSessions(ctx, 0, time.Time{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d sessions, want 3", len(all))
	}
	wantOrder := []string{"s-new", "s-mid", "s-old"}
	for i, s := range all {
		if s.ID != wantOrder[i] {
			t.Errorf("sessions[%d].ID = %q, want %q", i, s.ID, wantOrder[i])
		}
	}
	if all[1].EventCount != 2 {
		t.Errorf("s-mid eventCount = %d, want 2", all[1].EventCount)
	}
	if !all[1].LastEventAt.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("s-mid lastEventAt = %v, want %v", all[1].LastEventAt, t0.Add(2*time.Hour))
	}

	// since filter excludes s-old.
	filtered, err := st.ListSessions(ctx, 0, t0.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("ListSessions since: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("filtered len = %d, want 2", len(filtered))
	}

	// limit truncates from the front (most recent kept).
	limited, err := st.ListSessions(ctx, 1, time.Time{})
	if err != nil {
		t.Fatalf("ListSessions limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "s-new" {
		t.Errorf("limited = %+v, want [s-new]", limited)
	}
}

// TestPostgresLinkRoundTrip exercises EventsByRef + AppendLink +
// LinksForEvent end-to-end against the real GIN index added in 0002.
func TestPostgresLinkRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openPostgresWithSchema(ctx, t)
	defer cleanup()

	now := time.Now().UTC()
	e1 := &Event{
		ID: "01HLINKA", TS: now, SessionID: "git-r", ActorType: "human",
		ActorID: "alice", Kind: "commit", Hash: "h1",
		Refs: []string{"git:abc123"},
	}
	e2 := &Event{
		ID: "01HLINKB", TS: now.Add(time.Second), SessionID: "github-pr:o/r/1",
		ActorType: "human", ActorID: "alice", Kind: "pr", Hash: "h2",
		Refs: []string{"git:abc123"},
	}
	for _, e := range []*Event{e1, e2} {
		if err := st.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}

	peers, err := st.EventsByRef(ctx, "git:abc123")
	if err != nil {
		t.Fatalf("EventsByRef: %v", err)
	}
	if len(peers) != 2 {
		t.Errorf("got %d peers, want 2", len(peers))
	}

	link := &Link{
		FromEvent: e1.ID, ToEvent: e2.ID, Relation: "references",
		Confidence: 1.0, InferredBy: "shared_ref:git:abc123",
	}
	if err := st.AppendLink(ctx, link); err != nil {
		t.Fatalf("AppendLink: %v", err)
	}
	if err := st.AppendLink(ctx, link); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate link err = %v, want ErrDuplicate", err)
	}

	got, err := st.LinksForEvent(ctx, e1.ID)
	if err != nil {
		t.Fatalf("LinksForEvent(from): %v", err)
	}
	if len(got) != 1 || got[0].ToEvent != e2.ID {
		t.Errorf("links for e1 = %+v, want one to e2", got)
	}
	got2, err := st.LinksForEvent(ctx, e2.ID)
	if err != nil {
		t.Fatalf("LinksForEvent(to): %v", err)
	}
	if len(got2) != 1 || got2[0].FromEvent != e1.ID {
		t.Errorf("links for e2 = %+v, want one from e1", got2)
	}

	none, err := st.EventsByRef(ctx, "git:notfound")
	if err != nil {
		t.Fatalf("EventsByRef miss: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("EventsByRef miss returned %d events", len(none))
	}
}
