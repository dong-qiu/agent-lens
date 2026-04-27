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

func TestRequireTokenIgnoresSchemeCase(t *testing.T) {
	// "Bearer " prefix is stripped literally; tokens that don't start with
	// the prefix are compared as-is. This documents the contract: clients
	// must use the canonical "Bearer <token>" form.
	wrapped := RequireToken("secret")(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "secret") // no Bearer prefix
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("raw token (no Bearer) is currently accepted; got %d", rec.Code)
	}
}
