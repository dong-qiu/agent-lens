// Package auth provides HTTP middleware for token-based authentication.
//
// v1 model: a single shared bearer token, supplied via env var. Empty
// token means "permit" — preserves the local-dev experience and the
// air-gap default. Real multi-user auth lives in M3+ alongside RBAC.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const bearerScheme = "Bearer "

// RequireToken returns a middleware that requires a properly formed
// `Authorization: Bearer <token>` header matching the configured token.
// The scheme name is matched case-insensitively per RFC 7235; the token
// itself is compared in constant time.
//
// If token is empty the middleware is a no-op (dev / air-gap default).
func RequireToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		want := []byte(token)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if len(header) <= len(bearerScheme) ||
				!strings.EqualFold(header[:len(bearerScheme)], bearerScheme) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			got := header[len(bearerScheme):]
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
