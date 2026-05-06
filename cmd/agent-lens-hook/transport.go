package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fallbackWarned ensures we print the "fallback engaged" warning to stderr
// exactly once per hook process invocation. Each Claude Code hook event
// runs a fresh agent-lens-hook process, so "once per process" naturally
// translates to "once per hook event" — visible enough that operators
// notice when the server is down, quiet enough that we don't spam the
// hook log with N copies for an N-event batch.
var fallbackWarned sync.Once

const (
	defaultIngestURL = "http://localhost:8787"
	defaultTimeout   = 2 * time.Second
)

type transport struct {
	url    string
	token  string
	client *http.Client
}

func newTransport() *transport {
	url := os.Getenv("AGENT_LENS_URL")
	if url == "" {
		url = defaultIngestURL
	}
	return &transport{
		url:    url,
		token:  os.Getenv("AGENT_LENS_TOKEN"),
		client: &http.Client{Timeout: defaultTimeout},
	}
}

// Send POSTs the events as NDJSON. On any failure (network, non-2xx,
// invalid request), it falls back to appending the same NDJSON to a
// per-session file under $HOME/.agent-lens/sessions/<sid>.ndjson, so
// events can be replayed later via `agent-lens-hook replay`.
//
// On the first fallback in this process, we print a one-line warning to
// stderr — closes issue #71's silent-degradation gap. Without it, a
// stopped server is invisible to the user until they go looking.
func (t *transport) Send(events []map[string]any, sessionID string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	body := buf.Bytes()

	req, err := http.NewRequest(http.MethodPost, t.url+"/v1/events", bytes.NewReader(body))
	if err != nil {
		t.warnFallback(sessionID, len(events), fmt.Sprintf("request build: %v", err))
		return appendToSink(sessionID, body)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		t.warnFallback(sessionID, len(events), fmt.Sprintf("transport: %v", err))
		return appendToSink(sessionID, body)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.warnFallback(sessionID, len(events), fmt.Sprintf("HTTP %d", resp.StatusCode))
		return appendToSink(sessionID, body)
	}
	return nil
}

// warnFallback prints a single line to stderr the first time the fallback
// engages in this process. Format intentionally compact (one line) so it
// shows cleanly in Claude Code's hook log surface. The "run replay"
// suggestion gives users an actionable next step rather than just a
// warning that decays into noise.
func (t *transport) warnFallback(sessionID string, batchSize int, reason string) {
	fallbackWarned.Do(func() {
		sid := sessionID
		if sid == "" {
			sid = "unknown"
		}
		path := filepath.Join(homeDir(), ".agent-lens", "sessions", sid+".ndjson")
		fmt.Fprintf(os.Stderr,
			"agent-lens-hook: ingest at %s unavailable (%s); falling back to %s (this batch: %d events). Run `agent-lens-hook replay --remove-on-success` after the server is back up to flush the backlog (the flag prevents duplicates if you re-run; see issue #81).\n",
			t.url, reason, path, batchSize,
		)
	})
}

func appendToSink(sessionID string, body []byte) error {
	if sessionID == "" {
		sessionID = "unknown"
	}
	dir := filepath.Join(homeDir(), ".agent-lens", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sink mkdir: %w", err)
	}
	path := filepath.Join(dir, sessionID+".ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sink open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("sink write: %w", err)
	}
	return nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.TempDir()
}
