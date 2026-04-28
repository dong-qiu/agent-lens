package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/linking"
	"github.com/dongqiu/agent-lens/internal/query"
	"github.com/dongqiu/agent-lens/internal/store"
)

// startTestServer wires ingest + query + linking against an in-memory
// store, mirroring the production assembly minus auth. Returns the
// httptest URL for direct GraphQL POSTs. The linker runs in a
// goroutine cancelled at test end.
func startTestServer(t *testing.T) string {
	t.Helper()
	st := store.NewMemory()

	ingestH := ingest.NewHandler(st)
	linker := linking.New(st, 1024)
	ingestH.AfterAppend(func(_ context.Context, ev *ingest.WireEvent) {
		linker.Notify(ev)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		linker.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		sub.Post("/events", ingestH.IngestNDJSON)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL
}

// waitForLinks polls until the named session has at least one event
// with non-empty links, then returns. Caps at ~1s to avoid hanging
// CI if the linker isn't running. The linker is async (Notify is
// non-blocking) so a test posting events and immediately reading
// back the link graph would otherwise race the worker.
func waitForLinks(t *testing.T, url, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := json.Marshal(map[string]any{
			"query":     `query($s: String!) { events(sessionId: $s, limit: 10) { id links { fromEvent } } }`,
			"variables": map[string]any{"s": sessionID},
		})
		resp, err := http.Post(url+"/v1/graphql", "application/json", strings.NewReader(string(body)))
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var out struct {
			Data struct {
				Events []struct {
					Links []struct{ FromEvent string } `json:"links"`
				} `json:"events"`
			} `json:"data"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		for _, e := range out.Data.Events {
			if len(e.Links) > 0 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("linker didn't produce links for session %q within 1s", sessionID)
}

// postNDJSON helper — caller passes the raw NDJSON body.
func postNDJSON(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// firstEventID does a minimal GraphQL `events(sessionId)` to discover
// the server-assigned id of an event in the named session — most
// builder tests need this to pass --root.
func firstEventID(t *testing.T, url, sessionID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"query":     `query($s: String!) { events(sessionId: $s, limit: 1) { id } }`,
		"variables": map[string]any{"s": sessionID},
	})
	resp, err := http.Post(url+"/v1/graphql", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data.Events) == 0 {
		t.Fatalf("no events for session %q", sessionID)
	}
	return out.Data.Events[0].ID
}

func TestBuildSingleSession(t *testing.T) {
	url := startTestServer(t)
	postNDJSON(t, url, strings.Join([]string{
		`{"session_id":"s-only","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"hi"}}`,
		`{"session_id":"s-only","actor":{"type":"agent","id":"claude-code"},"kind":"thought","payload":{"text":"plan"}}`,
	}, "\n"))

	rootID := firstEventID(t, url, "s-only")
	r, err := Build(context.Background(), BuildOptions{
		URL:         url,
		RootEventID: rootID,
		Generator:   "test",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if r.Version != Version {
		t.Errorf("version = %q", r.Version)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].SessionID != "s-only" {
		t.Errorf("sessions = %+v", r.Sessions)
	}
	if len(r.Sessions[0].Events) != 2 {
		t.Errorf("events = %d, want 2", len(r.Sessions[0].Events))
	}
	if r.Manifest.SessionsSha256 == "" || r.Manifest.AttestationsSha256 == "" {
		t.Errorf("manifest empty: %+v", r.Manifest)
	}

	// The fresh report should pass Verify on its own.
	res, err := Verify(r, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 0 {
		t.Errorf("self-verify failed: %v", res.Issues)
	}
}

func TestBuildLinkedSessionsBFS(t *testing.T) {
	// Two sessions sharing a git: ref → linker creates a Link →
	// builder should walk from one root and pull both sessions in.
	url := startTestServer(t)
	postNDJSON(t, url, strings.Join([]string{
		`{"session_id":"s-prompt","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"build it"},"refs":["git:abc123"]}`,
		`{"session_id":"s-build","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"sha":"abc123"},"refs":["git:abc123"]}`,
	}, "\n"))
	waitForLinks(t, url, "s-prompt")

	rootID := firstEventID(t, url, "s-prompt")
	r, err := Build(context.Background(), BuildOptions{
		URL:         url,
		RootEventID: rootID,
		Generator:   "test",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(r.Sessions) != 2 {
		t.Fatalf("expected 2 sessions (prompt+build), got %d (%+v)", len(r.Sessions), sessionIDs(r))
	}
	have := map[string]bool{}
	for _, s := range r.Sessions {
		have[s.SessionID] = true
	}
	if !have["s-prompt"] || !have["s-build"] {
		t.Errorf("missing sessions: %+v", have)
	}
}

func TestBuildMaxSessionsCap(t *testing.T) {
	// Hit the cap by setting MaxSessions=1 against a 2-session graph.
	url := startTestServer(t)
	postNDJSON(t, url, strings.Join([]string{
		`{"session_id":"s-a","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"x"},"refs":["git:cap"]}`,
		`{"session_id":"s-b","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"sha":"cap"},"refs":["git:cap"]}`,
	}, "\n"))
	waitForLinks(t, url, "s-a")

	rootID := firstEventID(t, url, "s-a")
	_, err := Build(context.Background(), BuildOptions{
		URL:         url,
		RootEventID: rootID,
		MaxSessions: 1,
		Generator:   "test",
	})
	if err == nil {
		t.Fatal("expected max-sessions cap error")
	}
	if !strings.Contains(err.Error(), "max-sessions") {
		t.Errorf("error didn't mention cap: %v", err)
	}
}

func TestBuildMissingRootEventErrors(t *testing.T) {
	url := startTestServer(t)
	_, err := Build(context.Background(), BuildOptions{
		URL:         url,
		RootEventID: "01HNOSUCH",
		Generator:   "test",
	})
	if err == nil {
		t.Fatal("expected error on unknown root id")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestBuildEmbedsAttestationsAndManifest(t *testing.T) {
	url := startTestServer(t)
	postNDJSON(t, url, `{"session_id":"s-att","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"x"}}`)
	rootID := firstEventID(t, url, "s-att")

	dir := t.TempDir()
	att := filepath.Join(dir, "deploy.intoto.jsonl")
	if err := os.WriteFile(att, []byte("ATTEST-CONTENTS"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Build(context.Background(), BuildOptions{
		URL:          url,
		RootEventID:  rootID,
		Attestations: []string{att},
		Generator:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Attestations) != 1 {
		t.Fatalf("attestations = %d", len(r.Attestations))
	}
	if r.Attestations[0].Filename != "deploy.intoto.jsonl" {
		t.Errorf("filename = %q", r.Attestations[0].Filename)
	}
	if !strings.HasPrefix(r.Attestations[0].Sha256, "sha256:") {
		t.Errorf("sha256 = %q", r.Attestations[0].Sha256)
	}

	// And it should round-trip through Verify cleanly.
	res, _ := Verify(r, VerifyOptions{})
	if len(res.Issues) != 0 {
		t.Errorf("self-verify of fresh report failed: %v", res.Issues)
	}
}

func sessionIDs(r *Report) []string {
	out := make([]string, 0, len(r.Sessions))
	for _, s := range r.Sessions {
		out = append(out, s.SessionID)
	}
	return out
}
