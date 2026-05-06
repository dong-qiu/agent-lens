package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReplay(t *testing.T) {
	t.Run("HappyPath", func(t *testing.T) {
		dir := t.TempDir()

		// Two files with distinct mtimes; older first in ascending order.
		oldPath := filepath.Join(dir, "session-old.ndjson")
		newPath := filepath.Join(dir, "session-new.ndjson")
		oldBody := []byte(`{"id":"e1","kind":"prompt"}` + "\n")
		newBody := []byte(`{"id":"e2","kind":"prompt"}` + "\n")
		if err := os.WriteFile(oldPath, oldBody, 0o600); err != nil {
			t.Fatalf("write old: %v", err)
		}
		if err := os.WriteFile(newPath, newBody, 0o600); err != nil {
			t.Fatalf("write new: %v", err)
		}
		// Force a clear mtime ordering: old = 2h ago, new = 1h ago.
		old := time.Now().Add(-2 * time.Hour)
		newer := time.Now().Add(-1 * time.Hour)
		if err := os.Chtimes(oldPath, old, old); err != nil {
			t.Fatalf("chtimes old: %v", err)
		}
		if err := os.Chtimes(newPath, newer, newer); err != nil {
			t.Fatalf("chtimes new: %v", err)
		}

		// Also drop a dot-file and a non-.ndjson file to verify they are
		// skipped without showing up in the summary.
		if err := os.WriteFile(filepath.Join(dir, ".hidden.ndjson"), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write hidden: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
			t.Fatalf("write txt: %v", err)
		}

		var (
			mu       sync.Mutex
			gotPaths []string
			gotCT    []string
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/events" {
				http.NotFound(w, r)
				return
			}
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotPaths = append(gotPaths, string(body))
			gotCT = append(gotCT, r.Header.Get("Content-Type"))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"accepted":1}`))
		}))
		defer srv.Close()

		var stdout, stderr bytes.Buffer
		code := replayCore(dir, srv.URL, "", time.Time{}, false, false,
			&http.Client{Timeout: 5 * time.Second}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
		}

		mu.Lock()
		defer mu.Unlock()
		if len(gotPaths) != 2 {
			t.Fatalf("server saw %d POSTs, want 2 (stderr=%s)", len(gotPaths), stderr.String())
		}
		// mtime ascending → old before new
		if string(gotPaths[0]) != string(oldBody) {
			t.Errorf("first POST body = %q, want old %q", gotPaths[0], oldBody)
		}
		if string(gotPaths[1]) != string(newBody) {
			t.Errorf("second POST body = %q, want new %q", gotPaths[1], newBody)
		}
		for i, ct := range gotCT {
			if ct != "application/x-ndjson" {
				t.Errorf("POST[%d] Content-Type = %q, want application/x-ndjson", i, ct)
			}
		}

		out := stdout.String()
		if !strings.Contains(out, "replay summary: 2 files (2 succeeded, 0 failed, 0 skipped") {
			t.Errorf("summary line missing in stdout:\n%s", out)
		}
	})

	t.Run("DryRun", func(t *testing.T) {
		dir := t.TempDir()

		a := filepath.Join(dir, "a.ndjson")
		b := filepath.Join(dir, "b.ndjson")
		if err := os.WriteFile(a, []byte(`{"id":"x","kind":"prompt"}`+"\n"), 0o600); err != nil {
			t.Fatalf("write a: %v", err)
		}
		if err := os.WriteFile(b, []byte(`{"id":"y","kind":"prompt"}`+"\n"), 0o600); err != nil {
			t.Fatalf("write b: %v", err)
		}

		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits++
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		var stdout, stderr bytes.Buffer
		code := replayCore(dir, srv.URL, "", time.Time{}, true, false,
			&http.Client{Timeout: 5 * time.Second}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
		}
		if hits != 0 {
			t.Errorf("server saw %d POSTs in --dry-run, want 0", hits)
		}

		out := stdout.String()
		if !strings.Contains(out, "would replay: "+a) {
			t.Errorf("missing 'would replay' line for %s in:\n%s", a, out)
		}
		if !strings.Contains(out, "would replay: "+b) {
			t.Errorf("missing 'would replay' line for %s in:\n%s", b, out)
		}
	})
}
