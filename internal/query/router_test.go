package query_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/query"
	"github.com/dongqiu/agent-lens/internal/store"
)

// TestVerticalSliceIngestThenQuery is the M1 happy-path acceptance test:
// a hook posts events via /v1/events; a UI queries /v1/graphql; the
// timeline of events for that session is returned in append order.
func TestVerticalSliceIngestThenQuery(t *testing.T) {
	st := store.NewMemory()

	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	ndjson := strings.Join([]string{
		`{"session_id":"s1","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"do X"}}`,
		`{"session_id":"s1","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"thought","payload":{"text":"reasoning"}}`,
		`{"session_id":"s1","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"tool_call","payload":{"name":"Edit"}}`,
	}, "\n")
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(ndjson))
	if err != nil {
		t.Fatalf("ingest post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200", resp.StatusCode)
	}

	gqlBody := `{"query":"{ events(sessionId: \"s1\") { id kind actor { type model } payload } }"}`
	resp, err = http.Post(srv.URL+"/v1/graphql", "application/json", strings.NewReader(gqlBody))
	if err != nil {
		t.Fatalf("graphql post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graphql status = %d, want 200", resp.StatusCode)
	}

	var got struct {
		Data struct {
			Events []struct {
				ID    string         `json:"id"`
				Kind  string         `json:"kind"`
				Actor struct{ Type, Model string } `json:"actor"`
				Payload map[string]any `json:"payload"`
			} `json:"events"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", got.Errors)
	}
	if got.Data.Events == nil || len(got.Data.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(got.Data.Events))
	}

	wantKinds := []string{"PROMPT", "THOUGHT", "TOOL_CALL"}
	for i, e := range got.Data.Events {
		if e.Kind != wantKinds[i] {
			t.Errorf("events[%d].kind = %q, want %q", i, e.Kind, wantKinds[i])
		}
		if e.ID == "" {
			t.Errorf("events[%d].id empty", i)
		}
	}

	if got.Data.Events[1].Actor.Type != "AGENT" || got.Data.Events[1].Actor.Model != "claude-opus-4-7" {
		t.Errorf("thought actor = %+v", got.Data.Events[1].Actor)
	}
	if got.Data.Events[0].Payload["text"] != "do X" {
		t.Errorf("prompt payload = %+v", got.Data.Events[0].Payload)
	}
}

func TestQueryEventNotFoundReturnsNull(t *testing.T) {
	st := store.NewMemory()
	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	body := `{"query":"{ event(id: \"01HMISSING\") { id } }"}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var got struct {
		Data struct {
			Event *struct{ ID string } `json:"event"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data.Event != nil {
		t.Errorf("expected null event, got %+v", got.Data.Event)
	}
}

func TestQuerySessionHeadEmpty(t *testing.T) {
	st := store.NewMemory()
	head, err := st.HeadHash(context.Background(), "s-unknown")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head != "" {
		t.Errorf("head for unknown session = %q, want empty", head)
	}
}
