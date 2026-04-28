package linking

import "strings"

// SPEC §7 vocabulary. The linker emits exactly one of these per link.
const (
	RelationProduces   = "produces"
	RelationBuilds     = "builds"
	RelationDeploys    = "deploys"
	RelationReviews    = "reviews"
	RelationReferences = "references" // neutral fallback
)

// InferRelation maps a (kindA, kindB) pair to a SPEC §7 relation. The
// match is symmetric on the args — order doesn't matter — so the linker
// can call it with either (peer.Kind, new.Kind) ordering and get the
// same answer.
//
// Direction in the (FromEvent, ToEvent) tuple is currently arrival
// order, which usually aligns with semantic direction (the agent action
// arrives before its commit, the commit arrives before its build) but
// isn't guaranteed under hook timing skew. The relation label
// identifies the relationship *type* regardless; flipping From/To is a
// follow-up if audit reports demand strict directional semantics.
//
// Fallback is `references` — a link whose pair we don't yet recognise
// is still informative as "these two events share an artifact".
func InferRelation(a, b string) string {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	pair := func(x, y string) bool {
		return (a == x && b == y) || (a == y && b == x)
	}

	// Any agent-side event paired with a commit event = the agent
	// action `produces` the commit. Phase A's git-commit linking
	// surfaces this exact case via shared `git:<sha>` ref.
	for _, agent := range []string{"prompt", "thought", "tool_call", "tool_result", "decision"} {
		if pair(agent, "commit") {
			return RelationProduces
		}
	}

	switch {
	case pair("commit", "pr"):
		return RelationProduces
	case pair("commit", "build"):
		return RelationBuilds
	case pair("commit", "deploy"):
		return RelationDeploys
	case pair("commit", "review"):
		return RelationReviews
	}
	return RelationReferences
}
