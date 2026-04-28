package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// gitCommitShortShaRE matches the canonical line git emits at the end
// of a successful `git commit` (and `git commit --amend`):
//
//   [main abc1234] subject
//   [feature/foo abc1234] subject
//   [detached HEAD abc1234] subject
//   [main (root-commit) abc1234] initial commit
//
// The 7..40 hex range covers default short, custom core.abbrev, and
// the (rare) full-SHA case. We capture only the SHA. The branch label
// is permissive `[^\]]+` so spaces inside it (detached HEAD, root-commit
// annotation) match without enumerating the variants.
var gitCommitShortShaRE = regexp.MustCompile(`(?m)^\[[^\]]+ ([0-9a-f]{7,40})\] `)

// gitCommitRefsFromBash inspects a Claude Code Bash TOOL_RESULT and,
// when the underlying command was a successful `git commit`, returns
// `["git:<full-sha>"]` so the linker can stitch this Claude session to
// the commit's `git-<repoFingerprint>` session via shared-ref matching.
//
// The expansion to full SHA matters: the git-post-commit hook records
// `git rev-parse HEAD` (always full SHA), so a short-SHA ref from
// here would never join. If we cannot expand (cwd missing, SHA not
// resolvable), we silently return nil rather than emit a non-matching
// ref — a degraded ref that pretends to link is worse than no ref.
//
// `git commit` only — `git rebase` / `cherry-pick` / `tag` produce
// different SHA semantics and need their own treatment (out of scope
// for the Phase A linking).
func gitCommitRefsFromBash(toolInput, toolResponse json.RawMessage, cwd string) []string {
	if cwd == "" || len(toolInput) == 0 || len(toolResponse) == 0 {
		return nil
	}
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(toolInput, &input); err != nil {
		return nil
	}
	if !looksLikeGitCommit(input.Command) {
		return nil
	}
	var resp struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(toolResponse, &resp); err != nil {
		return nil
	}
	m := gitCommitShortShaRE.FindStringSubmatch(resp.Stdout)
	if m == nil {
		// Older git versions and some hook configurations route the
		// `[branch sha] subject` line to stderr.
		m = gitCommitShortShaRE.FindStringSubmatch(resp.Stderr)
	}
	if m == nil {
		return nil
	}
	short := m[1]
	full, err := gitOutput(cwd, "rev-parse", short)
	if err != nil || len(full) < 40 {
		return nil
	}
	return []string{"git:" + full}
}

// looksLikeGitCommit returns true iff some segment of the bash command
// invokes `git commit` (allowing for prefixed env, sudo, redirections,
// and shell separators). A naive `strings.Contains(cmd, "git commit")`
// would also fire on `echo "git commit"` style noise, so we tokenize
// on common shell separators and require a segment whose first word is
// `git` and second is `commit`.
func looksLikeGitCommit(cmd string) bool {
	for _, seg := range splitShellSegments(cmd) {
		f := strings.Fields(seg)
		// Skip leading env assignments and `sudo` / `nice` / `time` wrappers.
		i := 0
		for i < len(f) && (strings.Contains(f[i], "=") || isCommandPrefix(f[i])) {
			i++
		}
		if i+1 < len(f) && strings.HasSuffix(f[i], "git") && f[i+1] == "commit" {
			return true
		}
	}
	return false
}

func isCommandPrefix(token string) bool {
	switch token {
	case "sudo", "nice", "time", "env", "exec":
		return true
	}
	return false
}

// splitShellSegments breaks a command string on the operators that
// chain multiple commands so each segment can be inspected on its
// own. Doesn't aim for full POSIX shell parsing — just enough that
// `echo foo; git commit -m bar` and `... && git commit ...` both
// surface a `git commit` segment.
func splitShellSegments(cmd string) []string {
	repl := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	return strings.Split(repl.Replace(cmd), "\n")
}
