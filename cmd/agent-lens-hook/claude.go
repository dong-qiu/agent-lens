package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dong-qiu/agent-lens/internal/redact"
	"github.com/dong-qiu/agent-lens/internal/transcript"
)

// redactText runs the v0.1 rule-based redactor over free-text content
// (prompts / thinking / assistant decisions) before it lands in the
// event payload. Tool-call payloads are NOT redacted because the
// canonical command (e.g. "curl -H 'Authorization: ...'") is part of
// what audit needs to see verbatim — redacting commands would break
// reproducibility checks and replay scenarios.
func redactText(s string) (string, int) {
	return redact.Redact(s)
}

// claudeHookInput captures the fields we read from a Claude Code hook
// payload on stdin. Other fields are ignored; the original payload is not
// echoed back so secrets in transcript paths don't leak by accident.
type claudeHookInput struct {
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
	// SubagentStart / SubagentStop carry the sub-agent's identity. agent_id
	// is the child-side key that matches the parent's Agent tool_result
	// response.agentId (issue #85).
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	// SessionStart carries source (startup/resume/clear/compact); SessionEnd
	// carries reason (clear/resume/logout/prompt_input_exit/...). ADR 0012.
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// runClaude reads a Claude Code hook payload on stdin and forwards a wire
// event. The hook always exits 0 so it cannot block Claude Code; failures
// are logged to stderr and (when possible) persisted to the local sink for
// later replay.
func runClaude(_ []string) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		warn("read stdin: %v", err)
		os.Exit(0)
	}
	if len(raw) == 0 {
		os.Exit(0)
	}
	var in claudeHookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		warn("decode stdin: %v", err)
		os.Exit(0)
	}
	if in.SessionID == "" {
		os.Exit(0)
	}

	events, commit := buildEvents(&in)
	if len(events) == 0 {
		os.Exit(0)
	}
	if err := newTransport().Send(events, in.SessionID); err != nil {
		warn("send: %v", err)
		os.Exit(0) // do not commit cursor on hard failure
	}
	if commit != nil {
		if err := commit(); err != nil {
			warn("commit cursor: %v", err)
		}
	}
	os.Exit(0)
}

// buildEvents turns one hook input into one or more wire events. The
// returned commit, when non-nil, must be invoked only after a successful
// transport.Send so that transcript cursors advance only on durable
// delivery.
func buildEvents(in *claudeHookInput) (events []map[string]any, commit func() error) {
	switch in.HookEventName {
	case "UserPromptSubmit":
		return []map[string]any{makePrompt(in)}, nil
	case "PreToolUse":
		return []map[string]any{makeToolCall(in)}, nil
	case "PostToolUse":
		evs := []map[string]any{makeToolResult(in)}
		// A Bash command that ran a test suite additionally derives a
		// `test_run` event, co-existing with the tool_result (ADR 0011).
		if in.ToolName == "Bash" {
			if tr := makeTestRun(in); tr != nil {
				evs = append(evs, tr)
			}
		}
		return evs, nil
	case "SessionStart":
		return []map[string]any{makeSessionStart(in)}, nil
	case "SessionEnd":
		return []map[string]any{makeSessionEnd(in)}, nil
	case "Stop":
		return makeStopEvents(in)
	case "SubagentStart":
		return []map[string]any{makeSubagentLifecycle(in, "subagent_start")}, nil
	case "SubagentStop":
		return []map[string]any{makeSubagentLifecycle(in, "subagent_stop")}, nil
	}
	return nil, nil
}

// makeSubagentLifecycle captures a Claude Code SubagentStart / SubagentStop
// hook as a decision-marker event, mirroring makeSessionStart's shape. The
// hook fires in the *sub-agent's own* session, so in.SessionID is the child
// session id and payload.agent_id is the child's agent id.
//
// That (session_id, agent_id) pair is the child-side half of the parent→child
// bridge: the parent's Agent tool_result carries the same agent_id under
// response.agentId, so a linker can match the two and emit a `delegates` link
// from parent to child. Capturing the lifecycle is ADR-free (payload-only);
// the delegates link + RELATION_DELEGATES schema change is the ADR-gated
// follow-up (issue #85, PR 2). Until then this already ends our blindness to
// sub-agent start/stop in the timeline.
func makeSubagentLifecycle(in *claudeHookInput, marker string) map[string]any {
	payload := map[string]any{
		"marker": marker,
		"cwd":    in.CWD,
	}
	if in.AgentID != "" {
		payload["agent_id"] = in.AgentID
	}
	if in.AgentType != "" {
		payload["agent_type"] = in.AgentType
	}
	return baseEvent(in, map[string]any{"type": "system", "id": "claude-code"}, "decision", payload)
}

func makePrompt(in *claudeHookInput) map[string]any {
	text, n := redactText(in.Prompt)
	payload := map[string]any{
		"text": text,
		"cwd":  in.CWD,
	}
	if n > 0 {
		payload["redacted_count"] = n
	}
	// UserPromptSubmit also fires for system-injected blocks — a backgrounded
	// task / sub-agent completion (<task-notification>) or a system nudge
	// (<system-reminder>) — which are NOT human input. Attribute them to the
	// system and record payload.source so "who said what" stays honest and
	// "human prompt" queries don't over-count (issue #118). Detection is on the
	// raw prompt (the structural tag survives redaction and rides at the head).
	actor := map[string]any{"type": "human", "id": "user"}
	if src := injectedPromptSource(in.Prompt); src != "" {
		actor = map[string]any{"type": "system", "id": "claude-code"}
		payload["source"] = src
	}
	return baseEvent(in, actor, "prompt", payload)
}

// injectedPromptSource classifies a UserPromptSubmit body that is actually a
// system-injected block rather than human input, returning a low-cardinality
// source label ("" for a genuine human prompt). Conservative on purpose:
// only clearly system-generated blocks are matched — slash-command echoes are
// human-initiated and intentionally left as human input. Content-based because
// the hook payload carries no source field; the blocks open with known tags.
func injectedPromptSource(prompt string) string {
	t := strings.TrimLeft(prompt, " \t\r\n")
	switch {
	case strings.HasPrefix(t, "<task-notification"):
		return "task_notification"
	case strings.HasPrefix(t, "<system-reminder"):
		return "system_reminder"
	case strings.HasPrefix(t, "<task-reminder"):
		return "task_reminder"
	}
	return ""
}

func makeToolCall(in *claudeHookInput) map[string]any {
	payload := map[string]any{
		"name":  in.ToolName,
		"input": in.ToolInput,
	}
	// Authorization context: which allowlist rule matched (if any),
	// and any high-risk patterns detected in the input. PreToolUse
	// only fires after Claude Code has granted permission, so this
	// classifies the *path* by which permission was granted:
	//   allowlist_match != "" → auto-allowed by policy
	//   allowlist_match == "" → user must have approved interactively
	// risk_signals flags audit-relevant patterns regardless of path.
	auth := map[string]any{
		"risk_signals": detectRiskSignalsOrEmpty(in.ToolName, in.ToolInput),
	}
	if perms := loadPermissionsSnapshot(in.CWD); perms != nil {
		if allowAny, ok := perms["allow"].([]any); ok {
			allow := make([]string, 0, len(allowAny))
			for _, e := range allowAny {
				if s, ok := e.(string); ok {
					allow = append(allow, s)
				}
			}
			primary := extractPrimaryArg(in.ToolName, in.ToolInput)
			if match := matchAllowlist(in.ToolName, primary, allow); match != "" {
				auth["allowlist_match"] = match
			}
		}
	}
	payload["authorization"] = auth
	if skill := skillInvocation(in.ToolName, in.ToolInput); skill != nil {
		payload["skill"] = skill
	}
	return baseEvent(in, agentActor(), "tool_call", payload)
}

// skillInvocation extracts a normalized skill discriminator from a Skill
// tool_call's input. Returns nil for non-Skill tools or unparseable input
// (fail-soft: a missing discriminator just leaves a plain tool_call). This is
// gap 1 of issue #101 — a queryable marker that "a skill ran" and which one,
// without downstream having to special-case the Skill tool's input shape.
//
// Deliberately neutral: it records name+args only, not whether the skill was
// human-typed (/cmd) vs model-invoked — the hook can't tell them apart, and
// the "human-initiated workflow" semantics are the open design question #101
// leaves for a possible human_intervention sub_kind (ADR 0004). The injected
// instruction *body* (gap 2) isn't reachable here — the PostToolUse response
// carries only {success, commandName}; tracked in #110.
func skillInvocation(toolName string, toolInput json.RawMessage) map[string]any {
	if toolName != "Skill" {
		return nil
	}
	var s struct {
		Skill string `json:"skill"`
		Args  string `json:"args"`
	}
	if err := json.Unmarshal(toolInput, &s); err != nil || s.Skill == "" {
		return nil
	}
	out := map[string]any{"name": s.Skill}
	if s.Args != "" {
		out["args"] = s.Args
	}
	return out
}

// detectRiskSignalsOrEmpty wraps detectRiskSignals so the authorization
// payload always carries a present-but-possibly-empty array (rather
// than null), which is friendlier for downstream UI / SQL filtering.
func detectRiskSignalsOrEmpty(toolName string, toolInput json.RawMessage) []string {
	out := detectRiskSignals(toolName, toolInput)
	if out == nil {
		return []string{}
	}
	return out
}

func makeToolResult(in *claudeHookInput) map[string]any {
	ev := baseEvent(in, agentActor(), "tool_result", map[string]any{
		"name":     in.ToolName,
		"input":    in.ToolInput,
		"response": in.ToolResponse,
	})
	// Stitch this Claude session to the corresponding git-post-commit
	// session by attaching the same `git:<full-sha>` ref the post-commit
	// hook emits. Without this, the linker has no shared ref to match
	// across the two sessions and the cross-stage chain has a hole at
	// the prompt-to-commit edge. See issue #48.
	if in.ToolName == "Bash" {
		if refs := gitCommitRefsFromBash(in.ToolInput, in.ToolResponse, in.CWD); len(refs) > 0 {
			ev["refs"] = refs
		}
	}
	return ev
}

func makeSessionStart(in *claudeHookInput) map[string]any {
	payload := map[string]any{
		"marker": "session_start",
		"cwd":    in.CWD,
	}
	// SessionStart.source (startup/resume/clear/compact) gives the session a
	// typed origin; source=resume pairs with SessionEnd.reason=resume to
	// reconstruct cross-process continuation. ADR 0012 D2. Only `source` is
	// taken here — model / agent_type / session_title belong to the
	// agent_config_snapshot (ADR 0003), not loose in this payload.
	if in.Source != "" {
		payload["source"] = in.Source
	}
	// Capture the project-local Claude Code permission policy in
	// effect for this session so audit reports can answer "what
	// authorization rules were running at the time?". Absent settings
	// is fine — the field is just omitted.
	if perms := loadPermissionsSnapshot(in.CWD); perms != nil {
		payload["permissions"] = perms
	}
	return baseEvent(in, map[string]any{"type": "system", "id": "claude-code"}, "decision", payload)
}

// makeSessionEnd captures the Claude Code SessionEnd hook as a decision-marker
// event, mirroring makeSessionStart, so a session has an explicit closing
// boundary. reason (clear/resume/logout/prompt_input_exit/...) lets audit
// distinguish a clean exit from a resume continuation or a /clear. A crash or
// kill fires no SessionEnd — the absence of session_end is itself the signal.
// ADR 0012 D1.
func makeSessionEnd(in *claudeHookInput) map[string]any {
	payload := map[string]any{
		"marker": "session_end",
		"cwd":    in.CWD,
	}
	if in.Reason != "" {
		payload["reason"] = in.Reason
	}
	return baseEvent(in, map[string]any{"type": "system", "id": "claude-code"}, "decision", payload)
}

// makeStopEvents reads the transcript for the just-completed turn and
// emits a thought event per `thinking` block, an assistant_message event
// per `text` block, and a turn_end marker. The returned commit advances
// the transcript cursor; call it only after successful Send.
func makeStopEvents(in *claudeHookInput) ([]map[string]any, func() error) {
	turnEnd := baseEvent(in, agentActor(), "decision", map[string]any{"marker": "turn_end"})

	if in.TranscriptPath == "" {
		return []map[string]any{turnEnd}, nil
	}

	r := transcript.NewReader(cursorDir())
	blocks, offset, err := r.Read(in.TranscriptPath, in.SessionID)
	if err != nil {
		warn("transcript read: %v", err)
		return []map[string]any{turnEnd}, nil
	}

	// Per-session dedup state for system-reminder injections (ADR 0005 D3):
	// emit each distinct reminder once. Fail-soft — a read error just means we
	// might re-emit a reminder, never lose the turn's other events.
	seenReminders, err := r.SeenReminders(in.SessionID)
	if err != nil {
		warn("seen reminders: %v", err)
		seenReminders = map[string]bool{}
	}
	var newReminderHashes []string

	events := make([]map[string]any, 0, len(blocks)+1)
	for _, b := range blocks {
		switch b.Kind {
		case "thinking":
			text, n := redactText(b.Content)
			payload := map[string]any{
				"text":       text,
				"message_id": b.MessageID,
				"source":     "transcript",
			}
			if n > 0 {
				payload["redacted_count"] = n
			}
			attachUsageMetadata(payload, &b)
			events = append(events, baseEvent(in, agentActorWithModel(b.Model), "thought", payload))
		case "text":
			text, n := redactText(b.Content)
			payload := map[string]any{
				"marker":     "assistant_message",
				"text":       text,
				"message_id": b.MessageID,
			}
			if n > 0 {
				payload["redacted_count"] = n
			}
			// Surface Claude-Code-side redaction of `thinking` content
			// so audit readers don't mistake "transcript field empty"
			// for "model didn't think". Capturing the actual content
			// requires §10.4 proxy mode.
			if b.RedactedThinking > 0 {
				payload["thinking_redacted_by_claude_code"] = b.RedactedThinking
			}
			attachUsageMetadata(payload, &b)
			events = append(events, baseEvent(in, agentActorWithModel(b.Model), "decision", payload))
		case "system_reminder":
			// Harness-injected <system-reminder> → context_transform (ADR 0005
			// D3). Dedup by content hash so a re-injected static reminder is
			// recorded once per session; dynamic reminders (hash varies) each
			// land once. observed: the reminder text is in the transcript.
			h := sha256Hex(b.Content)
			if seenReminders[h] {
				continue
			}
			seenReminders[h] = true
			newReminderHashes = append(newReminderHashes, h)
			text, n := redactText(b.Content)
			payload := map[string]any{
				"sub_kind":       "system_reminder_injection",
				"triggered_by":   "harness_auto",
				"content_sha256": h,
				"after":          map[string]any{"injected_text": text},
				"loss_hint":      map[string]any{"confidence": "observed"},
			}
			if n > 0 {
				payload["redacted_count"] = n
			}
			events = append(events, baseEvent(in, map[string]any{"type": "system", "id": "claude-code"}, "context_transform", payload))
		}
	}
	events = append(events, turnEnd)

	commit := func() error {
		if err := r.Commit(in.SessionID, offset); err != nil {
			return err
		}
		return r.AddSeenReminders(in.SessionID, newReminderHashes)
	}
	return events, commit
}

// sha256Hex is the content key used to dedup system-reminder injections.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// attachUsageMetadata copies per-message usage / stop_reason from the
// transcript block onto the event payload. The reader attaches both to
// a single carrier block per message so this won't double-count in
// turn / session aggregation; see ADR 0002 D1.
func attachUsageMetadata(payload map[string]any, b *transcript.Block) {
	if b.Usage != nil {
		payload["usage"] = b.Usage
	}
	if b.StopReason != "" {
		payload["stop_reason"] = b.StopReason
	}
}

func baseEvent(in *claudeHookInput, actor map[string]any, kind string, payload map[string]any) map[string]any {
	return map[string]any{
		"ts":         time.Now().UTC().Format(time.RFC3339Nano),
		"session_id": in.SessionID,
		"actor":      actor,
		"kind":       kind,
		"payload":    payload,
	}
}

func agentActor() map[string]any {
	model := os.Getenv("CLAUDE_CODE_MODEL")
	if model == "" {
		model = "claude-code"
	}
	return agentActorWithModel(model)
}

func agentActorWithModel(model string) map[string]any {
	if model == "" {
		model = "claude-code"
	}
	return map[string]any{"type": "agent", "id": "claude-code", "model": model}
}

func cursorDir() string {
	if d := os.Getenv("AGENT_LENS_CURSOR_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".agent-lens", "cursors")
}

func warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "agent-lens-hook: "+format+"\n", a...)
}
