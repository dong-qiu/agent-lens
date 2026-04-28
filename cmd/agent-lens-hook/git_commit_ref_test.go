package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeGitCommit(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
		why  string
	}{
		{"git commit -m foo", true, "plain commit"},
		{"git commit --amend --no-edit", true, "amend"},
		{"git commit --no-verify -m hi", true, "with flag"},
		{"/usr/bin/git commit -m hi", true, "absolute git path"},
		{"GIT_COMMITTER_DATE='2026-01-01' git commit -m foo", true, "leading env"},
		{"sudo git commit -m hi", true, "sudo prefix"},
		{"echo done && git commit -m foo", true, "after &&"},
		{"git status; git commit -m foo", true, "after semicolon"},
		{"git status | grep . && git commit", true, "after pipe + &&"},

		{"echo \"git commit\"", false, "literal in echo"},
		{"git status", false, "different subcommand"},
		{"git commit-tree", false, "different subcommand starting with commit"},
		{"./bin/agentcommit -m foo", false, "unrelated tool with substring"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		got := looksLikeGitCommit(c.cmd)
		if got != c.want {
			t.Errorf("looksLikeGitCommit(%q) = %v, want %v (%s)", c.cmd, got, c.want, c.why)
		}
	}
}

func TestGitCommitShortShaRE(t *testing.T) {
	cases := []struct {
		stdout string
		want   string
	}{
		{"[main abc1234] subject\n 1 file changed, 0 insertions(+)\n", "abc1234"},
		{"[feature/foo abcdef0] add stuff\n", "abcdef0"},
		{"[detached HEAD 1a2b3c4] checkout-time commit\n", "1a2b3c4"},
		{"[main (root-commit) 0123456] initial\n", "0123456"},
		{"[release-1.0 deadbeefcafe1234567890abcdef123456789012] tagged\n", "deadbeefcafe1234567890abcdef123456789012"},

		{"On branch main\nnothing to commit, working tree clean\n", ""},
		{"error: pathspec did not match any files\n", ""},
		{"foo [main abc1234] but not at line start\n", ""},
		{"", ""},
	}
	for _, c := range cases {
		m := gitCommitShortShaRE.FindStringSubmatch(c.stdout)
		var got string
		if m != nil {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("regex on %q: got %q, want %q", c.stdout, got, c.want)
		}
	}
}

// TestGitCommitRefsFromBash_NoMatch covers the negative cases where
// the helper must NOT emit a ref: non-Bash payloads, non-commit
// commands, failed commits with no SHA in output, and missing cwd.
func TestGitCommitRefsFromBash_NoMatch(t *testing.T) {
	cwd, _ := os.Getwd()
	cases := []struct {
		name     string
		input    string
		response string
		cwd      string
	}{
		{"empty cwd",
			`{"command":"git commit -m hi"}`,
			`{"stdout":"[main abc1234] hi\n"}`,
			""},
		{"non-commit command",
			`{"command":"git status"}`,
			`{"stdout":"On branch main\n"}`,
			cwd},
		{"echo-noise command",
			`{"command":"echo \"git commit\""}`,
			`{"stdout":"git commit\n"}`,
			cwd},
		{"failed commit (pre-commit reject)",
			`{"command":"git commit -m hi"}`,
			`{"stdout":"","stderr":"hook rejected\n"}`,
			cwd},
		{"empty input",
			``, ``, cwd},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := gitCommitRefsFromBash(json.RawMessage(c.input), json.RawMessage(c.response), c.cwd)
			if got != nil {
				t.Errorf("expected no refs, got %v", got)
			}
		})
	}
}

// TestGitCommitRefsFromBash_RealRepo creates a tiny throwaway git
// repo, makes a real commit, then feeds the captured short-SHA back
// through the helper and verifies the returned ref expands to the
// full SHA we just produced. This is the only positive-path test
// because everything below the regex (rev-parse) needs a real repo.
func TestGitCommitRefsFromBash_RealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Force a known identity so commit doesn't depend on user config.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	out := run("commit", "-m", "first")

	wantFull := strings.TrimSpace(run("rev-parse", "HEAD"))

	input := json.RawMessage(`{"command":"git commit -m first"}`)
	resp, _ := json.Marshal(map[string]string{"stdout": out, "stderr": ""})

	refs := gitCommitRefsFromBash(input, resp, dir)
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1: %v", len(refs), refs)
	}
	want := "git:" + wantFull
	if refs[0] != want {
		t.Errorf("ref = %q, want %q", refs[0], want)
	}
}
