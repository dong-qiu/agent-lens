package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongqiu/agent-lens/internal/attest"
)

// writeSignedEnvelope signs a tiny in-toto Statement with priv, dumps
// the DSSE envelope to a .intoto.jsonl file under dir, and returns the
// path. Tests use this to set up the inputs verify-attestation expects.
func writeSignedEnvelope(t *testing.T, dir string, priv *attest.PrivateKey, predicateType string) string {
	t.Helper()
	stmt := attest.Statement{
		Type:          attest.InTotoStatementType,
		PredicateType: predicateType,
		Subject: []attest.Subject{
			{Name: "test", Digest: map[string]string{"sha256": "abc123"}},
		},
		Predicate: json.RawMessage(`{"hello":"world"}`),
	}
	stmtBytes, err := json.Marshal(stmt)
	if err != nil {
		t.Fatal(err)
	}
	env, err := attest.Sign(priv, attest.InTotoPayloadType, stmtBytes)
	if err != nil {
		t.Fatal(err)
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "att.intoto.jsonl")
	if err := os.WriteFile(path, append(envBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// saveTestKey writes a fresh keypair under dir using SaveKeyPair and
// returns (privKey, pubKeyPath). The public file is path+".pub".
func saveTestKey(t *testing.T, dir, name string) (*attest.PrivateKey, string) {
	t.Helper()
	priv, _, err := attest.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, name)
	if err := attest.SaveKeyPair(keyPath, priv); err != nil {
		t.Fatal(err)
	}
	return priv, keyPath + ".pub"
}

func TestVerifyAttestationHappyPath(t *testing.T) {
	dir := t.TempDir()
	priv, pubPath := saveTestKey(t, dir, "ed25519")
	envPath := writeSignedEnvelope(t, dir, priv, "agent-lens.dev/code-provenance/v1")

	var out bytes.Buffer
	if err := verifyAttestationCore([]string{"--pub", pubPath, envPath}, &out); err != nil {
		t.Fatalf("verify: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "OK ·") {
		t.Errorf("output missing OK marker: %q", got)
	}
	if !strings.Contains(got, "code-provenance") {
		t.Errorf("output missing predicateType: %q", got)
	}
	if !strings.Contains(got, "subject: test (sha256:abc123)") {
		t.Errorf("output missing subject: %q", got)
	}
}

func TestVerifyAttestationWrongKeyFails(t *testing.T) {
	dir := t.TempDir()
	priv, _, err := attest.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	// Save a *different* keypair — the one paired with this private
	// key isn't who signed the envelope.
	_, pubPath := saveTestKey(t, dir, "wrong")
	envPath := writeSignedEnvelope(t, dir, priv, "agent-lens.dev/code-provenance/v1")

	var out bytes.Buffer
	err = verifyAttestationCore([]string{"--pub", pubPath, envPath}, &out)
	if err == nil {
		t.Fatal("expected verify failure with wrong key, got nil")
	}
	if !isVerifyFailure(err) {
		t.Errorf("expected verifyFailure (exit 1), got plain error: %v", err)
	}
}

func TestVerifyAttestationRequireTypeMatch(t *testing.T) {
	dir := t.TempDir()
	priv, pubPath := saveTestKey(t, dir, "ed25519")
	envPath := writeSignedEnvelope(t, dir, priv, "agent-lens.dev/code-provenance/v1")

	// Matching --require-type passes.
	var out bytes.Buffer
	if err := verifyAttestationCore(
		[]string{"--pub", pubPath, "--require-type", "agent-lens.dev/code-provenance/v1", envPath},
		&out,
	); err != nil {
		t.Fatalf("verify with matching require-type: %v", err)
	}

	// Mismatched --require-type fails as a verify error (exit 1).
	var out2 bytes.Buffer
	err := verifyAttestationCore(
		[]string{"--pub", pubPath, "--require-type", "https://slsa.dev/provenance/v1", envPath},
		&out2,
	)
	if err == nil {
		t.Fatal("expected error on require-type mismatch")
	}
	if !isVerifyFailure(err) {
		t.Errorf("expected verifyFailure on type mismatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "predicateType") {
		t.Errorf("error should mention predicateType: %v", err)
	}
}

func TestVerifyAttestationMalformedEnvelope(t *testing.T) {
	dir := t.TempDir()
	_, pubPath := saveTestKey(t, dir, "ed25519")

	envPath := filepath.Join(dir, "bad.intoto.jsonl")
	if err := os.WriteFile(envPath, []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := verifyAttestationCore([]string{"--pub", pubPath, envPath}, &out)
	if err == nil {
		t.Fatal("expected error decoding malformed envelope")
	}
	if !isVerifyFailure(err) {
		t.Errorf("expected verifyFailure for malformed json (exit 1), got: %v", err)
	}
}

func TestVerifyAttestationMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, pubPath := saveTestKey(t, dir, "ed25519")

	var out bytes.Buffer
	err := verifyAttestationCore([]string{"--pub", pubPath, "/no/such/attestation.jsonl"}, &out)
	if err == nil {
		t.Fatal("expected error on missing file")
	}
	// Missing file is a usage / file error (exit 2), not a verification
	// failure (exit 1).
	if isVerifyFailure(err) {
		t.Errorf("missing file should not be a verifyFailure: %v", err)
	}
}

func TestVerifyAttestationRequiresExactlyOneFile(t *testing.T) {
	dir := t.TempDir()
	_, pubPath := saveTestKey(t, dir, "ed25519")

	var out bytes.Buffer
	if err := verifyAttestationCore([]string{"--pub", pubPath}, &out); err == nil {
		t.Error("expected error when no file provided")
	}

	var out2 bytes.Buffer
	if err := verifyAttestationCore(
		[]string{"--pub", pubPath, "/tmp/a", "/tmp/b"},
		&out2,
	); err == nil {
		t.Error("expected error when two files provided")
	}
}
