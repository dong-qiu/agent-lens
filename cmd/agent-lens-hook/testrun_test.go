package main

import (
	"encoding/json"
	"testing"
)

func TestRecognizeRunnerPositive(t *testing.T) {
	cases := map[string]string{
		"go test ./...":            "go test",
		"make test":                "make test",
		"make test-integration":    "make test-integration",
		"npm test":                 "npm test",
		"npm run test:unit":        "npm test",
		"pnpm test":                "pnpm test",
		"pytest -q":                "pytest",
		"cargo test":               "cargo test",
		"jest --watch=false":       "jest",
		"vitest run":               "vitest",
		"npx vitest run":           "vitest",
		"poetry run pytest tests/": "pytest",
		"uv run pytest":            "pytest",
		"sudo pytest":              "pytest",
		"env CI=1 go test ./...":   "go test",
		"cd web && npm test":       "npm test",
		"cd x && go test ./...":    "go test",
		`bash -lc "go test ./..."`: "go test",
		"python -m pytest":         "pytest",
		"time go test ./...":       "go test",
		"dotnet test":              "dotnet test",
	}
	for cmd, want := range cases {
		if got := recognizeRunner(cmd); got != want {
			t.Errorf("recognizeRunner(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestRecognizeRunnerRejectsNonExec(t *testing.T) {
	// Commands that mention a runner but don't execute one → no test_run.
	for _, cmd := range []string{
		`echo "run go test before pushing"`,
		"cat test_output.log",
		"grep FAIL test.log",
		`git log --grep="pytest"`,
		"rg 'go test' Makefile",
		"make build",
		"make lint",
		"ls test/",
		"go build ./...",
		"npm run lint",
	} {
		if got := recognizeRunner(cmd); got != "" {
			t.Errorf("recognizeRunner(%q) = %q, want \"\" (should reject)", cmd, got)
		}
	}
}

func TestParseTestResultGoPass(t *testing.T) {
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"stdout":"ok  \tgithub.com/x/y\t0.5s\nok  \tgithub.com/x/z\t0.1s\n"}`), p)
	if p["outcome"] != "pass" {
		t.Errorf("outcome = %v, want pass", p["outcome"])
	}
}

func TestParseTestResultGoFail(t *testing.T) {
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"stdout":"--- FAIL: TestX (0.00s)\nFAIL\tgithub.com/x/y\t0.2s\n"}`), p)
	if p["outcome"] != "fail" {
		t.Errorf("outcome = %v, want fail", p["outcome"])
	}
}

func TestParseTestResultPytestCounts(t *testing.T) {
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"stdout":"=== 2 failed, 5 passed, 1 skipped in 0.3s ==="}`), p)
	if p["outcome"] != "fail" {
		t.Errorf("outcome = %v, want fail", p["outcome"])
	}
	if p["failed"] != 2 {
		t.Errorf("failed = %v, want 2", p["failed"])
	}
	if p["passed"] != 5 {
		t.Errorf("passed = %v, want 5", p["passed"])
	}
	if p["skipped"] != 1 {
		t.Errorf("skipped = %v, want 1", p["skipped"])
	}
}

func TestParseTestResultPytestAllPass(t *testing.T) {
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"stdout":"=== 7 passed in 1.2s ==="}`), p)
	if p["outcome"] != "pass" {
		t.Errorf("outcome = %v, want pass", p["outcome"])
	}
	if p["passed"] != 7 {
		t.Errorf("passed = %v, want 7", p["passed"])
	}
}

func TestParseTestResultInterruptedNoVerdict(t *testing.T) {
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"interrupted":true,"stdout":"ok  \tfoo\n"}`), p)
	if _, ok := p["outcome"]; ok {
		t.Errorf("interrupted run should have no outcome, got %v", p["outcome"])
	}
	if p["interrupted"] != true {
		t.Errorf("interrupted flag not set")
	}
}

func TestParseTestResultAmbiguousRanOnly(t *testing.T) {
	// Output with no clear pass/fail signal → no outcome (ran only).
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"stdout":"running suite...\ndone\n"}`), p)
	if _, ok := p["outcome"]; ok {
		t.Errorf("ambiguous output should leave outcome unset, got %v", p["outcome"])
	}
}

func TestParseTestResultExitCode(t *testing.T) {
	p := map[string]any{}
	parseTestResult(json.RawMessage(`{"stdout":"FAIL\n","exit_code":1}`), p)
	if p["exit_code"] != 1 {
		t.Errorf("exit_code = %v, want 1", p["exit_code"])
	}
}

func TestBuildEventsBashTestRunDerivesTestRun(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PostToolUse",
		SessionID:     "s1",
		CWD:           "/repo",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"go test ./..."}`),
		ToolResponse:  json.RawMessage(`{"stdout":"ok  \tx\t0.1s\n"}`),
	})
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (tool_result + test_run): %+v", len(evs), evs)
	}
	if evs[0]["kind"] != "tool_result" || evs[1]["kind"] != "test_run" {
		t.Fatalf("kinds = %v / %v, want tool_result / test_run", evs[0]["kind"], evs[1]["kind"])
	}
	p := evs[1]["payload"].(map[string]any)
	if p["runner"] != "go test" {
		t.Errorf("runner = %v, want go test", p["runner"])
	}
	if p["command"] != "go test ./..." {
		t.Errorf("command = %v, want verbatim", p["command"])
	}
	if p["confidence"] != "inferred" {
		t.Errorf("confidence = %v, want inferred", p["confidence"])
	}
	if p["outcome"] != "pass" {
		t.Errorf("outcome = %v, want pass", p["outcome"])
	}
	if actor := evs[1]["actor"].(map[string]any); actor["type"] != "agent" {
		t.Errorf("test_run actor = %v, want agent", actor)
	}
}

func TestBuildEventsBashNonTestNoTestRun(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PostToolUse",
		SessionID:     "s1",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"go build ./..."}`),
		ToolResponse:  json.RawMessage(`{"stdout":""}`),
	})
	if len(evs) != 1 || evs[0]["kind"] != "tool_result" {
		t.Errorf("non-test Bash should yield only tool_result, got %+v", evs)
	}
}

func TestBuildEventsNonBashNoTestRun(t *testing.T) {
	evs, _ := buildEvents(&claudeHookInput{
		HookEventName: "PostToolUse",
		SessionID:     "s1",
		ToolName:      "Edit",
		ToolResponse:  json.RawMessage(`{"ok":true}`),
	})
	if len(evs) != 1 || evs[0]["kind"] != "tool_result" {
		t.Errorf("non-Bash should yield only tool_result, got %+v", evs)
	}
}
