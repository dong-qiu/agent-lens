package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildEventsUserPrompt(t *testing.T) {
	evs, commit := buildEvents(&claudeHookInput{
		HookEventName: "UserPromptSubmit",
		SessionID:     "s1",
		Prompt:        "build me an X",
	})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if evs[0]["kind"] != "prompt" {
		t.Errorf("kind = %v, want prompt", evs[0]["kind"])
	}
	if commit != nil {
		t.Error("commit should be nil for non-Stop events")
	}
}

func TestBuildEventsPreToolUse(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PreToolUse",
		SessionID:     "s1",
		ToolName:      "Edit",
		ToolInput:     json.RawMessage(`{"file":"x"}`),
	})
	if len(evs) != 1 || evs[0]["kind"] != "tool_call" {
		t.Errorf("got %+v, want one tool_call event", evs)
	}
}

func TestBuildEventsPostToolUse(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PostToolUse",
		SessionID:     "s1",
		ToolName:      "Edit",
		ToolResponse:  json.RawMessage(`{"ok":true}`),
	})
	if len(evs) != 1 || evs[0]["kind"] != "tool_result" {
		t.Errorf("got %+v, want one tool_result event", evs)
	}
}

func TestBuildEventsUnknown(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{HookEventName: "Mystery", SessionID: "s1"})
	if len(evs) != 0 {
		t.Errorf("got %+v, want no events", evs)
	}
}

func TestBuildEventsStopWithoutTranscript(t *testing.T) {
	evs, commit := buildEvents(&claudeHookInput{
		HookEventName: "Stop",
		SessionID:     "s1",
	})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1 (turn_end only)", len(evs))
	}
	payload := evs[0]["payload"].(map[string]any)
	if payload["marker"] != "turn_end" {
		t.Errorf("marker = %v, want turn_end", payload["marker"])
	}
	if commit != nil {
		t.Error("commit should be nil when there is no transcript path")
	}
}

func TestBuildEventsStopWithTranscript(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AGENT_LENS_CURSOR_DIR", filepath.Join(tmp, "cursors"))

	transcriptPath := filepath.Join(tmp, "tx.jsonl")
	transcript := `{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"hello"}],"model":"claude-opus-4-7"}}
`
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	evs, commit := buildEvents(&claudeHookInput{
		HookEventName:  "Stop",
		SessionID:      "s1",
		TranscriptPath: transcriptPath,
	})
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3 (thought + assistant_message + turn_end)", len(evs))
	}

	if evs[0]["kind"] != "thought" {
		t.Errorf("evs[0].kind = %v, want thought", evs[0]["kind"])
	}
	if p := evs[0]["payload"].(map[string]any); p["text"] != "reason" {
		t.Errorf("thought text = %v, want reason", p["text"])
	}
	if actor := evs[0]["actor"].(map[string]any); actor["model"] != "claude-opus-4-7" {
		t.Errorf("thought actor.model = %v, want claude-opus-4-7", actor["model"])
	}

	if evs[1]["kind"] != "decision" {
		t.Errorf("evs[1].kind = %v, want decision", evs[1]["kind"])
	}
	if p := evs[1]["payload"].(map[string]any); p["marker"] != "assistant_message" || p["text"] != "hello" {
		t.Errorf("assistant_message payload = %+v", p)
	}

	if p := evs[2]["payload"].(map[string]any); p["marker"] != "turn_end" {
		t.Errorf("evs[2] marker = %v, want turn_end", p["marker"])
	}

	if commit == nil {
		t.Fatal("commit should be non-nil after a successful transcript read")
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Second invocation with no new transcript content yields just turn_end.
	evs2, _ := buildEvents(&claudeHookInput{
		HookEventName:  "Stop",
		SessionID:      "s1",
		TranscriptPath: transcriptPath,
	})
	if len(evs2) != 1 || evs2[0]["payload"].(map[string]any)["marker"] != "turn_end" {
		t.Errorf("second Stop got %+v, want only turn_end", evs2)
	}
}

func TestBuildEventsMissingSessionShortCircuit(t *testing.T) {
	// runClaude rejects empty session at the entry point; buildEvents
	// itself does not validate, but exercising the contract here keeps
	// the test surface honest.
	evs, _ := buildEvents(&claudeHookInput{HookEventName: "UserPromptSubmit"})
	if len(evs) != 1 {
		t.Errorf("buildEvents accepts inputs with empty session_id; runClaude must guard at entry")
	}
}

func TestTransportPostsEvents(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("AGENT_LENS_URL", srv.URL)
	if err := newTransport().Send([]map[string]any{{"hello": "world"}}, "s1"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !bytes.Contains(got, []byte(`"hello":"world"`)) {
		t.Errorf("server got %s, want hello/world", got)
	}
}

func TestTransportFallsBackToSinkOnNetErr(t *testing.T) {
	t.Setenv("AGENT_LENS_URL", "http://127.0.0.1:1")
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := newTransport().Send([]map[string]any{{"k": "v"}}, "sessXYZ"); err != nil {
		t.Fatalf("send: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, ".agent-lens", "sessions", "sessXYZ.ndjson"))
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	if !strings.Contains(string(b), `"k":"v"`) {
		t.Errorf("sink content missing payload: %s", b)
	}
}

func TestTransportFallsBackToSinkOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("AGENT_LENS_URL", srv.URL)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := newTransport().Send([]map[string]any{{"k": "v"}}, "s1"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(tmp, ".agent-lens", "sessions", "s1.ndjson")); err != nil {
		t.Errorf("sink not written on 5xx: %v", err)
	}
}
