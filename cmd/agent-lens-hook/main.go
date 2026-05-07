package main

import (
	"fmt"
	"os"
)

const usage = `agent-lens-hook — capture and forward events to an Agent Lens server.

Usage:
  agent-lens-hook <subcommand> [flags]

Subcommands:
  claude                 Capture a Claude Code hook payload from stdin and forward.
  git-post-commit        Capture a git post-commit event and forward.
  verify                 Verify the local hash chain of a session.
  replay                 Re-POST fallback NDJSON files to the ingest server.
  setup                  Wire hooks into Claude Code (and start docker compose).
  keygen                 Generate an ed25519 key pair for DSSE attestations.
  export                 Export an in-toto / SLSA attestation, or an audit report.
  sign-release           Sign a release binary and emit a DSSE attestation.
  verify-attestation     Verify a DSSE-wrapped in-toto attestation file.
  verify-audit-report    Verify an audit-report bundle.

Environment:
  AGENT_LENS_URL    Ingest endpoint (default http://localhost:8787)
  AGENT_LENS_TOKEN  Optional bearer token.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "claude":
		runClaude(os.Args[2:])
	case "git-post-commit":
		runGitPostCommit(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "replay":
		runReplay(os.Args[2:])
	case "setup":
		runSetup(os.Args[2:])
	case "keygen":
		runKeygen(os.Args[2:])
	case "export":
		runExport(os.Args[2:])
	case "sign-release":
		runSignRelease(os.Args[2:])
	case "verify-attestation":
		runVerifyAttestation(os.Args[2:])
	case "verify-audit-report":
		runVerifyAuditReport(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

// runClaude is implemented in claude.go.
// runGitPostCommit is implemented in git.go.
// runVerify is implemented in verify.go.
// runReplay is implemented in replay.go.
// runSetup is implemented in setup.go.
// runKeygen is implemented in keygen.go.
// runExport is implemented in export.go.
// runSignRelease is implemented in sign_release.go.
// runVerifyAttestation is implemented in verify_attestation.go.
// runVerifyAuditReport is implemented in audit_report.go.
