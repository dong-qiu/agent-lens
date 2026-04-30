package transcript

import (
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

func TestReadMissingTranscript(t *testing.T) {
	r := NewReader(t.TempDir())
	if _, _, err := r.Read("/nonexistent/path/transcript.jsonl", "s1"); err == nil {
		t.Error("expected error for missing transcript, got nil")
	}
}
