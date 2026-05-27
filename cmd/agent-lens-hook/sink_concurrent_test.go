//go:build unix

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAppendToSinkConcurrentLargePayloads is issue #3's acceptance criterion:
// many writers append large (> PIPE_BUF) NDJSON records to one session sink
// concurrently, and every resulting line must still be valid, complete JSON.
// Each appendToSink call opens its own fd, so the goroutines behave like the
// separate hook processes (parallel sub-agents) the bug is about.
//
// Note on platform sensitivity: Linux serializes write() to a regular file via
// the inode lock, so this test can pass even without the flock there; macOS
// does interleave > 4 KiB writes and fails without it. TestLockSinkExclusive
// below is the platform-independent regression guard on the lock itself.
func TestAppendToSinkConcurrentLargePayloads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		sid       = "concurrent-session"
		writers   = 16
		perWriter = 8
		pad       = 16 * 1024 // each record well above PIPE_BUF (4 KiB)
	)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				var buf bytes.Buffer
				// json.Encoder appends the trailing '\n', giving one NDJSON line.
				if err := json.NewEncoder(&buf).Encode(map[string]any{
					"writer": w,
					"seq":    i,
					"pad":    strings.Repeat("x", pad),
				}); err != nil {
					t.Errorf("encode: %v", err)
					return
				}
				if err := appendToSink(sid, buf.Bytes()); err != nil {
					t.Errorf("appendToSink: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	path := filepath.Join(os.Getenv("HOME"), ".agent-lens", "sessions", sid+".ndjson")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	defer f.Close()

	type rec struct {
		Writer int    `json:"writer"`
		Seq    int    `json:"seq"`
		Pad    string `json:"pad"`
	}
	seen := map[[2]int]bool{}
	lines := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // records are ~16 KiB; raise the cap comfortably
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines++
		var r rec
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("line %d is not valid JSON (interleaved write): %v\nfirst 120 bytes: %q",
				lines, err, line[:min(120, len(line))])
		}
		if len(r.Pad) != pad {
			t.Fatalf("line %d pad len = %d, want %d (truncated/interleaved write)", lines, len(r.Pad), pad)
		}
		seen[[2]int{r.Writer, r.Seq}] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	want := writers * perWriter
	if lines != want {
		t.Errorf("got %d lines, want %d", lines, want)
	}
	if len(seen) != want {
		t.Errorf("got %d distinct records, want %d (records lost or duplicated)", len(seen), want)
	}
}

// TestLockSinkExclusive deterministically verifies lockSink is mutually
// exclusive across open file descriptions (what serializes separate hook
// processes): while one holds the lock, a second acquire on an independent fd
// of the same file must block until the first releases. This fails fast if the
// flock is ever weakened to a no-op, regardless of kernel write() semantics.
func TestLockSinkExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-target")
	open := func() *os.File {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}

	unlock1, err := lockSink(open().Fd())
	if err != nil {
		t.Fatalf("first lockSink: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		unlock2, err := lockSink(open().Fd())
		if err != nil {
			t.Errorf("second lockSink: %v", err)
			return
		}
		close(acquired)
		unlock2()
	}()

	select {
	case <-acquired:
		t.Fatal("second lockSink acquired while the first was held — lock is not exclusive")
	case <-time.After(150 * time.Millisecond):
		// Expected: still blocked.
	}

	unlock1()
	select {
	case <-acquired:
		// Expected: lock became available once released.
	case <-time.After(2 * time.Second):
		t.Fatal("second lockSink never acquired after release — lock not freed")
	}
}
