package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

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
// events can be replayed later via `agent-lens replay`.
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
		return appendToSink(sessionID, body)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return appendToSink(sessionID, body)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return appendToSink(sessionID, body)
	}
	return nil
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
