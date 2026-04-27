// Package hashchain computes the per-event hash that links events into an
// append-only chain. Hash input is the canonical-JSON event with the hash
// and sig fields cleared, so the chain remains verifiable without the
// signature.
package hashchain

import (
	"crypto/sha256"
	"encoding/hex"
)

// Compute returns hex(sha256(prevHash || canonicalPayload)).
func Compute(prevHash string, canonicalPayload []byte) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte{0x1f}) // unit separator, prevents prefix collisions
	h.Write(canonicalPayload)
	return hex.EncodeToString(h.Sum(nil))
}
