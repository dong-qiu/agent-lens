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
