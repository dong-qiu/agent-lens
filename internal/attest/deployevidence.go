package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// DeployEvidencePredicate identifies the v1 deploy attestation produced
// by Agent Lens. Subject of a deploy-evidence is the container image.
const (
	DeployEvidencePredicate = "agent-lens.dev/deploy-evidence/v1"
	DeployEvidenceBuildType = "https://agent-lens.dev/deploy-evidence/v1"
)

// DeployEvidence is the v1 predicate body. It records the deploy
// itself plus hashes of upstream attestations (build / code) so a
// verifier can walk the attestation graph from deploy → build → code.
type DeployEvidence struct {
	BuildType        string         `json:"buildType"`
	Environment      string         `json:"environment"`
	Platform         string         `json:"platform,omitempty"`
	Cluster          string         `json:"cluster,omitempty"`
	Namespace        string         `json:"namespace,omitempty"`
	DeployedBy       string         `json:"deployed_by,omitempty"`
	DeployedAt       string         `json:"deployed_at,omitempty"`
	Status           string         `json:"status,omitempty"`
	Upstream         DeployUpstream `json:"upstream"`
	StoreURL         string         `json:"store_url,omitempty"`
	TraceRootEventID string         `json:"trace_root_event_id,omitempty"`
}

// DeployUpstream holds digest pointers back to the build and code
// attestations that authorized this deploy. \`sha256:<hex>\` form. Empty
// values mean the deployer didn't attach the corresponding upstream;
// verifiers should treat that as reduced assurance, not silent ok.
type DeployUpstream struct {
	BuildAttestationDigest string `json:"build_attestation,omitempty"`
	CodeAttestationDigest  string `json:"code_attestation,omitempty"`
	GitCommit              string `json:"git_commit,omitempty"`
}

// DeployEvidenceInputs bundles values an exporter has assembled. Image
// + ImageDigest determine the subject; everything else feeds the
// predicate body.
type DeployEvidenceInputs struct {
	Image       string // e.g. ghcr.io/acme/widget
	ImageDigest string // sha256:<hex> or bare hex
	Environment string
	Platform    string
	Cluster     string
	Namespace   string
	DeployedBy  string
	DeployedAt  string
	Status      string
	GitCommit   string

	BuildAttestationDigest string // sha256:<hex>
	CodeAttestationDigest  string

	StoreURL         string
	TraceRootEventID string
}

// BuildDeployEvidenceStatement builds an in-toto Statement for a
// deploy. Subject is the container image (sha256 of its digest).
func BuildDeployEvidenceStatement(in DeployEvidenceInputs) (*Statement, error) {
	if in.Environment == "" {
		return nil, errors.New("environment required")
	}
	digestHex := strings.TrimPrefix(in.ImageDigest, "sha256:")
	if digestHex == "" {
		return nil, errors.New("image_digest required (becomes the subject's sha256)")
	}

	name := in.Image
	if name == "" {
		// SLSA / in-toto subjects need a name even when only a digest
		// is meaningful; "image" is a non-confusing placeholder.
		name = "image"
	}

	pred := DeployEvidence{
		BuildType:   DeployEvidenceBuildType,
		Environment: in.Environment,
		Platform:    in.Platform,
		Cluster:     in.Cluster,
		Namespace:   in.Namespace,
		DeployedBy:  in.DeployedBy,
		DeployedAt:  in.DeployedAt,
		Status:      in.Status,
		Upstream: DeployUpstream{
			BuildAttestationDigest: in.BuildAttestationDigest,
			CodeAttestationDigest:  in.CodeAttestationDigest,
			GitCommit:              in.GitCommit,
		},
		StoreURL:         in.StoreURL,
		TraceRootEventID: in.TraceRootEventID,
	}
	predBytes, err := json.Marshal(&pred)
	if err != nil {
		return nil, fmt.Errorf("marshal predicate: %w", err)
	}

	return &Statement{
		Type: InTotoStatementType,
		Subject: []Subject{
			{Name: name, Digest: map[string]string{"sha256": digestHex}},
		},
		PredicateType: DeployEvidencePredicate,
		Predicate:     predBytes,
	}, nil
}

// DigestFile returns sha256:<hex> of the file at path. Used to record
// upstream attestation hashes in DeployUpstream — a verifier hashes
// the same .intoto.jsonl bytes and compares.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
