package github

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dongqiu/agent-lens/internal/ingest"
)

// pullRequestPayload is the subset of the GitHub `pull_request` webhook
// payload we read. Unrecognized fields are ignored; the original payload
// is preserved verbatim in the wire event's payload field so consumers
// can introspect the full GitHub state.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// mapPullRequest turns a parsed pull_request webhook payload into a
// wire event. session_id stays stable across the PR lifetime
// (`github-pr:<owner>/<repo>#<number>`) so all actions on a PR group
// in the timeline. The head commit SHA is also recorded as a ref so
// the M2-B linking worker can correlate with COMMIT events.
func mapPullRequest(raw json.RawMessage) (*ingest.WireEvent, error) {
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

	payload, err := json.Marshal(map[string]any{
		"action":      p.Action,
		"number":      p.Number,
		"title":       p.PullRequest.Title,
		"url":         p.PullRequest.HTMLURL,
		"state":       p.PullRequest.State,
		"head_sha":    p.PullRequest.Head.SHA,
		"head_branch": p.PullRequest.Head.Ref,
		"base_branch": p.PullRequest.Base.Ref,
		"repo":        p.Repository.FullName,
		"author":      p.PullRequest.User.Login,
	})
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}

	var refs []string
	if p.PullRequest.Head.SHA != "" {
		refs = []string{"git:" + p.PullRequest.Head.SHA}
	}

	return &ingest.WireEvent{
		TS:        time.Now().UTC(),
		SessionID: fmt.Sprintf("github-pr:%s#%d", p.Repository.FullName, p.Number),
		Actor: ingest.WireActor{
			Type: "human",
			ID:   actorID,
		},
		Kind:    "pr",
		Payload: payload,
		Refs:    refs,
	}, nil
}
