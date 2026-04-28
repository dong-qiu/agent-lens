package query_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestSessionsResolver exercises the sessions(limit, since) query.
// Three sessions with distinct activity times are seeded; the result
// is expected ordered by lastEventAt DESC, with eventCount and
// lastEventAt aggregated correctly. The `since` filter is also exercised.
func TestSessionsResolver(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()

	t0, _ := time.Parse(time.RFC3339, "2026-04-01T00:00:00Z")
	mustAppend := func(id, sid string, ts time.Time) {
		t.Helper()
		if err := st.AppendEvent(ctx, &store.Event{
			ID: id, SessionID: sid, TS: ts,
			ActorType: "human", ActorID: "alice", Kind: "prompt", Hash: id,
		}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}
	// s-old: single event at t0
	mustAppend("e-old-1", "s-old", t0)
	// s-mid: two events at t0+1h and t0+2h
	mustAppend("e-mid-1", "s-mid", t0.Add(1*time.Hour))
	mustAppend("e-mid-2", "s-mid", t0.Add(2*time.Hour))
	// s-new: one event at t0+5h (most recent)
	mustAppend("e-new-1", "s-new", t0.Add(5*time.Hour))

	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	post := func(t *testing.T, body string) []struct {
		ID         string `json:"id"`
		EventCount int    `json:"eventCount"`
	} {
		t.Helper()
		resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		var got struct {
			Data struct {
				Sessions []struct {
					ID         string `json:"id"`
					EventCount int    `json:"eventCount"`
				} `json:"sessions"`
			} `json:"data"`
			Errors []map[string]any `json:"errors"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Errors) > 0 {
			t.Fatalf("graphql errors: %+v", got.Errors)
		}
		return got.Data.Sessions
	}

	all := post(t, `{"query":"{ sessions { id eventCount } }"}`)
	if len(all) != 3 {
		t.Fatalf("got %d sessions, want 3", len(all))
	}
	wantOrder := []string{"s-new", "s-mid", "s-old"}
	for i, s := range all {
		if s.ID != wantOrder[i] {
			t.Errorf("sessions[%d].id = %q, want %q", i, s.ID, wantOrder[i])
		}
	}
	if all[1].EventCount != 2 {
		t.Errorf("s-mid eventCount = %d, want 2", all[1].EventCount)
	}

	// `since` excludes s-old (lastEventAt = t0) but keeps s-mid / s-new.
	sinceISO := t0.Add(30 * time.Minute).Format(time.RFC3339)
	body := `{"query":"{ sessions(since: \"` + sinceISO + `\") { id } }"}`
	filtered := post(t, body)
	if len(filtered) != 2 {
		t.Fatalf("got %d filtered sessions, want 2", len(filtered))
	}
}

// TestEventLinksResolver exercises the new Event.links resolver
// (M2-B). Two events sharing a `git:<sha>` ref get linked via
// AppendLink in the store; the GraphQL query for either event
// returns the corresponding link.
func TestEventLinksResolver(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e1", SessionID: "s1", ActorType: "human", ActorID: "alice",
		Kind: "commit", Hash: "h1", Refs: []string{"git:abc"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e2", SessionID: "s2", ActorType: "human", ActorID: "alice",
		Kind: "pr", Hash: "h2", Refs: []string{"git:abc"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLink(ctx, &store.Link{
		FromEvent: "e1", ToEvent: "e2", Relation: "references",
		Confidence: 1.0, InferredBy: "shared_ref:git:abc",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	body := `{"query":"{ event(id: \"e2\") { id links { fromEvent toEvent relation inferredBy } } }"}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var got struct {
		Data struct {
			Event struct {
				ID    string `json:"id"`
				Links []struct {
					FromEvent  string `json:"fromEvent"`
					ToEvent    string `json:"toEvent"`
					Relation   string `json:"relation"`
					InferredBy string `json:"inferredBy"`
				} `json:"links"`
			} `json:"event"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", got.Errors)
	}
	if len(got.Data.Event.Links) != 1 {
		t.Fatalf("got %d links, want 1", len(got.Data.Event.Links))
	}
	link := got.Data.Event.Links[0]
	if link.FromEvent != "e1" || link.ToEvent != "e2" || link.Relation != "references" {
		t.Errorf("link = %+v", link)
	}
	if link.InferredBy != "shared_ref:git:abc" {
		t.Errorf("inferred_by = %q", link.InferredBy)
	}
}
