package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestRegisterHealthz guards issue #99: /healthz must answer 200 to both
// GET and HEAD. Before the fix, only GET was registered, so HEAD probes
// (uptime checkers, load balancers) fell through to the catch-all and 404'd.
func TestRegisterHealthz(t *testing.T) {
	r := chi.NewRouter()
	registerHealthz(r)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("%s /healthz = %d, want %d", method, rec.Code, http.StatusOK)
			}
		})
	}
}
