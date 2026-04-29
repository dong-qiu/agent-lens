// Package transcript reads Claude Code's per-session jsonl transcript
// incrementally and extracts thinking + text content blocks. The
// transcript schema is not a public stable contract, so this package
// is intentionally fail-soft: lines we cannot parse are skipped.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Block is a single content block extracted from one assistant message.
//
// RedactedThinking carries a per-message count of `thinking` content
// blocks Claude Code wrote to the transcript with an empty `thinking`
// field (only the `signature` was preserved). The count is attached to
// the first `text` block of the same message so the derived
// `assistant_message` DECISION event can surface it; thinking blocks
// themselves do not carry the count to avoid mis-attributing the
// redaction signal to the THOUGHT event.
type Block struct {
	Kind             string // "thinking" or "text"
	Content          string
	MessageID        string
	Model            string
	RedactedThinking int
}

// Reader reads new content from a transcript file since the last
// committed cursor for a given session.
type Reader struct {
	cursorDir string
}

func NewReader(cursorDir string) *Reader {
	return &Reader{cursorDir: cursorDir}
}

// Read parses lines newly appended since the last cursor for sessionID.
// It returns the blocks and the offset to commit if forwarding succeeds.
// Partial trailing lines without a newline are not consumed; they will
// be revisited on the next Read.
func (r *Reader) Read(transcriptPath, sessionID string) ([]Block, int64, error) {
	if transcriptPath == "" {
		return nil, 0, errors.New("transcript path is empty")
	}
	offset, err := r.readCursor(sessionID)
	if err != nil {
		return nil, 0, err
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}

	br := bufio.NewReader(f)
	var blocks []Block
	pos := offset
	for {
		line, err := br.ReadBytes('\n')
		hasNewline := len(line) > 0 && line[len(line)-1] == '\n'
		if hasNewline {
			pos += int64(len(line))
			blocks = append(blocks, parseLine(line[:len(line)-1])...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return blocks, pos, err
		}
	}
	return blocks, pos, nil
}

// Commit persists the offset for sessionID. Call only after the blocks
// returned by Read have been delivered (or persisted to the local sink).
func (r *Reader) Commit(sessionID string, offset int64) error {
	if err := os.MkdirAll(r.cursorDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(r.cursorDir, sessionID+".offset")
	return os.WriteFile(path, []byte(strconv.FormatInt(offset, 10)), 0o600)
}

func (r *Reader) readCursor(sessionID string) (int64, error) {
	path := filepath.Join(r.cursorDir, sessionID+".offset")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

// transcriptEntry is the minimal envelope shape we recognize. Unknown
// fields are preserved (json.RawMessage) so schema drift only loses the
// fields we explicitly named — never the line.
type transcriptEntry struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

type assistantMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

func parseLine(line []byte) []Block {
	line = trimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var e transcriptEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil
	}
	if e.Type != "assistant" || len(e.Message) == 0 {
		return nil
	}
	var msg assistantMessage
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return nil
	}
	if len(msg.Content) == 0 || msg.Content[0] != '[' {
		// Content can also be a string for some entries; skip those —
		// they are not assistant content blocks we can dissect.
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var (
		out              []Block
		redactedThinking int
	)
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			if b.Thinking == "" {
				// Claude Code persists `signature` but drops the
				// thinking content; remember that the redaction
				// happened so audit reports can say "N blocks
				// redacted by Claude Code" instead of silently
				// looking like the model didn't think at all.
				redactedThinking++
				continue
			}
			out = append(out, Block{Kind: "thinking", Content: b.Thinking, MessageID: msg.ID, Model: msg.Model})
		case "text":
			if b.Text == "" {
				continue
			}
			out = append(out, Block{Kind: "text", Content: b.Text, MessageID: msg.ID, Model: msg.Model})
		}
	}
	if redactedThinking > 0 {
		// Attach the count to the first text block so the
		// `assistant_message` DECISION carries it. If the message had
		// only redacted thinking (no text), emit a stub text block
		// so the DECISION still fires; downstream UI shows just the
		// "redacted" pill in that case.
		attached := false
		for i := range out {
			if out[i].Kind == "text" {
				out[i].RedactedThinking = redactedThinking
				attached = true
				break
			}
		}
		if !attached {
			out = append(out, Block{
				Kind:             "text",
				MessageID:        msg.ID,
				Model:            msg.Model,
				RedactedThinking: redactedThinking,
			})
		}
	}
	return out
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
