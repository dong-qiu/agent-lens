package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dongqiu/agent-lens/internal/attest"
)

const keygenUsage = `agent-lens-hook keygen — generate an ed25519 key pair for
in-toto / DSSE attestations.

  agent-lens-hook keygen [--out <path>]

Writes <path> (private key, mode 0600) and <path>.pub (public key, mode
0644). Default <path> is $HOME/.agent-lens/keys/ed25519. Both files are
PEM-encoded (PKCS#8 / PKIX) so cosign and openssl can read them.

Refuses to overwrite an existing file. Rotate by writing to a new path
and updating callers; silent overwrite of a private key would be a
footgun in a deploy pipeline.
`

func runKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "output path (default $HOME/.agent-lens/keys/ed25519)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, keygenUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	path := *out
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-lens-hook keygen: %v\n", err)
			os.Exit(2)
		}
		path = filepath.Join(home, ".agent-lens", "keys", "ed25519")
	}

	priv, _, err := attest.GenerateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-lens-hook keygen: generate: %v\n", err)
		os.Exit(1)
	}
	if err := attest.SaveKeyPair(path, priv); err != nil {
		fmt.Fprintf(os.Stderr, "agent-lens-hook keygen: save: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ed25519 key written\n  private: %s (mode 0600)\n  public:  %s.pub (mode 0644)\n  key id:  %s\n",
		path, path, priv.KeyID)
}
