package linking

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/store"
)

func appendEvent(t *testing.T, st store.Store, id string, refs []string) {
	t.Helper()
	if err := st.AppendEvent(context.Background(), &store.Event{
		ID:        id,
		TS:        time.Now().UTC(),
		SessionID: "s-" + id,
		ActorType: "human",
		ActorID:   "alice",
		Kind:      "prompt",
		Hash:      "h-" + id,
		Refs:      refs,
	}); err != nil {
		t.Fatalf("append %s: %v", id, err)
	}
}

func TestProcessOnceLinksSharedRef(t *testing.T) {
	st := store.NewMemory()
	l := New(st, 0)
	ctx := context.Background()

	// e1 already in store with shared ref; e2 just arrived.
	appendEvent(t, st, "e1", []string{"git:abc"})
	appendEvent(t, st, "e2", []string{"git:abc", "git:def"})

	if err := l.ProcessOnce(ctx, &ingest.WireEvent{ID: "e2", Refs: []string{"git:abc", "git:def"}}); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	links, _ := st.LinksForEvent(ctx, "e2")
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if links[0].FromEvent != "e1" || links[0].ToEvent != "e2" {
		t.Errorf("link direction = %+v", links[0])
	}
	if links[0].Relation != DefaultRelation {
		t.Errorf("relation = %q, want %q", links[0].Relation, DefaultRelation)
	}
}

func TestProcessOnceMultiplePeers(t *testing.T) {
	st := store.NewMemory()
	l := New(st, 0)
	ctx := context.Background()

	appendEvent(t, st, "commit-a", []string{"git:abc"})
	appendEvent(t, st, "pr-a", []string{"git:abc"})
	appendEvent(t, st, "build-a", []string{"git:abc"})

	if err := l.ProcessOnce(ctx, &ingest.WireEvent{ID: "build-a", Refs: []string{"git:abc"}}); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	links, _ := st.LinksForEvent(ctx, "build-a")
	if len(links) != 2 {
		t.Errorf("got %d links, want 2 (commit-a→build-a, pr-a→build-a)", len(links))
	}
}

func TestProcessOnceIdempotent(t *testing.T) {
	st := store.NewMemory()
	l := New(st, 0)
	ctx := context.Background()

	appendEvent(t, st, "e1", []string{"git:abc"})
	appendEvent(t, st, "e2", []string{"git:abc"})

	for i := 0; i < 3; i++ {
		if err := l.ProcessOnce(ctx, &ingest.WireEvent{ID: "e2", Refs: []string{"git:abc"}}); err != nil {
			t.Fatalf("ProcessOnce iter %d: %v", i, err)
		}
	}
	links, _ := st.LinksForEvent(ctx, "e2")
	if len(links) != 1 {
		t.Errorf("got %d links after 3 calls, want 1 (ErrDuplicate must be silenced)", len(links))
	}
}

func TestProcessOnceSkipsSelfAndEmpty(t *testing.T) {
	st := store.NewMemory()
	l := New(st, 0)
	ctx := context.Background()

	appendEvent(t, st, "solo", []string{"git:onlyme"})

	if err := l.ProcessOnce(ctx, &ingest.WireEvent{ID: "solo", Refs: []string{"git:onlyme"}}); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	links, _ := st.LinksForEvent(ctx, "solo")
	if len(links) != 0 {
		t.Errorf("self-link created: %+v", links)
	}

	// Empty refs / empty id are no-ops.
	if err := l.ProcessOnce(ctx, &ingest.WireEvent{ID: "x", Refs: nil}); err != nil {
		t.Errorf("empty refs returned err: %v", err)
	}
	if err := l.ProcessOnce(ctx, nil); err != nil {
		t.Errorf("nil event returned err: %v", err)
	}
}

func TestRunConsumesQueueAndShutsDown(t *testing.T) {
	st := store.NewMemory()
	l := New(st, 16)
	ctx, cancel := context.WithCancel(context.Background())

	appendEvent(t, st, "e1", []string{"git:run"})
	appendEvent(t, st, "e2", []string{"git:run"})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.Run(ctx) }()

	l.Notify(&ingest.WireEvent{ID: "e2", Refs: []string{"git:run"}})

	// Poll briefly for the link to appear.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		links, _ := st.LinksForEvent(ctx, "e2")
		if len(links) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	links, _ := st.LinksForEvent(ctx, "e2")
	if len(links) != 1 {
		t.Errorf("worker did not produce link within 500ms: got %d", len(links))
	}

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not shut down within 1s after cancel")
	}
}

func TestNotifyDropsWhenQueueFull(t *testing.T) {
	st := store.NewMemory()
	l := New(st, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx

	// Fill the queue without starting Run, then over-fill to trigger drop.
	first := &ingest.WireEvent{ID: "first", Refs: []string{"git:full"}}
	l.Notify(first)
	// This Notify must NOT block; it's expected to drop.
	dropped := make(chan struct{})
	go func() {
		l.Notify(&ingest.WireEvent{ID: "second", Refs: []string{"git:full"}})
		close(dropped)
	}()
	select {
	case <-dropped:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Notify blocked when queue was full; should drop instead")
	}
}

func TestProcessOncePropagatesStoreError(t *testing.T) {
	l := New(failingStore{}, 0)
	err := l.ProcessOnce(context.Background(), &ingest.WireEvent{ID: "x", Refs: []string{"git:any"}})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

type failingStore struct{ store.Store }

func (failingStore) EventsByRef(_ context.Context, _ string) ([]*store.Event, error) {
	return nil, errors.New("boom")
}
