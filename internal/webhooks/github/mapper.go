package github

import (
	"encoding/json"
	"fmt"
	"strings"
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

// pullRequestReviewPayload reads the small subset needed to derive
// session_id / refs / actor. The PR review session is the same as the
// PR's so reviews show up under the PR timeline alongside pr events.
type pullRequestReviewPayload struct {
	Review struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
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

func mapPullRequestReview(raw json.RawMessage, deliveryID string) (*ingest.WireEvent, error) {
	var p pullRequestReviewPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode pull_request_review: %w", err)
	}
	if p.Repository.FullName == "" || p.PullRequest.Number == 0 {
		return nil, fmt.Errorf("pull_request_review missing repository.full_name or pull_request.number")
	}

	actorID := p.Sender.Login
	if actorID == "" {
		actorID = p.Review.User.Login
	}

	var refs []string
	if p.PullRequest.Head.SHA != "" {
		refs = []string{"git:" + p.PullRequest.Head.SHA}
	}

	return &ingest.WireEvent{
		ID:        deliveryID,
		TS:        time.Now().UTC(),
		SessionID: fmt.Sprintf("github-pr:%s/%d", p.Repository.FullName, p.PullRequest.Number),
		Actor: ingest.WireActor{
			Type: "human",
			ID:   actorID,
		},
		Kind:    "review",
		Payload: raw,
		Refs:    refs,
	}, nil
}

// pushPayload reads the branch ref + per-commit shas. session_id groups
// pushes per branch; refs include head + every commit in the push so the
// linker can connect this remote-side event to local commit events the
// git-post-commit hook produced.
type pushPayload struct {
	Ref     string `json:"ref"`     // refs/heads/<branch> or refs/tags/<tag>
	After   string `json:"after"`   // head sha after the push
	Deleted bool   `json:"deleted"` // true when branch was deleted
	Commits []struct {
		ID string `json:"id"`
	} `json:"commits"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"pusher"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func mapPush(raw json.RawMessage, deliveryID string) (*ingest.WireEvent, error) {
	var p pushPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode push: %w", err)
	}
	if p.Repository.FullName == "" || p.Ref == "" {
		return nil, fmt.Errorf("push missing repository.full_name or ref")
	}

	// Strip exactly one of the known ref-type prefixes so a contrived
	// branch name like "refs/tags/v1" pushed under refs/heads/ doesn't
	// get its prefix stripped twice.
	var branch string
	switch {
	case strings.HasPrefix(p.Ref, "refs/heads/"):
		branch = strings.TrimPrefix(p.Ref, "refs/heads/")
	case strings.HasPrefix(p.Ref, "refs/tags/"):
		branch = strings.TrimPrefix(p.Ref, "refs/tags/")
	default:
		branch = p.Ref
	}

	actorID := p.Sender.Login
	if actorID == "" {
		actorID = p.Pusher.Name
	}

	// Refs: head sha + every commit sha in the push, deduplicated.
	// Branch deletion (after = "0000000...") is skipped from refs so
	// linker doesn't try to look it up.
	seen := map[string]struct{}{}
	var refs []string
	addRef := func(sha string) {
		if sha == "" || isZeroSHA(sha) {
			return
		}
		key := "git:" + sha
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, key)
	}
	addRef(p.After)
	for _, c := range p.Commits {
		addRef(c.ID)
	}

	return &ingest.WireEvent{
		ID:        deliveryID,
		TS:        time.Now().UTC(),
		SessionID: fmt.Sprintf("github-push:%s/%s", p.Repository.FullName, branch),
		Actor: ingest.WireActor{
			Type: "human",
			ID:   actorID,
		},
		Kind:    "push",
		Payload: raw,
		Refs:    refs,
	}, nil
}

func isZeroSHA(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return s != ""
}

// workflowRunPayload reads the small subset needed to derive
// session_id / refs / actor for GitHub Actions runs. session_id keys
// on run_id so the three lifecycle deliveries (requested →
// in_progress → completed) group together in the timeline.
type workflowRunPayload struct {
	WorkflowRun struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		HeadSHA string `json:"head_sha"`
	} `json:"workflow_run"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func mapWorkflowRun(raw json.RawMessage, deliveryID string) (*ingest.WireEvent, error) {
	var p workflowRunPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode workflow_run: %w", err)
	}
	if p.Repository.FullName == "" || p.WorkflowRun.ID == 0 {
		return nil, fmt.Errorf("workflow_run missing repository.full_name or workflow_run.id")
	}

	// Actor for a build is the system that ran it, not whoever pushed.
	// Use the workflow name (e.g. "CI") so the UI reads
	// "system · CI · <sha>" rather than "system · alice · <sha>".
	actorID := p.WorkflowRun.Name
	if actorID == "" {
		actorID = "github-actions"
	}

	var refs []string
	if p.WorkflowRun.HeadSHA != "" {
		refs = []string{"git:" + p.WorkflowRun.HeadSHA}
	}

	return &ingest.WireEvent{
		ID:        deliveryID,
		TS:        time.Now().UTC(),
		SessionID: fmt.Sprintf("github-build:%s/%d", p.Repository.FullName, p.WorkflowRun.ID),
		Actor: ingest.WireActor{
			Type: "system",
			ID:   actorID,
		},
		Kind:    "build",
		Payload: raw,
		Refs:    refs,
	}, nil
}
