package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

func TestExportSLSABuildEndToEnd(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Two BUILD events on the same per-run session: one workflow_run
	// webhook (lifecycle metadata) and one composite-action (artifact
	// hashes, the SLSA subjects).
	body := strings.Join([]string{
		`{"session_id":"github-build:acme/widget/123","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"workflow_run":{"id":123,"name":"CI","status":"completed","conclusion":"success","head_sha":"deadbeefcafe","head_branch":"main","run_started_at":"2026-04-27T10:00:00Z","updated_at":"2026-04-27T10:05:00Z"}}}`,
		`{"session_id":"github-build:acme/widget/123","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"source":"composite-action","status":"success","workflow":"CI","run_id":"123","run_number":"42","run_attempt":"1","ref":"refs/heads/main","sha":"deadbeefcafe","artifacts":[{"path":"dist/widget.tar.gz","sha256":"abc111","bytes":12345},{"path":"dist/widget.bin","sha256":"def222","bytes":67890}]}}`,
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
	args := []string{
		"--session", "github-build:acme/widget/123",
		"--repo", "https://github.com/acme/widget",
		"--key", keyPath,
		"--url", srv.URL,
	}
	if err := exportSLSABuild(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}

	var env attest.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	payload, _, err := attest.Verify(pub, &env)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	if stmt.PredicateType != attest.SLSAProvenancePredicate {
		t.Errorf("predicateType = %q, want %q", stmt.PredicateType, attest.SLSAProvenancePredicate)
	}
	if len(stmt.Subject) != 2 {
		t.Fatalf("subjects = %d, want 2", len(stmt.Subject))
	}
	if stmt.Subject[0].Name != "dist/widget.tar.gz" || stmt.Subject[0].Digest["sha256"] != "abc111" {
		t.Errorf("subject[0] = %+v", stmt.Subject[0])
	}

	var pred attest.SLSAProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if pred.BuildDefinition.BuildType != attest.SLSABuildType {
		t.Errorf("buildType = %q", pred.BuildDefinition.BuildType)
	}
	if pred.BuildDefinition.ExternalParameters["workflow"] != "CI" {
		t.Errorf("externalParameters.workflow = %v", pred.BuildDefinition.ExternalParameters["workflow"])
	}
	if pred.BuildDefinition.InternalParameters["run_id"] != "123" {
		t.Errorf("internalParameters.run_id = %v", pred.BuildDefinition.InternalParameters["run_id"])
	}
	if pred.RunDetails.Builder.ID != attest.SLSABuilderID {
		t.Errorf("builder.id = %q", pred.RunDetails.Builder.ID)
	}
	if pred.RunDetails.Metadata.InvocationID != "123" {
		t.Errorf("metadata.invocationId = %q", pred.RunDetails.Metadata.InvocationID)
	}

	// resolvedDependencies should have the source commit
	if len(pred.BuildDefinition.ResolvedDependencies) != 1 {
		t.Fatalf("resolvedDependencies = %d, want 1", len(pred.BuildDefinition.ResolvedDependencies))
	}
	if pred.BuildDefinition.ResolvedDependencies[0].URI != "git+https://github.com/acme/widget@deadbeefcafe" {
		t.Errorf("dep URI = %q", pred.BuildDefinition.ResolvedDependencies[0].URI)
	}
}

func TestExportSLSABuildErrorsWithoutCompositeAction(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Only the workflow_run webhook event — no composite-action,
	// therefore no artifact subjects available.
	body := `{"session_id":"github-build:acme/widget/456","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"workflow_run":{"id":456,"name":"CI","status":"completed","conclusion":"success","head_sha":"deadbeef"}}}`
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
		"--session", "github-build:acme/widget/456",
		"--key", keyPath,
		"--url", srv.URL,
	}
	err = exportSLSABuild(args, &buf)
	if err == nil {
		t.Fatal("expected error when no composite-action event, got nil")
	}
	if !strings.Contains(err.Error(), "composite-action") {
		t.Errorf("error doesn't mention composite-action: %v", err)
	}
}

func TestExportSLSABuildRequiresSession(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, _, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	if err := exportSLSABuild([]string{"--key", keyPath}, &buf); err == nil {
		t.Error("expected error when --session missing")
	}
}

func TestBuildSLSAInputsFromEventsCompositeActionPreferred(t *testing.T) {
	// When both composite-action and workflow_run events are present,
	// composite-action's flat fields win over webhook's nested fields
	// (composite values are more authoritative — they came from inside
	// the build itself).
	events := []provenanceEvent{
		{
			Kind: "BUILD",
			Payload: map[string]any{
				"workflow_run": map[string]any{
					"id":          float64(999),
					"name":        "OTHER",
					"head_sha":    "fromwebhook",
					"head_branch": "fromwebhook",
				},
			},
		},
		{
			Kind: "BUILD",
			Payload: map[string]any{
				"source":   "composite-action",
				"workflow": "CI-PREFERRED",
				"run_id":   "111",
				"sha":      "frominsidebuild",
				"ref":      "refs/heads/main",
				"artifacts": []any{
					map[string]any{"path": "x", "sha256": "abc"},
				},
			},
		},
	}
	in, err := buildSLSAInputsFromEvents(events, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.WorkflowName != "CI-PREFERRED" {
		t.Errorf("workflow = %q, want CI-PREFERRED (composite action wins)", in.WorkflowName)
	}
	if in.RunID != "111" {
		t.Errorf("run_id = %q, want 111", in.RunID)
	}
	if in.CommitSHA != "frominsidebuild" {
		t.Errorf("sha = %q, want frominsidebuild", in.CommitSHA)
	}
	if in.Ref != "refs/heads/main" {
		t.Errorf("ref = %q", in.Ref)
	}
}

func TestExportSLSABuildBuilderIDFlag(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := `{"session_id":"github-build:acme/widget/789","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"source":"composite-action","status":"success","workflow":"CI","run_id":"789","sha":"deadbeef","artifacts":[{"path":"x","sha256":"abc"}]}}`
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
		"--session", "github-build:acme/widget/789",
		"--builder-id", "https://acme.example.com/runner/self-hosted",
		"--key", keyPath,
		"--url", srv.URL,
	}
	if err := exportSLSABuild(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var env attest.Envelope
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	payload, _, _ := attest.Verify(pub, &env)
	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	var pred attest.SLSAProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if pred.RunDetails.Builder.ID != "https://acme.example.com/runner/self-hosted" {
		t.Errorf("builder.id = %q, want override", pred.RunDetails.Builder.ID)
	}
}

// postDeployEvent injects a kind=deploy event via NDJSON so we don't
// need to wire the deploy webhook handler into every test. It returns
// the server-assigned event id (read back via GraphQL) so the exporter
// has something to look up.
func postDeployEvent(t *testing.T, srvURL, sessionID string, payload map[string]any) string {
	t.Helper()
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	wire := map[string]any{
		"session_id": sessionID,
		"actor":      map[string]any{"type": "system", "id": "deploy-system"},
		"kind":       "deploy",
		"payload":    json.RawMessage(rawPayload),
	}
	wireBytes, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srvURL+"/v1/events", "application/x-ndjson", bytes.NewReader(wireBytes))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Read the event id back via GraphQL — NDJSON's HTTP response body
	// only carries counts, not assigned ids.
	gqlBody, _ := json.Marshal(map[string]any{
		"query": `query($s: String!) { events(sessionId: $s, limit: 10) { id } }`,
		"variables": map[string]any{"s": sessionID},
	})
	resp2, err := http.Post(srvURL+"/v1/graphql", "application/json", bytes.NewReader(gqlBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out struct {
		Data struct {
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Events) == 0 {
		t.Fatalf("no events found for session %q", sessionID)
	}
	return out.Data.Events[0].ID
}

func TestExportDeployEvidenceEndToEnd(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	eventID := postDeployEvent(t, srv.URL, "deploy:production", map[string]any{
		"environment":  "production",
		"image":        "ghcr.io/acme/widget",
		"image_digest": "sha256:deadbeef0123456789",
		"platform":     "k8s",
		"cluster":      "prod-us-east",
		"namespace":    "default",
		"deployed_by":  "alice",
		"status":       "succeeded",
		"git_sha":      "feedface1234",
		"started_at":   "2026-04-27T10:00:00Z",
		"finished_at":  "2026-04-27T10:01:00Z",
	})

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, pub, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	// Pre-create dummy upstream attestation files so DigestFile has
	// something to hash. Real callers point at .intoto.jsonl outputs
	// from earlier export runs.
	buildAtt := filepath.Join(dir, "build.intoto.jsonl")
	codeAtt := filepath.Join(dir, "code.intoto.jsonl")
	if err := os.WriteFile(buildAtt, []byte("BUILD-ATTESTATION-CONTENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codeAtt, []byte("CODE-ATTESTATION-CONTENTS"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	args := []string{
		"--event", eventID,
		"--build-attestation", buildAtt,
		"--code-attestation", codeAtt,
		"--key", keyPath,
		"--url", srv.URL,
	}
	if err := exportDeployEvidence(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}

	var env attest.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	payload, _, err := attest.Verify(pub, &env)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	if stmt.PredicateType != attest.DeployEvidencePredicate {
		t.Errorf("predicateType = %q", stmt.PredicateType)
	}
	if len(stmt.Subject) != 1 ||
		stmt.Subject[0].Name != "ghcr.io/acme/widget" ||
		stmt.Subject[0].Digest["sha256"] != "deadbeef0123456789" {
		t.Errorf("subject = %+v", stmt.Subject)
	}

	var pred attest.DeployEvidence
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if pred.Environment != "production" {
		t.Errorf("environment = %q", pred.Environment)
	}
	if pred.Cluster != "prod-us-east" {
		t.Errorf("cluster = %q", pred.Cluster)
	}
	if pred.Status != "succeeded" {
		t.Errorf("status = %q", pred.Status)
	}
	if pred.DeployedAt != "2026-04-27T10:01:00Z" {
		t.Errorf("deployed_at = %q, want finished_at", pred.DeployedAt)
	}
	if pred.Upstream.GitCommit != "feedface1234" {
		t.Errorf("upstream.git_commit = %q", pred.Upstream.GitCommit)
	}
	if pred.Upstream.BuildAttestationDigest == "" {
		t.Errorf("upstream.build_attestation should be a sha256")
	}
	wantBuildDigest, _ := attest.DigestFile(buildAtt)
	if pred.Upstream.BuildAttestationDigest != wantBuildDigest {
		t.Errorf("upstream.build_attestation = %q, want %q", pred.Upstream.BuildAttestationDigest, wantBuildDigest)
	}
	wantCodeDigest, _ := attest.DigestFile(codeAtt)
	if pred.Upstream.CodeAttestationDigest != wantCodeDigest {
		t.Errorf("upstream.code_attestation = %q, want %q", pred.Upstream.CodeAttestationDigest, wantCodeDigest)
	}
	if pred.TraceRootEventID != eventID {
		t.Errorf("trace_root_event_id = %q, want %q", pred.TraceRootEventID, eventID)
	}
}

func TestExportDeployEvidenceWithoutUpstreamAttestations(t *testing.T) {
	// Upstream attestation flags are optional — the deploy is still
	// signable, but predicate.upstream.{build,code} are empty so a
	// verifier knows the chain is incomplete.
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	eventID := postDeployEvent(t, srv.URL, "deploy:staging", map[string]any{
		"environment":  "staging",
		"image_digest": "sha256:abc",
	})

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, pub, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	if err := exportDeployEvidence(
		[]string{"--event", eventID, "--key", keyPath, "--url", srv.URL},
		&buf,
	); err != nil {
		t.Fatalf("export: %v", err)
	}
	var env attest.Envelope
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	payload, _, _ := attest.Verify(pub, &env)
	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	var pred attest.DeployEvidence
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if pred.Upstream.BuildAttestationDigest != "" || pred.Upstream.CodeAttestationDigest != "" {
		t.Errorf("upstream digests should be empty without flags: %+v", pred.Upstream)
	}
}

func TestExportDeployEvidenceRejectsNonDeployEvent(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Post a PROMPT event, then ask exportDeployEvidence to use its id —
	// should fail because kind != DEPLOY.
	body := `{"session_id":"s-not-deploy","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"hi"}}`
	resp, err := http.Post(srv.URL+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	gqlBody, _ := json.Marshal(map[string]any{
		"query": `query { events(sessionId: "s-not-deploy", limit: 1) { id } }`,
	})
	resp2, err := http.Post(srv.URL+"/v1/graphql", "application/json", bytes.NewReader(gqlBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out struct {
		Data struct {
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out)
	if len(out.Data.Events) == 0 {
		t.Fatal("event missing")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, _, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	err = exportDeployEvidence(
		[]string{"--event", out.Data.Events[0].ID, "--key", keyPath, "--url", srv.URL},
		&buf,
	)
	if err == nil {
		t.Fatal("expected error for non-DEPLOY event")
	}
	if !strings.Contains(err.Error(), "DEPLOY") {
		t.Errorf("error should mention DEPLOY: %v", err)
	}
}

func TestExportDeployEvidenceMissingEventErrors(t *testing.T) {
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, _, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	err := exportDeployEvidence(
		[]string{"--event", "01HNOSUCH", "--key", keyPath, "--url", srv.URL},
		&buf,
	)
	if err == nil {
		t.Fatal("expected error when event id is unknown")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found: %v", err)
	}
}

func TestExportDeployEvidenceRequiresEventFlag(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, _, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)

	var buf bytes.Buffer
	if err := exportDeployEvidence([]string{"--key", keyPath}, &buf); err == nil {
		t.Error("expected error when --event missing")
	}
}

func TestExportSLSABuildHandlesLargeRunID(t *testing.T) {
	// json.Number path: a large numeric run_id from the workflow_run
	// webhook decodes correctly without precision loss. 13-digit run
	// IDs are well within float64 mantissa today, but UseNumber is
	// the forward-compatible idiom — this test pins it.
	st := store.NewMemory()
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		ingest.RegisterRoutes(sub, st)
		query.RegisterRoutes(sub, st)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	const bigID = 9007199254740991 // 2^53 - 1, the largest exact float64 integer
	body := strings.Join([]string{
		`{"session_id":"github-build:acme/widget/big","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"workflow_run":{"id":9007199254740991,"name":"CI","head_sha":"deadbeef"}}}`,
		`{"session_id":"github-build:acme/widget/big","actor":{"type":"system","id":"CI"},"kind":"build","payload":{"source":"composite-action","status":"success","workflow":"CI","sha":"deadbeef","artifacts":[{"path":"x","sha256":"abc"}]}}`,
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
	args := []string{
		"--session", "github-build:acme/widget/big",
		"--key", keyPath,
		"--url", srv.URL,
	}
	if err := exportSLSABuild(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var env attest.Envelope
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	payload, _, _ := attest.Verify(pub, &env)
	var stmt attest.Statement
	_ = json.Unmarshal(payload, &stmt)
	var pred attest.SLSAProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)

	// composite-action run_id was empty for this test; webhook id should populate.
	wantID := strconv.FormatInt(int64(bigID), 10)
	if pred.RunDetails.Metadata.InvocationID != wantID {
		t.Errorf("invocationId = %q, want %q (%d)", pred.RunDetails.Metadata.InvocationID, wantID, bigID)
	}
}
