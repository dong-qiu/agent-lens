package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Identifiers used across all in-toto attestations we produce.
const (
	// InTotoStatementType is the v1 Statement type URI.
	InTotoStatementType = "https://in-toto.io/Statement/v1"
	// InTotoPayloadType is the DSSE payloadType for in-toto statements.
	InTotoPayloadType = "application/vnd.in-toto+json"
	// CodeProvenancePredicate is the predicateType for our v1
	// code-provenance attestation.
	CodeProvenancePredicate = "agent-lens.dev/code-provenance/v1"
	// CodeProvenanceBuildType identifies the producer of the predicate.
	CodeProvenanceBuildType = "https://agent-lens.dev/code-provenance/claude-code/v1"

	// previewLen caps the per-event preview included in the predicate.
	// Long enough to be useful for "what did the user prompt"; short
	// enough that 100 events fit in a few KB of attestation. Full
	// content stays in the agent-lens store; consumers cross-reference
	// by ContentDigest + ID.
	previewLen = 200
)

// Statement is the in-toto Statement envelope. The Predicate field is
// json.RawMessage so we don't have to teach the encoder about every
// predicate type.
type Statement struct {
	Type          string          `json:"_type"`
	Subject       []Subject       `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

// Subject identifies what the attestation describes. For
// code-provenance, it's the git commit; for SLSA build, the artifact
// files; for deploy-evidence, the image digest.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// CodeProvenance is the v1 predicate for agent-lens.dev/code-provenance.
type CodeProvenance struct {
	BuildType        string             `json:"buildType"`
	Session          ProvenanceSession  `json:"session"`
	Events           []ProvenanceEvent  `json:"events"`
	Actor            map[string]string  `json:"actor,omitempty"`
	Metadata         ProvenanceMetadata `json:"metadata"`
	StoreURL         string             `json:"store_url,omitempty"`
	TraceRootEventID string             `json:"trace_root_event_id,omitempty"`
}

type ProvenanceSession struct {
	ID    string `json:"id"`
	Agent string `json:"agent,omitempty"`
	Model string `json:"model,omitempty"`
}

// ProvenanceEvent is one row of the AI-side activity that produced
// the commit. We store digests + short previews — never the full
// prompt / thinking text — so a signed attestation never carries
// payload that may contain secrets.
type ProvenanceEvent struct {
	ID             string `json:"id"`
	TS             string `json:"ts"`
	Kind           string `json:"kind"`
	ContentDigest  string `json:"content_digest,omitempty"`
	ContentPreview string `json:"content_preview,omitempty"`
	ToolName       string `json:"tool_name,omitempty"`
	Marker         string `json:"marker,omitempty"`
}

type ProvenanceMetadata struct {
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// BuildCodeProvenanceStatement assembles an in-toto Statement whose
// subject is the git commit (digest type "gitCommit", standard in-toto
// digest name for git commits) and whose predicate is the v1
// code-provenance shape.
//
// The events slice should already be filtered to AI-side events
// (PROMPT / THOUGHT / TOOL_CALL / TOOL_RESULT / DECISION) and sorted
// by ts. Started/ended metadata is taken from the first/last event.
func BuildCodeProvenanceStatement(
	commit string,
	session ProvenanceSession,
	events []ProvenanceEvent,
	storeURL, rootEventID string,
) (*Statement, error) {
	if commit == "" {
		return nil, errors.New("commit sha required")
	}
	if len(events) == 0 {
		return nil, errors.New("no events; nothing to attest")
	}

	pred := CodeProvenance{
		BuildType: CodeProvenanceBuildType,
		Session:   session,
		Events:    events,
		Metadata: ProvenanceMetadata{
			StartedAt: events[0].TS,
			EndedAt:   events[len(events)-1].TS,
		},
		StoreURL:         storeURL,
		TraceRootEventID: rootEventID,
	}
	predBytes, err := json.Marshal(&pred)
	if err != nil {
		return nil, fmt.Errorf("marshal predicate: %w", err)
	}

	return &Statement{
		Type: InTotoStatementType,
		Subject: []Subject{
			{
				Name:   "git+local",
				Digest: map[string]string{"gitCommit": commit},
			},
		},
		PredicateType: CodeProvenancePredicate,
		Predicate:     predBytes,
	}, nil
}

// SummarizeText returns a sha256 digest (in "sha256:<hex>" form) and
// a clipped preview. Preview clip uses bytes (not runes) so a
// multi-byte character at the boundary gets cleanly cut at the
// previous byte; that's a known minor trade-off for simplicity.
// Empty input still returns the digest of empty bytes.
func SummarizeText(s string) (digest, preview string) {
	sum := sha256.Sum256([]byte(s))
	digest = "sha256:" + hex.EncodeToString(sum[:])
	if len(s) > previewLen {
		preview = s[:previewLen] + "..."
	} else {
		preview = s
	}
	return digest, preview
}
