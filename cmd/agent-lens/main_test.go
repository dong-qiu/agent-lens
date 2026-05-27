package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeHealth struct{ err error }

func (f fakeHealth) Ping(context.Context) error { return f.err }

// TestRegisterHealthz guards issue #99 (both GET and HEAD are answered — HEAD
// probes used to fall through to the catch-all and 404) and issue #10 (the
// probe reflects store reachability: 200 when the store pings, 503 when it
// fails, instead of a misleading unconditional 200).
func TestRegisterHealthz(t *testing.T) {
	cases := []struct {
		name string
		hc   healthChecker
		want int
	}{
		{"healthy", fakeHealth{nil}, http.StatusOK},
		{"store down", fakeHealth{errors.New("dial tcp: connection refused")}, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := chi.NewRouter()
			registerHealthz(r, tc.hc)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))
				if rec.Code != tc.want {
					t.Errorf("%s /healthz (%s) = %d, want %d", method, tc.name, rec.Code, tc.want)
				}
			}
		})
	}
}
