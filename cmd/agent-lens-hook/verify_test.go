package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dong-qiu/agent-lens/internal/ingest"
	"github.com/dong-qiu/agent-lens/internal/query"
	"github.com/dong-qiu/agent-lens/internal/store"
)

// fakeGQL is a tiny stand-in that decodes the verify query, looks the
// requested sessionId up in a fixture map, and returns the events as
// the GraphQL response shape that runVerify expects.
type fakeGQL struct {
	sessions map[string][]verifyEvent
}

func (f fakeGQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/graphql" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Variables struct {
			SessionID string `json:"sessionId"`
		} `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	events, ok := f.sessions[req.Variables.SessionID]
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"events": []verifyEvent{}}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"events": events}})
}

func ptr(s string) *string { return &s }

func TestFetchSessionRoundTripsTheChain(t *testing.T) {
	srv := httptest.NewServer(fakeGQL{
		sessions: map[string][]verifyEvent{
			"s1": {
				{ID: "e1", Kind: "PROMPT", Hash: "h1", PrevHash: nil},
				{ID: "e2", Kind: "THOUGHT", Hash: "h2", PrevHash: ptr("h1")},
				{ID: "e3", Kind: "TOOL_CALL", Hash: "h3", PrevHash: ptr("h2")},
			},
		},
	})
	defer srv.Close()

	got, err := fetchSession(srv.URL, "", "s1", 0, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[2].Hash != "h3" || got[2].PrevHash == nil || *got[2].PrevHash != "h2" {
		t.Errorf("event 3 = %+v", got[2])
	}
}

func TestFetchSessionForwardsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"events": []verifyEvent{}}})
	}))
	defer srv.Close()

	if _, err := fetchSession(srv.URL, "secret", "s1", 0, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization header = %q, want Bearer secret", gotAuth)
	}
}

func TestFetchSessionReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	if _, err := fetchSession(srv.URL, "", "s1", 0, 0); err == nil {
		t.Error("expected error from 500, got nil")
	}
}

func TestFetchSessionReportsGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "session not found"}},
		})
	}))
	defer srv.Close()

	_, err := fetchSession(srv.URL, "", "s1", 0, 0)
	if err == nil {
		t.Fatal("expected graphql error, got nil")
	}
	if got := err.Error(); got != "graphql: session not found" {
		t.Errorf("err = %q", got)
	}
}

func TestChooseURLAndToken(t *testing.T) {
	t.Setenv("AGENT_LENS_URL", "http://from-env:9999")
	t.Setenv("AGENT_LENS_TOKEN", "envtoken")

	if got := chooseURL(""); got != "http://from-env:9999" {
		t.Errorf("env URL = %q", got)
	}
	if got := chooseURL("http://flag:1234"); got != "http://flag:1234" {
		t.Errorf("flag URL = %q", got)
	}
	if got := chooseToken(""); got != "envtoken" {
		t.Errorf("env token = %q", got)
	}
	if got := chooseToken("flagtoken"); got != "flagtoken" {
		t.Errorf("flag token = %q", got)
	}

	t.Setenv("AGENT_LENS_URL", "")
	if got := chooseURL(""); got != "http://localhost:8787" {
		t.Errorf("default URL = %q", got)
	}
}

func TestFetchSessionRespectsTimeout(t *testing.T) {
	// Server that hangs forever on a request body read.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"events": []verifyEvent{}}})
	}))
	defer srv.Close()

	start := time.Now()
	_, err := fetchSession(srv.URL, "", "s1", 0, 100*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("returned after %v, expected ~100ms timeout", elapsed)
	}
}

// TestVerifyEndToEnd posts events through the real ingest pipeline,
// then reads them back through the verify CLI's GraphQL fetch path.
// Catches any future schema / mapper drift between writer and verifier.
func TestVerifyEndToEnd(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := strings.Join([]string{
		`{"session_id":"s1","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"do X"}}`,
		`{"session_id":"s1","actor":{"type":"agent","id":"claude"},"kind":"thought","payload":{"text":"reasoning"}}`,
		`{"session_id":"s1","actor":{"type":"agent","id":"claude"},"kind":"tool_call","payload":{"name":"Edit"}}`,
	}, "\n")
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()

	events, err := fetchSession(srv.URL, "", "s1", 0, 5*time.Second)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	if events[0].PrevHash != nil {
		t.Errorf("events[0].prevHash = %v, want nil", *events[0].PrevHash)
	}
	for i := 1; i < len(events); i++ {
		if events[i].PrevHash == nil {
			t.Errorf("events[%d].prevHash is nil", i)
			continue
		}
		if *events[i].PrevHash != events[i-1].Hash {
			t.Errorf("chain broken at %d: prev=%q want %q", i, *events[i].PrevHash, events[i-1].Hash)
		}
	}
}
