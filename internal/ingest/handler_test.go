package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dongqiu/agent-lens/internal/store"
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

