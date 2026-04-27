package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const verifyUsage = `agent-lens-hook verify — walk a session's hash chain and verify
that every event's prev_hash matches the previous event's hash.

  agent-lens-hook verify --session <id> [--url <url>] [--token <token>]

This v1 verifier checks chain *linkage* only: events are returned by
the server in append order, and each event's prev_hash must equal its
predecessor's hash. It does NOT yet re-derive each event's hash from
its content; an attacker who can write to the store directly could
produce a self-consistent chain of fabricated events. Content
re-derivation is a follow-up (server-side, since the canonical-JSON
encoding lives in the ingest package).

Exit codes: 0 on success, 1 on chain break, 2 on usage / network /
server errors.
`

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		sessionID = fs.String("session", "", "session id to verify (required)")
		urlFlag   = fs.String("url", "", "Agent Lens server URL (defaults to AGENT_LENS_URL or http://localhost:8787)")
		tokenFlag = fs.String("token", "", "bearer token (defaults to AGENT_LENS_TOKEN)")
		limit     = fs.Int("limit", 0, "maximum events to fetch (0 = all)")
		timeout   = fs.Duration("timeout", 30*time.Second, "HTTP request timeout")
		quiet     = fs.Bool("quiet", false, "only print FAIL or summary, not per-event progress")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, verifyUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *sessionID == "" {
		fs.Usage()
		os.Exit(2)
	}

	url := chooseURL(*urlFlag)
	token := chooseToken(*tokenFlag)

	events, err := fetchSession(url, token, *sessionID, *limit, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch: %v\n", err)
		os.Exit(2)
	}
	if len(events) == 0 {
		fmt.Printf("session %q: no events\n", *sessionID)
		os.Exit(0)
	}

	if !*quiet {
		fmt.Printf("session %q: walking %d events\n", *sessionID, len(events))
	}

	for i, e := range events {
		var prev string
		if i > 0 {
			prev = events[i-1].Hash
		}
		gotPrev := ""
		if e.PrevHash != nil {
			gotPrev = *e.PrevHash
		}
		if gotPrev != prev {
			fmt.Fprintf(os.Stderr, "FAIL at index %d (id=%s): prev_hash=%q, expected %q\n",
				i, e.ID, gotPrev, prev)
			os.Exit(1)
		}
		if !*quiet {
			fmt.Printf("  [%d] %s · %s · %s\n", i, e.Kind, e.Hash[:12], e.ID)
		}
	}

	head := events[len(events)-1].Hash
	fmt.Printf("OK · %d events · head %s\n", len(events), head[:16])
	os.Exit(0)
}

type verifyEvent struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Hash     string  `json:"hash"`
	PrevHash *string `json:"prevHash"`
}

const verifyQuery = `query Verify($sessionId: String!, $limit: Int) {
  events(sessionId: $sessionId, limit: $limit) {
    id
    kind
    hash
    prevHash
  }
}`

// fetchSession queries the server for sessionID's event chain. Server
// is contracted to return events in append order (ListBySession's
// `ORDER BY ts ASC, id ASC`); the chain walk in runVerify implicitly
// validates that ordering — events out of order would surface as a
// prev_hash mismatch — so a separate sort here would only mask
// genuine integrity problems.
func fetchSession(url, token, sessionID string, limit int, timeout time.Duration) ([]verifyEvent, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}

	body, err := json.Marshal(map[string]any{
		"query": verifyQuery,
		"variables": map[string]any{
			"sessionId": sessionID,
			"limit":     limit,
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url+"/v1/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Data struct {
			Events []verifyEvent `json:"events"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", out.Errors[0].Message)
	}
	return out.Data.Events, nil
}

func chooseURL(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("AGENT_LENS_URL"); v != "" {
		return v
	}
	return "http://localhost:8787"
}

func chooseToken(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("AGENT_LENS_TOKEN")
}
