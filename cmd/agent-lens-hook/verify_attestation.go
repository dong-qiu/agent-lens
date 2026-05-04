package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dong-qiu/agent-lens/internal/attest"
)

const verifyAttestationUsage = `agent-lens-hook verify-attestation — verify a
DSSE-wrapped in-toto attestation file produced by ` + "`agent-lens-hook export`" + `.

Usage:
  agent-lens-hook verify-attestation <file>
    [--pub <key.pub>] [--require-type <predicate-type>]

  <file>          .intoto.jsonl file containing a single DSSE envelope
  --pub           ed25519 public key path
                  (default $HOME/.agent-lens/keys/ed25519.pub)
  --require-type  if set, fail unless the inner Statement's
                  predicateType matches exactly (e.g.
                  "agent-lens.dev/code-provenance/v1")

Exit codes: 0 valid, 1 verification failed (bad signature, type
mismatch, malformed envelope), 2 usage / file errors.
`

func runVerifyAttestation(args []string) {
	if err := verifyAttestationCore(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "agent-lens-hook verify-attestation: %v\n", err)
		// Verification errors exit 1 (caller can gate on it); usage /
		// file errors exit 2 so a CD pipeline doesn't confuse "we
		// couldn't even check" with "we checked and it's bad".
		if isVerifyFailure(err) {
			os.Exit(1)
		}
		os.Exit(2)
	}
}

// verifyFailure is a sentinel wrapper used to mark verification (vs.
// argument / file) errors so runVerifyAttestation can pick the right
// exit code.
type verifyFailure struct{ err error }

func (v *verifyFailure) Error() string { return v.err.Error() }
func (v *verifyFailure) Unwrap() error { return v.err }

func isVerifyFailure(err error) bool {
	var vf *verifyFailure
	return errors.As(err, &vf)
}

func verifyAttestationCore(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify-attestation", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		pubPath     = fs.String("pub", "", "ed25519 public key path")
		requireType = fs.String("require-type", "", "fail unless predicateType matches")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, verifyAttestationUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	files := fs.Args()
	if len(files) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one attestation file is required (got %d)", len(files))
	}
	path := files[0]

	pp := *pubPath
	if pp == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		pp = filepath.Join(home, ".agent-lens", "keys", "ed25519.pub")
	}
	pub, err := attest.LoadPublicKey(pp)
	if err != nil {
		return fmt.Errorf("load public key from %s: %w", pp, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var env attest.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &verifyFailure{fmt.Errorf("decode envelope: %w", err)}
	}

	payload, payloadType, err := attest.Verify(pub, &env)
	if err != nil {
		return &verifyFailure{err}
	}

	var stmt attest.Statement
	if err := json.Unmarshal(payload, &stmt); err != nil {
		return &verifyFailure{fmt.Errorf("decode statement: %w", err)}
	}

	if *requireType != "" && stmt.PredicateType != *requireType {
		return &verifyFailure{fmt.Errorf("predicateType = %q, --require-type = %q", stmt.PredicateType, *requireType)}
	}

	subjects := make([]string, 0, len(stmt.Subject))
	for _, s := range stmt.Subject {
		// in-toto Subject.Digest is `map[algo]hex`; sort algos so the
		// output is stable across runs (Go map iteration is random).
		// Sorted alphabetically because there's no canonical priority
		// in the spec — sha256 happens to come before sha512.
		algos := make([]string, 0, len(s.Digest))
		for a := range s.Digest {
			algos = append(algos, a)
		}
		sort.Strings(algos)
		parts := make([]string, 0, len(algos))
		for _, a := range algos {
			parts = append(parts, fmt.Sprintf("%s:%s", a, s.Digest[a]))
		}
		joined := strings.Join(parts, ",")
		if s.Name != "" {
			subjects = append(subjects, fmt.Sprintf("%s (%s)", s.Name, joined))
		} else {
			subjects = append(subjects, joined)
		}
	}

	if _, err := fmt.Fprintf(out,
		"OK · payloadType %s · predicateType %s · keyid %s\n",
		payloadType, stmt.PredicateType, pub.KeyID,
	); err != nil {
		return err
	}
	for _, s := range subjects {
		if _, err := fmt.Fprintf(out, "  subject: %s\n", s); err != nil {
			return err
		}
	}
	return nil
}
