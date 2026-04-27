package attest

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ed25519")

	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveKeyPair(path, priv); err != nil {
		t.Fatalf("save: %v", err)
	}

	loadedPriv, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatalf("load private: %v", err)
	}
	if !bytes.Equal(loadedPriv.Key, priv.Key) {
		t.Error("loaded private key bytes differ from saved")
	}
	if loadedPriv.KeyID != priv.KeyID {
		t.Errorf("loaded private KeyID = %q, want %q", loadedPriv.KeyID, priv.KeyID)
	}

	loadedPub, err := LoadPublicKey(path + ".pub")
	if err != nil {
		t.Fatalf("load public: %v", err)
	}
	if !bytes.Equal(loadedPub.Key, pub.Key) {
		t.Error("loaded public key bytes differ from generated")
	}
}

func TestSaveRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ed25519")
	priv, _, _ := GenerateKey()
	if err := SaveKeyPair(path, priv); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := SaveKeyPair(path, priv); err == nil {
		t.Error("second save should refuse to overwrite existing private key")
	}
}

func TestKeyIDIsDeterministicAndShort(t *testing.T) {
	priv, pub, _ := GenerateKey()
	if priv.KeyID != pub.KeyID {
		t.Errorf("priv/pub KeyID mismatch: %q vs %q", priv.KeyID, pub.KeyID)
	}
	if len(priv.KeyID) != 16 {
		t.Errorf("KeyID len = %d, want 16 (8 bytes hex)", len(priv.KeyID))
	}
	again := keyIDFor(pub.Key)
	if again != pub.KeyID {
		t.Errorf("keyIDFor not deterministic: %q vs %q", again, pub.KeyID)
	}
}

func TestSavedFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't match POSIX semantics on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ed25519")
	priv, _, _ := GenerateKey()
	if err := SaveKeyPair(path, priv); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %o, want 0600", info.Mode().Perm())
	}
	info, err = os.Stat(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("public key mode = %o, want 0644", info.Mode().Perm())
	}
}

func TestLoadRejectsNonPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage")
	if err := os.WriteFile(path, []byte("this is not a PEM file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivateKey(path); err == nil {
		t.Error("LoadPrivateKey accepted non-PEM input")
	}
	if _, err := LoadPublicKey(path); err == nil {
		t.Error("LoadPublicKey accepted non-PEM input")
	}
}

func TestPEMOutputIsCosignReadable(t *testing.T) {
	// Sanity: the bytes we write are valid PEM with the expected
	// header. cosign / openssl readability is implied by using
	// PKCS#8 / PKIX standard encodings.
	dir := t.TempDir()
	path := filepath.Join(dir, "ed25519")
	priv, _, _ := GenerateKey()
	if err := SaveKeyPair(path, priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.HasPrefix(raw, []byte("-----BEGIN PRIVATE KEY-----")) {
		t.Errorf("private PEM missing standard header:\n%s", raw)
	}
	raw, _ = os.ReadFile(path + ".pub")
	if !bytes.HasPrefix(raw, []byte("-----BEGIN PUBLIC KEY-----")) {
		t.Errorf("public PEM missing standard header:\n%s", raw)
	}
}
