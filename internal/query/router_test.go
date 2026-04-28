package query_test

import (
	"context"
	"encoding/json"
	"fmt"
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


// TestLinkedEventsResolver covers the BFS by session id: two sessions
// share a `git:<sha>` ref; the linker emits a cross-session link. A
// linkedEvents query starting at one session at depth=1 returns events
// from both. depth=0 collapses to single-session (parity with
// events()).
func TestLinkedEventsResolver(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()

	// Seed: claude session has a tool_result with git:<sha> ref;
	// git session has a commit with the same ref. linker emits link.
	mustAppend := func(id, sid, kind string, refs []string) {
		t.Helper()
		if err := st.AppendEvent(ctx, &store.Event{
			ID:        id,
			SessionID: sid,
			ActorType: "agent",
			ActorID:   "claude-code",
			Kind:      kind,
			Hash:      "h-" + id,
			Refs:      refs,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend("e-claude-prompt", "s-claude", "prompt", nil)
	mustAppend("e-claude-call", "s-claude", "tool_call", nil)
	mustAppend("e-claude-result", "s-claude", "tool_result", []string{"git:abc"})
	mustAppend("e-git-commit", "s-git", "commit", []string{"git:abc"})

	if err := st.AppendLink(ctx, &store.Link{
		FromEvent:  "e-claude-result",
		ToEvent:    "e-git-commit",
		Relation:   "references",
		Confidence: 1.0,
		InferredBy: "shared_ref:git:abc",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	type result struct {
		Data struct {
			LinkedEvents []struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionId"`
			} `json:"linkedEvents"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	post := func(t *testing.T, body string) result {
		t.Helper()
		resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		var r result
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(r.Errors) > 0 {
			t.Fatalf("graphql errors: %+v", r.Errors)
		}
		return r
	}

	// depth=0 returns only the seed session's events.
	r0 := post(t, `{"query":"{ linkedEvents(sessionId:\"s-claude\", depth:0) { id sessionId } }"}`)
	gotSessions0 := map[string]int{}
	for _, e := range r0.Data.LinkedEvents {
		gotSessions0[e.SessionID]++
	}
	if gotSessions0["s-claude"] != 3 || gotSessions0["s-git"] != 0 {
		t.Errorf("depth=0 sessions = %v, want s-claude:3 only", gotSessions0)
	}

	// depth=1 picks up the linked git session via the shared ref.
	r1 := post(t, `{"query":"{ linkedEvents(sessionId:\"s-claude\", depth:1) { id sessionId } }"}`)
	gotSessions1 := map[string]int{}
	for _, e := range r1.Data.LinkedEvents {
		gotSessions1[e.SessionID]++
	}
	if gotSessions1["s-claude"] != 3 || gotSessions1["s-git"] != 1 {
		t.Errorf("depth=1 sessions = %v, want s-claude:3 + s-git:1", gotSessions1)
	}

	// Out-of-range depth is clamped to [0, 3]; depth=99 should not
	// blow up and should return at least the depth=1 surface.
	rBig := post(t, `{"query":"{ linkedEvents(sessionId:\"s-claude\", depth:99) { id sessionId } }"}`)
	if len(rBig.Data.LinkedEvents) < len(r1.Data.LinkedEvents) {
		t.Errorf("depth=99 returned fewer events (%d) than depth=1 (%d)", len(rBig.Data.LinkedEvents), len(r1.Data.LinkedEvents))
	}
}

// TestLinkedEventsResolver_LinkPastLimit is the regression that drove
// the switch from per-event link discovery to LinksForSession. With
// 100 leading no-link events and the link-bearing event at index 100,
// a perSessionLimit=10 read fetches only the first 10 events; if BFS
// only walks links of fetched events, it never sees the link and the
// neighbouring session is invisible. With LinksForSession, the link
// is discovered regardless of which events were paged in.
func TestLinkedEventsResolver_LinkPastLimit(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		if err := st.AppendEvent(ctx, &store.Event{
			ID: fmt.Sprintf("e-noise-%03d", i), SessionID: "s-big",
			ActorType: "agent", ActorID: "claude-code",
			Kind: "tool_call", Hash: fmt.Sprintf("h-noise-%03d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The link-bearing event sits at the far end.
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e-link-bearer", SessionID: "s-big",
		ActorType: "agent", ActorID: "claude-code",
		Kind: "tool_result", Hash: "h-link-bearer",
		Refs: []string{"git:xyz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e-peer", SessionID: "s-peer",
		ActorType: "human", ActorID: "alice",
		Kind: "commit", Hash: "h-peer", Refs: []string{"git:xyz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLink(ctx, &store.Link{
		FromEvent: "e-link-bearer", ToEvent: "e-peer",
		Relation: "references", Confidence: 1.0, InferredBy: "shared_ref:git:xyz",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	body := `{"query":"{ linkedEvents(sessionId:\"s-big\", depth:1, perSessionLimit:10) { sessionId } }"}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Data struct {
			LinkedEvents []struct {
				SessionID string `json:"sessionId"`
			} `json:"linkedEvents"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	hasPeer := false
	for _, e := range got.Data.LinkedEvents {
		if e.SessionID == "s-peer" {
			hasPeer = true
			break
		}
	}
	if !hasPeer {
		t.Errorf("expected s-peer events in result; LinksForSession should have surfaced the link despite limit=10 (got sessions=%v)", got.Data.LinkedEvents)
	}
}

// TestLinkedEventsResolver_LinkBearerPastLimit_PatchedIn is the
// regression for the silent-degradation case where the anchor
// session's link-bearing event sits past perSessionLimit. Without
// the patch-in, the cross-session edge has source=<paged-out event
// id> and ReactFlow silently drops it — the user sees the peer
// session events but no visible link back to the anchor.
//
// This test asserts the link-bearing event is included in the
// linkedEvents result *even when* it falls past the perSessionLimit
// slice — so the frontend can render the edge with both endpoints.
func TestLinkedEventsResolver_LinkBearerPastLimit_PatchedIn(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()

	// 100 leading no-link anchor events.
	for i := 0; i < 100; i++ {
		if err := st.AppendEvent(ctx, &store.Event{
			ID: fmt.Sprintf("e-anchor-noise-%03d", i), SessionID: "s-anchor",
			ActorType: "agent", ActorID: "claude-code",
			Kind: "tool_call", Hash: fmt.Sprintf("h-anchor-noise-%03d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Anchor's link-bearing event past perSessionLimit=10.
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e-anchor-bearer", SessionID: "s-anchor",
		ActorType: "agent", ActorID: "claude-code",
		Kind: "tool_result", Hash: "h-anchor-bearer",
		Refs: []string{"git:xyz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e-peer-commit", SessionID: "s-peer",
		ActorType: "human", ActorID: "alice",
		Kind: "commit", Hash: "h-peer-commit", Refs: []string{"git:xyz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLink(ctx, &store.Link{
		FromEvent: "e-anchor-bearer", ToEvent: "e-peer-commit",
		Relation: "references", Confidence: 1.0, InferredBy: "shared_ref:git:xyz",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	body := `{"query":"{ linkedEvents(sessionId:\"s-anchor\", depth:1, perSessionLimit:10) { id sessionId } }"}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Data struct {
			LinkedEvents []struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionId"`
			} `json:"linkedEvents"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	hasBearer := false
	for _, e := range got.Data.LinkedEvents {
		if e.ID == "e-anchor-bearer" {
			hasBearer = true
			break
		}
	}
	if !hasBearer {
		t.Errorf("expected e-anchor-bearer (anchor's link-bearing event past perSessionLimit) to be patched into the result; got %d events: %v", len(got.Data.LinkedEvents), got.Data.LinkedEvents)
	}
}

// TestLinkedEventsResolver_BearerChainContext is the regression for
// the visual silent-degradation reported by the user: when the
// link-bearing event in any session sits past perSessionLimit, the
// bearer is patched in but its chain ancestors aren't, so the dashed
// prev_hash edge into the bearer has source=non-rendered and
// ReactFlow silently drops it — the bearer floats next to the peer
// COMMIT with no chain connection back.
//
// Fix: for each patched-in bearer, also pull a small chain-context
// window of predecessors via Store.EventsBeforeID. Seed session stays
// bounded by perSessionLimit so the UI doesn't have to render
// thousands of unrelated events when investigating a long session.
func TestLinkedEventsResolver_BearerChainContext(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()

	// 50 anchor events, link-bearing TOOL_RESULT at index 50.
	for i := 0; i < 50; i++ {
		if err := st.AppendEvent(ctx, &store.Event{
			ID: fmt.Sprintf("e-anchor-%03d", i), SessionID: "s-seed",
			ActorType: "agent", ActorID: "claude-code",
			Kind: "tool_call", Hash: fmt.Sprintf("h-anchor-%03d", i),
			PrevHash: func() string {
				if i == 0 {
					return ""
				}
				return fmt.Sprintf("h-anchor-%03d", i-1)
			}(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e-anchor-bearer", SessionID: "s-seed",
		ActorType: "agent", ActorID: "claude-code",
		Kind: "tool_result", Hash: "h-anchor-bearer",
		PrevHash: "h-anchor-049",
		Refs:     []string{"git:xyz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(ctx, &store.Event{
		ID: "e-peer-commit", SessionID: "s-peer",
		ActorType: "human", ActorID: "alice",
		Kind: "commit", Hash: "h-peer-commit", Refs: []string{"git:xyz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLink(ctx, &store.Link{
		FromEvent: "e-anchor-bearer", ToEvent: "e-peer-commit",
		Relation: "produces", Confidence: 1.0, InferredBy: "shared_ref:git:xyz",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(query.NewRouter(st))
	defer srv.Close()

	body := `{"query":"{ linkedEvents(sessionId:\"s-seed\", depth:1, perSessionLimit:10) { id sessionId hash prevHash } }"}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Data struct {
			LinkedEvents []struct {
				ID        string  `json:"id"`
				SessionID string  `json:"sessionId"`
				Hash      string  `json:"hash"`
				PrevHash  *string `json:"prevHash"`
			} `json:"linkedEvents"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Build hash → id map of returned events; the bearer's prev_hash
	// must resolve to a rendered event so the dashed chain edge can
	// land. With chain-context the bearer + its 10 predecessors are
	// patched in; without it only first-psl events come back and the
	// bearer's predecessor (h-anchor-049) is missing.
	hashes := map[string]bool{}
	var bearer *struct {
		ID        string  `json:"id"`
		SessionID string  `json:"sessionId"`
		Hash      string  `json:"hash"`
		PrevHash  *string `json:"prevHash"`
	}
	for i, e := range got.Data.LinkedEvents {
		hashes[e.Hash] = true
		if e.ID == "e-anchor-bearer" {
			bearer = &got.Data.LinkedEvents[i]
		}
	}
	if bearer == nil {
		t.Fatalf("e-anchor-bearer not in result")
	}
	if bearer.PrevHash == nil {
		t.Fatalf("e-anchor-bearer.prev_hash is null")
	}
	if !hashes[*bearer.PrevHash] {
		t.Errorf("bearer's prev_hash %q is not in the rendered hash set; chain edge would be orphaned. Got %d events total — chain-context should pull predecessors via EventsBeforeID.", *bearer.PrevHash, len(got.Data.LinkedEvents))
	}
	// Sanity: total events should be bounded — chain context adds
	// up to ~10 events per bearer, not the whole session. We have
	// 51 anchor events; expect first-psl=10 + ~10 chain context +
	// the bearer + 1 peer commit ≈ 22, NOT 52.
	if len(got.Data.LinkedEvents) > 30 {
		t.Errorf("expected chain-context to bound result (~22 events), got %d — looks like seed session was returned in full", len(got.Data.LinkedEvents))
	}
}
