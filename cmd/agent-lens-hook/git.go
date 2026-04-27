package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runGitPostCommit is invoked from a git post-commit hook (typically
// `.git/hooks/post-commit`). It reads the just-created commit's metadata
// via `git` subprocess calls and forwards a `commit` event. Always exits
// 0 so the hook never breaks the user's git workflow.
func runGitPostCommit(_ []string) {
	cwd, err := os.Getwd()
	if err != nil {
		warn("getwd: %v", err)
		os.Exit(0)
	}
	ev, err := buildGitCommitEvent(cwd)
	if err != nil {
		warn("git commit event: %v", err)
		os.Exit(0)
	}
	if err := newTransport().Send([]map[string]any{ev}, ev["session_id"].(string)); err != nil {
		warn("send: %v", err)
	}
	os.Exit(0)
}

// buildGitCommitEvent shells out to `git` in workdir to assemble a wire
// event describing HEAD. Each git invocation is independent so any one
// returning empty (e.g. `%b` with no body) is benign.
func buildGitCommitEvent(workdir string) (map[string]any, error) {
	repo, err := gitOutput(workdir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("rev-parse repo: %w", err)
	}
	sha, err := gitOutput(workdir, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	if sha == "" {
		return nil, errors.New("empty HEAD sha")
	}

	author, _ := gitOutput(workdir, "log", "-1", "--pretty=%an")
	email, _ := gitOutput(workdir, "log", "-1", "--pretty=%ae")
	subject, _ := gitOutput(workdir, "log", "-1", "--pretty=%s")
	body, _ := gitOutput(workdir, "log", "-1", "--pretty=%b")
	parentsLine, _ := gitOutput(workdir, "log", "-1", "--pretty=%P")
	branch, _ := gitOutput(workdir, "rev-parse", "--abbrev-ref", "HEAD")

	var parents []string
	if parentsLine != "" {
		parents = strings.Fields(parentsLine)
	}
	files, _ := gitChangedFiles(workdir, sha)

	return map[string]any{
		"ts":         time.Now().UTC().Format(time.RFC3339Nano),
		"session_id": gitSessionID(repo),
		"actor":      map[string]any{"type": "human", "id": email},
		"kind":       "commit",
		"payload": map[string]any{
			"sha":     sha,
			"subject": subject,
			"body":    body,
			"branch":  branch,
			"parents": parents,
			"files":   files,
			"repo":    repo,
			"author": map[string]string{
				"name":  author,
				"email": email,
			},
		},
		"refs": []string{"git:" + sha},
	}, nil
}

type changedFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// gitChangedFiles lists the files touched by sha. `git show
// --name-status --pretty=format:` handles both root and non-root
// commits uniformly, unlike `git diff-tree` which needs a parent.
func gitChangedFiles(workdir, sha string) ([]changedFile, error) {
	out, err := gitOutput(workdir, "show", "--name-status", "--pretty=format:", sha)
	if err != nil {
		return nil, err
	}
	var files []changedFile
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) < 2 {
			continue
		}
		files = append(files, changedFile{Status: fields[0], Path: fields[1]})
	}
	return files, nil
}

func gitOutput(workdir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n\r "), nil
}

// gitSessionID derives a stable per-repo session ID from the repo root
// path. Linking to actual Claude Code sessions is deferred to the M2
// linking worker (which correlates by cwd + timestamp).
func gitSessionID(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return "git-" + hex.EncodeToString(h[:8])
}
