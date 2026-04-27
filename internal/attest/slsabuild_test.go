package attest

import (
	"encoding/json"
	"testing"
)

func TestBuildSLSAProvenanceStatementHappyPath(t *testing.T) {
	in := SLSABuildInputs{
		Subjects: []Subject{
			{Name: "dist/widget.tar.gz", Digest: map[string]string{"sha256": "abc"}},
			{Name: "dist/widget.bin", Digest: map[string]string{"sha256": "def"}},
		},
		WorkflowName: "CI",
		RunID:        "123456789",
		RunNumber:    "42",
		RunAttempt:   "1",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeefcafe",
		Repo:         "https://github.com/acme/widget",
		StartedOn:    "2026-04-27T10:00:00Z",
		FinishedOn:   "2026-04-27T10:05:00Z",
		Conclusion:   "success",
	}
	stmt, err := BuildSLSAProvenanceStatement(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if stmt.Type != InTotoStatementType {
		t.Errorf("_type = %q", stmt.Type)
	}
	if stmt.PredicateType != SLSAProvenancePredicate {
		t.Errorf("predicateType = %q, want %q", stmt.PredicateType, SLSAProvenancePredicate)
	}
	if len(stmt.Subject) != 2 {
		t.Fatalf("subjects = %d, want 2", len(stmt.Subject))
	}
	if stmt.Subject[0].Digest["sha256"] != "abc" {
		t.Errorf("first subject digest = %+v", stmt.Subject[0].Digest)
	}

	var pred SLSAProvenance
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		t.Fatalf("predicate decode: %v", err)
	}

	if pred.BuildDefinition.BuildType != SLSABuildType {
		t.Errorf("buildType = %q", pred.BuildDefinition.BuildType)
	}
	if pred.BuildDefinition.ExternalParameters["workflow"] != "CI" {
		t.Errorf("externalParameters.workflow = %v", pred.BuildDefinition.ExternalParameters["workflow"])
	}
	if pred.BuildDefinition.InternalParameters["run_id"] != "123456789" {
		t.Errorf("internalParameters.run_id = %v", pred.BuildDefinition.InternalParameters["run_id"])
	}
	if len(pred.BuildDefinition.ResolvedDependencies) != 1 {
		t.Fatalf("resolvedDependencies = %d, want 1 (source commit)", len(pred.BuildDefinition.ResolvedDependencies))
	}
	dep := pred.BuildDefinition.ResolvedDependencies[0]
	if dep.URI != "git+https://github.com/acme/widget@deadbeefcafe" {
		t.Errorf("dep.URI = %q", dep.URI)
	}
	if dep.Digest["gitCommit"] != "deadbeefcafe" {
		t.Errorf("dep.digest = %+v", dep.Digest)
	}

	if pred.RunDetails.Builder.ID != SLSABuilderID {
		t.Errorf("builder.id = %q", pred.RunDetails.Builder.ID)
	}
	if pred.RunDetails.Metadata.InvocationID != "123456789" {
		t.Errorf("metadata.invocationId = %q", pred.RunDetails.Metadata.InvocationID)
	}
	if pred.RunDetails.Metadata.StartedOn != "2026-04-27T10:00:00Z" {
		t.Errorf("metadata.startedOn = %q", pred.RunDetails.Metadata.StartedOn)
	}

	if len(pred.RunDetails.Byproducts) != 1 || pred.RunDetails.Byproducts[0].Digest["text"] != "success" {
		t.Errorf("byproducts = %+v", pred.RunDetails.Byproducts)
	}
}

func TestBuildSLSAProvenanceStatementRejectsEmptySubjects(t *testing.T) {
	in := SLSABuildInputs{
		Subjects:     nil,
		WorkflowName: "CI",
	}
	if _, err := BuildSLSAProvenanceStatement(in); err == nil {
		t.Error("accepted empty subjects (SLSA spec requires ≥1)")
	}
}

func TestBuildSLSAProvenanceStatementOmitsResolvedDepsWithoutCommit(t *testing.T) {
	in := SLSABuildInputs{
		Subjects: []Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
	}
	stmt, err := BuildSLSAProvenanceStatement(in)
	if err != nil {
		t.Fatal(err)
	}
	var pred SLSAProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if len(pred.BuildDefinition.ResolvedDependencies) != 0 {
		t.Errorf("resolvedDependencies = %+v, want empty without commit",
			pred.BuildDefinition.ResolvedDependencies)
	}
}

func TestBuildSLSAProvenanceStatementCommitWithoutRepoStillEmitsDigest(t *testing.T) {
	in := SLSABuildInputs{
		Subjects:  []Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
		CommitSHA: "deadbeef",
	}
	stmt, _ := BuildSLSAProvenanceStatement(in)
	var pred SLSAProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if len(pred.BuildDefinition.ResolvedDependencies) != 1 {
		t.Fatalf("deps = %d, want 1", len(pred.BuildDefinition.ResolvedDependencies))
	}
	dep := pred.BuildDefinition.ResolvedDependencies[0]
	if dep.URI != "" {
		t.Errorf("URI should be empty without --repo, got %q", dep.URI)
	}
	if dep.Digest["gitCommit"] != "deadbeef" {
		t.Errorf("digest = %+v", dep.Digest)
	}
}

func TestSLSAProvenanceJSONStable(t *testing.T) {
	stmt, _ := BuildSLSAProvenanceStatement(SLSABuildInputs{
		Subjects: []Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
		RunID:    "1",
	})
	raw, err := json.Marshal(stmt)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Statement
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if parsed.PredicateType != SLSAProvenancePredicate {
		t.Errorf("round-trip lost predicateType")
	}
}

func TestBuildSLSAProvenanceStatementBuilderIDOverride(t *testing.T) {
	// Default → SLSABuilderID.
	stmt, _ := BuildSLSAProvenanceStatement(SLSABuildInputs{
		Subjects: []Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
	})
	var pred SLSAProvenance
	_ = json.Unmarshal(stmt.Predicate, &pred)
	if pred.RunDetails.Builder.ID != SLSABuilderID {
		t.Errorf("default builder.id = %q, want %q", pred.RunDetails.Builder.ID, SLSABuilderID)
	}

	// Override.
	stmt2, _ := BuildSLSAProvenanceStatement(SLSABuildInputs{
		Subjects:  []Subject{{Name: "x", Digest: map[string]string{"sha256": "abc"}}},
		BuilderID: "https://acme.example.com/runner/self-hosted",
	})
	var pred2 SLSAProvenance
	_ = json.Unmarshal(stmt2.Predicate, &pred2)
	if pred2.RunDetails.Builder.ID != "https://acme.example.com/runner/self-hosted" {
		t.Errorf("override builder.id = %q", pred2.RunDetails.Builder.ID)
	}
}
