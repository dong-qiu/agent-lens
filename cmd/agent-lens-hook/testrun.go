package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// testrun.go implements ADR 0011: recognise a Bash PostToolUse that ran a test
// suite and derive a `test_run` event alongside the tool_result. Recognition is
// anchored to the command's *effective* first token (after stripping wrappers /
// launchers) and explicitly rejects non-executing commands that merely mention
// a runner name (echo/cat/grep/…). Result parsing is conservative: pass/fail is
// filled only when a runner's output says so unambiguously — otherwise the event
// records `ran` only. Every derived verdict is confidence=inferred (D5).

// makeTestRun returns a `test_run` event for a recognised test command, or nil.
// Fail-soft throughout: any parse miss degrades to "ran only", never a guess.
func makeTestRun(in *claudeHookInput) map[string]any {
	cmd := bashCommand(in.ToolInput)
	if cmd == "" {
		return nil
	}
	runner := recognizeRunner(cmd)
	if runner == "" {
		return nil
	}
	payload := map[string]any{
		"runner":     runner,
		"command":    cmd, // verbatim — same redaction posture as tool_result (D4)
		"confidence": "inferred",
		"ran":        true,
	}
	if in.CWD != "" {
		payload["cwd"] = in.CWD
	}
	parseTestResult(in.ToolResponse, payload)
	return baseEvent(in, agentActor(), "test_run", payload)
}

func bashCommand(toolInput json.RawMessage) string {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(toolInput, &input); err != nil {
		return ""
	}
	return strings.TrimSpace(input.Command)
}

// --- recognition -----------------------------------------------------------

// rejectLeads are commands that read / print / search but do NOT execute a test
// suite, even when a runner name appears in their arguments (e.g.
// `echo "run go test"`, `grep FAIL log`, `git log --grep=pytest`). A command
// whose effective lead is one of these never yields a test_run.
var rejectLeads = map[string]bool{
	"echo": true, "cat": true, "grep": true, "rg": true, "printf": true,
	"git": true, "ls": true, "find": true, "head": true, "tail": true,
	"sed": true, "awk": true, "less": true, "which": true,
}

// wrapperLeads are transparent prefixes stripped to reach the real command
// (e.g. `sudo go test`, `time pytest`, `env A=B go test`).
var wrapperLeads = map[string]bool{
	"sudo": true, "time": true, "nice": true, "nohup": true,
	"env": true, "xargs": true, "command": true, "exec": true,
}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// recognizeRunner returns a runner label if cmd invokes a known test runner,
// else "". It scans each &&/;/| -joined segment, normalises its leading tokens
// (strip env-assignments and wrappers, unwrap launchers), rejects non-executing
// leads, and matches the runner table. First match wins.
func recognizeRunner(cmd string) string {
	for _, seg := range splitSegments(cmd) {
		if r := runnerOfSegment(seg); r != "" {
			return r
		}
	}
	return ""
}

// splitSegments breaks a command line on shell sequencing operators so that
// `cd x && go test` is examined segment-by-segment. Quotes are not parsed (a
// best-effort split); launcher unwrapping (bash -c "…") is handled separately.
func splitSegments(cmd string) []string {
	parts := regexp.MustCompile(`&&|\|\||;|\|`).Split(cmd, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runnerOfSegment(seg string) string {
	toks := strings.Fields(seg)
	// Strip leading env-assignments and transparent wrappers.
	for len(toks) > 0 && (envAssign.MatchString(toks[0]) || wrapperLeads[toks[0]]) {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return ""
	}
	// Unwrap a launcher: take the inner command's leading tokens.
	if inner := unwrapLauncher(toks); inner != nil {
		toks = inner
		if len(toks) == 0 {
			return ""
		}
	}
	if rejectLeads[toks[0]] {
		return ""
	}
	return matchRunner(toks)
}

// unwrapLauncher peels runner-launchers so the wrapped command is matched:
// `npx jest` → [jest], `poetry run pytest` → [pytest], `bash -lc "go test"` →
// [go test]. Returns nil when toks isn't a launcher invocation.
func unwrapLauncher(toks []string) []string {
	switch toks[0] {
	case "npx":
		return toks[1:]
	case "pnpm", "yarn", "poetry", "pdm", "uv", "rye":
		// pnpm exec X / yarn dlx X / poetry run X / uv run X
		if len(toks) >= 3 && (toks[1] == "exec" || toks[1] == "dlx" || toks[1] == "run") {
			return toks[2:]
		}
	case "bash", "sh", "zsh":
		// bash -lc "go test ./..." → the quoted script. Rejoin from the first
		// non-flag token so the inner command survives the Fields() split.
		for i := 1; i < len(toks); i++ {
			if strings.HasPrefix(toks[i], "-") {
				continue
			}
			inner := strings.Trim(strings.Join(toks[i:], " "), `"'`)
			return strings.Fields(inner)
		}
	case "python", "python3":
		// python -m pytest
		if len(toks) >= 3 && toks[1] == "-m" {
			return toks[2:]
		}
	}
	return nil
}

// matchRunner maps normalised leading tokens to a runner label. Extend this
// table to teach new runners (D5); recognition is intentionally non-exhaustive
// and confidence stays `inferred`.
func matchRunner(toks []string) string {
	lead := toks[0]
	second := ""
	if len(toks) > 1 {
		second = toks[1]
	}
	switch lead {
	case "go":
		if second == "test" {
			return "go test"
		}
	case "make":
		// `make test`, `make test-integration`, `make test-unit` … any target
		// whose name contains "test". (R11: a test-named target that actually
		// lints is an accepted false-positive; confidence=inferred covers it.)
		if strings.Contains(second, "test") {
			return "make " + second
		}
	case "npm", "pnpm", "yarn", "bun":
		if second == "test" || second == "t" ||
			(second == "run" && len(toks) > 2 && strings.Contains(toks[2], "test")) {
			return lead + " test"
		}
	case "pytest", "py.test":
		return "pytest"
	case "cargo":
		if second == "test" || second == "nextest" {
			return "cargo test"
		}
	case "jest", "vitest", "ctest", "tox", "mocha", "ava", "rspec", "phpunit":
		return lead
	case "gradle", "./gradlew", "gradlew":
		if strings.Contains(second, "test") || strings.Contains(second, "check") {
			return "gradle test"
		}
	case "dotnet":
		if second == "test" {
			return "dotnet test"
		}
	case "deno":
		if second == "test" {
			return "deno test"
		}
	}
	return ""
}

// --- result parsing --------------------------------------------------------

var (
	reGoFail   = regexp.MustCompile(`(?m)^(--- FAIL|FAIL\b|FAIL\t)`)
	reGoOK     = regexp.MustCompile(`(?m)^(ok\s|PASS\b)`)
	rePyPassed = regexp.MustCompile(`(\d+) passed`)
	rePyFailed = regexp.MustCompile(`(\d+) failed`)
	rePyError  = regexp.MustCompile(`(\d+) error`)
	rePySkip   = regexp.MustCompile(`(\d+) skipped`)
	reGenFail  = regexp.MustCompile(`(?i)\b(\d+) (failing|failed)\b`)
	reGenPass  = regexp.MustCompile(`(?i)\b(\d+) (passing|passed)\b`)
)

// parseTestResult reads the Bash tool_response and fills outcome / counts on the
// payload when the output says so unambiguously. Conservative: it never invents
// a pass/fail; ambiguous output leaves the event as `ran` only. exit_code is
// captured opportunistically (its presence/shape is harness-dependent — ADR
// §验证), but is not the primary signal since pipes/tee can mask it.
func parseTestResult(toolResponse json.RawMessage, payload map[string]any) {
	if len(toolResponse) == 0 {
		return
	}
	var resp map[string]any
	if err := json.Unmarshal(toolResponse, &resp); err != nil {
		return
	}
	if b, ok := resp["interrupted"].(bool); ok && b {
		payload["interrupted"] = true
		return // interrupted run → no verdict
	}
	for _, k := range []string{"exit_code", "exitCode", "code", "returnCode", "status"} {
		if v, ok := resp[k].(float64); ok {
			payload["exit_code"] = int(v)
			break
		}
	}
	var text strings.Builder
	for _, k := range []string{"stdout", "stderr", "output"} {
		if s, ok := resp[k].(string); ok {
			text.WriteString(s)
			text.WriteString("\n")
		}
	}
	out := text.String()
	if strings.TrimSpace(out) == "" {
		return
	}

	failed, hasFailed := firstCount(out, rePyFailed, reGenFail)
	passed, hasPassed := firstCount(out, rePyPassed, reGenPass)
	if errs, ok := matchCount(out, rePyError); ok {
		failed += errs
		hasFailed = true
	}
	if sk, ok := matchCount(out, rePySkip); ok {
		payload["skipped"] = sk
	}

	switch {
	case hasFailed && failed > 0:
		setOutcome(payload, "fail", passed, failed, hasPassed)
	case reGoFail.MatchString(out):
		setOutcome(payload, "fail", passed, failed, hasPassed)
	case hasFailed && failed == 0 && hasPassed:
		setOutcome(payload, "pass", passed, failed, hasPassed)
	case reGoOK.MatchString(out) && !reGoFail.MatchString(out):
		setOutcome(payload, "pass", passed, failed, hasPassed)
	case hasPassed && !hasFailed:
		setOutcome(payload, "pass", passed, failed, hasPassed)
	}
	// else: leave as `ran` only — no confident verdict.
}

func setOutcome(payload map[string]any, outcome string, passed, failed int, hasPassed bool) {
	payload["outcome"] = outcome
	if hasPassed {
		payload["passed"] = passed
	}
	if failed > 0 {
		payload["failed"] = failed
	}
}

func firstCount(s string, res ...*regexp.Regexp) (int, bool) {
	for _, re := range res {
		if n, ok := matchCount(s, re); ok {
			return n, true
		}
	}
	return 0, false
}

func matchCount(s string, re *regexp.Regexp) (int, bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	return n, true
}
