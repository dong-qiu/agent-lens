package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PrivateKey wraps an ed25519 private key with its short KeyID.
type PrivateKey struct {
	Key   ed25519.PrivateKey
	KeyID string
}

// PublicKey wraps an ed25519 public key with its short KeyID.
type PublicKey struct {
	Key   ed25519.PublicKey
	KeyID string
}

const (
	pemTypePrivate = "PRIVATE KEY"
	pemTypePublic  = "PUBLIC KEY"
)

// GenerateKey returns a fresh ed25519 key pair sharing the same
// deterministic short KeyID.
func GenerateKey() (*PrivateKey, *PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	id := keyIDFor(pub)
	return &PrivateKey{Key: priv, KeyID: id}, &PublicKey{Key: pub, KeyID: id}, nil
}

// SaveKeyPair writes the private half of priv to path (mode 0600) and
// the matching public half to path+".pub" (mode 0644). Both files are
// PEM-encoded (PKCS#8 / PKIX) so cosign and openssl can read them.
//
// Refuses to overwrite either file. Rotate by writing to a new path;
// silent overwrite of a private key would be a footgun in a deploy
// pipeline.
func SaveKeyPair(path string, priv *PrivateKey) error {
	if priv == nil {
		return errors.New("nil private key")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	privPEM, err := encodePrivatePEM(priv.Key)
	if err != nil {
		return err
	}
	if err := writeNewFile(path, 0o600, privPEM); err != nil {
		return err
	}

	pub := priv.Key.Public().(ed25519.PublicKey)
	pubPEM, err := encodePublicPEM(pub)
	if err != nil {
		_ = os.Remove(path) // roll back the private file we just wrote
		return err
	}
	if err := writeNewFile(path+".pub", 0o644, pubPEM); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// LoadPrivateKey reads a PEM-encoded ed25519 private key (PKCS#8) from
// path. Returns an error if the file isn't a PEM block or the parsed
// key isn't ed25519.
func LoadPrivateKey(path string) (*PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("not a PEM file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an ed25519 key (got %T)", parsed)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return &PrivateKey{Key: priv, KeyID: keyIDFor(pub)}, nil
}

// LoadPublicKey reads a PEM-encoded ed25519 public key (PKIX) from path.
func LoadPublicKey(path string) (*PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("not a PEM file")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an ed25519 key (got %T)", parsed)
	}
	return &PublicKey{Key: pub, KeyID: keyIDFor(pub)}, nil
}

func encodePrivatePEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePrivate, Bytes: der}), nil
}

func encodePublicPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePublic, Bytes: der}), nil
}

// writeNewFile creates path with mode and writes data. Refuses to
// overwrite an existing file.
func writeNewFile(path string, mode os.FileMode, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
