package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dongqiu/agent-lens/internal/attest"
)

// makeReport returns a tiny, internally-consistent Report that
// individual tests then mutate to introduce specific failures.
func makeReport(t *testing.T) *Report {
	t.Helper()
	r := &Report{
		Version:     Version,
		GeneratedAt: "2026-04-28T00:00:00Z",
		Generator:   "test",
		StoreURL:    "http://lens.test",
		RootEventID: "e1",
		Sessions: []Session{
			{
				SessionID: "s1",
				HeadHash:  "h2",
				Events: []Event{
					{ID: "e1", TS: "2026-04-28T00:00:00Z", SessionID: "s1", Kind: "PROMPT",
						Actor: Actor{Type: "HUMAN", ID: "alice"},
						Hash:  "h1"},
					{ID: "e2", TS: "2026-04-28T00:00:01Z", SessionID: "s1", Kind: "THOUGHT",
						Actor: Actor{Type: "AGENT", ID: "claude-code"},
						Hash:  "h2", PrevHash: "h1"},
				},
			},
		},
	}
	m, err := computeManifest(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Manifest = m
	return r
}

func TestVerifyHappyPath(t *testing.T) {
	r := makeReport(t)
	res, err := Verify(r, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 0 {
		t.Errorf("expected zero issues on a clean report, got: %v", res.Issues)
	}
	if res.SessionsCount != 1 || res.EventsCount != 2 {
		t.Errorf("counts wrong: %+v", res)
	}
}

func TestVerifyRejectsUnknownVersion(t *testing.T) {
	r := makeReport(t)
	r.Version = "agent-lens.dev/audit-report/v999"
	res, _ := Verify(r, VerifyOptions{})
	if len(res.Issues) == 0 || !strings.Contains(res.Issues[0], "unrecognized") {
		t.Errorf("expected version issue, got %v", res.Issues)
	}
}

func TestVerifyDetectsSessionsTamper(t *testing.T) {
	r := makeReport(t)
	// Mutate an event after manifest is computed → sessions hash drifts.
	r.Sessions[0].Events[0].Kind = "TAMPERED"
	res, _ := Verify(r, VerifyOptions{})
	if len(res.Issues) == 0 {
		t.Fatal("expected manifest mismatch issue")
	}
	found := false
	for _, iss := range res.Issues {
		if strings.Contains(iss, "sessions_sha256") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected sessions_sha256 issue, got: %v", res.Issues)
	}
}

func TestVerifyDetectsBrokenChain(t *testing.T) {
	r := makeReport(t)
	r.Sessions[0].Events[1].PrevHash = "not-the-right-hash"
	// Recompute manifest so we don't also trip the sessions_sha256 check —
	// the test isolates the chain-walk failure.
	m, _ := computeManifest(r)
	r.Manifest = m
	res, _ := Verify(r, VerifyOptions{})
	if len(res.Issues) == 0 {
		t.Fatal("expected chain mismatch")
	}
	if !strings.Contains(res.Issues[0], "prev_hash") {
		t.Errorf("expected prev_hash issue, got: %v", res.Issues)
	}
}

func TestVerifyDetectsHeadHashMismatch(t *testing.T) {
	r := makeReport(t)
	r.Sessions[0].HeadHash = "wrong-head"
	m, _ := computeManifest(r)
	r.Manifest = m
	res, _ := Verify(r, VerifyOptions{})
	found := false
	for _, iss := range res.Issues {
		if strings.Contains(iss, "head_hash") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected head_hash issue, got: %v", res.Issues)
	}
}

func TestVerifyAttestationRehashAndDSSE(t *testing.T) {
	priv, pub, err := attest.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	stmt := attest.Statement{
		Type: attest.InTotoStatementType,
		Subject: []attest.Subject{
			{Name: "x", Digest: map[string]string{"sha256": "abc"}},
		},
		PredicateType: "agent-lens.dev/code-provenance/v1",
		Predicate:     json.RawMessage(`{}`),
	}
	stmtBytes, _ := json.Marshal(stmt)
	env, err := attest.Sign(priv, attest.InTotoPayloadType, stmtBytes)
	if err != nil {
		t.Fatal(err)
	}
	envBytes, _ := json.Marshal(env)

	atts, err := loadAttestationBytes(map[string][]byte{"test.intoto.jsonl": envBytes})
	if err != nil {
		t.Fatal(err)
	}

	r := makeReport(t)
	r.Attestations = atts
	m, _ := computeManifest(r)
	r.Manifest = m

	// With pub key: DSSE verifies, count goes up.
	res, _ := Verify(r, VerifyOptions{PubKey: pub})
	if len(res.Issues) != 0 {
		t.Fatalf("clean report with valid attestation should have no issues, got: %v", res.Issues)
	}
	if res.AttestationsVerified != 1 || res.AttestationsSkipped != 0 {
		t.Errorf("expected 1 verified, got %+v", res)
	}

	// Without pub key: skipped, no issues.
	res2, _ := Verify(r, VerifyOptions{})
	if len(res2.Issues) != 0 {
		t.Errorf("nil pub key should skip without issues, got: %v", res2.Issues)
	}
	if res2.AttestationsVerified != 0 || res2.AttestationsSkipped != 1 {
		t.Errorf("expected skip when pub key nil, got %+v", res2)
	}

	// Tampered attestation bytes (change one byte AFTER hash recorded) →
	// re-hash mismatch.
	tampered := append([]byte{}, envBytes...)
	tampered[0] = 'X'
	r.Attestations[0].EnvelopeB64 = base64.StdEncoding.EncodeToString(tampered)
	m, _ = computeManifest(r) // re-record manifest so we isolate the rehash issue
	r.Manifest = m
	res3, _ := Verify(r, VerifyOptions{PubKey: pub})
	if len(res3.Issues) == 0 {
		t.Fatal("expected attestation rehash mismatch")
	}
}

func TestVerifyWrongPubKeyFlagsAttestation(t *testing.T) {
	priv, _, _ := attest.GenerateKey()
	_, otherPub, _ := attest.GenerateKey()
	stmt := attest.Statement{
		Type:          attest.InTotoStatementType,
		Subject:       []attest.Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
		PredicateType: "x", Predicate: json.RawMessage(`{}`),
	}
	stmtBytes, _ := json.Marshal(stmt)
	env, _ := attest.Sign(priv, attest.InTotoPayloadType, stmtBytes)
	envBytes, _ := json.Marshal(env)
	atts, _ := loadAttestationBytes(map[string][]byte{"x.intoto.jsonl": envBytes})

	r := makeReport(t)
	r.Attestations = atts
	m, _ := computeManifest(r)
	r.Manifest = m

	res, _ := Verify(r, VerifyOptions{PubKey: otherPub})
	if len(res.Issues) == 0 {
		t.Fatal("expected DSSE verify failure with wrong key")
	}
	if !strings.Contains(strings.Join(res.Issues, "|"), "DSSE") {
		t.Errorf("expected DSSE error, got: %v", res.Issues)
	}
}

// loadAttestationBytes is a test helper that mirrors loadAttestations
// without needing files on disk — keyed by name → bytes.
func loadAttestationBytes(in map[string][]byte) ([]Attestation, error) {
	out := make([]Attestation, 0, len(in))
	// Iterate in deterministic order so tests don't flake on map order.
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	// No sort.Strings here on purpose — tests pass a single key today;
	// add sort if multi-attestation cases land later.
	for _, k := range keys {
		raw := in[k]
		out = append(out, Attestation{
			Filename:    k,
			Sha256:      hexHash(raw),
			EnvelopeB64: base64.StdEncoding.EncodeToString(raw),
		})
	}
	return out, nil
}

func hexHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
