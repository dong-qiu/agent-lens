package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dong-qiu/agent-lens/internal/attest"
)

// ReleaseArtifactPredicate is the predicateType for the v0.1 release-binary
// attestation. The subject is the file's sha256 (not gitCommit) so that
// `cosign verify-blob-attestation` can natively walk the subject linkage —
// PR #76 found gitCommit subjects break cosign's blob verification.
const ReleaseArtifactPredicate = "agent-lens.dev/release-artifact/v1"

const signReleaseUsage = `agent-lens-hook sign-release — sign a release binary
with the project's ed25519 key and emit a DSSE-wrapped in-toto attestation.

Usage:
  agent-lens-hook sign-release \
    [--key <path>] --in <binary-path> --out <sig-path>

  --key   ed25519 private key path
          (default $HOME/.agent-lens/keys/ed25519)
  --in    path to the binary file to sign (required)
  --out   path where the .intoto.jsonl DSSE envelope is written (required)

Output is one JSON-encoded DSSE envelope on a single line, suitable for a
.intoto.jsonl release artifact. The signed payload is an in-toto v1
Statement whose subject is the binary's sha256 (so cosign verify-blob-
attestation works) and whose predicate carries the platform parsed from
the filename (GOOS/GOARCH for darwin|linux × amd64|arm64) and the build
timestamp. Unrecognized filenames yield an empty platform string rather
than an error.

Exit codes: 0 success, 2 usage / file errors (missing flag, unreadable
key, missing --in, unwritable --out, signing error).
`

func runSignRelease(args []string) {
	if err := signReleaseCore(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "agent-lens-hook sign-release: %v\n", err)
		// All failures (including signing) exit 2 per ADR 0006: there's
		// no "verification failed" vs "couldn't check" split here, only
		// "we produced a sig" or "we didn't".
		os.Exit(2)
	}
}

// releaseArtifactPredicate is the v1 predicate body. Kept tiny on
// purpose — anything richer (build env, source commit, builder id)
// belongs in the SLSA build provenance, not in the release-artifact
// attestation whose only job is "this is a sha256 + platform that the
// project signed".
type releaseArtifactPredicate struct {
	Platform string `json:"platform"`
	BuiltAt  string `json:"builtAt"`
}

func signReleaseCore(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sign-release", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		keyPath = fs.String("key", "", "ed25519 private key path")
		inPath  = fs.String("in", "", "binary file to sign (required)")
		outPath = fs.String("out", "", "path to write the DSSE envelope (required)")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, signReleaseUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" || *outPath == "" {
		fs.Usage()
		return fmt.Errorf("--in and --out are both required")
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

	digest, err := sha256File(*inPath)
	if err != nil {
		return fmt.Errorf("hash --in %s: %w", *inPath, err)
	}

	base := filepath.Base(*inPath)
	predicate, err := json.Marshal(releaseArtifactPredicate{
		Platform: detectPlatform(base),
		BuiltAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal predicate: %w", err)
	}

	stmt := attest.Statement{
		Type:          attest.InTotoStatementType,
		PredicateType: ReleaseArtifactPredicate,
		Subject: []attest.Subject{
			{Name: base, Digest: map[string]string{"sha256": digest}},
		},
		Predicate: predicate,
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

	if err := os.WriteFile(*outPath, envBytes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *outPath, err)
	}

	fmt.Fprintf(os.Stderr,
		"release attestation written: %s (sha256:%s, %d bytes, key id %s)\n",
		*outPath, digest, len(envBytes), priv.KeyID,
	)
	// out is currently informational — keep stdout silent in success
	// path so callers can pipe sign-release into a pipeline without
	// stripping noise. (Stderr keeps the human-readable summary.)
	_ = out
	return nil
}

// sha256File hashes the file at path. Streamed so we don't allocate the
// whole binary in memory; release binaries can be tens of MB.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recognizedGOOS / recognizedGOARCH are the v0.1 platform matrix per
// ADR 0006 D5. Anything outside this set yields an empty platform
// string — unrecognized filenames are not an error, they just don't get
// platform metadata.
var (
	recognizedGOOS   = map[string]bool{"darwin": true, "linux": true}
	recognizedGOARCH = map[string]bool{"amd64": true, "arm64": true}
)

// detectPlatform parses GOOS/GOARCH out of release-style binary names
// like "agent-lens-darwin-arm64" or "agent-lens-hook-linux-amd64.exe".
// We anchor on the *last* two hyphen-separated segments after stripping
// any extension; that's robust against the binary name itself containing
// hyphens (agent-lens-hook), which is exactly our case.
//
// Returns "" for anything we don't recognize — including the bare
// "agent-lens" with no platform suffix, which is intentional: the user
// can still produce an attestation, it just won't claim a platform it
// can't prove.
func detectPlatform(filename string) string {
	name := filename
	// Strip a single trailing extension (e.g. ".exe"). Doing this once
	// rather than via filepath.Ext-in-a-loop matches the spec ("trailing
	// extensions like .exe or anything after the arch can be ignored")
	// without accidentally eating a platform segment that contains a dot.
	if dot := strings.LastIndex(name, "."); dot > 0 && dot > strings.LastIndex(name, "-") {
		name = name[:dot]
	}
	parts := strings.Split(name, "-")
	if len(parts) < 2 {
		return ""
	}
	arch := parts[len(parts)-1]
	goos := parts[len(parts)-2]
	if !recognizedGOOS[goos] || !recognizedGOARCH[arch] {
		return ""
	}
	return goos + "/" + arch
}
