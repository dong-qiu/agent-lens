package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// BuildOptions configures Build. URL / RootEventID are required.
type BuildOptions struct {
	URL          string
	Token        string
	RootEventID  string
	Attestations []string // file paths to embed verbatim
	MaxSessions  int      // hard cap on BFS expansion (default 50)
	Timeout      time.Duration
	Generator    string // recorded in Report.Generator
	GeneratedAt  string // optional override; defaults to time.Now().UTC().Format(RFC3339)
}

const (
	defaultMaxSessions = 50
	defaultTimeout     = 30 * time.Second
)

// Build walks the link graph from RootEventID via GraphQL, collects
// every reachable event (grouped by session), embeds each attestation
// file verbatim, and computes the sha256 manifest. Returns a Report
// suitable for json.MarshalIndent + os.WriteFile.
//
// Error semantics: missing root event is a hard error; an exceeded
// MaxSessions cap is a hard error (the report would be incomplete);
// per-attestation read errors are hard errors. Network errors fail
// fast — partial reports would silently misrepresent the trace.
func Build(ctx context.Context, opts BuildOptions) (*Report, error) {
	if opts.URL == "" {
		return nil, errors.New("URL required")
	}
	if opts.RootEventID == "" {
		return nil, errors.New("RootEventID required")
	}
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = defaultMaxSessions
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	generatedAt := opts.GeneratedAt
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	root, err := fetchEventByID(ctx, opts, opts.RootEventID)
	if err != nil {
		return nil, fmt.Errorf("fetch root event: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("root event %q not found", opts.RootEventID)
	}

	// BFS over sessions. We index every event we've fetched by id so
	// peer-id lookups (from links) only trigger an extra HTTP roundtrip
	// when the peer is in a session we haven't visited yet.
	visited := map[string]bool{}
	knownEventIDs := map[string]bool{}
	queue := []string{root.SessionID}
	visited[root.SessionID] = true
	var sessions []Session

	for len(queue) > 0 {
		if len(sessions) >= opts.MaxSessions {
			return nil, fmt.Errorf(
				"max-sessions cap (%d) hit while traversing link graph; raise --max-sessions or trim the trace",
				opts.MaxSessions)
		}
		sessionID := queue[0]
		queue = queue[1:]

		events, err := fetchSessionEvents(ctx, opts, sessionID)
		if err != nil {
			return nil, fmt.Errorf("fetch session %q: %w", sessionID, err)
		}
		head, err := fetchSessionHead(ctx, opts, sessionID)
		if err != nil {
			return nil, fmt.Errorf("fetch session head %q: %w", sessionID, err)
		}
		sessions = append(sessions, Session{
			SessionID: sessionID,
			HeadHash:  head,
			Events:    events,
		})
		for _, e := range events {
			knownEventIDs[e.ID] = true
		}

		// Walk links → discover peer events → discover new sessions.
		// peerSet dedupes within this session's events.
		peerSet := map[string]bool{}
		for _, e := range events {
			for _, l := range e.Links {
				peer := peerEventID(l, e.ID)
				if peer != "" && !knownEventIDs[peer] {
					peerSet[peer] = true
				}
			}
		}
		for peerID := range peerSet {
			peer, err := fetchEventByID(ctx, opts, peerID)
			if err != nil {
				return nil, fmt.Errorf("fetch peer event %q: %w", peerID, err)
			}
			if peer == nil {
				// Dangling link target — log and skip rather than fail
				// the whole report. A link to a deleted/never-stored
				// event isn't fatal; it's just a gap the verifier sees.
				continue
			}
			if !visited[peer.SessionID] {
				visited[peer.SessionID] = true
				queue = append(queue, peer.SessionID)
			}
		}
	}

	atts, err := loadAttestations(opts.Attestations)
	if err != nil {
		return nil, err
	}

	r := &Report{
		Version:      Version,
		GeneratedAt:  generatedAt,
		Generator:    opts.Generator,
		StoreURL:     opts.URL,
		RootEventID:  opts.RootEventID,
		Sessions:     sessions,
		Attestations: atts,
	}
	manifest, err := computeManifest(r)
	if err != nil {
		return nil, fmt.Errorf("compute manifest: %w", err)
	}
	r.Manifest = manifest
	return r, nil
}

// peerEventID picks the side of a link that isn't `eventID`. Returns
// "" if eventID matches neither side (shouldn't happen since the
// server filters links by the event we asked for, but defensive).
func peerEventID(l Link, eventID string) string {
	switch {
	case l.FromEvent == eventID:
		return l.ToEvent
	case l.ToEvent == eventID:
		return l.FromEvent
	}
	return ""
}

// loadAttestations reads each file verbatim, base64-encodes for JSON
// transport, and records the sha256 of the original bytes. The
// envelope bytes are stored as-is so a verifier hashes the same input
// cosign / sigstore would.
func loadAttestations(paths []string) ([]Attestation, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]Attestation, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read attestation %s: %w", p, err)
		}
		sum := sha256.Sum256(raw)
		out = append(out, Attestation{
			Filename:    filepath.Base(p),
			Sha256:      "sha256:" + hex.EncodeToString(sum[:]),
			EnvelopeB64: base64.StdEncoding.EncodeToString(raw),
		})
	}
	return out, nil
}

// computeManifest serializes the bulky sections to canonical JSON
// (encoding/json sorts map keys alphabetically since Go 1.12; struct
// field order is source-stable) and hashes them. Verifiers re-do the
// same marshal+hash and compare.
func computeManifest(r *Report) (Manifest, error) {
	sessionsBytes, err := json.Marshal(r.Sessions)
	if err != nil {
		return Manifest{}, err
	}
	attsBytes, err := json.Marshal(r.Attestations)
	if err != nil {
		return Manifest{}, err
	}
	sessionsSum := sha256.Sum256(sessionsBytes)
	attsSum := sha256.Sum256(attsBytes)
	return Manifest{
		SessionsSha256:     "sha256:" + hex.EncodeToString(sessionsSum[:]),
		AttestationsSha256: "sha256:" + hex.EncodeToString(attsSum[:]),
	}, nil
}

// --- GraphQL fetch helpers ---

// auditEventQueryFields is the field set every fetcher requests; kept
// as a string constant so a future schema bump only needs touching
// here.
const auditEventQueryFields = `
    id
    ts
    sessionId
    kind
    actor { type id model }
    payload
    hash
    prevHash
    refs
    links { fromEvent toEvent relation confidence inferredBy }
`

const eventByIDForAudit = `query AuditEventByID($id: ID!) {
  event(id: $id) {` + auditEventQueryFields + `}
}`

const eventsBySessionForAudit = `query AuditEventsBySession($s: String!) {
  events(sessionId: $s, limit: 100000) {` + auditEventQueryFields + `}
}`

const sessionHeadForAudit = `query AuditSessionHead($s: String!) {
  sessionHead(sessionId: $s)
}`

func fetchEventByID(ctx context.Context, opts BuildOptions, id string) (*Event, error) {
	var out struct {
		Data struct {
			Event *Event `json:"event"`
		} `json:"data"`
	}
	if err := graphqlPost(ctx, opts, eventByIDForAudit, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	return out.Data.Event, nil
}

func fetchSessionEvents(ctx context.Context, opts BuildOptions, sessionID string) ([]Event, error) {
	var out struct {
		Data struct {
			Events []Event `json:"events"`
		} `json:"data"`
	}
	if err := graphqlPost(ctx, opts, eventsBySessionForAudit, map[string]any{"s": sessionID}, &out); err != nil {
		return nil, err
	}
	return out.Data.Events, nil
}

func fetchSessionHead(ctx context.Context, opts BuildOptions, sessionID string) (string, error) {
	var out struct {
		Data struct {
			SessionHead string `json:"sessionHead"`
		} `json:"data"`
	}
	if err := graphqlPost(ctx, opts, sessionHeadForAudit, map[string]any{"s": sessionID}, &out); err != nil {
		return "", err
	}
	return out.Data.SessionHead, nil
}

// graphqlPost POSTs a query+variables and decodes into out. Decodes
// with UseNumber so payload integers don't lose precision in float64
// (build / deploy webhook ids can grow).
func graphqlPost(ctx context.Context, opts BuildOptions, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.URL+"/v1/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}
	resp, err := (&http.Client{Timeout: opts.Timeout}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	// Surface graphql-level errors before trying to decode the payload —
	// the schema is guaranteed to omit `data` when there are errors,
	// so json.Unmarshal into a zero out would just leave it empty and
	// the caller would get a confusing "session has 0 events".
	var ep struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &ep); err == nil && len(ep.Errors) > 0 {
		return fmt.Errorf("graphql: %s", ep.Errors[0].Message)
	}

	// Decoder over a bytes.Reader so we keep UseNumber semantics for
	// payload integers (build / deploy webhook ids can grow).
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}
