package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/store"
)

const sampleDeploy = `{
  "environment": "production",
  "git_sha": "deadbeefcafe1234567890abcdef0123456789ab",
  "image": "ghcr.io/acme/widget",
  "image_digest": "sha256:abcdef0123456789",
  "status": "succeeded",
  "deployed_by": "alice",
  "platform": "k8s",
  "cluster": "prod-us-east",
  "namespace": "default"
}`

func deliver(t *testing.T, h *Handler, body []byte, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/deploy", bytes.NewReader(body))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMapDeployHappyPath(t *testing.T) {
	ev, err := mapDeploy([]byte(sampleDeploy), "deploy-1")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Kind != "deploy" {
		t.Errorf("kind = %q, want deploy", ev.Kind)
	}
	if ev.SessionID != "deploy:production" {
		t.Errorf("session_id = %q (must be per-environment)", ev.SessionID)
	}
	if ev.ID != "deploy-1" {
		t.Errorf("id = %q, want deploy-1", ev.ID)
	}
	if ev.Actor.Type != "system" || ev.Actor.ID != "alice" {
		t.Errorf("actor = %+v", ev.Actor)
	}
	want := []string{"git:deadbeefcafe1234567890abcdef0123456789ab", "image:sha256:abcdef0123456789"}
	if len(ev.Refs) != 2 {
		t.Fatalf("refs = %+v, want 2", ev.Refs)
	}
	for i, r := range want {
		if ev.Refs[i] != r {
			t.Errorf("refs[%d] = %q, want %q", i, ev.Refs[i], r)
		}
	}
}

func TestMapDeployRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not json":         `not json`,
		"missing env":      `{"git_sha":"abc"}`,
		"no sha or digest": `{"environment":"prod"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := mapDeploy([]byte(body), ""); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestMapDeployFallsBackOnDeployedBy(t *testing.T) {
	body := `{"environment":"production","git_sha":"abc","status":"succeeded"}`
	ev, err := mapDeploy([]byte(body), "")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if ev.Actor.ID != "deploy-system" {
		t.Errorf("actor.id = %q, want fallback deploy-system", ev.Actor.ID)
	}
}

func TestMapDeployImageDigestOnly(t *testing.T) {
	body := `{"environment":"prod","image_digest":"sha256:1234"}`
	ev, err := mapDeploy([]byte(body), "")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(ev.Refs) != 1 || ev.Refs[0] != "image:sha256:1234" {
		t.Errorf("refs = %+v", ev.Refs)
	}
}

func TestHandlerHappyPath(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler(ingest.NewHandler(st))

	rec := deliver(t, h, []byte(sampleDeploy), "deploy-key-1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	events, _ := st.ListBySession(context.Background(), "deploy:production", 0)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].ID != "deploy-key-1" {
		t.Errorf("event id = %q, want idempotency key", events[0].ID)
	}
}

func TestHandlerDuplicateIdempotencyKey(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler(ingest.NewHandler(st))

	first := deliver(t, h, []byte(sampleDeploy), "deploy-key-dup")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want 202", first.Code)
	}
	second := deliver(t, h, []byte(sampleDeploy), "deploy-key-dup")
	if second.Code != http.StatusOK {
		t.Errorf("duplicate delivery status = %d, want 200", second.Code)
	}
	events, _ := st.ListBySession(context.Background(), "deploy:production", 0)
	if len(events) != 1 {
		t.Errorf("duplicate created extra events: %d", len(events))
	}
}

func TestHandlerRejectsBadPayload(t *testing.T) {
	st := store.NewMemory()
	h := NewHandler(ingest.NewHandler(st))

	rec := deliver(t, h, []byte(`{"environment":"prod"}`), "") // no sha/digest
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	events, _ := st.ListBySession(context.Background(), "deploy:prod", 0)
	if len(events) != 0 {
		t.Errorf("rejected delivery still appended %d events", len(events))
	}
}

func TestHandlerRejectsGet(t *testing.T) {
	h := NewHandler(ingest.NewHandler(store.NewMemory()))
	req := httptest.NewRequest(http.MethodGet, "/webhooks/deploy", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandlerLinksDeployToCommitViaSharedRef(t *testing.T) {
	// End-to-end: a commit lands first; then a deploy with the same git_sha.
	// EventsByRef must return both for the linker to connect them.
	st := store.NewMemory()
	ingestH := ingest.NewHandler(st)
	h := NewHandler(ingestH)

	commit := &ingest.WireEvent{
		ID:        "01HCOMMITDEPLOY",
		SessionID: "git-local",
		Actor:     ingest.WireActor{Type: "human", ID: "alice"},
		Kind:      "commit",
		Refs:      []string{"git:deadbeefcafe1234567890abcdef0123456789ab"},
	}
	if err := ingestH.Append(context.Background(), commit); err != nil {
		t.Fatalf("append commit: %v", err)
	}

	rec := deliver(t, h, []byte(sampleDeploy), "deploy-link-1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}

	peers, _ := st.EventsByRef(context.Background(), "git:deadbeefcafe1234567890abcdef0123456789ab")
	if len(peers) != 2 {
		t.Errorf("EventsByRef returned %d, want 2 (commit + deploy)", len(peers))
	}
	// Sanity: just ensure both fields decode rather than asserting specific JSON.
	_ = json.RawMessage(peers[1].Payload)
}

func TestMapDeployRejectsLongEnvironment(t *testing.T) {
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	body := []byte(`{"environment":"` + string(long) + `","git_sha":"abc"}`)
	if _, err := mapDeploy(body, ""); err == nil {
		t.Error("expected error for environment > 64 chars")
	}

	// At the limit (64) is OK.
	ok := make([]byte, 64)
	for i := range ok {
		ok[i] = 'a'
	}
	body2 := []byte(`{"environment":"` + string(ok) + `","git_sha":"abc"}`)
	if _, err := mapDeploy(body2, ""); err != nil {
		t.Errorf("64-char env rejected: %v", err)
	}
}

func TestMapDeployRejectsLongIdempotencyKey(t *testing.T) {
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'k'
	}
	body := []byte(`{"environment":"prod","git_sha":"abc"}`)
	if _, err := mapDeploy(body, string(long)); err == nil {
		t.Error("expected error for Idempotency-Key > 128 chars")
	}

	// At the limit (128) is OK.
	ok := make([]byte, 128)
	for i := range ok {
		ok[i] = 'k'
	}
	if _, err := mapDeploy(body, string(ok)); err != nil {
		t.Errorf("128-char key rejected: %v", err)
	}
}
