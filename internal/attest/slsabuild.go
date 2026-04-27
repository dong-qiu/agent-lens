package attest

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SLSA Provenance v1.0 (Build Track) identifiers.
// Spec: https://slsa.dev/spec/v1.0/provenance
const (
	SLSAProvenancePredicate = "https://slsa.dev/provenance/v1"
	// SLSABuildType identifies the producer of this provenance —
	// the agent-lens flavor of "GitHub Actions ran this build".
	SLSABuildType = "https://agent-lens.dev/build-types/github-actions/v1"
	// SLSABuilderID is the runner that executed the build. v0 assumes
	// GitHub-hosted; self-hosted runners would need a per-org override
	// later.
	SLSABuilderID = "https://github.com/actions/runner/github-hosted"
)

// SLSAProvenance is the v1.0 predicate body. Field names must match
// the SLSA JSON schema exactly so cosign / slsa-verifier accept it.
type SLSAProvenance struct {
	BuildDefinition SLSABuildDefinition `json:"buildDefinition"`
	RunDetails      SLSARunDetails      `json:"runDetails"`
}

type SLSABuildDefinition struct {
	BuildType            string           `json:"buildType"`
	ExternalParameters   map[string]any   `json:"externalParameters"`
	InternalParameters   map[string]any   `json:"internalParameters,omitempty"`
	ResolvedDependencies []SLSADependency `json:"resolvedDependencies,omitempty"`
}

type SLSADependency struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
	Name   string            `json:"name,omitempty"`
}

type SLSARunDetails struct {
	Builder    SLSABuilder       `json:"builder"`
	Metadata   SLSARunMetadata   `json:"metadata"`
	Byproducts []SLSAByproduct   `json:"byproducts,omitempty"`
}

type SLSABuilder struct {
	ID                  string           `json:"id"`
	Version             map[string]any   `json:"version,omitempty"`
	BuilderDependencies []SLSADependency `json:"builderDependencies,omitempty"`
}

type SLSARunMetadata struct {
	InvocationID string `json:"invocationId,omitempty"`
	StartedOn    string `json:"startedOn,omitempty"`
	FinishedOn   string `json:"finishedOn,omitempty"`
}

type SLSAByproduct struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
	Name   string            `json:"name,omitempty"`
}

// SLSABuildInputs bundles the values an SLSA Build Track v1 provenance
// needs. All fields except Subjects can be empty; the builder will
// emit a sparse but valid predicate. SLSA requires ≥1 subject.
type SLSABuildInputs struct {
	Subjects     []Subject
	WorkflowName string
	RunID        string
	RunNumber    string
	RunAttempt   string
	Ref          string
	CommitSHA    string
	Repo         string // optional, e.g. https://github.com/acme/widget
	StartedOn    string
	FinishedOn   string
	Conclusion   string // e.g. "success", "failure" — recorded as a byproduct
}

// BuildSLSAProvenanceStatement assembles a SLSA Build Track v1
// in-toto Statement. The subject set is the build's output artifacts;
// each must carry a sha256 digest produced by the M2-C-2 composite
// Action.
func BuildSLSAProvenanceStatement(in SLSABuildInputs) (*Statement, error) {
	if len(in.Subjects) == 0 {
		return nil, errors.New("at least one subject required (artifact with sha256)")
	}

	external := map[string]any{
		"workflow": in.WorkflowName,
		"ref":      in.Ref,
		"sha":      in.CommitSHA,
	}
	internal := map[string]any{
		"run_id":      in.RunID,
		"run_number":  in.RunNumber,
		"run_attempt": in.RunAttempt,
	}

	var deps []SLSADependency
	if in.CommitSHA != "" {
		dep := SLSADependency{
			Digest: map[string]string{"gitCommit": in.CommitSHA},
		}
		if in.Repo != "" {
			dep.URI = "git+" + in.Repo + "@" + in.CommitSHA
		}
		deps = append(deps, dep)
	}

	var byproducts []SLSAByproduct
	if in.Conclusion != "" {
		byproducts = append(byproducts, SLSAByproduct{
			Name: "conclusion",
			URI:  "agent-lens:workflow-conclusion",
			Digest: map[string]string{
				// Encode the conclusion string itself so a verifier
				// notices if it's been swapped post-signing.
				"text": in.Conclusion,
			},
		})
	}

	pred := SLSAProvenance{
		BuildDefinition: SLSABuildDefinition{
			BuildType:            SLSABuildType,
			ExternalParameters:   external,
			InternalParameters:   internal,
			ResolvedDependencies: deps,
		},
		RunDetails: SLSARunDetails{
			Builder: SLSABuilder{ID: SLSABuilderID},
			Metadata: SLSARunMetadata{
				InvocationID: in.RunID,
				StartedOn:    in.StartedOn,
				FinishedOn:   in.FinishedOn,
			},
			Byproducts: byproducts,
		},
	}

	predBytes, err := json.Marshal(&pred)
	if err != nil {
		return nil, fmt.Errorf("marshal predicate: %w", err)
	}

	return &Statement{
		Type:          InTotoStatementType,
		Subject:       in.Subjects,
		PredicateType: SLSAProvenancePredicate,
		Predicate:     predBytes,
	}, nil
}
