package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dongqiu/agent-lens/internal/attest"
)

const exportUsage = `agent-lens-hook export — export an in-toto / SLSA attestation
for a stage boundary.

Usage:
  agent-lens-hook export <kind> [flags]

Kinds:
  code-provenance   agent-lens.dev/code-provenance/v1 (commit boundary)
  slsa-build        slsa.dev/provenance/v1 (build boundary; M3-B-3)
  deploy-evidence   agent-lens.dev/deploy-evidence/v1 (deploy boundary; M3-B-4)
`

func runExport(args []string) {
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, exportUsage)
		if len(args) < 1 {
			os.Exit(2)
		}
		return
	}
	switch args[0] {
	case "code-provenance":
		if err := exportCodeProvenance(args[1:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "agent-lens-hook export code-provenance: %v\n", err)
			os.Exit(1)
		}
	case "slsa-build":
		fmt.Fprintln(os.Stderr, "TODO: slsa-build (M3-B-3)")
		os.Exit(1)
	case "deploy-evidence":
		fmt.Fprintln(os.Stderr, "TODO: deploy-evidence (M3-B-4)")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown export kind: %s\n\n%s", args[0], exportUsage)
		os.Exit(2)
	}
}

const codeProvenanceUsage = `agent-lens-hook export code-provenance — sign an
in-toto Statement asserting which Claude Code session events
contributed to a git commit.

Usage:
  agent-lens-hook export code-provenance \
    --commit <sha> --session <claude-session-id> \
    [--key <path>] [--out <file>] [--url <url>] [--token <token>]

  --commit   git commit SHA-1 (required); becomes the in-toto subject
             with digest type "gitCommit"
  --session  Claude Code session id (required); enumerates the
             prompt / thought / tool_call events that produced the
             commit. v0 is manual; auto-correlation between commits
             and Claude sessions is a follow-up
  --key      ed25519 private key path
             (default $HOME/.agent-lens/keys/ed25519)
  --out      output file (default stdout)
  --url      Agent Lens server URL
             (default $AGENT_LENS_URL or http://localhost:8787)
  --token    bearer token (default $AGENT_LENS_TOKEN)
  --timeout  HTTP timeout for the GraphQL fetch (default 30s)

Output is one JSON-encoded DSSE envelope, suitable for appending to
a .intoto.jsonl file. The signed payload is an in-toto v1 Statement
whose predicate carries digests + 200-char previews of the prompt /
thinking / tool_call content — never the full text. Auditors who
want the raw bytes follow predicate.store_url back to agent-lens.
`

func exportCodeProvenance(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export code-provenance", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		commit    = fs.String("commit", "", "git commit SHA (required)")
		session   = fs.String("session", "", "Claude Code session id (required)")
		keyPath   = fs.String("key", "", "ed25519 private key path")
		outPath   = fs.String("out", "", "output file (default stdout)")
		urlFlag   = fs.String("url", "", "server URL")
		tokenFlag = fs.String("token", "", "bearer token")
		timeout   = fs.Duration("timeout", 30*time.Second, "HTTP timeout")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, codeProvenanceUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *commit == "" || *session == "" {
		fs.Usage()
		return fmt.Errorf("--commit and --session are both required")
	}

	kp := *keyPath
	if kp == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		kp = filepath.Join(home, ".agent-lens", "keys", "ed25519")
	}
	priv, err := attest.LoadPrivateKey(kp)
	if err != nil {
		return fmt.Errorf("load private key from %s: %w", kp, err)
	}

	url := chooseURL(*urlFlag)
	token := chooseToken(*tokenFlag)
	events, err := fetchProvenanceEvents(url, token, *session, *timeout)
	if err != nil {
		return fmt.Errorf("fetch session events: %w", err)
	}
	if len(events) == 0 {
		return fmt.Errorf("session %q has no events", *session)
	}

	pEvents := mapToProvenanceEvents(events)
	if len(pEvents) == 0 {
		return fmt.Errorf("session %q has no AI-side events (PROMPT/THOUGHT/TOOL_CALL/TOOL_RESULT/DECISION)", *session)
	}

	stmt, err := attest.BuildCodeProvenanceStatement(
		*commit,
		provenanceSessionFromEvents(*session, events),
		pEvents,
		url,
		events[0].ID,
	)
	if err != nil {
		return fmt.Errorf("build statement: %w", err)
	}

	stmtBytes, err := json.Marshal(stmt)
	if err != nil {
		return fmt.Errorf("marshal statement: %w", err)
	}
	env, err := attest.Sign(priv, attest.InTotoPayloadType, stmtBytes)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	envBytes = append(envBytes, '\n')

	if *outPath != "" {
		if err := os.WriteFile(*outPath, envBytes, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *outPath, err)
		}
	} else if _, err := out.Write(envBytes); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr,
		"code-provenance attestation written: %d events, %d bytes, key id %s\n",
		len(pEvents), len(envBytes), priv.KeyID,
	)
	return nil
}

// provenanceEvent is the shape we read off GraphQL. Unlike verify.go's
// minimal verifyEvent, we need ts / actor / payload to build the
// predicate.
type provenanceEvent struct {
	ID    string         `json:"id"`
	TS    string         `json:"ts"`
	Kind  string         `json:"kind"`
	Actor provenanceActor `json:"actor"`
	Payload map[string]any `json:"payload"`
}

type provenanceActor struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model"`
}

const provenanceQuery = `query Provenance($sessionId: String!) {
  events(sessionId: $sessionId, limit: 1000) {
    id
    ts
    kind
    actor { type id model }
    payload
  }
}`

func fetchProvenanceEvents(url, token, sessionID string, timeout time.Duration) ([]provenanceEvent, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	body, err := json.Marshal(map[string]any{
		"query": provenanceQuery,
		"variables": map[string]any{
			"sessionId": sessionID,
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
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
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
			Events []provenanceEvent `json:"events"`
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

// provenanceSessionFromEvents derives the session metadata. agent is
// hardcoded to claude-code today (only producer); model is taken from
// the first agent event that has it set.
func provenanceSessionFromEvents(sessionID string, events []provenanceEvent) attest.ProvenanceSession {
	s := attest.ProvenanceSession{ID: sessionID, Agent: "claude-code"}
	for _, e := range events {
		if e.Actor.Type == "AGENT" && e.Actor.Model != "" {
			s.Model = e.Actor.Model
			break
		}
	}
	return s
}

// mapToProvenanceEvents filters and projects events to the predicate's
// event row. Only AI-side kinds are included; downstream events (commit
// itself, pr, build, deploy) are NOT in the predicate — those are
// other attestations' subjects.
func mapToProvenanceEvents(events []provenanceEvent) []attest.ProvenanceEvent {
	out := make([]attest.ProvenanceEvent, 0, len(events))
	for _, e := range events {
		switch e.Kind {
		case "PROMPT", "THOUGHT", "TOOL_CALL", "TOOL_RESULT", "DECISION":
		default:
			continue
		}
		pe := attest.ProvenanceEvent{ID: e.ID, TS: e.TS, Kind: e.Kind}
		switch e.Kind {
		case "PROMPT", "THOUGHT":
			if text, _ := e.Payload["text"].(string); text != "" {
				pe.ContentDigest, pe.ContentPreview = attest.SummarizeText(text)
			}
		case "TOOL_CALL", "TOOL_RESULT":
			if name, _ := e.Payload["name"].(string); name != "" {
				pe.ToolName = name
			}
			// Hash the entire payload (input + response) so a tampered
			// tool log is detectable; preview omitted (often binary).
			if e.Payload != nil {
				if raw, err := json.Marshal(e.Payload); err == nil {
					pe.ContentDigest, _ = attest.SummarizeText(string(raw))
				}
			}
		case "DECISION":
			if marker, _ := e.Payload["marker"].(string); marker != "" {
				pe.Marker = marker
			}
			if text, _ := e.Payload["text"].(string); text != "" {
				pe.ContentDigest, pe.ContentPreview = attest.SummarizeText(text)
			}
		}
		out = append(out, pe)
	}
	return out
}
