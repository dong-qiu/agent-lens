package github

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dongqiu/agent-lens/internal/ingest"
)

// pullRequestPayload is the subset of GitHub's `pull_request` webhook
// payload that we read to derive session_id / actor / refs. The wire
// event's payload field receives the FULL webhook body verbatim so
// consumers can navigate any field GitHub sent (labels, reviewers,
// draft status, mergeable, etc.) without us pre-curating.
type pullRequestPayload struct {
	Number      int `json:"number"`
	PullRequest struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// mapPullRequest derives a wire event from a `pull_request` webhook
// body. deliveryID (from the X-GitHub-Delivery header) is set as the
// event ID so duplicate deliveries hit ErrDuplicate at the store
// layer; if empty, the ingest pipeline assigns a ULID.
//
// session_id: `github-pr:<owner>/<repo>/<number>`. Slashes are
// query-string-safe, so the format survives `?session=...` in the
// Lens UI URL — the previous `#`-separated form was eaten by the
// browser as a fragment.
func mapPullRequest(raw json.RawMessage, deliveryID string) (*ingest.WireEvent, error) {
	var p pullRequestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode pull_request: %w", err)
	}
	if p.Repository.FullName == "" || p.Number == 0 {
		return nil, fmt.Errorf("pull_request missing repository.full_name or number")
	}

	actorID := p.Sender.Login
	if actorID == "" {
		actorID = p.PullRequest.User.Login
	}

	var refs []string
	if p.PullRequest.Head.SHA != "" {
		refs = []string{"git:" + p.PullRequest.Head.SHA}
	}

	return &ingest.WireEvent{
		ID:        deliveryID,
		TS:        time.Now().UTC(),
		SessionID: fmt.Sprintf("github-pr:%s/%d", p.Repository.FullName, p.Number),
		Actor: ingest.WireActor{
			Type: "human",
			ID:   actorID,
		},
		Kind:    "pr",
		Payload: raw,
		Refs:    refs,
	}, nil
}
