package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dong-qiu/agent-lens/internal/attest"
)

// TestSignReleaseHappyPath signs a tiny test binary, then walks the
// envelope back to confirm payloadType, subject digest, predicateType,
// and that the signature verifies under the matching public key. One
// test rather than a suite per the v0.1 implementation prompt — this
// happy path exercises every field that downstream verifiers (cosign,
// agent-lens-hook verify-attestation) actually read.
func TestSignReleaseHappyPath(t *testing.T) {
	dir := t.TempDir()

	// Use the existing test helper from verify_attestation_test.go so
	// the keypair lives on disk in the same PEM format the real
	// command will load.
	priv, pubPath := saveTestKey(t, dir, "ed25519")
	keyPath := filepath.Join(dir, "ed25519")

	// A platform-bearing filename so we also assert detectPlatform
	// flows through the predicate. Body is arbitrary — we just need
	// stable bytes to recompute sha256 over.
	binPath := filepath.Join(dir, "agent-lens-darwin-arm64")
	body := []byte("hello\n")
	if err := os.WriteFile(binPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	wantHex := hex.EncodeToString(want[:])

	outPath := filepath.Join(dir, "agent-lens-darwin-arm64.intoto.jsonl")
	var stdout bytes.Buffer
	if err := signReleaseCore(
		[]string{"--key", keyPath, "--in", binPath, "--out", outPath},
		&stdout,
	); err != nil {
		t.Fatalf("signReleaseCore: %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var env attest.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.PayloadType != attest.InTotoPayloadType {
		t.Errorf("payloadType = %q, want %q", env.PayloadType, attest.InTotoPayloadType)
	}

	pub, err := attest.LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("load public key: %v", err)
	}
	payload, _, err := attest.Verify(pub, &env)
	if err != nil {
		t.Fatalf("verify envelope: %v", err)
	}
	if env.Signatures[0].KeyID != priv.KeyID {
		t.Errorf("envelope keyid = %q, want %q", env.Signatures[0].KeyID, priv.KeyID)
	}

	var stmt attest.Statement
	if err := json.Unmarshal(payload, &stmt); err != nil {
		t.Fatalf("unmarshal statement: %v", err)
	}
	if stmt.PredicateType != ReleaseArtifactPredicate {
		t.Errorf("predicateType = %q, want %q", stmt.PredicateType, ReleaseArtifactPredicate)
	}
	if len(stmt.Subject) != 1 {
		t.Fatalf("got %d subjects, want 1", len(stmt.Subject))
	}
	if got := stmt.Subject[0].Digest["sha256"]; got != wantHex {
		t.Errorf("subject sha256 = %q, want %q", got, wantHex)
	}
	if stmt.Subject[0].Name != "agent-lens-darwin-arm64" {
		t.Errorf("subject name = %q, want basename of --in", stmt.Subject[0].Name)
	}

	var pred releaseArtifactPredicate
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		t.Fatalf("unmarshal predicate: %v", err)
	}
	if pred.Platform != "darwin/arm64" {
		t.Errorf("platform = %q, want darwin/arm64", pred.Platform)
	}
	if pred.BuiltAt == "" {
		t.Error("builtAt is empty; want RFC3339 timestamp")
	}
}
