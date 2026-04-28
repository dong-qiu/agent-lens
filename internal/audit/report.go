// Package audit produces and verifies M3-D audit report bundles —
// self-contained JSON files that capture an evidence chain rooted at
// a single event id, plus optional in-toto / SLSA attestations, plus
// a sha256 manifest making the bundle tamper-evident.
//
// A bundle is built by the agent-lens-hook CLI by walking the link
// graph via GraphQL (single-hop links → linked sessions → events),
// and is verified offline by re-hashing the canonicalized sections
// and re-walking each session's prev_hash → hash chain.
package audit

// Version identifies the report schema. Bumped only on breaking
// shape changes; verifiers are expected to refuse unknown versions.
const Version = "agent-lens.dev/audit-report/v1"

// Report is the on-disk JSON shape. Marshaled with json.MarshalIndent
// for human readability; verifiers re-hash the canonical (compact)
// form of `Sessions` and `Attestations` to compare against
// `Manifest.{SessionsSha256,AttestationsSha256}`.
//
// JSON field tags use snake_case to match the rest of the agent-lens
// wire format (events, predicates).
type Report struct {
	Version      string        `json:"version"`
	GeneratedAt  string        `json:"generated_at"`
	Generator    string        `json:"generator"`
	StoreURL     string        `json:"store_url,omitempty"`
	RootEventID  string        `json:"root_event_id"`
	Sessions     []Session     `json:"sessions"`
	Attestations []Attestation `json:"attestations,omitempty"`
	Manifest     Manifest      `json:"manifest"`
}

// Session bundles every event in one session_id, plus the head hash
// the server reported at fetch time. The head lets a verifier notice
// "we have N events and the head matches event[N-1]; nothing was
// truncated mid-fetch".
type Session struct {
	SessionID string  `json:"session_id"`
	HeadHash  string  `json:"head_hash"`
	Events    []Event `json:"events"`
}

// Event is the per-event record. Mirrors the GraphQL `Event` type
// (so JSON tags are camelCase to decode straight from the server)
// but only carries the fields a verifier needs: id + ts + kind +
// actor for audit context, hash + prevHash for chain integrity,
// payload for re-derivation if (when) we add server-side hash
// recompute.
type Event struct {
	ID        string         `json:"id"`
	TS        string         `json:"ts"`
	SessionID string         `json:"sessionId"`
	Kind      string         `json:"kind"`
	Actor     Actor          `json:"actor"`
	Payload   map[string]any `json:"payload,omitempty"`
	Hash      string         `json:"hash"`
	PrevHash  string         `json:"prevHash,omitempty"`
	Refs      []string       `json:"refs,omitempty"`
	Links     []Link         `json:"links,omitempty"`
}

// Actor mirrors the GraphQL Actor type.
type Actor struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

// Link mirrors the GraphQL Link type. Carried so a verifier can
// re-render the trace graph offline without re-querying.
type Link struct {
	FromEvent  string  `json:"fromEvent"`
	ToEvent    string  `json:"toEvent"`
	Relation   string  `json:"relation"`
	Confidence float64 `json:"confidence"`
	InferredBy string  `json:"inferredBy"`
}

// Attestation embeds one .intoto.jsonl envelope verbatim. The bytes
// are *not* re-encoded — Sha256 is computed over the original file
// bytes so a verifier hashes the same input cosign / sigstore would.
type Attestation struct {
	Filename    string `json:"filename"`
	Sha256      string `json:"sha256"` // sha256:<hex>
	EnvelopeB64 string `json:"envelope_b64"`
}

// Manifest carries the canonical sha256 of each bulky section so the
// report itself is tamper-evident: a verifier re-marshals Sessions
// and Attestations, hashes, and compares.
type Manifest struct {
	SessionsSha256     string `json:"sessions_sha256"`
	AttestationsSha256 string `json:"attestations_sha256"`
}
