package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dongqiu/agent-lens/internal/transcript"
)

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
		return []map[string]any{makeToolResult(in)}, nil
	case "SessionStart":
		return []map[string]any{makeSessionStart(in)}, nil
	case "Stop":
		return makeStopEvents(in)
	}
	return nil, nil
}

func makePrompt(in *claudeHookInput) map[string]any {
	return baseEvent(in, map[string]any{"type": "human", "id": "user"}, "prompt", map[string]any{
		"text": in.Prompt,
		"cwd":  in.CWD,
	})
}

func makeToolCall(in *claudeHookInput) map[string]any {
	return baseEvent(in, agentActor(), "tool_call", map[string]any{
		"name":  in.ToolName,
		"input": in.ToolInput,
	})
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
	return baseEvent(in, map[string]any{"type": "system", "id": "claude-code"}, "decision", map[string]any{
		"marker": "session_start",
		"cwd":    in.CWD,
	})
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

	events := make([]map[string]any, 0, len(blocks)+1)
	for _, b := range blocks {
		switch b.Kind {
		case "thinking":
			events = append(events, baseEvent(in, agentActorWithModel(b.Model), "thought", map[string]any{
				"text":       b.Content, // TODO: redact per SPEC §12 before forwarding
				"message_id": b.MessageID,
				"source":     "transcript",
			}))
		case "text":
			events = append(events, baseEvent(in, agentActorWithModel(b.Model), "decision", map[string]any{
				"marker":     "assistant_message",
				"text":       b.Content,
				"message_id": b.MessageID,
			}))
		}
	}
	events = append(events, turnEnd)

	commit := func() error { return r.Commit(in.SessionID, offset) }
	return events, commit
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
