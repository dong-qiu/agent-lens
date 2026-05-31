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

// TestBuildEventsUserPromptRedacts: the v0.1 redactor must run on
// prompt content before the event hits ingest. Without this, a user
// pasting a secret into a Claude prompt sees it land verbatim in the
// audit DB — exactly the failure mode SPEC §12 promises to prevent.
func TestBuildEventsUserPromptRedacts(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "UserPromptSubmit",
		SessionID:     "s1",
		Prompt:        "Help me debug: AKIAIOSFODNN7EXAMPLE",
	})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	payload, _ := evs[0]["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("event has no payload: %+v", evs[0])
	}
	text, _ := payload["text"].(string)
	if strings.Contains(text, "AKIAIOSFODNN7") {
		t.Errorf("AWS access key leaked through redaction: %q", text)
	}
	if !strings.Contains(text, "[REDACTED:aws-access-key-id]") {
		t.Errorf("expected [REDACTED:aws-access-key-id] marker in payload.text, got %q", text)
	}
	if n, _ := payload["redacted_count"].(int); n != 1 {
		t.Errorf("redacted_count = %v, want 1", payload["redacted_count"])
	}
}

func TestMakePromptHumanUnchanged(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "UserPromptSubmit",
		SessionID:     "s1",
		Prompt:        "build me an X",
	})
	actor := evs[0]["actor"].(map[string]any)
	if actor["type"] != "human" || actor["id"] != "user" {
		t.Errorf("actor = %+v, want human/user", actor)
	}
	if _, ok := evs[0]["payload"].(map[string]any)["source"]; ok {
		t.Errorf("a genuine human prompt should not carry payload.source")
	}
}

// TestMakePromptInjectedAttributedToSystem guards issue #118: system-injected
// UserPromptSubmit blocks must be attributed to the system, not the human, and
// carry a source discriminator.
func TestMakePromptInjectedAttributedToSystem(t *testing.T) {
	cases := []struct{ name, prompt, wantSource string }{
		{"task-notification", "<task-notification>\n<task-id>blr2t3oka</task-id>\n<status>completed</status>\n</task-notification>", "task_notification"},
		{"system-reminder", "<system-reminder>Plan mode is active.</system-reminder>", "system_reminder"},
		{"leading whitespace", "\n  <task-notification><task-id>x</task-id></task-notification>", "task_notification"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs, _ := buildEvents(&claudeHookInput{
				HookEventName: "UserPromptSubmit",
				SessionID:     "s1",
				Prompt:        tc.prompt,
			})
			if evs[0]["kind"] != "prompt" {
				t.Fatalf("kind = %v, want prompt", evs[0]["kind"])
			}
			actor := evs[0]["actor"].(map[string]any)
			if actor["type"] != "system" || actor["id"] != "claude-code" {
				t.Errorf("actor = %+v, want system/claude-code", actor)
			}
			if got := evs[0]["payload"].(map[string]any)["source"]; got != tc.wantSource {
				t.Errorf("payload.source = %v, want %v", got, tc.wantSource)
			}
		})
	}
}

func TestInjectedPromptSource(t *testing.T) {
	cases := []struct{ in, want string }{
		{"build me an X", ""},
		{"<div> is broken, fix it", ""}, // human prompt that merely starts with "<"
		{"<task-notification><task-id>x</task-id></task-notification>", "task_notification"},
		{"  \n<system-reminder>hi</system-reminder>", "system_reminder"},
		{"<task-reminder>do the thing</task-reminder>", "task_reminder"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := injectedPromptSource(tc.in); got != tc.want {
			t.Errorf("injectedPromptSource(%q) = %q, want %q", tc.in, got, tc.want)
		}
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

func TestBuildEventsSkillInvocation(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PreToolUse",
		SessionID:     "s1",
		ToolName:      "Skill",
		ToolInput:     json.RawMessage(`{"skill":"review","args":"100"}`),
	})
	if len(evs) != 1 || evs[0]["kind"] != "tool_call" {
		t.Fatalf("got %+v, want one tool_call event", evs)
	}
	payload, ok := evs[0]["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload not a map: %T", evs[0]["payload"])
	}
	skill, ok := payload["skill"].(map[string]any)
	if !ok {
		t.Fatalf("payload.skill missing or not a map: %+v", payload)
	}
	if skill["name"] != "review" {
		t.Errorf("skill.name = %v, want review", skill["name"])
	}
	if skill["args"] != "100" {
		t.Errorf("skill.args = %v, want 100", skill["args"])
	}
}

func TestSkillDiscriminatorOnlyForSkillTool(t *testing.T) {
	// A non-Skill tool must never get a skill discriminator, even if its
	// input happens to contain a "skill" key.
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PreToolUse",
		SessionID:     "s1",
		ToolName:      "Edit",
		ToolInput:     json.RawMessage(`{"skill":"sneaky"}`),
	})
	payload := evs[0]["payload"].(map[string]any)
	if _, present := payload["skill"]; present {
		t.Errorf("non-Skill tool got a skill discriminator: %+v", payload)
	}
}

func TestSkillInvocationArgless(t *testing.T) {
	got := skillInvocation("Skill", json.RawMessage(`{"skill":"self-review"}`))
	if got == nil || got["name"] != "self-review" {
		t.Fatalf("got %+v, want name=self-review", got)
	}
	if _, hasArgs := got["args"]; hasArgs {
		t.Errorf("args present for an argless skill: %+v", got)
	}
}

func TestSkillInvocationFailSoft(t *testing.T) {
	cases := []struct {
		name, tool, input string
	}{
		{"non-skill tool", "Bash", `{"skill":"x"}`},
		{"empty skill name", "Skill", `{"args":"100"}`},
		{"unparseable input", "Skill", `not json`},
		{"empty input", "Skill", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillInvocation(tc.tool, json.RawMessage(tc.input)); got != nil {
				t.Errorf("skillInvocation(%q, %q) = %+v, want nil", tc.tool, tc.input, got)
			}
		})
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

func TestBuildEventsSubagentStart(t *testing.T) {
	evs, commit := buildEvents(&claudeHookInput{
		HookEventName: "SubagentStart",
		SessionID:     "child-uuid",
		CWD:           "/repo",
		AgentID:       "a06a387fa5403439d",
		AgentType:     "Explore",
	})
	if commit != nil {
		t.Errorf("SubagentStart should not return a commit fn")
	}
	if len(evs) != 1 || evs[0]["kind"] != "decision" {
		t.Fatalf("got %+v, want one decision event", evs)
	}
	ev := evs[0]
	// Attributed to the *child* session — the side that carries
	// (session_id, agent_id) for the #85 parent→child bridge.
	if ev["session_id"] != "child-uuid" {
		t.Errorf("session_id = %v, want child-uuid", ev["session_id"])
	}
	if actor := ev["actor"].(map[string]any); actor["type"] != "system" {
		t.Errorf("actor.type = %v, want system", actor["type"])
	}
	p := ev["payload"].(map[string]any)
	if p["marker"] != "subagent_start" {
		t.Errorf("marker = %v, want subagent_start", p["marker"])
	}
	if p["agent_id"] != "a06a387fa5403439d" {
		t.Errorf("agent_id = %v, want a06a387fa5403439d", p["agent_id"])
	}
	if p["agent_type"] != "Explore" {
		t.Errorf("agent_type = %v, want Explore", p["agent_type"])
	}
}

func TestBuildEventsSubagentStop(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "SubagentStop",
		SessionID:     "child-uuid",
		AgentID:       "a06a387fa5403439d",
	})
	if len(evs) != 1 || evs[0]["kind"] != "decision" {
		t.Fatalf("got %+v, want one decision event", evs)
	}
	p := evs[0]["payload"].(map[string]any)
	if p["marker"] != "subagent_stop" {
		t.Errorf("marker = %v, want subagent_stop", p["marker"])
	}
	if p["agent_id"] != "a06a387fa5403439d" {
		t.Errorf("agent_id = %v, want a06a387fa5403439d", p["agent_id"])
	}
}

func TestBuildEventsSubagentStartOmitsEmptyIDs(t *testing.T) {
	// The lifecycle marker still emits without agent_id/agent_type (it has
	// timeline value); the optional keys are simply omitted, not null.
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "SubagentStart",
		SessionID:     "child-uuid",
	})
	p := evs[0]["payload"].(map[string]any)
	if p["marker"] != "subagent_start" {
		t.Errorf("marker = %v, want subagent_start", p["marker"])
	}
	if _, ok := p["agent_id"]; ok {
		t.Errorf("agent_id present despite empty input: %+v", p)
	}
	if _, ok := p["agent_type"]; ok {
		t.Errorf("agent_type present despite empty input: %+v", p)
	}
}

func TestBuildEventsSessionEnd(t *testing.T) {
	evs, commit := buildEvents(&claudeHookInput{
		HookEventName: "SessionEnd",
		SessionID:     "s1",
		CWD:           "/repo",
		Reason:        "logout",
	})
	if commit != nil {
		t.Errorf("SessionEnd should not return a commit fn")
	}
	if len(evs) != 1 || evs[0]["kind"] != "decision" {
		t.Fatalf("got %+v, want one decision event", evs)
	}
	ev := evs[0]
	if actor := ev["actor"].(map[string]any); actor["type"] != "system" || actor["id"] != "claude-code" {
		t.Errorf("actor = %v, want system/claude-code", actor)
	}
	p := ev["payload"].(map[string]any)
	if p["marker"] != "session_end" {
		t.Errorf("marker = %v, want session_end", p["marker"])
	}
	if p["reason"] != "logout" {
		t.Errorf("reason = %v, want logout", p["reason"])
	}
	if p["cwd"] != "/repo" {
		t.Errorf("cwd = %v, want /repo", p["cwd"])
	}
}

func TestBuildEventsSessionEndOmitsEmptyReason(t *testing.T) {
	// SessionEnd still emits the boundary marker without a reason; the
	// optional key is omitted, not null.
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "SessionEnd",
		SessionID:     "s1",
	})
	p := evs[0]["payload"].(map[string]any)
	if p["marker"] != "session_end" {
		t.Errorf("marker = %v, want session_end", p["marker"])
	}
	if _, ok := p["reason"]; ok {
		t.Errorf("reason present despite empty input: %+v", p)
	}
}

func TestBuildEventsSessionStartCapturesSource(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "SessionStart",
		SessionID:     "s1",
		CWD:           "/repo",
		Source:        "resume",
	})
	if len(evs) != 1 || evs[0]["kind"] != "decision" {
		t.Fatalf("got %+v, want one decision event", evs)
	}
	p := evs[0]["payload"].(map[string]any)
	if p["marker"] != "session_start" {
		t.Errorf("marker = %v, want session_start", p["marker"])
	}
	if p["source"] != "resume" {
		t.Errorf("source = %v, want resume", p["source"])
	}
}

func TestBuildEventsSessionStartOmitsEmptySource(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "SessionStart",
		SessionID:     "s1",
		CWD:           "/repo",
	})
	p := evs[0]["payload"].(map[string]any)
	if _, ok := p["source"]; ok {
		t.Errorf("source present despite empty input: %+v", p)
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

func countKind(evs []map[string]any, kind string) int {
	n := 0
	for _, e := range evs {
		if e["kind"] == kind {
			n++
		}
	}
	return n
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildEventsStopSystemReminderDedup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AGENT_LENS_CURSOR_DIR", filepath.Join(tmp, "cursors"))
	path := filepath.Join(tmp, "tx.jsonl")

	stop := func() ([]map[string]any, func() error) {
		return buildEvents(&claudeHookInput{HookEventName: "Stop", SessionID: "s1", TranscriptPath: path})
	}

	// Turn 1: a static system-reminder + assistant text.
	appendLine(t, path, `{"type":"user","message":{"content":"<system-reminder>auto-memory header</system-reminder>"}}`)
	appendLine(t, path, `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"ok"}]}}`)
	evs, commit := stop()
	if got := countKind(evs, "context_transform"); got != 1 {
		t.Fatalf("turn1 context_transform = %d, want 1", got)
	}
	var ct map[string]any
	for _, e := range evs {
		if e["kind"] == "context_transform" {
			ct = e
		}
	}
	p := ct["payload"].(map[string]any)
	if p["sub_kind"] != "system_reminder_injection" {
		t.Errorf("sub_kind = %v", p["sub_kind"])
	}
	if after := p["after"].(map[string]any); after["injected_text"] != "auto-memory header" {
		t.Errorf("injected_text = %v", after["injected_text"])
	}
	if lh := p["loss_hint"].(map[string]any); lh["confidence"] != "observed" {
		t.Errorf("confidence = %v, want observed", lh["confidence"])
	}
	if actor := ct["actor"].(map[string]any); actor["type"] != "system" {
		t.Errorf("actor.type = %v, want system", actor["type"])
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Turn 2: SAME reminder re-injected (static) → deduped, no new event.
	appendLine(t, path, `{"type":"user","message":{"content":"<system-reminder>auto-memory header</system-reminder>"}}`)
	appendLine(t, path, `{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"again"}]}}`)
	evs2, commit2 := stop()
	if got := countKind(evs2, "context_transform"); got != 0 {
		t.Errorf("turn2 context_transform = %d, want 0 (static reminder deduped)", got)
	}
	if err := commit2(); err != nil {
		t.Fatalf("commit2: %v", err)
	}

	// Turn 3: a DIFFERENT reminder → emitted (distinct hash).
	appendLine(t, path, `{"type":"user","message":{"content":"<system-reminder>todo changed</system-reminder>"}}`)
	evs3, _ := stop()
	if got := countKind(evs3, "context_transform"); got != 1 {
		t.Errorf("turn3 context_transform = %d, want 1 (distinct reminder)", got)
	}
}
