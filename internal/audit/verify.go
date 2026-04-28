package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dongqiu/agent-lens/internal/attest"
)

// DecodeReport unmarshals a report file with UseNumber so payload
// integers stay as json.Number rather than float64 — that's what
// Build wrote, and re-marshaling a float64 of a >2^53 integer would
// drift bytes and surface as a spurious manifest mismatch. Use this
// in preference to plain json.Unmarshal when reading a report off
// disk.
func DecodeReport(raw []byte, r *Report) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(r)
}

// VerifyOptions configures Verify. PubKey is optional — if nil, DSSE
// envelope verification is skipped (the manifest + chain checks still
// run, so the report is meaningfully checked even on hosts without
// the signing key).
type VerifyOptions struct {
	PubKey *attest.PublicKey
}

// VerifyResult records what passed and what didn't. A non-nil result
// with len(Issues) == 0 means the report is internally consistent.
type VerifyResult struct {
	SessionsCount        int
	EventsCount          int
	AttestationsVerified int // 0 if PubKey nil; otherwise attestations whose DSSE signature checked out
	AttestationsSkipped  int // counted only when PubKey is nil
	Issues               []string
}

// Verify checks a Report's internal consistency:
//  1. Version is recognized.
//  2. Manifest sha256s match a fresh hash of Sessions / Attestations.
//  3. Each session's events have prev_hash → hash linkage and the
//     last event's hash matches Session.HeadHash.
//  4. Each attestation's recorded sha256 matches the embedded bytes.
//  5. (Optional) Each DSSE envelope verifies with PubKey.
//
// Returns the result regardless of pass/fail; callers decide how to
// surface issues. Returns a non-nil error only on programming-level
// problems (failed json.Marshal, etc.).
func Verify(r *Report, opts VerifyOptions) (*VerifyResult, error) {
	if r == nil {
		return nil, errors.New("nil report")
	}
	res := &VerifyResult{}

	if r.Version != Version {
		res.Issues = append(res.Issues, fmt.Sprintf(
			"unrecognized report version %q (expected %q); refusing to interpret further",
			r.Version, Version))
		return res, nil
	}

	// Manifest re-hash. Use the same canonical encoding the builder
	// used (encoding/json with default settings).
	wantManifest, err := computeManifest(r)
	if err != nil {
		return nil, fmt.Errorf("recompute manifest: %w", err)
	}
	if wantManifest.SessionsSha256 != r.Manifest.SessionsSha256 {
		res.Issues = append(res.Issues, fmt.Sprintf(
			"manifest.sessions_sha256 mismatch: report says %s, recomputed %s",
			r.Manifest.SessionsSha256, wantManifest.SessionsSha256))
	}
	if wantManifest.AttestationsSha256 != r.Manifest.AttestationsSha256 {
		res.Issues = append(res.Issues, fmt.Sprintf(
			"manifest.attestations_sha256 mismatch: report says %s, recomputed %s",
			r.Manifest.AttestationsSha256, wantManifest.AttestationsSha256))
	}

	// Per-session chain walk. Server returns events in append order
	// (ts ASC, id ASC); each prev_hash should equal the previous
	// event's hash, and the head should equal the last event's hash.
	res.SessionsCount = len(r.Sessions)
	for _, s := range r.Sessions {
		res.EventsCount += len(s.Events)
		for i, e := range s.Events {
			var want string
			if i > 0 {
				want = s.Events[i-1].Hash
			}
			if e.PrevHash != want {
				res.Issues = append(res.Issues, fmt.Sprintf(
					"session %q event[%d] (id=%s): prev_hash=%q, expected %q",
					s.SessionID, i, e.ID, e.PrevHash, want))
			}
		}
		if len(s.Events) > 0 && s.HeadHash != "" {
			lastHash := s.Events[len(s.Events)-1].Hash
			if s.HeadHash != lastHash {
				res.Issues = append(res.Issues, fmt.Sprintf(
					"session %q: head_hash %s != last event hash %s (truncated mid-fetch?)",
					s.SessionID, s.HeadHash, lastHash))
			}
		}
	}

	// Attestation re-hash.
	for _, a := range r.Attestations {
		envBytes, err := base64.StdEncoding.DecodeString(a.EnvelopeB64)
		if err != nil {
			res.Issues = append(res.Issues, fmt.Sprintf(
				"attestation %q: base64 decode: %v", a.Filename, err))
			continue
		}
		sum := sha256.Sum256(envBytes)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if got != a.Sha256 {
			res.Issues = append(res.Issues, fmt.Sprintf(
				"attestation %q: recorded sha256 %s != computed %s",
				a.Filename, a.Sha256, got))
		}

		if opts.PubKey == nil {
			res.AttestationsSkipped++
			continue
		}
		var env attest.Envelope
		if err := json.Unmarshal(envBytes, &env); err != nil {
			res.Issues = append(res.Issues, fmt.Sprintf(
				"attestation %q: decode envelope: %v", a.Filename, err))
			continue
		}
		if _, _, err := attest.Verify(opts.PubKey, &env); err != nil {
			res.Issues = append(res.Issues, fmt.Sprintf(
				"attestation %q: DSSE verify: %v", a.Filename, err))
			continue
		}
		res.AttestationsVerified++
	}
	return res, nil
}
