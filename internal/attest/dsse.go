// Package attest implements Dead Simple Signing Envelope (DSSE) sign/verify
// over local ed25519 keys for in-toto attestations. v0 supports local key
// files only; Sigstore (Fulcio / Rekor) network signing is a future option
// that can plug into the same Sign / Verify API.
//
// DSSE wire format: https://github.com/secure-systems-lab/dsse
package attest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Envelope is the DSSE envelope serialized to/from JSON.
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"` // base64
	Signatures  []Signature `json:"signatures"`
}

// Signature carries one signer's bytes and an optional key id (sha256
// prefix of the public key by convention).
type Signature struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   string `json:"sig"` // base64
}

// pae implements DSSE Pre-Authentication Encoding:
//
//	"DSSEv1" SP LEN(type) SP type SP LEN(payload) SP payload
//
// Hand-written rather than via a library so the spec is auditable in
// one function and we don't add transitive deps for a 10-line algo.
func pae(payloadType string, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("DSSEv1 ")
	buf.WriteString(strconv.Itoa(len(payloadType)))
	buf.WriteByte(' ')
	buf.WriteString(payloadType)
	buf.WriteByte(' ')
	buf.WriteString(strconv.Itoa(len(payload)))
	buf.WriteByte(' ')
	buf.Write(payload)
	return buf.Bytes()
}

// Sign returns a DSSE envelope wrapping payload with one ed25519 signature.
// payloadType identifies what the payload is (e.g.
// "application/vnd.in-toto+json").
func Sign(priv *PrivateKey, payloadType string, payload []byte) (*Envelope, error) {
	if priv == nil {
		return nil, errors.New("nil private key")
	}
	if payloadType == "" {
		return nil, errors.New("empty payload type")
	}
	msg := pae(payloadType, payload)
	sig := ed25519.Sign(priv.Key, msg)
	return &Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []Signature{
			{KeyID: priv.KeyID, Sig: base64.StdEncoding.EncodeToString(sig)},
		},
	}, nil
}

// Verify checks the envelope's signatures against pub. On success it
// returns the decoded payload bytes and the envelope's payloadType.
//
// Multiple signatures: any signature matching pub validates the
// envelope. Mismatched key ids are skipped (not an error) so a multi-
// signed envelope verifies under each individual key.
func Verify(pub *PublicKey, env *Envelope) ([]byte, string, error) {
	if pub == nil {
		return nil, "", errors.New("nil public key")
	}
	if env == nil {
		return nil, "", errors.New("nil envelope")
	}
	if len(env.Signatures) == 0 {
		return nil, "", errors.New("envelope has no signatures")
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, "", fmt.Errorf("decode payload: %w", err)
	}
	msg := pae(env.PayloadType, payload)
	for _, s := range env.Signatures {
		if pub.KeyID != "" && s.KeyID != "" && s.KeyID != pub.KeyID {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			continue
		}
		if ed25519.Verify(pub.Key, msg, sig) {
			return payload, env.PayloadType, nil
		}
	}
	return nil, "", errors.New("no valid signature for the provided public key")
}

// keyIDFor returns sha256(pubBytes)[:8] hex-encoded — a deterministic
// 16-char short identifier used in Signature.keyid and key file naming.
func keyIDFor(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}
