package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongqiu/agent-lens/internal/attest"
	"github.com/dongqiu/agent-lens/internal/audit"
)

const auditReportUsage = `agent-lens-hook export audit-report — bundle the
evidence chain rooted at one event id into a single tamper-evident
JSON file.

Usage:
  agent-lens-hook export audit-report \
    --root <event-id> \
    [--attestation <file>...] \
    [--out <file>] [--max-sessions N] \
    [--url <url>] [--token <token>]

  --root           required; trace graph entry point. Typically a
                   deploy / commit / pr event id.
  --attestation    repeatable; embeds a .intoto.jsonl envelope
                   verbatim. The bundle records sha256 of the file
                   and the report's manifest hashes them together so
                   the bundle is tamper-evident.
  --max-sessions   BFS cap (default 50). Errors out — does NOT
                   silently truncate — so a partial report can't be
                   mistaken for a complete one.
  --out            output path; default stdout.
  --url            Agent Lens URL (default $AGENT_LENS_URL or
                   http://localhost:8787)
  --token          bearer token (default $AGENT_LENS_TOKEN)
  --timeout        per-GraphQL-call timeout (default 30s)

The bundle is JSON, suitable for archiving / mailing / pasting into
a compliance tracker. Use ` + "`agent-lens-hook verify-audit-report`" + ` to
re-check it offline.
`

func runExportAuditReport(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export audit-report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		rootID     = fs.String("root", "", "root event id (required)")
		outPath    = fs.String("out", "", "output file (default stdout)")
		urlFlag    = fs.String("url", "", "server URL")
		tokenFlag  = fs.String("token", "", "bearer token")
		maxSession = fs.Int("max-sessions", 50, "BFS cap")
		timeout    = fs.Duration("timeout", 30*time.Second, "HTTP timeout")
	)
	var atts repeatedString
	fs.Var(&atts, "attestation", "attestation file path (repeatable)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, auditReportUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rootID == "" {
		fs.Usage()
		return fmt.Errorf("--root is required")
	}

	url := chooseURL(*urlFlag)
	token := chooseToken(*tokenFlag)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, err := audit.Build(ctx, audit.BuildOptions{
		URL:          url,
		Token:        token,
		RootEventID:  *rootID,
		Attestations: atts,
		MaxSessions:  *maxSession,
		Timeout:      *timeout,
		Generator:    "agent-lens-hook",
	})
	if err != nil {
		return err
	}

	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	body = append(body, '\n')

	if *outPath != "" {
		if err := os.WriteFile(*outPath, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *outPath, err)
		}
	} else if _, err := out.Write(body); err != nil {
		return err
	}

	totalEvents := 0
	for _, s := range r.Sessions {
		totalEvents += len(s.Events)
	}
	fmt.Fprintf(os.Stderr,
		"audit-report written: %d sessions, %d events, %d attestations, %d bytes\n",
		len(r.Sessions), totalEvents, len(r.Attestations), len(body),
	)
	return nil
}

const verifyAuditReportUsage = `agent-lens-hook verify-audit-report — re-check
a bundle's manifest, per-session hash chains, and (optionally) the
DSSE signatures of any embedded attestations.

Usage:
  agent-lens-hook verify-audit-report <file> [--pub <key.pub>]

  <file>   audit report JSON (produced by ` + "`agent-lens-hook export audit-report`" + `)
  --pub    ed25519 public key for DSSE verification (default
           $HOME/.agent-lens/keys/ed25519.pub). Omit by passing an
           empty value (--pub=) to skip signature verification — the
           manifest + chain checks still run.

Exit codes: 0 clean, 1 issues found (manifest mismatch, broken chain,
DSSE failure), 2 usage / file errors.
`

func runVerifyAuditReport(args []string) {
	if err := verifyAuditReportCore(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "agent-lens-hook verify-audit-report: %v\n", err)
		if isAuditReportIssue(err) {
			os.Exit(1)
		}
		os.Exit(2)
	}
}

// auditReportIssue marks errors that represent a verification failure
// (exit 1) vs. usage / file errors (exit 2). Same separation pattern
// as verify-attestation.
type auditReportIssue struct{ msg string }

func (e *auditReportIssue) Error() string { return e.msg }

func isAuditReportIssue(err error) bool {
	_, ok := err.(*auditReportIssue)
	return ok
}

func verifyAuditReportCore(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify-audit-report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pubFlag := fs.String("pub", "", "ed25519 public key path (empty to skip DSSE)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, verifyAuditReportUsage) }
	// `--pub` semantics: omitted → default home-dir key, silently skip
	// DSSE if it's not on disk; explicit `--pub <path>` → must load;
	// explicit `--pub=` → skip DSSE on purpose. We use fs.Visit to
	// distinguish "set but empty" from "not set", which Go's flag
	// package doesn't expose otherwise.
	if err := fs.Parse(args); err != nil {
		return err
	}
	files := fs.Args()
	if len(files) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one report file is required (got %d)", len(files))
	}
	path := files[0]

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var r audit.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("decode report: %w", err)
	}

	var pubKey *attest.PublicKey
	if pubFlagWasSet(fs) {
		if *pubFlag == "" {
			pubKey = nil // explicit --pub= means skip DSSE
		} else {
			pk, err := attest.LoadPublicKey(*pubFlag)
			if err != nil {
				return fmt.Errorf("load public key from %s: %w", *pubFlag, err)
			}
			pubKey = pk
		}
	} else {
		// Default path: try $HOME/.agent-lens/keys/ed25519.pub; if it
		// isn't there, silently skip DSSE so the verifier is still
		// useful on a host without the signing key.
		if home, herr := os.UserHomeDir(); herr == nil {
			if pk, err := attest.LoadPublicKey(filepath.Join(home, ".agent-lens", "keys", "ed25519.pub")); err == nil {
				pubKey = pk
			}
		}
	}

	res, err := audit.Verify(&r, audit.VerifyOptions{PubKey: pubKey})
	if err != nil {
		return err
	}

	if len(res.Issues) > 0 {
		if _, err := fmt.Fprintf(out, "FAIL · %d issues\n", len(res.Issues)); err != nil {
			return err
		}
		for _, iss := range res.Issues {
			if _, err := fmt.Fprintf(out, "  - %s\n", iss); err != nil {
				return err
			}
		}
		return &auditReportIssue{msg: fmt.Sprintf("%d issue(s) found", len(res.Issues))}
	}

	if _, err := fmt.Fprintf(out,
		"OK · version %s · %d sessions · %d events · attestations: %d verified, %d skipped\n",
		r.Version, res.SessionsCount, res.EventsCount,
		res.AttestationsVerified, res.AttestationsSkipped,
	); err != nil {
		return err
	}
	return nil
}

// pubFlagWasSet returns true if --pub was passed on the command line
// (even with an empty value). Required because Go's flag package
// doesn't distinguish "not passed" from "passed empty".
func pubFlagWasSet(fs *flag.FlagSet) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pub" {
			set = true
		}
	})
	return set
}

// repeatedString collects multiple --flag values into a slice. Used
// by --attestation.
type repeatedString []string

func (r *repeatedString) String() string     { return strings.Join(*r, ",") }
func (r *repeatedString) Set(v string) error { *r = append(*r, v); return nil }
