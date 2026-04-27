package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireTokenEmptyPermits(t *testing.T) {
	wrapped := RequireToken("")(okHandler())
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("empty token must permit, got %d", rec.Code)
	}
}

func TestRequireTokenMissingHeader(t *testing.T) {
	wrapped := RequireToken("secret")(okHandler())
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing header must reject, got %d", rec.Code)
	}
}

func TestRequireTokenWrongValue(t *testing.T) {
	wrapped := RequireToken("secret")(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token must reject, got %d", rec.Code)
	}
}

func TestRequireTokenCorrectValue(t *testing.T) {
	wrapped := RequireToken("secret")(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("correct token must permit, got %d", rec.Code)
	}
}

func TestRequireTokenRejectsMissingScheme(t *testing.T) {
	// RFC 6750 requires the "Bearer" scheme prefix; a bare token must
	// be rejected so we don't silently accept malformed clients.
	wrapped := RequireToken("secret")(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "secret")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bare token must be rejected, got %d", rec.Code)
	}
}

func TestRequireTokenAcceptsLowercaseScheme(t *testing.T) {
	// RFC 7235: auth scheme names are case-insensitive.
	wrapped := RequireToken("secret")(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer secret")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("lowercase scheme must be accepted, got %d", rec.Code)
	}
}

func TestRequireTokenRejectsSchemeOnly(t *testing.T) {
	wrapped := RequireToken("secret")(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("scheme without token must be rejected, got %d", rec.Code)
	}
}
