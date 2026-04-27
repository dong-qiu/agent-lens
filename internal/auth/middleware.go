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

// RequireToken returns a middleware that rejects requests whose
// Authorization header does not carry the configured bearer token.
// If token is empty the middleware is a no-op (dev / air-gap default).
func RequireToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		want := []byte(token)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
