package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const sampleTranscript = `{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1"}
{"type":"assistant","message":{"id":"msg_001","content":[{"type":"thinking","thinking":"reasoning A"},{"type":"text","text":"hello world"}],"model":"claude-opus-4-7"},"uuid":"u2"}
{"type":"assistant","message":{"id":"msg_002","content":[{"type":"tool_use","name":"Read","input":{}}],"model":"claude-opus-4-7"},"uuid":"u3"}
{"type":"assistant","message":{"id":"msg_003","content":[{"type":"thinking","thinking":"reasoning B"}],"model":"claude-opus-4-7"},"uuid":"u4"}
`

func writeTranscript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestReadExtractsThinkingAndText(t *testing.T) {
	path := writeTranscript(t, sampleTranscript)
	r := NewReader(t.TempDir())

	blocks, offset, err := r.Read(path, "s1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := len(blocks), 3; got != want {
		t.Fatalf("blocks = %d, want %d (thinking, text, thinking)", got, want)
	}
	if blocks[0].Kind != "thinking" || blocks[0].Content != "reasoning A" {
		t.Errorf("blocks[0] = %+v", blocks[0])
	}
	if blocks[1].Kind != "text" || blocks[1].Content != "hello world" {
		t.Errorf("blocks[1] = %+v", blocks[1])
	}
	if blocks[2].Kind != "thinking" || blocks[2].Content != "reasoning B" {
		t.Errorf("blocks[2] = %+v", blocks[2])
	}
	if offset != int64(len(sampleTranscript)) {
		t.Errorf("offset = %d, want %d", offset, len(sampleTranscript))
	}
}

func TestReadIncrementalAfterCommit(t *testing.T) {
	path := writeTranscript(t, sampleTranscript)
	r := NewReader(t.TempDir())

	_, offset, err := r.Read(path, "s1")
	if err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if err := r.Commit("s1", offset); err != nil {
		t.Fatalf("commit: %v", err)
	}

	blocks, _, err := r.Read(path, "s1")
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if len(blocks) != 0 {
		t.Errorf("second read returned %d blocks, want 0", len(blocks))
	}

	addition := `{"type":"assistant","message":{"id":"msg_004","content":[{"type":"text","text":"new"}]},"uuid":"u5"}` + "\n"
	if err := os.WriteFile(path, []byte(sampleTranscript+addition), 0o600); err != nil {
		t.Fatalf("append: %v", err)
	}

	blocks, _, err = r.Read(path, "s1")
	if err != nil {
		t.Fatalf("read 3: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Content != "new" {
		t.Errorf("incremental read = %+v, want one new text block", blocks)
	}
}

func TestReadIgnoresPartialTrailingLine(t *testing.T) {
	path := writeTranscript(t, sampleTranscript+`{"type":"assistant","message":{"id":"`)
	r := NewReader(t.TempDir())

	_, offset, err := r.Read(path, "s1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if offset != int64(len(sampleTranscript)) {
		t.Errorf("offset = %d, partial line should not advance offset; want %d", offset, len(sampleTranscript))
	}
}

func TestRedactedThinkingAttachedToTextBlock(t *testing.T) {
	// Claude Code's transcript writes `signature` but drops `thinking`
	// content (the field is empty). Verify we count those instead of
	// silently dropping them, and attach the count to the first text
	// block so the derived assistant_message DECISION carries it.
	line := `{"type":"assistant","message":{"id":"msg_x","content":[` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"thinking","thinking":"real reasoning"},` +
		`{"type":"text","text":"hello"}` +
		`],"model":"claude-opus-4-7"}}`
	got := parseLine([]byte(line))
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2 (one non-empty thinking + one text); got=%+v", len(got), got)
	}
	if got[0].Kind != "thinking" || got[0].Content != "real reasoning" || got[0].RedactedThinking != 0 {
		t.Errorf("non-empty thinking should not carry redaction count: %+v", got[0])
	}
	if got[1].Kind != "text" || got[1].Content != "hello" || got[1].RedactedThinking != 2 {
		t.Errorf("text block should carry redaction count of 2: %+v", got[1])
	}
}

func TestRedactedThinkingOnlyEmitsStubTextBlock(t *testing.T) {
	// When a message has only redacted thinking and no text, we still
	// need to produce a DECISION event so the count surfaces. The
	// reader emits a stub text block (empty content) to drive that.
	line := `{"type":"assistant","message":{"id":"msg_y","content":[` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"thinking","thinking":""}` +
		`],"model":"claude-opus-4-7"}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1 stub text block; got=%+v", len(got), got)
	}
	if got[0].Kind != "text" || got[0].Content != "" || got[0].RedactedThinking != 2 {
		t.Errorf("stub text block should be empty with count=2: %+v", got[0])
	}
	if got[0].MessageID != "msg_y" {
		t.Errorf("stub should carry message id: %+v", got[0])
	}
}

func TestRedactedThinkingPlusRealThinkingNoText(t *testing.T) {
	// Message has both real thinking AND redacted thinking but no text
	// block. The non-empty thinking block must still emit a THOUGHT event
	// without picking up the redaction count, AND a stub text block must
	// fire with the count so the assistant_message DECISION carries it.
	// Verifies we attach the count to the stub even when other (non-text)
	// blocks were emitted before the loop reached its end.
	line := `{"type":"assistant","message":{"id":"msg_w","content":[` +
		`{"type":"thinking","thinking":"real reasoning"},` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"thinking","thinking":""}` +
		`],"model":"claude-opus-4-7"}}`
	got := parseLine([]byte(line))
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2 (real thinking + stub text); got=%+v", len(got), got)
	}
	if got[0].Kind != "thinking" || got[0].Content != "real reasoning" || got[0].RedactedThinking != 0 {
		t.Errorf("real thinking block must not carry redaction count: %+v", got[0])
	}
	if got[1].Kind != "text" || got[1].Content != "" || got[1].RedactedThinking != 2 {
		t.Errorf("stub text block should be empty with count=2: %+v", got[1])
	}
	if got[1].MessageID != "msg_w" {
		t.Errorf("stub should carry message id: %+v", got[1])
	}
}

func TestNoRedactionWhenAllThinkingNonEmpty(t *testing.T) {
	// Sanity: when no thinking blocks are empty, the text block must
	// not pick up a stale RedactedThinking value (default zero).
	line := `{"type":"assistant","message":{"id":"msg_z","content":[` +
		`{"type":"thinking","thinking":"real"},` +
		`{"type":"text","text":"hi"}` +
		`],"model":"claude-opus-4-7"}}`
	got := parseLine([]byte(line))
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2; got=%+v", len(got), got)
	}
	for _, b := range got {
		if b.RedactedThinking != 0 {
			t.Errorf("expected RedactedThinking=0, got %+v", b)
		}
	}
}

func TestParseLineSkipsUnknownAndMalformed(t *testing.T) {
	cases := []string{
		``,
		`not json`,
		`{"type":"system","data":"x"}`,
		`{"type":"user","message":{"content":"plain"}}`,
		`{"type":"assistant","message":{"id":"x","content":"a string instead of array"}}`,
		`{"type":"assistant","message":{"id":"x","content":[{"type":"tool_use"}]}}`,
	}
	for _, c := range cases {
		if got := parseLine([]byte(c)); len(got) != 0 {
			t.Errorf("parseLine(%q) = %+v, want empty", c, got)
		}
	}
}

func TestCursorPersists(t *testing.T) {
	dir := t.TempDir()
	r := NewReader(dir)
	if err := r.Commit("sess", 12345); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, err := r.readCursor("sess")
	if err != nil {
		t.Fatalf("readCursor: %v", err)
	}
	if got != 12345 {
		t.Errorf("offset = %d, want 12345", got)
	}

	// Whitespace tolerance.
	if err := os.WriteFile(filepath.Join(dir, "sess2.offset"), []byte(" 42\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err = r.readCursor("sess2")
	if err != nil {
		t.Fatalf("readCursor2: %v", err)
	}
	if got != 42 {
		t.Errorf("whitespace cursor = %d, want 42", got)
	}
}

// --- ADR 0002: token usage emission ---

func TestUsageMappingCoversAllD2Fields(t *testing.T) {
	// Anthropic shape with every field populated; verify each ADR 0002
	// D2 mapping lands on the right TokenUsage field. raw must round-trip
	// the original JSON so forensic re-parse stays possible.
	usage := `{
		"input_tokens": 11,
		"output_tokens": 22,
		"cache_creation_input_tokens": 33,
		"cache_read_input_tokens": 44,
		"service_tier": "priority",
		"cache_creation": {
			"ephemeral_5m_input_tokens": 55,
			"ephemeral_1h_input_tokens": 66
		},
		"server_tool_use": {
			"web_search_requests": 7,
			"web_fetch_requests": 8
		}
	}`
	line := `{"type":"assistant","message":{"id":"msg_a","content":[` +
		`{"type":"text","text":"hello"}` +
		`],"model":"claude-opus-4-7","stop_reason":"end_turn","usage":` + usage + `}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1", len(got))
	}
	u := got[0].Usage
	if u == nil {
		t.Fatalf("expected non-nil Usage, got nil; block=%+v", got[0])
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Vendor", u.Vendor, "anthropic"},
		{"Model", u.Model, "claude-opus-4-7"},
		{"ServiceTier", u.ServiceTier, "priority"},
		{"InputTokens", u.InputTokens, 11},
		{"OutputTokens", u.OutputTokens, 22},
		{"CacheReadTokens", u.CacheReadTokens, 44},
		{"CacheWrite5mTokens", u.CacheWrite5mTokens, 55},
		{"CacheWrite1hTokens", u.CacheWrite1hTokens, 66},
		{"WebSearchCalls", u.WebSearchCalls, 7},
		{"WebFetchCalls", u.WebFetchCalls, 8},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if got[0].StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", got[0].StopReason, "end_turn")
	}
	// Raw must be valid JSON re-decodable to the same numbers.
	var roundTrip rawUsage
	if err := json.Unmarshal(u.Raw, &roundTrip); err != nil {
		t.Fatalf("Raw is not valid JSON: %v", err)
	}
	if roundTrip.InputTokens != 11 || roundTrip.OutputTokens != 22 {
		t.Errorf("Raw round-trip lost data: %+v", roundTrip)
	}
}

func TestSyntheticMessageIsMetadataOnly(t *testing.T) {
	// `<synthetic>` is Claude Code's own stop-sequence placeholder;
	// usage is meaningless. ADR 0002 D3 says skip + INFO log. The
	// derived blocks themselves still pass through.
	line := `{"type":"assistant","message":{"id":"msg_s","content":[` +
		`{"type":"text","text":"placeholder"}` +
		`],"model":"<synthetic>","stop_reason":"stop_sequence","usage":{` +
		`"input_tokens":0,"output_tokens":0` +
		`}}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1; got=%+v", len(got), got)
	}
	if got[0].Usage != nil {
		t.Errorf("expected nil Usage for <synthetic>; got %+v", got[0].Usage)
	}
	// stop_reason still attaches — it's audit-relevant even when usage
	// is meaningless (tells us this was a stop_sequence boundary).
	if got[0].StopReason != "stop_sequence" {
		t.Errorf("StopReason = %q, want stop_sequence", got[0].StopReason)
	}
}

func TestAllZeroUsageIsMetadataOnly(t *testing.T) {
	// Even with a real model, a usage block where every counter is 0
	// is treated as metadata-only per ADR 0002 D3 (third branch).
	line := `{"type":"assistant","message":{"id":"msg_z","content":[` +
		`{"type":"text","text":"x"}` +
		`],"model":"claude-opus-4-7","usage":{` +
		`"input_tokens":0,"output_tokens":0,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0` +
		`}}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1", len(got))
	}
	if got[0].Usage != nil {
		t.Errorf("all-zero usage should be metadata-only: %+v", got[0].Usage)
	}
}

func TestUsageAttachedToTextWhenBothExist(t *testing.T) {
	// Mixed message: thinking + text, both real. Usage and stop_reason
	// must land on the text block (the assistant_message DECISION),
	// not on the thinking block — DECISION is the canonical "this
	// message" event when text exists.
	line := `{"type":"assistant","message":{"id":"msg_mix","content":[` +
		`{"type":"thinking","thinking":"plan"},` +
		`{"type":"text","text":"answer"}` +
		`],"model":"claude-opus-4-7","stop_reason":"end_turn","usage":{` +
		`"input_tokens":1,"output_tokens":2` +
		`}}}`
	got := parseLine([]byte(line))
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2", len(got))
	}
	if got[0].Kind != "thinking" || got[0].Usage != nil || got[0].StopReason != "" {
		t.Errorf("thinking block must not carry usage/stop_reason: %+v", got[0])
	}
	if got[1].Kind != "text" || got[1].Usage == nil || got[1].StopReason != "end_turn" {
		t.Errorf("text block must carry usage + stop_reason: %+v", got[1])
	}
}

func TestUsageAttachedToThinkingWhenNoText(t *testing.T) {
	// Thinking-only message with real thinking content (no text, no
	// redaction) — usage rides on the THOUGHT event rather than
	// synthesizing a stub. (Stubs are reserved for the redacted-only
	// case, where we need the assistant_message marker for UI.)
	line := `{"type":"assistant","message":{"id":"msg_th","content":[` +
		`{"type":"thinking","thinking":"only thinking"}` +
		`],"model":"claude-opus-4-7","stop_reason":"end_turn","usage":{` +
		`"input_tokens":3,"output_tokens":4` +
		`}}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1", len(got))
	}
	if got[0].Kind != "thinking" {
		t.Errorf("expected thinking carrier, got %+v", got[0])
	}
	if got[0].Usage == nil || got[0].Usage.OutputTokens != 4 {
		t.Errorf("thinking block must carry usage: %+v", got[0].Usage)
	}
	if got[0].StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", got[0].StopReason)
	}
}

func TestUsageOnRedactedOnlyMessage(t *testing.T) {
	// Redacted-thinking-only message still produces a stub text block
	// for the assistant_message DECISION; usage rides that same stub
	// (avoids creating two stubs for the same message).
	line := `{"type":"assistant","message":{"id":"msg_r","content":[` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"thinking","thinking":""}` +
		`],"model":"claude-opus-4-7","stop_reason":"end_turn","usage":{` +
		`"input_tokens":5,"output_tokens":6` +
		`}}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1 stub; got=%+v", len(got), got)
	}
	if got[0].Kind != "text" || got[0].Content != "" {
		t.Errorf("expected stub text block: %+v", got[0])
	}
	if got[0].RedactedThinking != 2 {
		t.Errorf("RedactedThinking = %d, want 2", got[0].RedactedThinking)
	}
	if got[0].Usage == nil || got[0].Usage.InputTokens != 5 {
		t.Errorf("stub must carry usage: %+v", got[0].Usage)
	}
}

func TestUsageOnToolOnlyMessageCreatesStub(t *testing.T) {
	// Tool-use-only message with real usage. Without a content block
	// of kind "thinking" or "text" there's no natural carrier — the
	// reader must synthesize a stub text block so the usage isn't
	// dropped. This is the only branch where stub creation happens
	// purely on usage's behalf (redacted_thinking is what triggers
	// stubs in the other tests).
	line := `{"type":"assistant","message":{"id":"msg_t","content":[` +
		`{"type":"tool_use","id":"tu_1","name":"Read","input":{}}` +
		`],"model":"claude-opus-4-7","stop_reason":"tool_use","usage":{` +
		`"input_tokens":7,"output_tokens":8` +
		`}}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1 stub text block; got=%+v", len(got), got)
	}
	if got[0].Kind != "text" || got[0].Content != "" {
		t.Errorf("expected stub text block: %+v", got[0])
	}
	if got[0].MessageID != "msg_t" {
		t.Errorf("stub must carry message id: %+v", got[0])
	}
	if got[0].Usage == nil || got[0].Usage.OutputTokens != 8 {
		t.Errorf("stub must carry usage with output=8: %+v", got[0].Usage)
	}
	if got[0].StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", got[0].StopReason)
	}
	if got[0].RedactedThinking != 0 {
		t.Errorf("no redacted thinking in this case: %+v", got[0])
	}
}

func TestMissingUsageDoesNotCrash(t *testing.T) {
	// Real message, no usage field at all (early Claude Code versions
	// observed without usage on the very first turn). Should pass
	// through with nil Usage and not log spuriously.
	line := `{"type":"assistant","message":{"id":"msg_n","content":[` +
		`{"type":"text","text":"hi"}` +
		`],"model":"claude-opus-4-7"}}`
	got := parseLine([]byte(line))
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1", len(got))
	}
	if got[0].Usage != nil {
		t.Errorf("expected nil Usage, got %+v", got[0].Usage)
	}
}

func TestReadMissingTranscript(t *testing.T) {
	r := NewReader(t.TempDir())
	if _, _, err := r.Read("/nonexistent/path/transcript.jsonl", "s1"); err == nil {
		t.Error("expected error for missing transcript, got nil")
	}
}
