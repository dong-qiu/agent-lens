package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// verifySignature constant-time-compares GitHub's X-Hub-Signature-256
// header against HMAC-SHA256(secret, body). The header is in the form
// "sha256=<hex>". Empty header or any malformed input returns false.
func verifySignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if len(secret) == 0 || !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}
