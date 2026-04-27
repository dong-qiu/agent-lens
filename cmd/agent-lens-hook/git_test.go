package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func runIn(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	runIn(t, dir, "git", "init", "-q", "--initial-branch=main")
	runIn(t, dir, "git", "config", "user.email", "test@example.com")
	runIn(t, dir, "git", "config", "user.name", "Test User")
	runIn(t, dir, "git", "config", "commit.gpgsign", "false")
	return dir
}

func TestBuildGitCommitEventInitialCommit(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "hello.txt")
	runIn(t, dir, "git", "commit", "-q", "-m", "first commit")

	ev, err := buildGitCommitEvent(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if ev["kind"] != "commit" {
		t.Errorf("kind = %v, want commit", ev["kind"])
	}
	actor := ev["actor"].(map[string]any)
	if actor["type"] != "human" || actor["id"] != "test@example.com" {
		t.Errorf("actor = %+v", actor)
	}
	payload := ev["payload"].(map[string]any)
	if payload["subject"] != "first commit" {
		t.Errorf("subject = %v, want first commit", payload["subject"])
	}
	if payload["branch"] != "main" {
		t.Errorf("branch = %v, want main", payload["branch"])
	}
	parents := payload["parents"].([]string)
	if len(parents) != 0 {
		t.Errorf("parents = %v, want empty (initial commit)", parents)
	}
	files := payload["files"].([]changedFile)
	if len(files) != 1 || files[0].Path != "hello.txt" {
		t.Errorf("files = %+v, want one entry hello.txt", files)
	}
	if files[0].Status != "A" {
		t.Errorf("file status = %v, want A", files[0].Status)
	}
	refs := ev["refs"].([]string)
	if len(refs) != 1 {
		t.Errorf("refs = %+v, want one git: ref", refs)
	}
}

func TestBuildGitCommitEventTwoCommitsParentLink(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "a.txt")
	runIn(t, dir, "git", "commit", "-q", "-m", "first")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "commit", "-q", "-am", "second")

	ev, err := buildGitCommitEvent(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	payload := ev["payload"].(map[string]any)
	parents := payload["parents"].([]string)
	if len(parents) != 1 {
		t.Errorf("parents = %v, want exactly one parent", parents)
	}
	files := payload["files"].([]changedFile)
	if len(files) != 1 || files[0].Status != "M" {
		t.Errorf("files = %+v, want one M entry", files)
	}
}

func TestBuildGitCommitEventOutsideRepoFails(t *testing.T) {
	gitAvailable(t)
	if _, err := buildGitCommitEvent(t.TempDir()); err == nil {
		t.Error("expected error outside repo, got nil")
	}
}

func TestGitSessionIDStableForSameRepo(t *testing.T) {
	a := gitSessionID("/some/path")
	b := gitSessionID("/some/path")
	c := gitSessionID("/other/path")
	if a != b {
		t.Errorf("same path produced different IDs: %s vs %s", a, b)
	}
	if a == c {
		t.Errorf("different paths produced same ID: %s", a)
	}
}
