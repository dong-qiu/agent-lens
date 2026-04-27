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
