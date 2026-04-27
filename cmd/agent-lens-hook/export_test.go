package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dongqiu/agent-lens/internal/attest"
	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/query"
	"github.com/dongqiu/agent-lens/internal/store"
)

// TestExportCodeProvenanceEndToEnd posts realistic AI-side events
// through ingest, runs the export CLI core against them, then parses,
// verifies, and asserts the resulting DSSE envelope. Catches drift
// between the ingest wire format, the GraphQL fetch path, the
// predicate builder, and the signer.
func TestExportCodeProvenanceEndToEnd(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	ndjson := strings.Join([]string{
		`{"session_id":"s-export","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"add a button to checkout"}}`,
		`{"session_id":"s-export","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"thought","payload":{"text":"plan: open checkout.tsx, insert <Button>, wire onClick"}}`,
		`{"session_id":"s-export","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"tool_call","payload":{"name":"Edit","input":{"file":"src/Checkout.tsx","old":"foo","new":"bar"}}}`,
		`{"session_id":"s-export","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"tool_result","payload":{"name":"Edit","response":{"ok":true}}}`,
		`{"session_id":"s-export","actor":{"type":"agent","id":"claude-code","model":"claude-opus-4-7"},"kind":"decision","payload":{"marker":"assistant_message","text":"Button added in src/Checkout.tsx."}}`,
	}, "\n")
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(ndjson))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, pub, err := attest.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	if err := attest.SaveKeyPair(keyPath, priv); err != nil {
		t.Fatalf("save key: %v", err)
	}

	var buf bytes.Buffer
	args := []string{
		"--commit", "deadbeefcafe1234567890abcdef0123456789ab",
		"--session", "s-export",
		"--key", keyPath,
		"--url", srv.URL,
		"--token", "",
	}
	if err := exportCodeProvenance(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}

	var env attest.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env); err != nil {
		t.Fatalf("parse envelope: %v\nraw: %s", err, buf.String())
	}
	if env.PayloadType != attest.InTotoPayloadType {
		t.Errorf("payloadType = %q, want %q", env.PayloadType, attest.InTotoPayloadType)
	}

	payload, gotType, err := attest.Verify(pub, &env)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotType != attest.InTotoPayloadType {
		t.Errorf("verified payloadType = %q", gotType)
	}

	var stmt attest.Statement
	if err := json.Unmarshal(payload, &stmt); err != nil {
		t.Fatalf("parse statement: %v", err)
	}
	if stmt.Type != attest.InTotoStatementType {
		t.Errorf("_type = %q", stmt.Type)
	}
	if stmt.PredicateType != attest.CodeProvenancePredicate {
		t.Errorf("predicateType = %q", stmt.PredicateType)
	}
	if len(stmt.Subject) != 1 ||
		stmt.Subject[0].Digest["gitCommit"] != "deadbeefcafe1234567890abcdef0123456789ab" {
		t.Errorf("subject = %+v", stmt.Subject)
	}

	var pred attest.CodeProvenance
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		t.Fatalf("parse predicate: %v", err)
	}
	if pred.Session.Model != "claude-opus-4-7" {
		t.Errorf("session.model = %q", pred.Session.Model)
	}
	if len(pred.Events) != 5 {
		t.Fatalf("events = %d, want 5 (prompt + thought + tool_call + tool_result + decision)", len(pred.Events))
	}

	// Spot-check that the first event has a digest+preview but NOT the
	// raw text (privacy contract).
	first := pred.Events[0]
	if first.Kind != "PROMPT" {
		t.Errorf("first event kind = %q, want PROMPT", first.Kind)
	}
	if !strings.HasPrefix(first.ContentDigest, "sha256:") {
		t.Errorf("content_digest = %q", first.ContentDigest)
	}
	if first.ContentPreview != "add a button to checkout" {
		t.Errorf("content_preview = %q", first.ContentPreview)
	}
	// The full prompt was 24 chars; preview equals full text. The point
	// is the predicate has a digest the verifier can use to look up
	// the matching event in the store and confirm content match.

	// tool_call kept ToolName but not the full input text (only digest).
	tc := pred.Events[2]
	if tc.Kind != "TOOL_CALL" || tc.ToolName != "Edit" {
		t.Errorf("tool_call event = %+v", tc)
	}
	if tc.ContentPreview != "" {
		t.Errorf("tool_call preview should be empty (binary-ish), got %q", tc.ContentPreview)
	}
	if tc.ContentDigest == "" {
		t.Errorf("tool_call content_digest should be set")
	}

	// Verify with the wrong public key fails.
	_, otherPub, _ := attest.GenerateKey()
	if _, _, err := attest.Verify(otherPub, &env); err == nil {
		t.Error("verify with wrong key should fail")
	}
}

func TestExportCodeProvenanceRequiresCommitAndSession(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, _, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	cases := [][]string{
		{"--commit", "abc"},                             // missing session
		{"--session", "s1"},                             // missing commit
		{"--commit", "", "--session", "s1", "--key", keyPath},
		{"--commit", "abc", "--session", "", "--key", keyPath},
	}
	for i, args := range cases {
		var buf bytes.Buffer
		if err := exportCodeProvenance(args, &buf); err == nil {
			t.Errorf("case %d: expected error, got nil (args=%v)", i, args)
		}
	}
}

func TestMapToProvenanceEventsFiltersAndExtracts(t *testing.T) {
	events := []provenanceEvent{
		{ID: "e1", Kind: "PROMPT", Payload: map[string]any{"text": "hello"}},
		{ID: "e2", Kind: "THOUGHT", Payload: map[string]any{"text": "thinking..."}},
		{ID: "e3", Kind: "TOOL_CALL", Payload: map[string]any{"name": "Edit", "input": map[string]any{"x": 1}}},
		{ID: "e4", Kind: "COMMIT", Payload: map[string]any{"sha": "abc"}}, // should be filtered out
		{ID: "e5", Kind: "DECISION", Payload: map[string]any{"marker": "turn_end"}},
	}
	got := mapToProvenanceEvents(events)
	if len(got) != 4 {
		t.Fatalf("got %d events, want 4 (commit filtered)", len(got))
	}
	ids := []string{got[0].ID, got[1].ID, got[2].ID, got[3].ID}
	want := []string{"e1", "e2", "e3", "e5"}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("event[%d].id = %q, want %q", i, ids[i], w)
		}
	}

	// PROMPT event has digest + preview
	if got[0].ContentPreview != "hello" || got[0].ContentDigest == "" {
		t.Errorf("prompt event = %+v", got[0])
	}
	// TOOL_CALL has tool name + digest, no preview
	if got[2].ToolName != "Edit" || got[2].ContentDigest == "" {
		t.Errorf("tool_call event = %+v", got[2])
	}
	if got[2].ContentPreview != "" {
		t.Errorf("tool_call preview should be empty, got %q", got[2].ContentPreview)
	}
	// DECISION has marker
	if got[3].Marker != "turn_end" {
		t.Errorf("decision marker = %q", got[3].Marker)
	}
}

func TestProvenanceSessionFromEventsPicksFirstAgentModel(t *testing.T) {
	events := []provenanceEvent{
		{Actor: provenanceActor{Type: "HUMAN", ID: "alice"}},
		{Actor: provenanceActor{Type: "AGENT", ID: "claude-code", Model: "claude-opus-4-7"}},
		{Actor: provenanceActor{Type: "AGENT", ID: "claude-code", Model: "ignored"}},
	}
	s := provenanceSessionFromEvents("sx", events)
	if s.ID != "sx" || s.Agent != "claude-code" {
		t.Errorf("session = %+v", s)
	}
	if s.Model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7 (first agent event)", s.Model)
	}

	// No agent event → empty model, no panic.
	s2 := provenanceSessionFromEvents("sx", []provenanceEvent{
		{Actor: provenanceActor{Type: "HUMAN", ID: "alice"}},
	})
	if s2.Model != "" {
		t.Errorf("expected empty model when no agent event, got %q", s2.Model)
	}
}

// silence unused import warnings if context is dropped from any test
var _ = context.Background

func TestExportCodeProvenanceRepoAppearsInSubjectName(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := `{"session_id":"s-repo","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"hi"}}`
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, pub, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	args := []string{
		"--commit", "abc",
		"--session", "s-repo",
		"--repo", "https://github.com/acme/widget",
		"--key", keyPath,
		"--url", srv.URL,
	}
	if err := exportCodeProvenance(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var env attest.Envelope
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	payload, _, err := attest.Verify(pub, &env)
	if err != nil {
		t.Fatal(err)
	}
	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	if stmt.Subject[0].Name != "git+https://github.com/acme/widget" {
		t.Errorf("subject name = %q, want %q", stmt.Subject[0].Name, "git+https://github.com/acme/widget")
	}
}

func TestExportCodeProvenanceLimitCapErrors(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Post 3 events; use --limit 2 to force the cap.
	body := strings.Join([]string{
		`{"session_id":"s-cap","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"a"}}`,
		`{"session_id":"s-cap","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"b"}}`,
		`{"session_id":"s-cap","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"c"}}`,
	}, "\n")
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, _, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	args := []string{
		"--commit", "abc",
		"--session", "s-cap",
		"--limit", "2",
		"--key", keyPath,
		"--url", srv.URL,
	}
	err = exportCodeProvenance(args, &buf)
	if err == nil {
		t.Fatal("expected error when --limit cap is hit, got nil")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error didn't mention limit: %v", err)
	}
}

func TestExportCodeProvenanceClientSortsByTS(t *testing.T) {
	// Defense-against-server-contract test: feed events whose TS are
	// in store-insertion order but lexicographically increasing, then
	// verify the predicate's metadata.started_at == earliest TS even
	// after our client-side sort runs.
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Three events; first inserted has the latest ts. Server's
	// ListBySession orders by ts ASC so we'd get them ts-sorted
	// anyway, but the test pins client-side sort by inspecting the
	// derived metadata.
	body := strings.Join([]string{
		`{"ts":"2026-04-27T10:00:30Z","session_id":"s-sort","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"third"}}`,
		`{"ts":"2026-04-27T10:00:10Z","session_id":"s-sort","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"first"}}`,
		`{"ts":"2026-04-27T10:00:20Z","session_id":"s-sort","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"second"}}`,
	}, "\n")
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, pub, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	args := []string{"--commit", "abc", "--session", "s-sort", "--key", keyPath, "--url", srv.URL}
	if err := exportCodeProvenance(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var env attest.Envelope
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	payload, _, _ := attest.Verify(pub, &env)
	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	var pred attest.CodeProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)

	if pred.Metadata.StartedAt != "2026-04-27T10:00:10Z" {
		t.Errorf("started_at = %q, want earliest 10:00:10", pred.Metadata.StartedAt)
	}
	if pred.Metadata.EndedAt != "2026-04-27T10:00:30Z" {
		t.Errorf("ended_at = %q, want latest 10:00:30", pred.Metadata.EndedAt)
	}
}
