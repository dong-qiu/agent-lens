package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
    "html_url": "https://github.com/acme/widget/pull/42",
    "state": "open",
    "user": {"login": "alice"},
    "head": {"sha": "deadbeefcafe1234567890abcdef0123456789ab", "ref": "feature/button"},
    "base": {"ref": "main"}
  },
  "repository": {"full_name": "acme/widget"},
  "sender": {"login": "alice"}
}`

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
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

func TestMapPullRequestOpened(t *testing.T) {
	ev, err := mapPullRequest([]byte(samplePullRequestOpened))
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Kind != "pr" {
		t.Errorf("kind = %q, want pr", ev.Kind)
	}
	if ev.SessionID != "github-pr:acme/widget#42" {
		t.Errorf("session_id = %q", ev.SessionID)
	}
	if ev.Actor.Type != "human" || ev.Actor.ID != "alice" {
		t.Errorf("actor = %+v", ev.Actor)
	}
	if len(ev.Refs) != 1 || ev.Refs[0] != "git:deadbeefcafe1234567890abcdef0123456789ab" {
		t.Errorf("refs = %+v", ev.Refs)
	}
	if !bytes.Contains(ev.Payload, []byte(`"action":"opened"`)) {
		t.Errorf("payload missing action: %s", ev.Payload)
	}
	if !bytes.Contains(ev.Payload, []byte(`"head_branch":"feature/button"`)) {
		t.Errorf("payload missing head_branch: %s", ev.Payload)
	}
}

func TestMapPullRequestRejectsMalformed(t *testing.T) {
	if _, err := mapPullRequest([]byte(`not json`)); err == nil {
		t.Error("malformed json accepted")
	}
	// Missing repository / number.
	if _, err := mapPullRequest([]byte(`{"action":"opened"}`)); err == nil {
		t.Error("missing repository accepted")
	}
}

func TestHandlerHappyPath(t *testing.T) {
	st := store.NewMemory()
	ingestH := ingest.NewHandler(st)
	h := NewHandler("topsecret", ingestH)

	body := []byte(samplePullRequestOpened)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("topsecret"), body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	events, err := st.ListBySession(context.Background(), "github-pr:acme/widget#42", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Kind != "pr" {
		t.Errorf("event kind = %q", events[0].Kind)
	}
	if events[0].PrevHash != "" {
		t.Errorf("first event prev_hash = %q, want empty", events[0].PrevHash)
	}
	if events[0].Hash == "" {
		t.Errorf("event hash empty")
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
	events, _ := st.ListBySession(context.Background(), "github-pr:acme/widget#42", 0)
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
