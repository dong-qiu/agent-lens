package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// captureStderr runs fn with os.Stderr swapped to a pipe, returns what was
// written. Tests for stderr-side effects need this because we can't use
// the testing package's t.Log capture for stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	_ = w.Close()
	return <-done
}

// TestFallbackWarningOncePerProcess: when the ingest endpoint is
// unreachable, transport.Send should fall back to file sink AND print a
// one-line stderr warning. Subsequent failures in the same process must
// NOT re-print (sync.Once contract).
func TestFallbackWarningOncePerProcess(t *testing.T) {
	// Reset the package-level Once so this test runs deterministically
	// regardless of order with other tests that may have tripped it.
	fallbackWarned = sync.Once{}

	// Server that always returns 500 — exercises the "non-2xx" fallback path.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Redirect the per-session sink to a temp dir so the test doesn't
	// pollute $HOME/.agent-lens/sessions.
	t.Setenv("HOME", t.TempDir())

	tr := &transport{
		url:    srv.URL,
		client: &http.Client{Timeout: defaultTimeout},
	}

	// Two Send() calls in the same process — both must fall back, but
	// only the first should warn.
	stderr := captureStderr(t, func() {
		_ = tr.Send([]map[string]any{{"id": "01HX0", "kind": "prompt"}}, "test-session")
		_ = tr.Send([]map[string]any{{"id": "01HX1", "kind": "prompt"}}, "test-session")
	})

	if got := hits.Load(); got != 2 {
		t.Errorf("server saw %d requests, want 2", got)
	}

	occurrences := strings.Count(stderr, "agent-lens-hook: ingest at")
	if occurrences != 1 {
		t.Errorf("warning printed %d times, want exactly 1\nstderr was:\n%s",
			occurrences, stderr)
	}

	// Substring spot-checks on the single warning we did print.
	for _, want := range []string{
		"agent-lens-hook: ingest at",
		srv.URL,
		"falling back to",
		"this batch: 1 events",
		"agent-lens-hook replay",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q\nstderr was:\n%s", want, stderr)
		}
	}

	// Sink-file contents not asserted here; appendToSink writes to
	// $HOME/.agent-lens/sessions/<sid>.ndjson and we Setenv HOME above
	// only to keep the sink out of the dev's real home dir. The
	// warning behavior is the only contract this test owns.
}
