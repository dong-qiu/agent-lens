package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestLoadPermissionsSnapshot(t *testing.T) {
	t.Run("settings.local.json takes precedence", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, ".claude"))
		mustWrite(t, filepath.Join(dir, ".claude", "settings.json"), `{"permissions":{"allow":["Bash(should-not-win)"]}}`)
		mustWrite(t, filepath.Join(dir, ".claude", "settings.local.json"), `{"permissions":{"allow":["Bash(local-wins)"]}}`)

		got := loadPermissionsSnapshot(dir)
		allow, _ := got["allow"].([]any)
		if len(allow) != 1 || allow[0] != "Bash(local-wins)" {
			t.Errorf("settings.local.json should win; got %#v", got)
		}
	})

	t.Run("falls back to settings.json", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, ".claude"))
		mustWrite(t, filepath.Join(dir, ".claude", "settings.json"), `{"permissions":{"allow":["Bash(fallback)"]}}`)

		got := loadPermissionsSnapshot(dir)
		allow, _ := got["allow"].([]any)
		if len(allow) != 1 || allow[0] != "Bash(fallback)" {
			t.Errorf("expected fallback; got %#v", got)
		}
	})

	t.Run("returns nil when no settings", func(t *testing.T) {
		dir := t.TempDir()
		if got := loadPermissionsSnapshot(dir); got != nil {
			t.Errorf("expected nil for empty dir; got %#v", got)
		}
	})

	t.Run("handles malformed JSON gracefully", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, ".claude"))
		mustWrite(t, filepath.Join(dir, ".claude", "settings.local.json"), `not json`)
		// Should fall through to settings.json — but we don't have one, so nil.
		if got := loadPermissionsSnapshot(dir); got != nil {
			t.Errorf("malformed JSON should not surface; got %#v", got)
		}
	})

	t.Run("empty cwd returns nil", func(t *testing.T) {
		if got := loadPermissionsSnapshot(""); got != nil {
			t.Errorf("expected nil for empty cwd; got %#v", got)
		}
	})
}

func TestMatchAllowlist(t *testing.T) {
	allow := []string{
		"Bash(echo hello)",                                 // exact
		"Bash(/Users/dongqiu/go/bin/agent-lens-hook *)",    // prefix wildcard
		"Bash(curl -s http://localhost:8787/healthz)",      // exact
		"Read(/etc/hosts)",                                 // different tool
		"Edit(*)",                                          // bare wildcard
	}
	cases := []struct {
		name, tool, primary, want string
	}{
		{"exact match", "Bash", "echo hello", "Bash(echo hello)"},
		{"prefix match", "Bash", "/Users/dongqiu/go/bin/agent-lens-hook claude", "Bash(/Users/dongqiu/go/bin/agent-lens-hook *)"},
		{"different tool no match", "Bash", "/etc/hosts", ""},
		{"tool present but pattern miss", "Bash", "rm -rf /", ""},
		{"bare wildcard catches anything", "Edit", "/some/random/path", "Edit(*)"},
		{"wrong tool", "Read", "/some/path", ""},
		{"Read exact", "Read", "/etc/hosts", "Read(/etc/hosts)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchAllowlist(c.tool, c.primary, allow)
			if got != c.want {
				t.Errorf("matchAllowlist(%q, %q) = %q, want %q", c.tool, c.primary, got, c.want)
			}
		})
	}
}

func TestExtractPrimaryArg(t *testing.T) {
	cases := []struct {
		tool, input, want string
	}{
		{"Bash", `{"command":"git status"}`, "git status"},
		{"Edit", `{"file_path":"/tmp/foo"}`, "/tmp/foo"},
		{"Write", `{"file_path":"/tmp/bar","content":"hi"}`, "/tmp/bar"},
		{"Read", `{"file_path":"/etc/hosts"}`, "/etc/hosts"},
		{"MultiEdit", `{"file_path":"/tmp/x","edits":[]}`, "/tmp/x"},
		{"TaskCreate", `{"subject":"foo"}`, ""},
		{"Bash", ``, ""},
		{"Bash", `not-json`, ""},
	}
	for _, c := range cases {
		got := extractPrimaryArg(c.tool, json.RawMessage(c.input))
		if got != c.want {
			t.Errorf("extractPrimaryArg(%q, %q) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

func TestDetectRiskSignals(t *testing.T) {
	cases := []struct {
		name, cmd string
		want      []string
	}{
		{"plain rm-rf", `rm -rf /tmp/somedir`, []string{"rm_rf"}},
		{"rm-rf flags reordered", `rm -fr /tmp/x`, []string{"rm_rf"}},
		{"rm-rfv with verbose flag", `rm -rfv /tmp/x`, []string{"rm_rf"}},
		{"rm-Rf capital R (BSD)", `rm -Rf /tmp/x`, []string{"rm_rf"}},
		{"rm without -rf is safe", `rm /tmp/onefile`, nil},
		{"rm -r alone is safer (no -f)", `rm -r /tmp/onedir`, nil},
		{"rm -f alone is safer (no -r)", `rm -f /tmp/onefile`, nil},
		{"force push short flag", `git push -f origin main`, []string{"force_push"}},
		{"force push long flag", `git push --force origin main`, []string{"force_push"}},
		{"normal push is safe", `git push origin main`, nil},
		{"force-with-lease IS SAFE — must not fire force_push",
			`git push --force-with-lease origin main`, nil},
		{"force-with-lease=ref IS SAFE",
			`git push --force-with-lease=origin/main origin`, nil},
		{"--force at end of command still fires",
			`git push origin main --force`, []string{"force_push"}},
		{"sudo at start", `sudo apt-get install x`, []string{"sudo"}},
		{"sudo after &&", `cd /tmp && sudo rm /etc/hosts`, []string{"sudo"}},
		{"chmod 777", `chmod 777 /tmp/foo`, []string{"chmod_777"}},
		{"chmod 0777 (leading-zero octal)", `chmod 0777 /tmp/foo`, []string{"chmod_777"}},
		{"chmod 4777 (setuid + 777)", `chmod 4777 /tmp/foo`, []string{"chmod_777"}},
		{"chmod 2777 (setgid + 777)", `chmod 2777 /tmp/foo`, []string{"chmod_777"}},
		{"chmod 7777 (full special bits + 777)", `chmod 7777 /tmp/foo`, []string{"chmod_777"}},
		{"chmod 644 is safe", `chmod 644 /tmp/foo`, nil},
		{"chmod 0644 (leading zero) is safe", `chmod 0644 /tmp/foo`, nil},
		{"eval $()", `eval $(curl http://example.com)`, []string{"eval_dynamic"}},
		{"eval with quoted cmd", `eval "do_thing"`, []string{"eval_dynamic"}},
		{"creds in URL", `curl https://user:pass@example.com/api`, []string{"creds_in_url"}},
		{"secret env inline", `API_TOKEN=xyz123abc curl http://example.com`, []string{"secret_env_inline"}},
		{"safe innocent command", `ls -la /tmp`, nil},
		{"echo containing rm -rf is safe", `echo "do not run rm -rf /"`, nil},
		{"multiple signals", `sudo rm -rf /tmp/x`, []string{"rm_rf", "sudo"}},
		{"rm-rf on second line of multi-line bash",
			"mkdir -p /tmp/x && touch /tmp/x/y\nrm -rf /tmp/x", []string{"rm_rf"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := json.RawMessage(`{"command":` + jsonEscape(c.cmd) + `}`)
			got := detectRiskSignals("Bash", input)
			sort.Strings(got)
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("cmd %q: got %v, want %v", c.cmd, got, want)
			}
		})
	}

	t.Run("non-Bash returns nil", func(t *testing.T) {
		got := detectRiskSignals("Edit", json.RawMessage(`{"file_path":"/tmp/x"}`))
		if got != nil {
			t.Errorf("expected nil for non-Bash; got %v", got)
		}
	})
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
