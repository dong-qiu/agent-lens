package deploy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dongqiu/agent-lens/internal/ingest"
)

// payload is the shape /webhooks/deploy accepts. Most fields are
// optional; the deploy can come from K8s, Argo CD, a Helm post-render
// hook, or a hand-rolled curl in someone's Makefile, so the schema is
// deliberately loose. The full body is preserved verbatim in the wire
// event's payload field for downstream introspection.
type payload struct {
	Environment string    `json:"environment"`
	GitSHA      string    `json:"git_sha"`
	ImageDigest string    `json:"image_digest"`
	Image       string    `json:"image"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DeployedBy  string    `json:"deployed_by"`
	Platform    string    `json:"platform"`
	Cluster     string    `json:"cluster"`
	Namespace   string    `json:"namespace"`
}

// maxEnvironmentLen caps the environment string so an absurdly long
// payload can't blow up the session_id (used in URLs, logs, the UI
// dropdown). Realistic env names are <32 chars; 64 is generous.
const maxEnvironmentLen = 64

// maxIdempotencyKeyLen caps the Idempotency-Key header so an attacker
// can't smuggle a multi-MB key into the events.id column. ULIDs are
// 26 chars, UUIDs are 36; 128 is comfortable headroom.
const maxIdempotencyKeyLen = 128

// mapDeploy parses the body, derives session_id / actor / refs, and
// returns a wire event ready for ingest. eventID is the optional
// Idempotency-Key header value; empty falls back to a server-assigned
// ULID and the call won't be deduplicated.
func mapDeploy(raw json.RawMessage, eventID string) (*ingest.WireEvent, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode deploy: %w", err)
	}
	if p.Environment == "" {
		return nil, fmt.Errorf("deploy missing required field: environment")
	}
	if len(p.Environment) > maxEnvironmentLen {
		return nil, fmt.Errorf("deploy environment too long (got %d, max %d)", len(p.Environment), maxEnvironmentLen)
	}
	if len(eventID) > maxIdempotencyKeyLen {
		return nil, fmt.Errorf("Idempotency-Key too long (got %d, max %d)", len(eventID), maxIdempotencyKeyLen)
	}
	if p.GitSHA == "" && p.ImageDigest == "" {
		return nil, fmt.Errorf("deploy must include at least one of git_sha or image_digest")
	}

	actorID := p.DeployedBy
	if actorID == "" {
		actorID = "deploy-system"
	}

	var refs []string
	if p.GitSHA != "" {
		refs = append(refs, "git:"+p.GitSHA)
	}
	if p.ImageDigest != "" {
		refs = append(refs, "image:"+p.ImageDigest)
	}

	return &ingest.WireEvent{
		ID:        eventID,
		TS:        time.Now().UTC(),
		SessionID: fmt.Sprintf("deploy:%s", p.Environment),
		Actor: ingest.WireActor{
			Type: "system",
			ID:   actorID,
		},
		Kind:    "deploy",
		Payload: raw,
		Refs:    refs,
	}, nil
}
