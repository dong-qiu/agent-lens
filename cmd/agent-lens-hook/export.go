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
	"sort"
	"strconv"
	"time"

	"github.com/dongqiu/agent-lens/internal/attest"
)

const exportUsage = `agent-lens-hook export — export an in-toto / SLSA attestation
for a stage boundary.

Usage:
  agent-lens-hook export <kind> [flags]

Kinds:
  code-provenance   agent-lens.dev/code-provenance/v1 (commit boundary)
  slsa-build        slsa.dev/provenance/v1 (build boundary)
  deploy-evidence   agent-lens.dev/deploy-evidence/v1 (deploy boundary)
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
		if err := exportSLSABuild(args[1:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "agent-lens-hook export slsa-build: %v\n", err)
			os.Exit(1)
		}
	case "deploy-evidence":
		if err := exportDeployEvidence(args[1:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "agent-lens-hook export deploy-evidence: %v\n", err)
			os.Exit(1)
		}
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
  --repo     repo identifier for the in-toto subject name; emitted as
             "git+<repo>". Default empty falls back to "git"
             (uninformative; consider passing the repo URL)
  --key      ed25519 private key path
             (default $HOME/.agent-lens/keys/ed25519)
  --out      output file (default stdout)
  --url      Agent Lens server URL
             (default $AGENT_LENS_URL or http://localhost:8787)
  --token    bearer token (default $AGENT_LENS_TOKEN)
  --limit    max events to fetch (default 5000); the command errors
             rather than truncates if the cap is hit, since a partial
             predicate would silently misrepresent the trace
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
		repoFlag  = fs.String("repo", "", "repo identifier for the in-toto subject name")
		keyPath   = fs.String("key", "", "ed25519 private key path")
		outPath   = fs.String("out", "", "output file (default stdout)")
		urlFlag   = fs.String("url", "", "server URL")
		tokenFlag = fs.String("token", "", "bearer token")
		limit     = fs.Int("limit", 5000, "max events to fetch from the session")
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
	events, err := fetchProvenanceEvents(url, token, *session, *limit, *timeout)
	if err != nil {
		return fmt.Errorf("fetch session events: %w", err)
	}
	if len(events) == 0 {
		return fmt.Errorf("session %q has no events", *session)
	}
	if *limit > 0 && len(events) >= *limit {
		// Hitting the cap means the session probably has more events
		// than we fetched. A truncated predicate would silently
		// misrepresent the trace, which defeats the audit goal —
		// error out and let the operator raise --limit.
		return fmt.Errorf("session %q hit the --limit cap (%d events); rerun with --limit larger to ensure a complete attestation", *session, *limit)
	}

	// Defense against future server contract drift: ListBySession
	// returns events ordered by ts ASC, but client-side sort guards
	// the metadata we derive (started_at = first event, ended_at =
	// last) and the predicate's events array against any drift.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].TS < events[j].TS
	})

	pEvents := mapToProvenanceEvents(events)
	if len(pEvents) == 0 {
		return fmt.Errorf("session %q has no AI-side events (PROMPT/THOUGHT/TOOL_CALL/TOOL_RESULT/DECISION)", *session)
	}

	subjectName := "git"
	if *repoFlag != "" {
		subjectName = "git+" + *repoFlag
	}

	stmt, err := attest.BuildCodeProvenanceStatement(
		*commit,
		subjectName,
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

const provenanceQuery = `query Provenance($sessionId: String!, $limit: Int) {
  events(sessionId: $sessionId, limit: $limit) {
    id
    ts
    kind
    actor { type id model }
    payload
  }
}`

func fetchProvenanceEvents(url, token, sessionID string, limit int, timeout time.Duration) ([]provenanceEvent, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	body, err := json.Marshal(map[string]any{
		"query": provenanceQuery,
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
	// UseNumber so payload numbers (e.g. workflow_run.id) decode as
	// json.Number instead of float64. GitHub run ids are well under
	// 2^53 today so the float path also works in practice, but
	// json.Number is the forward-compatible idiom.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
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

const slsaBuildUsage = `agent-lens-hook export slsa-build — sign a SLSA Build
Track v1 provenance for a CI run.

Usage:
  agent-lens-hook export slsa-build \
    --session <github-build-session-id> \
    [--repo <url>] [--key <path>] [--out <file>] [--url <url>] [--token <token>]

  --session  Build session id (required), typically
             github-build:<owner>/<repo>/<run_id>. The session must
             contain a composite-action build event (kind=BUILD with
             payload.source="composite-action" and payload.artifacts);
             that's where the artifact sha256s come from. SLSA spec
             requires ≥1 subject so a session with only workflow_run
             webhook events errors out.
  --repo     repo URL (e.g. https://github.com/acme/widget); recorded
             as the source resolvedDependency URI. Without --repo the
             dep is digest-only.
  --builder-id   override builder.id in runDetails (default GitHub-hosted
             runner URI). Self-hosted runners or GHES installations
             should pass their own URI here.
  --key      ed25519 private key path
             (default $HOME/.agent-lens/keys/ed25519)
  --out      output file (default stdout)
  --url      Agent Lens server URL
             (default $AGENT_LENS_URL or http://localhost:8787)
  --token    bearer token (default $AGENT_LENS_TOKEN)
  --limit    max events to fetch (default 5000)
  --timeout  HTTP timeout (default 30s)

Output is one DSSE-wrapped in-toto Statement with predicateType
"https://slsa.dev/provenance/v1", suitable for cosign / slsa-verifier.
`

func exportSLSABuild(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export slsa-build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		session     = fs.String("session", "", "github-build session id (required)")
		repoFlag    = fs.String("repo", "", "repo URL recorded as the source resolvedDependency URI")
		builderID   = fs.String("builder-id", attest.SLSABuilderID, "override builder.id (e.g. self-hosted runner URI)")
		keyPath     = fs.String("key", "", "ed25519 private key path")
		outPath     = fs.String("out", "", "output file (default stdout)")
		urlFlag     = fs.String("url", "", "server URL")
		tokenFlag   = fs.String("token", "", "bearer token")
		limit       = fs.Int("limit", 5000, "max events to fetch")
		timeout     = fs.Duration("timeout", 30*time.Second, "HTTP timeout")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, slsaBuildUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *session == "" {
		fs.Usage()
		return fmt.Errorf("--session is required")
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
	events, err := fetchProvenanceEvents(url, token, *session, *limit, *timeout)
	if err != nil {
		return fmt.Errorf("fetch session events: %w", err)
	}
	if len(events) == 0 {
		return fmt.Errorf("session %q has no events", *session)
	}
	if *limit > 0 && len(events) >= *limit {
		return fmt.Errorf("session %q hit the --limit cap (%d events); rerun with --limit larger", *session, *limit)
	}

	in, err := buildSLSAInputsFromEvents(events, *repoFlag)
	if err != nil {
		return err
	}
	in.BuilderID = *builderID

	stmt, err := attest.BuildSLSAProvenanceStatement(in)
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
		"slsa-build attestation written: %d subjects, %d bytes, key id %s\n",
		len(in.Subjects), len(envBytes), priv.KeyID,
	)
	return nil
}

// buildSLSAInputsFromEvents walks BUILD events in the session and
// pulls out (a) the artifact subjects from the composite-action event
// and (b) builder/metadata fields preferring composite-action values
// over workflow_run, falling back to whatever the workflow_run webhook
// recorded. Errors when no composite-action artifacts are found —
// SLSA spec needs ≥1 subject.
func buildSLSAInputsFromEvents(events []provenanceEvent, repo string) (attest.SLSABuildInputs, error) {
	var in attest.SLSABuildInputs
	in.Repo = repo

	for _, e := range events {
		if e.Kind != "BUILD" || e.Payload == nil {
			continue
		}
		if src, _ := e.Payload["source"].(string); src == "composite-action" {
			// Composite-action: flat fields, has artifacts
			if arts, ok := e.Payload["artifacts"].([]any); ok {
				for _, a := range arts {
					am, _ := a.(map[string]any)
					path, _ := am["path"].(string)
					sha, _ := am["sha256"].(string)
					if path != "" && sha != "" {
						in.Subjects = append(in.Subjects, attest.Subject{
							Name:   path,
							Digest: map[string]string{"sha256": sha},
						})
					}
				}
			}
			if v, _ := e.Payload["workflow"].(string); v != "" {
				in.WorkflowName = v
			}
			if v, _ := e.Payload["run_id"].(string); v != "" {
				in.RunID = v
			}
			if v, _ := e.Payload["run_number"].(string); v != "" {
				in.RunNumber = v
			}
			if v, _ := e.Payload["run_attempt"].(string); v != "" {
				in.RunAttempt = v
			}
			if v, _ := e.Payload["ref"].(string); v != "" {
				in.Ref = v
			}
			if v, _ := e.Payload["sha"].(string); v != "" {
				in.CommitSHA = v
			}
			if v, _ := e.Payload["status"].(string); v != "" {
				in.Conclusion = v
			}
			continue
		}
		// Otherwise treat as workflow_run webhook payload (nested
		// `workflow_run` object). Fields here only fill in what the
		// composite-action event left blank.
		wr, _ := e.Payload["workflow_run"].(map[string]any)
		if wr == nil {
			continue
		}
		if in.WorkflowName == "" {
			in.WorkflowName, _ = wr["name"].(string)
		}
		if in.RunID == "" {
			// Accept whichever JSON-decoder type carries the number.
			// UseNumber gives json.Number; default decoder gives
			// float64; a webhook payload could even hand us the run
			// id pre-stringified.
			switch v := wr["id"].(type) {
			case json.Number:
				in.RunID = string(v)
			case float64:
				in.RunID = strconv.FormatInt(int64(v), 10)
			case string:
				in.RunID = v
			}
		}
		if in.CommitSHA == "" {
			in.CommitSHA, _ = wr["head_sha"].(string)
		}
		if in.Ref == "" {
			if v, ok := wr["head_branch"].(string); ok && v != "" {
				in.Ref = "refs/heads/" + v
			}
		}
		if in.StartedOn == "" {
			in.StartedOn, _ = wr["run_started_at"].(string)
		}
		if in.FinishedOn == "" {
			in.FinishedOn, _ = wr["updated_at"].(string)
		}
		if in.Conclusion == "" {
			in.Conclusion, _ = wr["conclusion"].(string)
		}
	}

	if len(in.Subjects) == 0 {
		return in, fmt.Errorf("session has no composite-action build event with artifacts; SLSA build provenance needs ≥1 subject. Run the agent-lens/actions/build action in your workflow to record artifact hashes")
	}
	return in, nil
}

const deployEvidenceUsage = `agent-lens-hook export deploy-evidence — sign an
agent-lens.dev/deploy-evidence/v1 attestation for a deploy event.

Usage:
  agent-lens-hook export deploy-evidence \
    --event <deploy-event-id> \
    [--build-attestation <file>] [--code-attestation <file>] \
    [--key <path>] [--out <file>] [--url <url>] [--token <token>]

  --event              deploy event id (required); fetched via GraphQL.
                       Must be kind=DEPLOY.
  --build-attestation  upstream build attestation file (.intoto.jsonl);
                       its sha256 is recorded in predicate.upstream so
                       a verifier can walk deploy → build.
  --code-attestation   upstream code-provenance attestation file; its
                       sha256 is recorded similarly.
  --key                ed25519 private key path
                       (default $HOME/.agent-lens/keys/ed25519)
  --out                output file (default stdout)
  --url                Agent Lens server URL
                       (default $AGENT_LENS_URL or http://localhost:8787)
  --token              bearer token (default $AGENT_LENS_TOKEN)
  --timeout            HTTP timeout (default 30s)

The attestation's subject is the container image (sha256 of its
image_digest); the predicate carries environment / cluster / deploy
metadata plus optional upstream attestation hashes for graph traversal.
`

func exportDeployEvidence(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export deploy-evidence", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		eventID    = fs.String("event", "", "deploy event id (required)")
		buildAtt   = fs.String("build-attestation", "", "upstream build attestation file")
		codeAtt    = fs.String("code-attestation", "", "upstream code-provenance attestation file")
		keyPath    = fs.String("key", "", "ed25519 private key path")
		outPath    = fs.String("out", "", "output file (default stdout)")
		urlFlag    = fs.String("url", "", "server URL")
		tokenFlag  = fs.String("token", "", "bearer token")
		timeout    = fs.Duration("timeout", 30*time.Second, "HTTP timeout")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, deployEvidenceUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *eventID == "" {
		fs.Usage()
		return fmt.Errorf("--event is required")
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
	ev, err := fetchEvent(url, token, *eventID, *timeout)
	if err != nil {
		return fmt.Errorf("fetch event: %w", err)
	}
	if ev == nil {
		return fmt.Errorf("event %q not found", *eventID)
	}
	if ev.Kind != "DEPLOY" {
		return fmt.Errorf("event %q has kind %q, want DEPLOY", *eventID, ev.Kind)
	}

	in := buildDeployInputsFromEvent(ev, url)
	if *buildAtt != "" {
		d, err := attest.DigestFile(*buildAtt)
		if err != nil {
			return fmt.Errorf("hash --build-attestation: %w", err)
		}
		in.BuildAttestationDigest = d
	}
	if *codeAtt != "" {
		d, err := attest.DigestFile(*codeAtt)
		if err != nil {
			return fmt.Errorf("hash --code-attestation: %w", err)
		}
		in.CodeAttestationDigest = d
	}

	stmt, err := attest.BuildDeployEvidenceStatement(in)
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
		"deploy-evidence attestation written: env=%s, %d bytes, key id %s\n",
		in.Environment, len(envBytes), priv.KeyID,
	)
	return nil
}

// fetchEventResponse mirrors the shape of `Query.event` in the schema.
type fetchEventResponse struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"sessionId"`
	Actor     provenanceActor `json:"actor"`
	Payload   map[string]any `json:"payload"`
}

const eventByIDQuery = `query EventByID($id: ID!) {
  event(id: $id) {
    id
    kind
    sessionId
    actor { type id model }
    payload
  }
}`

// fetchEvent looks one event up by id; returns (nil, nil) when the
// server responds with a null event (id not found).
func fetchEvent(url, token, eventID string, timeout time.Duration) (*fetchEventResponse, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	body, err := json.Marshal(map[string]any{
		"query":     eventByIDQuery,
		"variables": map[string]any{"id": eventID},
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
			Event *fetchEventResponse `json:"event"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", out.Errors[0].Message)
	}
	return out.Data.Event, nil
}

// buildDeployInputsFromEvent extracts the deploy fields from the
// event payload. Image digest can come either bare or with prefix
// (the deploy webhook accepts either).
func buildDeployInputsFromEvent(ev *fetchEventResponse, storeURL string) attest.DeployEvidenceInputs {
	in := attest.DeployEvidenceInputs{
		StoreURL:         storeURL,
		TraceRootEventID: ev.ID,
	}
	p := ev.Payload
	if v, _ := p["environment"].(string); v != "" {
		in.Environment = v
	}
	if v, _ := p["image"].(string); v != "" {
		in.Image = v
	}
	if v, _ := p["image_digest"].(string); v != "" {
		in.ImageDigest = v
	}
	if v, _ := p["platform"].(string); v != "" {
		in.Platform = v
	}
	if v, _ := p["cluster"].(string); v != "" {
		in.Cluster = v
	}
	if v, _ := p["namespace"].(string); v != "" {
		in.Namespace = v
	}
	if v, _ := p["deployed_by"].(string); v != "" {
		in.DeployedBy = v
	}
	if v, _ := p["status"].(string); v != "" {
		in.Status = v
	}
	if v, _ := p["git_sha"].(string); v != "" {
		in.GitCommit = v
	}
	// finished_at preferred, started_at as fallback. The event's own ts
	// would be a third fallback but it isn't on this GraphQL response;
	// re-querying for it is more cost than the field is worth.
	if v, _ := p["finished_at"].(string); v != "" {
		in.DeployedAt = v
	} else if v, _ := p["started_at"].(string); v != "" {
		in.DeployedAt = v
	}
	return in
}
