package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/store"
)

const samplePullRequestOpened = `{
  "action": "opened",
  "number": 42,
  "pull_request": {
    "title": "Add a button",
    "body": "Implements the checkout button.",
    "html_url": "https://github.com/acme/widget/pull/42",
    "state": "open",
    "draft": false,
    "labels": [{"name": "enhancement"}],
    "user": {"login": "alice"},
    "head": {"sha": "deadbeefcafe1234567890abcdef0123456789ab", "ref": "feature/button"},
    "base": {"ref": "main"}
  },
  "repository": {"full_name": "acme/widget"},
  "sender": {"login": "alice"}
}`

const sampleDeliveryID = "11111111-2222-3333-4444-555555555555"

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func deliverPR(t *testing.T, h *Handler, body []byte, deliveryID, secret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sign([]byte(secret), body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("topsecret")
	body := []byte(`{"x":1}`)
	good := sign(secret, body)

	if !verifySignature(secret, body, good) {
		t.Error("good signature rejected")
	}
	if verifySignature(secret, body, "sha256=00") {
		t.Error("wrong signature accepted")
	}
	if verifySignature(secret, body, "") {
		t.Error("empty header accepted")
	}
	if verifySignature(secret, body, "sha1=abc") {
		t.Error("wrong scheme accepted")
	}
	if verifySignature([]byte(""), body, good) {
		t.Error("empty secret accepted")
	}
}

func TestMapPullRequestSetsDerivedFields(t *testing.T) {
	ev, err := mapPullRequest([]byte(samplePullRequestOpened), sampleDeliveryID)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Kind != "pr" {
		t.Errorf("kind = %q, want pr", ev.Kind)
	}
	if ev.SessionID != "github-pr:acme/widget/42" {
		t.Errorf("session_id = %q (must use slash-separated number, not #)", ev.SessionID)
	}
	if ev.ID != sampleDeliveryID {
		t.Errorf("id = %q, want delivery uuid %q", ev.ID, sampleDeliveryID)
	}
	if ev.Actor.Type != "human" || ev.Actor.ID != "alice" {
		t.Errorf("actor = %+v", ev.Actor)
	}
	if len(ev.Refs) != 1 || ev.Refs[0] != "git:deadbeefcafe1234567890abcdef0123456789ab" {
		t.Errorf("refs = %+v", ev.Refs)
	}
}

func TestMapPullRequestPayloadPassesThroughVerbatim(t *testing.T) {
	ev, err := mapPullRequest([]byte(samplePullRequestOpened), sampleDeliveryID)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	// Every original field must be present; the mapper must not curate.
	var got map[string]any
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if got["action"] != "opened" {
		t.Errorf("action = %v, want opened", got["action"])
	}
	pr, ok := got["pull_request"].(map[string]any)
	if !ok {
		t.Fatalf("pull_request not an object: %T", got["pull_request"])
	}
	if pr["body"] != "Implements the checkout button." {
		t.Errorf("body = %v", pr["body"])
	}
	if pr["draft"] != false {
		t.Errorf("draft = %v", pr["draft"])
	}
	if labels, _ := pr["labels"].([]any); len(labels) != 1 {
		t.Errorf("labels = %v, want one entry", pr["labels"])
	}
	head, _ := pr["head"].(map[string]any)
	if head["ref"] != "feature/button" {
		t.Errorf("head.ref = %v", head["ref"])
	}
}

func TestMapPullRequestRejectsMalformed(t *testing.T) {
	if _, err := mapPullRequest([]byte(`not json`), sampleDeliveryID); err == nil {
		t.Error("malformed json accepted")
	}
	if _, err := mapPullRequest([]byte(`{"action":"opened"}`), sampleDeliveryID); err == nil {
		t.Error("missing repository accepted")
	}
}

func TestHandlerHappyPath(t *testing.T) {
	st := store.NewMemory()
	ingestH := ingest.NewHandler(st)
	h := NewHandler("topsecret", ingestH)

	body := []byte(samplePullRequestOpened)
	rec := deliverPR(t, h, body, sampleDeliveryID, "topsecret")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	events, err := st.ListBySession(context.Background(), "github-pr:acme/widget/42", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].ID != sampleDeliveryID {
		t.Errorf("event id = %q, want %q", events[0].ID, sampleDeliveryID)
	}
	if events[0].Kind != "pr" {
		t.Errorf("event kind = %q", events[0].Kind)
	}
}

func TestHandlerDuplicateDeliveryReturns200(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))
	body := []byte(samplePullRequestOpened)

	first := deliverPR(t, h, body, sampleDeliveryID, "topsecret")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want 202", first.Code)
	}
	second := deliverPR(t, h, body, sampleDeliveryID, "topsecret")
	if second.Code != http.StatusOK {
		t.Errorf("duplicate delivery status = %d, want 200", second.Code)
	}

	events, _ := st.ListBySession(context.Background(), "github-pr:acme/widget/42", 0)
	if len(events) != 1 {
		t.Errorf("duplicate delivery created extra events: %d", len(events))
	}
}

func TestHandlerRejectsBadSignature(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))

	body := []byte(samplePullRequestOpened)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	events, _ := st.ListBySession(context.Background(), "github-pr:acme/widget/42", 0)
	if len(events) != 0 {
		t.Errorf("rejected webhook still appended %d events", len(events))
	}
}

func TestHandlerPing(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))

	body := []byte(`{"zen":"Practicality beats purity."}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("topsecret"), body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHandlerIgnoresUnknownEvent(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))

	body := []byte(`{"some":"thing"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("topsecret"), body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestHandlerRejectsGet(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))

	req := httptest.NewRequest(http.MethodGet, "/webhooks/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

const samplePullRequestReview = `{
  "action": "submitted",
  "review": {
    "id": 99,
    "state": "approved",
    "body": "LGTM with one nit on naming.",
    "user": {"login": "bob"}
  },
  "pull_request": {
    "number": 42,
    "head": {"sha": "deadbeefcafe1234567890abcdef0123456789ab"}
  },
  "repository": {"full_name": "acme/widget"},
  "sender": {"login": "bob"}
}`

const samplePush = `{
  "ref": "refs/heads/main",
  "before": "0000000000000000000000000000000000000000",
  "after": "ffffffffffffffffffffffffffffffffffffffff",
  "deleted": false,
  "commits": [
    {"id": "ffffffffffffffffffffffffffffffffffffffff", "message": "x"},
    {"id": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "message": "y"}
  ],
  "repository": {"full_name": "acme/widget"},
  "pusher": {"name": "alice", "email": "alice@example.com"},
  "sender": {"login": "alice"}
}`

const sampleBranchDeletePush = `{
  "ref": "refs/heads/feature/dead",
  "before": "ffffffffffffffffffffffffffffffffffffffff",
  "after": "0000000000000000000000000000000000000000",
  "deleted": true,
  "commits": [],
  "repository": {"full_name": "acme/widget"},
  "pusher": {"name": "alice"},
  "sender": {"login": "alice"}
}`

func deliverEvent(t *testing.T, h *Handler, event string, body []byte, deliveryID, secret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sign([]byte(secret), body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMapPullRequestReview(t *testing.T) {
	ev, err := mapPullRequestReview([]byte(samplePullRequestReview), "delivery-review-1")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Kind != "review" {
		t.Errorf("kind = %q, want review", ev.Kind)
	}
	if ev.SessionID != "github-pr:acme/widget/42" {
		t.Errorf("session_id = %q (must match the PR's session)", ev.SessionID)
	}
	if ev.ID != "delivery-review-1" {
		t.Errorf("id = %q, want delivery", ev.ID)
	}
	if ev.Actor.ID != "bob" {
		t.Errorf("actor.id = %q, want bob", ev.Actor.ID)
	}
	if len(ev.Refs) != 1 || ev.Refs[0] != "git:deadbeefcafe1234567890abcdef0123456789ab" {
		t.Errorf("refs = %+v", ev.Refs)
	}
}

func TestMapPushEmitsAllCommitRefs(t *testing.T) {
	ev, err := mapPush([]byte(samplePush), "delivery-push-1")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Kind != "push" {
		t.Errorf("kind = %q, want push", ev.Kind)
	}
	if ev.SessionID != "github-push:acme/widget/main" {
		t.Errorf("session_id = %q", ev.SessionID)
	}
	// after + 2 commit ids; after equals first commit id so dedup keeps 2 unique refs.
	if len(ev.Refs) != 2 {
		t.Errorf("refs = %+v, want 2 (deduped)", ev.Refs)
	}
	for _, ref := range ev.Refs {
		if ref == "git:0000000000000000000000000000000000000000" {
			t.Errorf("zero sha should not be emitted as a ref")
		}
	}
}

func TestMapPushBranchDeleteDropsZeroRefs(t *testing.T) {
	ev, err := mapPush([]byte(sampleBranchDeletePush), "delivery-del-1")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Kind != "push" {
		t.Errorf("kind = %q, want push", ev.Kind)
	}
	if len(ev.Refs) != 0 {
		t.Errorf("refs = %+v, want empty for branch delete", ev.Refs)
	}
	if ev.SessionID != "github-push:acme/widget/feature/dead" {
		t.Errorf("session_id = %q", ev.SessionID)
	}
}

func TestHandlerDispatchesReview(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))

	rec := deliverEvent(t, h, "pull_request_review", []byte(samplePullRequestReview), "delivery-r1", "topsecret")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	events, _ := st.ListBySession(context.Background(), "github-pr:acme/widget/42", 0)
	if len(events) != 1 || events[0].Kind != "review" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDispatchesPush(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler("topsecret", ingest.NewHandler(st))

	rec := deliverEvent(t, h, "push", []byte(samplePush), "delivery-p1", "topsecret")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	events, _ := st.ListBySession(context.Background(), "github-push:acme/widget/main", 0)
	if len(events) != 1 || events[0].Kind != "push" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerLinksReviewToCommitViaSharedRef(t *testing.T) {
	// End-to-end: a commit event arrives first (e.g. from the local
	// git-post-commit hook), then a review webhook for the same SHA;
	// the linker must connect them.
	st := store.NewMemory()
	ingestH := ingest.NewHandler(st)
	h := NewHandler("topsecret", ingestH)

	commit := &ingest.WireEvent{
		ID:        "01HCOMMIT",
		SessionID: "git-local",
		Actor:     ingest.WireActor{Type: "human", ID: "alice"},
		Kind:      "commit",
		Refs:      []string{"git:deadbeefcafe1234567890abcdef0123456789ab"},
	}
	if err := ingestH.Append(context.Background(), commit); err != nil {
		t.Fatalf("append commit: %v", err)
	}

	rec := deliverEvent(t, h, "pull_request_review", []byte(samplePullRequestReview), "delivery-link-1", "topsecret")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}

	peers, _ := st.EventsByRef(context.Background(), "git:deadbeefcafe1234567890abcdef0123456789ab")
	if len(peers) != 2 {
		t.Errorf("EventsByRef returned %d, want 2 (commit + review)", len(peers))
	}
	// Linker writes are async via the AfterAppend hook in main, but
	// this test only exercises the wire events. The linking-via-hook
	// vertical-slice is covered by the linker package tests; here we
	// just assert the refs match for downstream linking.
}
