// Package transcript reads Claude Code's per-session jsonl transcript
// incrementally and extracts thinking + text content blocks. The
// transcript schema is not a public stable contract, so this package
// is intentionally fail-soft: lines we cannot parse are skipped.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// reSystemReminder matches a harness-injected <system-reminder> block in a
// user message (ADR 0005 D3). Non-greedy + dotall so a multi-line reminder is
// captured whole and multiple reminders in one message are matched separately.
var reSystemReminder = regexp.MustCompile(`(?s)<system-reminder>(.*?)</system-reminder>`)

// TokenUsage is the vendor-neutral token-counting shape per ADR 0002 D2.
// Field names match the JSON keys the hook will emit on payload.usage so
// downstream readers (GraphQL, audit dashboards, attestation predicates)
// don't need a second mapping layer.
//
// Optional cache + server_tool fields use omitempty so a vendor that
// doesn't have a notion of cache writes (e.g. OpenAI) just omits them
// rather than reporting zeros that look like real-but-empty buckets.
type TokenUsage struct {
	Vendor             string `json:"vendor"`
	Model              string `json:"model"`
	ServiceTier        string `json:"service_tier,omitempty"`
	InputTokens        int    `json:"input_tokens"`
	OutputTokens       int    `json:"output_tokens"`
	CacheReadTokens    int    `json:"cache_read_tokens,omitempty"`
	CacheWrite5mTokens int    `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1hTokens int    `json:"cache_write_1h_tokens,omitempty"`
	WebSearchCalls     int    `json:"web_search_calls,omitempty"`
	WebFetchCalls      int    `json:"web_fetch_calls,omitempty"`
	// Raw is the verbatim vendor `usage` block, kept so we can re-derive
	// fields if our normalization missed a column or if Anthropic's
	// schema drifts. ADR 0002 D2 calls this "有意冗余" — a few hundred
	// bytes per event in exchange for forensic resilience.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Block is a single content block extracted from one assistant message.
//
// RedactedThinking carries a per-message count of `thinking` content
// blocks Claude Code wrote to the transcript with an empty `thinking`
// field (only the `signature` was preserved). The count is attached to
// the first `text` block of the same message so the derived
// `assistant_message` DECISION event can surface it; thinking blocks
// themselves do not carry the count to avoid mis-attributing the
// redaction signal to the THOUGHT event.
//
// Usage and StopReason are message-level metadata (ADR 0002 D1). They
// attach to a single carrier block per message to avoid double-counting
// in turn / session aggregation. Carrier preference: first text block,
// then first thinking block, else a synthesized stub text block.
type Block struct {
	Kind             string // "thinking", "text", or "system_reminder"
	Content          string
	MessageID        string
	Model            string
	RedactedThinking int
	Usage            *TokenUsage
	StopReason       string
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

// SeenReminders loads the set of system-reminder content hashes already emitted
// for sessionID, so a re-injected (static) reminder is emitted only once per
// session (ADR 0005 D3). Missing file → empty set. Sidecar to the cursor,
// persisted via AddSeenReminders only after a successful send.
func (r *Reader) SeenReminders(sessionID string) (map[string]bool, error) {
	path := filepath.Join(r.cursorDir, sessionID+".reminders")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if h := strings.TrimSpace(line); h != "" {
			set[h] = true
		}
	}
	return set, nil
}

// AddSeenReminders appends newly-emitted reminder hashes to the session's
// sidecar (append-only, one hex hash per line). No-op for an empty slice.
func (r *Reader) AddSeenReminders(sessionID string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := os.MkdirAll(r.cursorDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(r.cursorDir, sessionID+".reminders")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(hashes, "\n") + "\n")
	return err
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
	ID         string          `json:"id"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Usage      json.RawMessage `json:"usage"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

// rawUsage mirrors the Anthropic `message.usage` shape we read from the
// transcript. Fields kept private; the public projection is TokenUsage.
type rawUsage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	ServiceTier              string `json:"service_tier"`
	CacheCreation            struct {
		Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse struct {
		WebSearchRequests int `json:"web_search_requests"`
		WebFetchRequests  int `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

// allZero reports whether every numeric / counter field is 0. Used to
// detect "metadata-only" messages per ADR 0002 D3 (the second branch:
// `usage` 里所有数值字段都为零).
func (u rawUsage) allZero() bool {
	return u.InputTokens == 0 &&
		u.OutputTokens == 0 &&
		u.CacheCreationInputTokens == 0 &&
		u.CacheReadInputTokens == 0 &&
		u.CacheCreation.Ephemeral5mInputTokens == 0 &&
		u.CacheCreation.Ephemeral1hInputTokens == 0 &&
		u.ServerToolUse.WebSearchRequests == 0 &&
		u.ServerToolUse.WebFetchRequests == 0
}

// extractUsage applies ADR 0002 D2 mapping. Returns nil for metadata-only
// messages per D3: `<synthetic>` model marker, missing/null usage block,
// or all-zero numeric fields. INFO-logs each metadata-only detection so
// future Claude-Code versions that start populating these messages with
// real numbers don't get silently skipped (D3 hedge rationale).
func extractUsage(model string, usageRaw json.RawMessage) *TokenUsage {
	if model == "<synthetic>" {
		fmt.Fprintf(os.Stderr, "transcript INFO: metadata-only message (model=<synthetic>); skipping usage\n")
		return nil
	}
	if len(usageRaw) == 0 || string(usageRaw) == "null" {
		// Missing / null usage is silenced. ADR 0002 D3's INFO-log
		// hedge targets cases where we *have* data and skip it (so a
		// future Claude Code version that fills these in with real
		// numbers doesn't get silently ignored). With no data present
		// there's nothing to silently skip; logging here would only
		// add noise without buying any forward-detection signal.
		return nil
	}
	var u rawUsage
	if err := json.Unmarshal(usageRaw, &u); err != nil {
		fmt.Fprintf(os.Stderr, "transcript INFO: usage decode failed (%v); skipping\n", err)
		return nil
	}
	if u.allZero() {
		fmt.Fprintf(os.Stderr, "transcript INFO: metadata-only message (all-zero usage, model=%q); skipping\n", model)
		return nil
	}
	return &TokenUsage{
		Vendor:             "anthropic",
		Model:              model,
		ServiceTier:        u.ServiceTier,
		InputTokens:        u.InputTokens,
		OutputTokens:       u.OutputTokens,
		CacheReadTokens:    u.CacheReadInputTokens,
		CacheWrite5mTokens: u.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1hTokens: u.CacheCreation.Ephemeral1hInputTokens,
		WebSearchCalls:     u.ServerToolUse.WebSearchRequests,
		WebFetchCalls:      u.ServerToolUse.WebFetchRequests,
		Raw:                usageRaw,
	}
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
	if len(e.Message) == 0 {
		return nil
	}
	// User messages carry harness-injected <system-reminder> blocks (ADR 0005
	// D3). They're the only thing we extract from the user side; everything
	// else below is assistant content.
	if e.Type == "user" {
		return parseUserReminders(e.Message)
	}
	if e.Type != "assistant" {
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
	// Per-message metadata (ADR 0002 D1: usage and stop_reason are
	// properties of the assistant message, not of any one derived
	// event). Attach to a single carrier block to avoid double-counting
	// in turn / session aggregation.
	usage := extractUsage(msg.Model, msg.Usage)
	stopReason := msg.StopReason

	needsCarrier := redactedThinking > 0 || usage != nil || stopReason != ""
	if !needsCarrier {
		return out
	}

	// redacted_thinking is a property of the *assistant_message*
	// marker — attaching it to a THOUGHT event would mis-categorize
	// the audit signal — so it strictly wants a text-block carrier
	// (synthesizing a stub if needed).
	if redactedThinking > 0 {
		textIdx := -1
		for i := range out {
			if out[i].Kind == "text" {
				textIdx = i
				break
			}
		}
		if textIdx < 0 {
			out = append(out, Block{
				Kind:      "text",
				MessageID: msg.ID,
				Model:     msg.Model,
			})
			textIdx = len(out) - 1
		}
		out[textIdx].RedactedThinking = redactedThinking
	}

	// usage / stop_reason are message-level metadata that read fine on
	// either DECISION or THOUGHT, so they can ride a thinking block
	// when no text exists rather than forcing a stub. Falls through to
	// a stub only when the message has no derived events at all (e.g.
	// tool-use only) — we still want to capture the usage in that case.
	if usage != nil || stopReason != "" {
		target := -1
		for i := range out {
			if out[i].Kind == "text" {
				target = i
				break
			}
		}
		if target < 0 {
			for i := range out {
				if out[i].Kind == "thinking" {
					target = i
					break
				}
			}
		}
		if target < 0 {
			out = append(out, Block{
				Kind:      "text",
				MessageID: msg.ID,
				Model:     msg.Model,
			})
			target = len(out) - 1
		}
		if usage != nil {
			out[target].Usage = usage
		}
		if stopReason != "" {
			out[target].StopReason = stopReason
		}
	}

	return out
}

// parseUserReminders extracts <system-reminder> blocks from a user message,
// one Block per reminder. Dedup (emit each distinct reminder once per session,
// ADR 0005 D3) is the caller's job — it needs cross-invocation state the reader
// doesn't hold. Fail-soft: unparseable content yields no blocks.
func parseUserReminders(message json.RawMessage) []Block {
	var msg struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return nil
	}
	text := userContentText(msg.Content)
	if text == "" {
		return nil
	}
	var out []Block
	for _, m := range reSystemReminder.FindAllStringSubmatch(text, -1) {
		rem := strings.TrimSpace(m[1])
		if rem == "" {
			continue
		}
		out = append(out, Block{Kind: "system_reminder", Content: rem, MessageID: msg.ID})
	}
	return out
}

// userContentText flattens a user message's content to text. Content is either
// a JSON string or an array of blocks; we concatenate the text blocks (where
// the harness injects reminders). tool_result blocks are intentionally skipped
// — reminders ride in text, and tool_result bodies are large and noisy.
func userContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	switch content[0] {
	case '"':
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			return ""
		}
		return s
	case '[':
		var blocks []contentBlock
		if err := json.Unmarshal(content, &blocks); err != nil {
			return ""
		}
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				b.WriteString(blk.Text)
				b.WriteString("\n")
			}
		}
		return b.String()
	}
	return ""
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
