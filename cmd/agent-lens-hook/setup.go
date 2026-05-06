package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed setup_compose.yml
var setupComposeTemplate string

const setupUsage = `agent-lens-hook setup — wire agent-lens into Claude Code.

Usage:
  agent-lens-hook setup [--personal | --project-only] [--uninstall]
                        [--image <ref>] [--skip-compose] [--healthz-timeout <dur>]

Modes (mutually exclusive):
  --personal       (default) hook config goes to $HOME/.claude/settings.json
                   so every Claude Code session anywhere on this machine is
                   captured. Required for sub-agent (Task tool) capture per
                   ADR 0007 D1.
  --project-only   hook config goes to .claude/settings.local.json in the
                   current working directory; only this repo's sessions
                   are captured. Sub-agents started in worktree-isolated
                   scopes (under .claude/worktrees/) WILL NOT be captured
                   under this mode — by design.

Compose orchestration (default: managed):
  --image <ref>    container image for the server (default
                   ghcr.io/dong-qiu/agent-lens:latest). Override for
                   private registry / dev builds.
  --skip-compose   don't manage docker compose; assume the user runs
                   the server themselves.
  --healthz-timeout <dur>
                   how long to wait for /healthz to return 200 after
                   compose up (default 60s).

Reverse:
  --uninstall      remove agent-lens hook entries from settings (leaves
                   other tools' entries untouched) and run
                   ` + "`docker compose down`" + ` if compose was managed.

The hook command path written into settings.json is the absolute path
of the running agent-lens-hook binary (os.Executable). So if you move
the binary or upgrade via ` + "`go install`" + `, just re-run setup.

Exit codes: 0 success, 2 usage / IO / docker errors.
`

// Hook event types we register for. The set MUST stay in sync with
// the events claude.go's Send() handles, otherwise some interactions
// stop being captured silently.
var setupHookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"Stop",
}

func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		personal       = fs.Bool("personal", false, "user-global hook install (default)")
		projectOnly    = fs.Bool("project-only", false, "scope hooks to current repo")
		uninstall      = fs.Bool("uninstall", false, "remove hook entries (and docker compose down)")
		image          = fs.String("image", "ghcr.io/dong-qiu/agent-lens:latest", "container image for the server")
		skipCompose    = fs.Bool("skip-compose", false, "don't manage docker compose")
		healthzTimeout = fs.Duration("healthz-timeout", 60*time.Second, "how long to wait for /healthz")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, setupUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *personal && *projectOnly {
		fmt.Fprintln(os.Stderr, "agent-lens-hook setup: --personal and --project-only are mutually exclusive")
		os.Exit(2)
	}
	scope := scopePersonal
	if *projectOnly {
		scope = scopeProjectOnly
	}

	if err := doSetup(setupOpts{
		uninstall:      *uninstall,
		scope:          scope,
		image:          *image,
		skipCompose:    *skipCompose,
		healthzTimeout: *healthzTimeout,
		stdout:         os.Stdout,
		stderr:         os.Stderr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "agent-lens-hook setup: %v\n", err)
		os.Exit(2)
	}
}

type setupScope int

const (
	scopePersonal setupScope = iota
	scopeProjectOnly
)

type setupOpts struct {
	uninstall      bool
	scope          setupScope
	image          string
	skipCompose    bool
	healthzTimeout time.Duration
	// I/O writers — injected for testability
	stdout, stderr interface{ Write([]byte) (int, error) }
}

func doSetup(o setupOpts) error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	settingsPath, err := resolveSettingsPath(o.scope)
	if err != nil {
		return err
	}

	if o.uninstall {
		if err := unmergeAgentLensHooks(settingsPath); err != nil {
			return fmt.Errorf("unmerge hooks: %w", err)
		}
		_, _ = fmt.Fprintf(o.stdout, "removed agent-lens hook entries from %s\n", settingsPath)
		if !o.skipCompose && o.scope == scopePersonal {
			if err := composeDown(); err != nil {
				_, _ = fmt.Fprintf(o.stderr, "WARN: compose down failed: %v\n", err)
			} else {
				_, _ = fmt.Fprintf(o.stdout, "stopped docker compose stack\n")
			}
		}
		return nil
	}

	if err := mergeAgentLensHooks(settingsPath, binPath); err != nil {
		return fmt.Errorf("merge hooks: %w", err)
	}
	_, _ = fmt.Fprintf(o.stdout, "wired agent-lens hooks into %s\n", settingsPath)

	if o.scope == scopePersonal && !o.skipCompose {
		if err := checkDocker(); err != nil {
			return fmt.Errorf("docker check: %w", err)
		}
		composePath, err := writeComposeFile(o.image)
		if err != nil {
			return fmt.Errorf("write compose: %w", err)
		}
		_, _ = fmt.Fprintf(o.stdout, "wrote compose file to %s\n", composePath)

		if err := composeUp(composePath); err != nil {
			return fmt.Errorf("compose up: %w", err)
		}
		_, _ = fmt.Fprintf(o.stdout, "compose stack starting (postgres + minio + agent-lens)\n")

		if err := waitHealthz("http://localhost:8787/healthz", o.healthzTimeout); err != nil {
			return fmt.Errorf("healthz wait: %w (run `docker compose -f %s logs agent-lens` to debug)", err, composePath)
		}
		_, _ = fmt.Fprintf(o.stdout, "agent-lens healthy at http://localhost:8787\n")
	}

	_, _ = fmt.Fprintf(o.stdout, "\nSetup complete. Open http://localhost:8787 in a browser for the Lens UI.\n")
	return nil
}

// resolveSettingsPath returns the absolute path the hook config should
// live in for the chosen scope. The personal path is created if it
// doesn't exist; the project-only path requires .claude/ to already exist
// (since that signals the user has set up Claude Code for this repo).
func resolveSettingsPath(scope setupScope) (string, error) {
	if scope == scopeProjectOnly {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".claude", "settings.local.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// mergeAgentLensHooks reads settings (or starts fresh if missing),
// inserts an agent-lens-hook entry under each event in setupHookEvents
// IF that event doesn't already have one for our binary, and writes the
// result back atomically. Other tools' hook entries are preserved
// verbatim — only-add semantics, never delete or reorder.
func mergeAgentLensHooks(settingsPath, binPath string) error {
	settings, err := readSettings(settingsPath)
	if err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}

	for _, event := range setupHookEvents {
		matchers, _ := hooks[event].([]any)
		// Walk existing matchers; if any already has an agent-lens-hook
		// command, we're done with this event (idempotent).
		if alreadyOurs(matchers) {
			continue
		}
		matchers = append(matchers, map[string]any{
			"matcher": "*",
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": binPath + " claude",
					"timeout": 5,
				},
			},
		})
		hooks[event] = matchers
	}

	return writeSettings(settingsPath, settings)
}

// unmergeAgentLensHooks is the inverse of mergeAgentLensHooks. For each
// event, drop matchers whose first hook command is ours, leaving the
// rest untouched. If an event ends up with no matchers, the key is
// removed (so we don't leave empty arrays scattered).
func unmergeAgentLensHooks(settingsPath string) error {
	settings, err := readSettings(settingsPath)
	if err != nil {
		// Missing settings = nothing to remove; success.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}

	for _, event := range setupHookEvents {
		matchers, _ := hooks[event].([]any)
		filtered := matchers[:0]
		for _, m := range matchers {
			mm, _ := m.(map[string]any)
			if !matcherIsOurs(mm) {
				filtered = append(filtered, m)
			}
		}
		if len(filtered) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = filtered
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}

	return writeSettings(settingsPath, settings)
}

// alreadyOurs reports whether any matcher in the slice has at least one
// hook entry whose command points at agent-lens-hook.
func alreadyOurs(matchers []any) bool {
	for _, m := range matchers {
		if matcherIsOurs(m) {
			return true
		}
	}
	return false
}

// matcherIsOurs reports whether `m` (a single matcher object) has at
// least one hook entry that's ours. Used by both alreadyOurs (for
// idempotency) and unmergeAgentLensHooks (for removal).
func matcherIsOurs(m any) bool {
	mm, _ := m.(map[string]any)
	if mm == nil {
		return false
	}
	hooks, _ := mm["hooks"].([]any)
	for _, h := range hooks {
		hh, _ := h.(map[string]any)
		if hh == nil {
			continue
		}
		cmd, _ := hh["command"].(string)
		if commandIsAgentLensHook(cmd) {
			return true
		}
	}
	return false
}

// commandIsAgentLensHook matches a hook command line if its first
// space-separated token's basename equals "agent-lens-hook". Catches
// our binary regardless of installation path (go install / brew /
// release tarball) without false-positives on tools whose names happen
// to contain "agent-lens-hook" as a substring.
func commandIsAgentLensHook(cmd string) bool {
	if cmd == "" {
		return false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	return filepath.Base(fields[0]) == "agent-lens-hook"
}

// readSettings reads + parses the JSON settings file. A missing file
// (typical for fresh installs) returns an empty map, NOT an error.
func readSettings(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

// writeSettings serializes settings as pretty JSON and writes it to
// path atomically (write to <path>.tmp + rename) so a crash mid-write
// can't corrupt the user's settings.json.
func writeSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// checkDocker returns an error with an actionable hint if `docker` is
// missing or the daemon isn't reachable. We don't try to install or
// start docker — that's a setup step the user owns.
func checkDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("`docker` not found on PATH; install Docker Desktop / colima / orbstack first, then re-run setup")
	}
	cmd := exec.Command("docker", "info")
	cmd.Stderr = nil
	cmd.Stdout = nil
	if err := cmd.Run(); err != nil {
		return errors.New("`docker info` failed; the docker daemon may not be running. Start Docker Desktop / `colima start` / `orbstack start` and re-run setup")
	}
	return nil
}

// writeComposeFile materializes the embedded compose template at
// $HOME/.agent-lens/compose.yml with the {{IMAGE}} placeholder
// substituted. Returns the absolute path so callers can pass it to
// `docker compose -f ...`.
func writeComposeFile(image string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".agent-lens")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	contents := strings.ReplaceAll(setupComposeTemplate, "{{IMAGE}}", image)
	path := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func composeUp(path string) error {
	cmd := exec.Command("docker", "compose", "-f", path, "up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func composeDown() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".agent-lens", "compose.yml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	cmd := exec.Command("docker", "compose", "-f", path, "down")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitHealthz polls the URL with exponential-ish backoff until 200 or
// timeout. Returns the wrapped error from the LAST attempt so the
// user sees a representative failure mode rather than the very first
// "connection refused" before the server has bound its port.
func waitHealthz(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var lastErr error
	delay := 500 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timed out after %s: last error: %w", timeout, lastErr)
			}
			return fmt.Errorf("timed out after %s", timeout)
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s: last error: %w", timeout, lastErr)
		case <-time.After(delay):
		}
		if delay < 4*time.Second {
			delay *= 2
		}
	}
}
