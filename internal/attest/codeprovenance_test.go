package attest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCodeProvenanceStatementHappyPath(t *testing.T) {
	events := []ProvenanceEvent{
		{ID: "e1", TS: "2026-04-27T10:00:00Z", Kind: "PROMPT", ContentDigest: "sha256:abc", ContentPreview: "do X"},
		{ID: "e2", TS: "2026-04-27T10:00:05Z", Kind: "THOUGHT", ContentDigest: "sha256:def", ContentPreview: "plan"},
		{ID: "e3", TS: "2026-04-27T10:00:10Z", Kind: "TOOL_CALL", ToolName: "Edit"},
	}
	stmt, err := BuildCodeProvenanceStatement(
		"deadbeefcafe",
		ProvenanceSession{ID: "s1", Agent: "claude-code", Model: "claude-opus-4-7"},
		events,
		"https://lens.example.com",
		"e1",
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if stmt.Type != InTotoStatementType {
		t.Errorf("_type = %q", stmt.Type)
	}
	if stmt.PredicateType != CodeProvenancePredicate {
		t.Errorf("predicateType = %q", stmt.PredicateType)
	}
	if len(stmt.Subject) != 1 {
		t.Fatalf("subject count = %d, want 1", len(stmt.Subject))
	}
	if stmt.Subject[0].Digest["gitCommit"] != "deadbeefcafe" {
		t.Errorf("subject digest = %+v", stmt.Subject[0].Digest)
	}

	var pred CodeProvenance
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		t.Fatalf("predicate decode: %v", err)
	}
	if pred.BuildType != CodeProvenanceBuildType {
		t.Errorf("buildType = %q", pred.BuildType)
	}
	if pred.Session.Model != "claude-opus-4-7" {
		t.Errorf("session.model = %q", pred.Session.Model)
	}
	if len(pred.Events) != 3 {
		t.Errorf("predicate.events len = %d, want 3", len(pred.Events))
	}
	if pred.Metadata.StartedAt != events[0].TS {
		t.Errorf("metadata.started_at = %q, want %q", pred.Metadata.StartedAt, events[0].TS)
	}
	if pred.Metadata.EndedAt != events[2].TS {
		t.Errorf("metadata.ended_at = %q, want %q", pred.Metadata.EndedAt, events[2].TS)
	}
	if pred.TraceRootEventID != "e1" {
		t.Errorf("trace_root_event_id = %q", pred.TraceRootEventID)
	}
}

func TestBuildCodeProvenanceStatementRejectsEmptyInput(t *testing.T) {
	if _, err := BuildCodeProvenanceStatement("", ProvenanceSession{}, []ProvenanceEvent{{ID: "x"}}, "", ""); err == nil {
		t.Error("accepted empty commit")
	}
	if _, err := BuildCodeProvenanceStatement("abc", ProvenanceSession{}, nil, "", ""); err == nil {
		t.Error("accepted empty events")
	}
}

func TestSummarizeTextDigestStable(t *testing.T) {
	a, _ := SummarizeText("hello")
	b, _ := SummarizeText("hello")
	if a != b {
		t.Errorf("non-deterministic digest: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("digest missing prefix: %s", a)
	}
	if len(a) != len("sha256:")+64 {
		t.Errorf("digest len = %d (want sha256: + 64 hex)", len(a))
	}
}

func TestSummarizeTextPreviewClips(t *testing.T) {
	short := strings.Repeat("a", previewLen)
	_, preview := SummarizeText(short)
	if preview != short {
		t.Errorf("at-limit string was clipped: %q", preview)
	}

	tooLong := strings.Repeat("a", previewLen+50)
	_, preview = SummarizeText(tooLong)
	if !strings.HasSuffix(preview, "...") {
		t.Errorf("over-limit string missing ellipsis: %q", preview)
	}
	// previewLen + len("...") = preview length
	if len(preview) != previewLen+3 {
		t.Errorf("clipped len = %d, want %d", len(preview), previewLen+3)
	}
}

func TestSummarizeTextEmpty(t *testing.T) {
	digest, preview := SummarizeText("")
	if digest == "" {
		t.Error("empty input still needs a digest (sha256 of empty bytes)")
	}
	if preview != "" {
		t.Errorf("empty input preview = %q, want empty", preview)
	}
}

func TestStatementJSONRoundTrip(t *testing.T) {
	stmt, err := BuildCodeProvenanceStatement(
		"abc",
		ProvenanceSession{ID: "s1", Agent: "claude-code"},
		[]ProvenanceEvent{{ID: "e1", TS: "t", Kind: "PROMPT"}},
		"", "e1",
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed Statement
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Type != stmt.Type || parsed.PredicateType != stmt.PredicateType {
		t.Errorf("round-trip lost fields: %+v", parsed)
	}

	// Predicate is RawMessage; canonicalize via re-marshal for compare.
	var ourPred, theirPred CodeProvenance
	_ = json.Unmarshal(stmt.Predicate, &ourPred)
	_ = json.Unmarshal(parsed.Predicate, &theirPred)
	ours, _ := json.Marshal(ourPred)
	theirs, _ := json.Marshal(theirPred)
	if string(ours) != string(theirs) {
		t.Errorf("predicate round-trip mismatch:\n  ours   = %s\n  theirs = %s", ours, theirs)
	}
}
