package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dong-qiu/agent-lens/internal/store"
)

func TestIngestHappyPath(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(NewRouter(st))
	defer srv.Close()

	body := strings.Join([]string{
		`{"session_id":"s1","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"add a button"}}`,
		`{"session_id":"s1","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"tool_call","payload":{"name":"Edit"}}`,
		`{"session_id":"s1","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"tool_result","payload":{"ok":true}}`,
	}, "\n")

	resp, err := http.Post(srv.URL+"/events", "application/x-ndjson", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var got struct{ Accepted int }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Accepted != 3 {
		t.Fatalf("accepted = %d, want 3", got.Accepted)
	}

	events, err := st.ListBySession(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("stored %d events, want 3", len(events))
	}

	// Hash chain: each event's prev_hash must equal the previous event's hash.
	if events[0].PrevHash != "" {
		t.Errorf("first event prev_hash = %q, want empty", events[0].PrevHash)
	}
	for i := 1; i < len(events); i++ {
		if events[i].PrevHash != events[i-1].Hash {
			t.Errorf("event[%d].prev_hash = %q, want %q", i, events[i].PrevHash, events[i-1].Hash)
		}
	}

	// ULIDs and timestamps must be filled by the handler.
	for i, e := range events {
		if e.ID == "" {
			t.Errorf("event[%d].id is empty", i)
		}
		if e.TS.IsZero() {
			t.Errorf("event[%d].ts is zero", i)
		}
	}
}

func TestIngestRejectsMissingFields(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(NewRouter(st))
	defer srv.Close()

	body := `{"kind":"prompt"}` // missing session_id and actor
	resp, err := http.Post(srv.URL+"/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIngestPreservesSubmittedID(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(NewRouter(st))
	defer srv.Close()

	body := `{"id":"01HSAMPLE","session_id":"s2","actor":{"type":"human","id":"alice"},"kind":"prompt"}`
	resp, err := http.Post(srv.URL+"/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	got, err := st.GetEvent(context.Background(), "01HSAMPLE")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "01HSAMPLE" {
		t.Errorf("id = %q, want %q", got.ID, "01HSAMPLE")
	}
}

// TestIngestRecoversHashChainAfterRestart guards the handler's in-memory
// head cache against forking the chain when the process restarts.
func TestIngestRecoversHashChainAfterRestart(t *testing.T) {
	st := store.NewMemory()

	srv1 := httptest.NewServer(NewRouter(st))
	body1 := `{"session_id":"s-restart","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"a"}}`
	resp, err := http.Post(srv1.URL+"/events", "application/x-ndjson", strings.NewReader(body1))
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	resp.Body.Close()
	srv1.Close()

	// Fresh handler: cache is empty, but the store still has the prior event.
	srv2 := httptest.NewServer(NewRouter(st))
	defer srv2.Close()
	body2 := `{"session_id":"s-restart","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"b"}}`
	resp, err = http.Post(srv2.URL+"/events", "application/x-ndjson", strings.NewReader(body2))
	if err != nil {
		t.Fatalf("second post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	events, err := st.ListBySession(context.Background(), "s-restart", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[1].PrevHash != events[0].Hash {
		t.Errorf("chain forked across restart: prev=%q want %q", events[1].PrevHash, events[0].Hash)
	}
}

func TestIngestRejectsInvalidKind(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(NewRouter(st))
	defer srv.Close()

	body := `{"session_id":"s1","actor":{"type":"human","id":"alice"},"kind":"bogus"}`
	resp, err := http.Post(srv.URL+"/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestIngestConcurrentWritesPreserveChain hammers the same session from
// many goroutines simultaneously. The handler's per-handler mutex must
// keep the load → compute → append → cache update sequence atomic so
// no two events fork the chain (and no event's prev_hash is "" once
// the session has any prior event).
func TestIngestConcurrentWritesPreserveChain(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(NewRouter(st))
	defer srv.Close()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(
				`{"session_id":"sc","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"i":%d}}`,
				i,
			)
			resp, err := http.Post(srv.URL+"/events", "application/x-ndjson", strings.NewReader(body))
			if err != nil {
				t.Errorf("post[%d]: %v", i, err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("post[%d] status = %d", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()

	events, err := st.ListBySession(context.Background(), "sc", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != N {
		t.Fatalf("stored %d events, want %d", len(events), N)
	}

	// Every event after the first must reference the previous one's
	// hash; no PrevHash should be "" except for index 0.
	if events[0].PrevHash != "" {
		t.Errorf("events[0].prev_hash = %q, want empty", events[0].PrevHash)
	}
	seen := make(map[string]int, N)
	for i, e := range events {
		if e.Hash == "" {
			t.Errorf("events[%d].hash empty", i)
		}
		if prev, dup := seen[e.Hash]; dup {
			t.Errorf("hash collision: events[%d] and events[%d] both have %s", prev, i, e.Hash)
		}
		seen[e.Hash] = i
		if i > 0 && e.PrevHash != events[i-1].Hash {
			t.Errorf("chain forked at index %d: prev=%q want %q", i, e.PrevHash, events[i-1].Hash)
		}
		// Issue #38: id-asc ordering must match append order, so that
		// Postgres's ORDER BY id ASC reads back the same sequence
		// Memory does. This holds only because handler.appendLocked
		// generates the ulid inside h.mu — see the comment there.
		if i > 0 && e.ID <= events[i-1].ID {
			t.Errorf("ids not monotonic at index %d: %s after %s", i, e.ID, events[i-1].ID)
		}
	}
}

func TestIngestReturns409OnDuplicateID(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(NewRouter(st))
	defer srv.Close()

	body := `{"id":"01HDUPEVENT","session_id":"s1","actor":{"type":"human","id":"alice"},"kind":"prompt"}`
	resp, err := http.Post(srv.URL+"/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(srv.URL+"/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatalf("second post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}

	// Sanity: the store still has exactly one event for the session, and
	// the head-cache wasn't advanced past the failed insert.
	events, err := st.ListBySession(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("stored %d events, want 1", len(events))
	}
}


// TestAppendDirectChainsAndDedupes exercises the public Handler.Append
// contract: chained writes preserve the per-session head, validation
// rejects unknown kinds, and a duplicate ID surfaces ErrDuplicate
// without advancing the head cache.
func TestAppendDirectChainsAndDedupes(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler(st)
	ctx := context.Background()

	a := &WireEvent{
		ID:        "01HAPPENDA",
		SessionID: "s-direct",
		Actor:     WireActor{Type: "human", ID: "alice"},
		Kind:      "prompt",
	}
	b := &WireEvent{
		ID:        "01HAPPENDB",
		SessionID: "s-direct",
		Actor:     WireActor{Type: "human", ID: "alice"},
		Kind:      "prompt",
	}
	if err := h.Append(ctx, a); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := h.Append(ctx, b); err != nil {
		t.Fatalf("append b: %v", err)
	}

	got, err := st.ListBySession(ctx, "s-direct", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[1].PrevHash != got[0].Hash {
		t.Errorf("chain broken: got[1].prev = %q, want %q", got[1].PrevHash, got[0].Hash)
	}

	// Invalid kind rejected by validation.
	bad := &WireEvent{
		SessionID: "s-direct",
		Actor:     WireActor{Type: "human", ID: "alice"},
		Kind:      "bogus",
	}
	if err := h.Append(ctx, bad); !errors.Is(err, errInvalidKind) {
		t.Errorf("invalid kind err = %v, want errInvalidKind", err)
	}

	// Duplicate ID returns ErrDuplicate; head cache must not advance
	// past the failed insert.
	dup := *a
	if err := h.Append(ctx, &dup); !errors.Is(err, store.ErrDuplicate) {
		t.Errorf("duplicate id err = %v, want ErrDuplicate", err)
	}

	c := &WireEvent{
		ID:        "01HAPPENDC",
		SessionID: "s-direct",
		Actor:     WireActor{Type: "human", ID: "alice"},
		Kind:      "prompt",
	}
	if err := h.Append(ctx, c); err != nil {
		t.Fatalf("append c: %v", err)
	}
	all, _ := st.ListBySession(ctx, "s-direct", 0)
	if len(all) != 3 {
		t.Fatalf("got %d events, want 3", len(all))
	}
	if all[2].PrevHash != all[1].Hash {
		t.Errorf("chain forked after failed insert: got[2].prev = %q, want %q", all[2].PrevHash, all[1].Hash)
	}
}
