package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const replayUsage = `agent-lens-hook replay — re-POST fallback NDJSON files
that the hook wrote when the ingest server was unreachable.

  agent-lens-hook replay [flags]

Flags:
  --dir <path>          directory containing fallback *.ndjson files
                        (default $HOME/.agent-lens/sessions)
  --url <url>           ingest endpoint
                        (default $AGENT_LENS_URL or http://localhost:8787)
  --token <token>       bearer token (default $AGENT_LENS_TOKEN)
  --since <RFC3339>     only replay files with mtime >= this timestamp
  --dry-run             list files that would be replayed and exit
  --remove-on-success   delete each file after a successful POST

Files are processed in mtime-ascending order so older sessions land
before newer ones. A failure on one file is logged to stderr and the
next file is still attempted.

Exit codes: 0 on success (or --dry-run with at least one match),
1 if any file failed, 2 on usage / IO errors (e.g. --dir missing).

Example:
  agent-lens-hook replay --dry-run
  agent-lens-hook replay --remove-on-success --url http://localhost:8787
`

func runReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		dirFlag    = fs.String("dir", "", "fallback directory (default $HOME/.agent-lens/sessions)")
		urlFlag    = fs.String("url", "", "Agent Lens server URL (defaults to AGENT_LENS_URL or http://localhost:8787)")
		tokenFlag  = fs.String("token", "", "bearer token (defaults to AGENT_LENS_TOKEN)")
		sinceFlag  = fs.String("since", "", "only replay files with mtime >= this RFC3339 timestamp")
		dryRun     = fs.Bool("dry-run", false, "list files that would be replayed and exit")
		removeFlag = fs.Bool("remove-on-success", false, "delete each file after successful POST")
		timeout    = fs.Duration("timeout", 30*time.Second, "HTTP request timeout per file")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, replayUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	dir := *dirFlag
	if dir == "" {
		dir = filepath.Join(homeDir(), ".agent-lens", "sessions")
	}

	var since time.Time
	if *sinceFlag != "" {
		t, err := time.Parse(time.RFC3339, *sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-lens-hook replay: invalid --since: %v\n", err)
			os.Exit(2)
		}
		since = t
	}

	url := chooseURL(*urlFlag)
	token := chooseToken(*tokenFlag)
	client := &http.Client{Timeout: *timeout}

	code := replayCore(dir, url, token, since, *dryRun, *removeFlag, client, os.Stdout, os.Stderr)
	os.Exit(code)
}

// replayCore is the testable core of the replay subcommand. It returns
// the exit code so tests can assert on it without invoking os.Exit.
func replayCore(dir, url, token string, since time.Time, dryRun, removeOnSuccess bool, client *http.Client, stdout, stderr io.Writer) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "agent-lens-hook replay: read dir %s: %v\n", dir, err)
		return 2
	}

	type candidate struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []candidate
	var skipped int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".ndjson") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			fmt.Fprintf(stderr, "failed: %s: stat: %v\n", name, err)
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			skipped++
			continue
		}
		files = append(files, candidate{
			path:  filepath.Join(dir, name),
			size:  info.Size(),
			mtime: info.ModTime(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.Before(files[j].mtime)
	})

	var succeeded, failed int
	for _, f := range files {
		if dryRun {
			fmt.Fprintf(stdout, "would replay: %s (%d bytes, mtime %s)\n",
				f.path, f.size, f.mtime.UTC().Format(time.RFC3339))
			succeeded++
			continue
		}

		body, err := os.ReadFile(f.path)
		if err != nil {
			fmt.Fprintf(stderr, "failed: %s: read: %v\n", f.path, err)
			failed++
			continue
		}

		if err := postNDJSON(client, url, token, body); err != nil {
			fmt.Fprintf(stderr, "failed: %s: %v\n", f.path, err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "replayed: %s (%d bytes)\n", f.path, len(body))
		succeeded++

		if removeOnSuccess {
			if err := os.Remove(f.path); err != nil {
				fmt.Fprintf(stderr, "WARN: removed: failed to remove %s: %v\n", f.path, err)
			} else {
				fmt.Fprintf(stdout, "removed: %s\n", f.path)
			}
		}
	}

	fmt.Fprintf(stdout, "replay summary: %d files (%d succeeded, %d failed, %d skipped via --since)\n",
		len(files), succeeded, failed, skipped)

	if failed > 0 {
		return 1
	}
	return 0
}

func postNDJSON(client *http.Client, url, token string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, url+"/v1/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return nil
}
