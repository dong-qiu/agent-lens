package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDeployEvidenceStatementHappyPath(t *testing.T) {
	in := DeployEvidenceInputs{
		Image:                  "ghcr.io/acme/widget",
		ImageDigest:            "sha256:abcdef0123456789",
		Environment:            "production",
		Platform:               "k8s",
		Cluster:                "prod-us-east",
		Namespace:              "default",
		DeployedBy:             "alice",
		DeployedAt:             "2026-04-28T12:00:00Z",
		Status:                 "succeeded",
		GitCommit:              "deadbeefcafe",
		BuildAttestationDigest: "sha256:00000000build",
		CodeAttestationDigest:  "sha256:00000000code",
		StoreURL:               "https://lens.example.com",
		TraceRootEventID:       "01HDEPLOY",
	}
	stmt, err := BuildDeployEvidenceStatement(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if stmt.PredicateType != DeployEvidencePredicate {
		t.Errorf("predicateType = %q", stmt.PredicateType)
	}
	if len(stmt.Subject) != 1 {
		t.Fatalf("subject count = %d, want 1", len(stmt.Subject))
	}
	if stmt.Subject[0].Name != "ghcr.io/acme/widget" {
		t.Errorf("subject.name = %q", stmt.Subject[0].Name)
	}
	if stmt.Subject[0].Digest["sha256"] != "abcdef0123456789" {
		t.Errorf("subject.digest = %+v (sha256: prefix should be stripped)", stmt.Subject[0].Digest)
	}

	var pred DeployEvidence
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		t.Fatalf("predicate decode: %v", err)
	}
	if pred.Environment != "production" {
		t.Errorf("environment = %q", pred.Environment)
	}
	if pred.Upstream.BuildAttestationDigest != "sha256:00000000build" {
		t.Errorf("upstream.build = %q", pred.Upstream.BuildAttestationDigest)
	}
	if pred.Upstream.CodeAttestationDigest != "sha256:00000000code" {
		t.Errorf("upstream.code = %q", pred.Upstream.CodeAttestationDigest)
	}
	if pred.TraceRootEventID != "01HDEPLOY" {
		t.Errorf("trace_root_event_id = %q", pred.TraceRootEventID)
	}
}

func TestBuildDeployEvidenceStatementRejectsEmptyEnvOrDigest(t *testing.T) {
	if _, err := BuildDeployEvidenceStatement(DeployEvidenceInputs{
		ImageDigest: "sha256:abc",
	}); err == nil {
		t.Error("accepted empty environment")
	}
	if _, err := BuildDeployEvidenceStatement(DeployEvidenceInputs{
		Environment: "production",
	}); err == nil {
		t.Error("accepted empty image_digest")
	}
}

func TestBuildDeployEvidenceStatementAcceptsBareHexDigest(t *testing.T) {
	stmt, err := BuildDeployEvidenceStatement(DeployEvidenceInputs{
		Environment: "prod",
		ImageDigest: "abcdef", // no sha256: prefix
	})
	if err != nil {
		t.Fatal(err)
	}
	if stmt.Subject[0].Digest["sha256"] != "abcdef" {
		t.Errorf("digest = %+v, want bare hex preserved", stmt.Subject[0].Digest)
	}
}

func TestBuildDeployEvidenceStatementMissingImageNameFallback(t *testing.T) {
	stmt, _ := BuildDeployEvidenceStatement(DeployEvidenceInputs{
		Environment: "prod",
		ImageDigest: "sha256:abc",
		// Image not set
	})
	if stmt.Subject[0].Name != "image" {
		t.Errorf("subject.name = %q, want fallback %q", stmt.Subject[0].Name, "image")
	}
}

func TestDigestFileMatchesKnownContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	const content = "deploy-attestation contents"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	want := "sha256:" + hex.EncodeToString(sha256Sum([]byte(content)))
	got, err := DigestFile(path)
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}
	if got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Errorf("digest missing prefix: %q", got)
	}
}

func TestDigestFileMissing(t *testing.T) {
	if _, err := DigestFile("/no/such/file/here"); err == nil {
		t.Error("DigestFile accepted missing path")
	}
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
