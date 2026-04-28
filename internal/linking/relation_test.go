package linking

import "testing"

func TestInferRelation(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		// Agent-side ↔ commit → produces. Symmetric on argument order.
		{"tool_result", "commit", RelationProduces},
		{"commit", "tool_result", RelationProduces},
		{"tool_call", "commit", RelationProduces},
		{"prompt", "commit", RelationProduces},
		{"thought", "commit", RelationProduces},
		{"decision", "commit", RelationProduces},
		// Commit ↔ PR → produces (commit content ends up in the PR).
		{"commit", "pr", RelationProduces},
		{"pr", "commit", RelationProduces},
		// Commit ↔ build → builds.
		{"commit", "build", RelationBuilds},
		{"build", "commit", RelationBuilds},
		// Commit ↔ deploy → deploys.
		{"commit", "deploy", RelationDeploys},
		{"deploy", "commit", RelationDeploys},
		// Commit ↔ review → reviews.
		{"commit", "review", RelationReviews},
		{"review", "commit", RelationReviews},
		// Mixed case input — InferRelation lowercases internally.
		{"COMMIT", "Tool_Result", RelationProduces},
		// Pairs with no specific rule fall back to references.
		{"push", "commit", RelationReferences},
		{"pr", "review", RelationReferences},
		{"build", "deploy", RelationReferences},
		{"prompt", "thought", RelationReferences},
		{"", "", RelationReferences},
		{"unknown_kind", "commit", RelationReferences},
	}
	for _, c := range cases {
		got := InferRelation(c.a, c.b)
		if got != c.want {
			t.Errorf("InferRelation(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
