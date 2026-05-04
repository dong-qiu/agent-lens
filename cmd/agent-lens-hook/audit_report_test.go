package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dong-qiu/agent-lens/internal/attest"
	"github.com/dong-qiu/agent-lens/internal/audit"
	"github.com/dong-qiu/agent-lens/internal/ingest"
	"github.com/dong-qiu/agent-lens/internal/linking"
	"github.com/dong-qiu/agent-lens/internal/query"
	"github.com/dong-qiu/agent-lens/internal/store"
)

// startHookTestServer mirrors internal/audit.startTestServer but lives
// here so the CLI tests don't depend on a test helper from another
// package. Returns the URL.
func startHookTestServer(t *testing.T) string {
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

func TestExportAuditReportRoundTrip(t *testing.T) {
	url := startHookTestServer(t)

	// Single-session trace, two events.
	body := strings.Join([]string{
		`{"session_id":"s-roundtrip","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"hi"}}`,
		`{"session_id":"s-roundtrip","actor":{"type":"agent","id":"claude-code"},"kind":"thought","payload":{"text":"thinking"}}`,
	}, "\n")
	resp, err := http.Post(url+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Get root id via GraphQL.
	gql, _ := json.Marshal(map[string]any{
		"query": `query { events(sessionId: "s-roundtrip", limit: 1) { id } }`,
	})
	resp2, err := http.Post(url+"/v1/graphql", "application/json", bytes.NewReader(gql))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var idOut struct {
		Data struct {
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&idOut)
	if len(idOut.Data.Events) == 0 {
		t.Fatal("missing event id")
	}
	rootID := idOut.Data.Events[0].ID

	// Build a real DSSE attestation to embed.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ed25519")
	priv, pub, _ := attest.GenerateKey()
	_ = attest.SaveKeyPair(keyPath, priv)
	stmt := attest.Statement{
		Type:          attest.InTotoStatementType,
		Subject:       []attest.Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
		PredicateType: "agent-lens.dev/code-provenance/v1",
		Predicate:     json.RawMessage(`{}`),
	}
	stmtBytes, _ := json.Marshal(stmt)
	env, _ := attest.Sign(priv, attest.InTotoPayloadType, stmtBytes)
	envBytes, _ := json.Marshal(env)
	attPath := filepath.Join(dir, "code.intoto.jsonl")
	if err := os.WriteFile(attPath, envBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Run the export CLI.
	reportPath := filepath.Join(dir, "report.json")
	var buf bytes.Buffer
	args := []string{
		"--root", rootID,
		"--attestation", attPath,
		"--out", reportPath,
		"--url", url,
	}
	if err := runExportAuditReport(args, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}

	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var r audit.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	if r.RootEventID != rootID {
		t.Errorf("root_event_id = %q", r.RootEventID)
	}
	if len(r.Sessions) != 1 || len(r.Sessions[0].Events) != 2 {
		t.Errorf("unexpected sessions: %+v", r.Sessions)
	}
	if len(r.Attestations) != 1 {
		t.Fatalf("expected 1 attestation embedded, got %d", len(r.Attestations))
	}

	// Verify the report via the verify CLI, with the matching key.
	pubPath := keyPath + ".pub"
	_ = pub
	var vout bytes.Buffer
	if err := verifyAuditReportCore([]string{"--pub", pubPath, reportPath}, &vout); err != nil {
		t.Fatalf("verify: %v\n%s", err, vout.String())
	}
	got := vout.String()
	if !strings.Contains(got, "OK ·") {
		t.Errorf("expected OK summary, got: %q", got)
	}
	if !strings.Contains(got, "1 verified") {
		t.Errorf("expected `1 verified` count: %q", got)
	}
}

func TestVerifyAuditReportDetectsTamper(t *testing.T) {
	url := startHookTestServer(t)
	body := `{"session_id":"s-tamper","actor":{"type":"human","id":"alice"},"kind":"prompt","payload":{"text":"hi"}}`
	resp, _ := http.Post(url+"/v1/events", "application/x-ndjson", strings.NewReader(body))
	resp.Body.Close()
	gql, _ := json.Marshal(map[string]any{
		"query": `query { events(sessionId: "s-tamper", limit: 1) { id } }`,
	})
	resp2, err := http.Post(url+"/v1/graphql", "application/json", bytes.NewReader(gql))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var idOut struct {
		Data struct {
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&idOut)
	rootID := idOut.Data.Events[0].ID

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	var out bytes.Buffer
	if err := runExportAuditReport(
		[]string{"--root", rootID, "--out", reportPath, "--url", url},
		&out,
	); err != nil {
		t.Fatal(err)
	}

	// Tamper: overwrite an event field but DON'T re-compute manifest.
	raw, _ := os.ReadFile(reportPath)
	var r audit.Report
	_ = json.Unmarshal(raw, &r)
	r.Sessions[0].Events[0].Kind = "TAMPERED"
	tamperedBytes, _ := json.MarshalIndent(&r, "", "  ")
	if err := os.WriteFile(reportPath, tamperedBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify with explicit --pub= (skip DSSE), so we isolate the manifest
	// failure.
	var vout bytes.Buffer
	err = verifyAuditReportCore([]string{"--pub", "", reportPath}, &vout)
	if err == nil {
		t.Fatal("expected verify to fail on tampered report")
	}
	if !isAuditReportIssue(err) {
		t.Errorf("expected auditReportIssue (exit 1), got: %v", err)
	}
	if !strings.Contains(vout.String(), "sessions_sha256") {
		t.Errorf("expected sessions_sha256 issue in output: %q", vout.String())
	}
}

func TestExportAuditReportRequiresRoot(t *testing.T) {
	var buf bytes.Buffer
	if err := runExportAuditReport([]string{}, &buf); err == nil {
		t.Error("expected error when --root missing")
	}
}

func TestVerifyAuditReportArgCount(t *testing.T) {
	dir := t.TempDir()
	r := audit.Report{Version: audit.Version}
	body, _ := json.MarshalIndent(&r, "", "  ")
	path := filepath.Join(dir, "r.json")
	_ = os.WriteFile(path, body, 0o644)

	var out bytes.Buffer
	// no file
	if err := verifyAuditReportCore([]string{"--pub", ""}, &out); err == nil {
		t.Error("expected error with no file")
	}
	// two files
	var out2 bytes.Buffer
	if err := verifyAuditReportCore([]string{"--pub", "", path, path}, &out2); err == nil {
		t.Error("expected error with two files")
	}
}

func TestVerifyAuditReportMissingFile(t *testing.T) {
	var out bytes.Buffer
	err := verifyAuditReportCore([]string{"--pub", "", "/no/such/report.json"}, &out)
	if err == nil {
		t.Fatal("expected error on missing file")
	}
	if isAuditReportIssue(err) {
		t.Errorf("missing file should not be auditReportIssue (should be exit 2): %v", err)
	}
}
