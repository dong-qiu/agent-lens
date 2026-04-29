package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// loadPermissionsSnapshot reads the project-local Claude Code settings
// to extract the `permissions` block (allow / deny lists) in effect for
// this session. Returns nil if no settings file is present or the
// `permissions` field is absent — callers must tolerate that.
//
// settings.local.json takes precedence over settings.json so a user's
// local override is the authoritative policy view in the audit.
// User-global ~/.claude/settings.json is intentionally NOT merged: it's
// rarely the audit-relevant policy (operators care about what was in
// the *project repo*) and merging would muddy the snapshot.
func loadPermissionsSnapshot(cwd string) map[string]any {
	if cwd == "" {
		return nil
	}
	candidates := []string{
		filepath.Join(cwd, ".claude", "settings.local.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			Permissions map[string]any `json:"permissions"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		if doc.Permissions != nil {
			return doc.Permissions
		}
	}
	return nil
}

// allowlistEntryRE captures `<ToolName>(<pattern>)` from a Claude Code
// permissions.allow string. The inner pattern can contain anything,
// including escaped quotes and parens — we deliberately match
// non-greedy up to the *last* `)` so nested-paren commands still parse
// (e.g. `Bash(echo $(date))`).
var allowlistEntryRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\((.*)\)$`)

// matchAllowlist scans permissions.allow for the first entry whose
// tool name and inner pattern match this tool call, returning the
// matched entry (verbatim) or "" if nothing matched. A non-empty
// return implies "auto-allowed by policy"; "" implies "no rule matched
// → user must have approved interactively (or via the deny list, but
// deny would have stopped PreToolUse from firing entirely)".
//
// Matching is intentionally simpler than Claude Code's internal logic:
// trailing `*` = prefix match against the input string, otherwise
// exact match. Mismatches between this and Claude Code's matcher will
// just under-attribute (we'll mark "user-approved" when Claude Code
// would have matched) — never over-attribute, which is the audit-safe
// failure mode.
func matchAllowlist(toolName, primaryArg string, allow []string) string {
	for _, entry := range allow {
		m := allowlistEntryRE.FindStringSubmatch(entry)
		if m == nil {
			continue
		}
		entryTool, pattern := m[1], m[2]
		if entryTool != toolName {
			continue
		}
		if strings.HasSuffix(pattern, "*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(primaryArg, prefix) {
				return entry
			}
		} else if pattern == primaryArg {
			return entry
		}
	}
	return ""
}

// extractPrimaryArg pulls the user-meaningful argument from a tool's
// input that the allowlist pattern would match against. For Bash that's
// the `command` string; for file-mutating tools it's the `file_path`.
// Returns "" when we can't determine — matchAllowlist short-circuits
// safely on "" so unknown shapes simply fall through to "user-approved".
func extractPrimaryArg(toolName string, toolInput json.RawMessage) string {
	if len(toolInput) == 0 {
		return ""
	}
	switch toolName {
	case "Bash":
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(toolInput, &p)
		return p.Command
	case "Read", "Edit", "Write", "MultiEdit":
		var p struct {
			FilePath string `json:"file_path"`
		}
		_ = json.Unmarshal(toolInput, &p)
		return p.FilePath
	}
	return ""
}

// detectRiskSignals scans the tool input for high-risk patterns. v1
// covers Bash; other tools return an empty slice. Each returned signal
// is a short stable identifier suitable for indexing / filtering by
// audit dashboards (e.g. "rm_rf", "force_push") rather than a
// human-readable description.
//
// Patterns are conservative: a signal only fires when the pattern is
// unambiguously risky in the bash command. Non-anchored substring
// matches (which can false-positive on echo "rm -rf example" etc) are
// avoided in favour of word-boundary checks.
func detectRiskSignals(toolName string, toolInput json.RawMessage) []string {
	if toolName != "Bash" || len(toolInput) == 0 {
		return nil
	}
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(toolInput, &p); err != nil || p.Command == "" {
		return nil
	}
	cmd := p.Command

	var out []string
	for _, r := range bashRiskRules {
		if r.re.MatchString(cmd) {
			out = append(out, r.id)
		}
	}
	return out
}

type bashRiskRule struct {
	id string
	re *regexp.Regexp
}

// Conservative bash risk patterns. Anchor on word boundaries / start
// of token so that `echo "rm -rf x"` doesn't false-positive (the
// echo'd token isn't at start-of-segment).
var bashRiskRules = []bashRiskRule{
	// Match rm -rf (or -fr / -R...f variants) at the start of a
	// shell segment. Allow any chain of command-modifier prefixes
	// (sudo / nice / time / env / exec) before `rm` so e.g.
	// `sudo rm -rf …` still fires.
	{"rm_rf", regexp.MustCompile(`(?:^|[;&|\n]\s*)(?:(?:sudo|nice|time|env|exec)\s+)*rm\s+(?:-[a-zA-Z]*[rR][a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*[rR]|--recursive\b\s*--force|--force\b\s*--recursive)\b`)},
	{"force_push", regexp.MustCompile(`(?:^|[;&|\n]\s*)git\s+push\s+(?:[^|;&]*\s+)?(?:--force\b|-f\b)`)},
	{"sudo", regexp.MustCompile(`(?:^|[;&|\n]\s*)sudo\b`)},
	{"chmod_777", regexp.MustCompile(`(?:^|[;&|\n]\s*)chmod\s+(?:-[a-zA-Z]+\s+)?777\b`)},
	{"eval_dynamic", regexp.MustCompile(`(?:^|[;&|\n]\s*)eval\s+["'$]`)},
	{"creds_in_url", regexp.MustCompile(`https?://[^/\s:@]+:[^/\s@]+@`)},
	{"secret_env_inline", regexp.MustCompile(`\b[A-Z_]+_(?:TOKEN|KEY|SECRET|PASSWORD|PASSWD)=[^\s'"]+`)},
}
