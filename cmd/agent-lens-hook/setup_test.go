package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMergeAgentLensHooksFreshFile: empty / missing settings.json → all
// 5 events get our hook entry, no other keys appear.
func TestMergeAgentLensHooksFreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	if err := mergeAgentLensHooks(path, "/usr/local/bin/agent-lens-hook"); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := readJSON(t, path)
	hooks, _ := got["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("settings missing hooks key:\n%s", debugJSON(got))
	}
	for _, ev := range setupHookEvents {
		matchers, _ := hooks[ev].([]any)
		if len(matchers) != 1 {
			t.Errorf("event %q: got %d matchers, want 1", ev, len(matchers))
			continue
		}
		if !alreadyOurs(matchers) {
			t.Errorf("event %q: alreadyOurs returned false on freshly-written entry", ev)
		}
	}
}

// TestMergeIdempotent: re-running merge twice produces a settings file
// byte-equal to running it once. Critical UX promise: users can re-run
// `setup --personal` to recover from a botched manual edit without
// duplicating entries.
func TestMergeIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	bin := "/usr/local/bin/agent-lens-hook"

	if err := mergeAgentLensHooks(path, bin); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := mergeAgentLensHooks(path, bin); err != nil {
		t.Fatalf("second merge: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("re-running merge changed settings (not idempotent)\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestMergePreservesOtherKeys: the user's settings.json with unrelated
// top-level keys (theme, plugins, permissions, OTHER tools' hooks)
// must come back unchanged except for the hooks we add. This is THE
// promise that justifies setup over a manual sed.
func TestMergePreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// Pre-populate with the kind of stuff a real user has — including
	// another tool's hook entry and a non-overlapping permissions block.
	pre := map[string]any{
		"theme": "dark",
		"enabledPlugins": map[string]any{
			"some-plugin@marketplace": true,
		},
		"permissions": map[string]any{
			"allow": []any{"Bash(npm test)"},
		},
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/some/other/tool/their-hook record",
							"timeout": 10,
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, pre)

	if err := mergeAgentLensHooks(path, "/usr/local/bin/agent-lens-hook"); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := readJSON(t, path)

	if got["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", got["theme"])
	}
	if !reflect.DeepEqual(got["enabledPlugins"], pre["enabledPlugins"]) {
		t.Errorf("enabledPlugins clobbered:\nwant %v\ngot  %v", pre["enabledPlugins"], got["enabledPlugins"])
	}
	if !reflect.DeepEqual(got["permissions"], pre["permissions"]) {
		t.Errorf("permissions clobbered:\nwant %v\ngot  %v", pre["permissions"], got["permissions"])
	}

	hooks, _ := got["hooks"].(map[string]any)
	preToolMatchers, _ := hooks["PreToolUse"].([]any)
	if len(preToolMatchers) != 2 {
		t.Errorf("PreToolUse: got %d matchers, want 2 (one ours, one preserved)\n%s",
			len(preToolMatchers), debugJSON(hooks))
	}
	// First matcher must be the user's existing one (not reordered).
	first, _ := preToolMatchers[0].(map[string]any)
	firstHooks, _ := first["hooks"].([]any)
	firstHook, _ := firstHooks[0].(map[string]any)
	if cmd, _ := firstHook["command"].(string); !strings.Contains(cmd, "their-hook") {
		t.Errorf("user's existing PreToolUse hook was reordered; first command = %q", cmd)
	}
}

// TestUnmergeRoundTrip: merge then unmerge yields settings semantically
// equal to the pre-state. Closes the install/uninstall promise:
// --uninstall reverts to the user's pre-install state. We compare by
// re-parsing both sides through encoding/json so trailing-newline /
// indentation / number-type (int vs float64) noise from the JSON round
// trip doesn't cause false failures.
func TestUnmergeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	pre := map[string]any{
		"theme": "dark",
	}
	// Use writeSettings (the production writer) so the pre-state's
	// formatting matches the post-state's, isolating the test from
	// trailing-newline noise.
	if err := writeSettings(path, pre); err != nil {
		t.Fatal(err)
	}
	preCanon := canonicalJSON(t, path)

	if err := mergeAgentLensHooks(path, "/path/to/agent-lens-hook"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if err := unmergeAgentLensHooks(path); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	postCanon := canonicalJSON(t, path)

	if preCanon != postCanon {
		t.Errorf("round-trip changed semantic content:\npre:\n%s\npost:\n%s", preCanon, postCanon)
	}
}

// TestUnmergeKeepsOthers: when a user has multiple tools' hooks
// registered, unmerge removes only ours, leaving the rest exact-shape.
func TestUnmergeKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	otherTool := map[string]any{
		"matcher": "*",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": "/some/other/tool/their-hook record",
				"timeout": 10,
			},
		},
	}
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{otherTool},
		},
	})

	if err := mergeAgentLensHooks(path, "/path/to/agent-lens-hook"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if err := unmergeAgentLensHooks(path); err != nil {
		t.Fatalf("unmerge: %v", err)
	}

	got := readJSON(t, path)
	hooks, _ := got["hooks"].(map[string]any)
	preToolMatchers, _ := hooks["PreToolUse"].([]any)
	if len(preToolMatchers) != 1 {
		t.Errorf("got %d PreToolUse matchers post-unmerge, want 1 (the other tool)\n%s",
			len(preToolMatchers), debugJSON(hooks))
	}
	// Compare via JSON to avoid int-vs-float64 noise from the unmarshal
	// path (Go literal int 10 in `otherTool` becomes float64 after JSON
	// round trip; reflect.DeepEqual would say not-equal even though
	// the JSON is byte-equal).
	if jsonEqual(t, preToolMatchers[0], otherTool) == false {
		t.Errorf("other tool's matcher was modified during round trip\nwant: %s\ngot:  %s",
			debugJSON(otherTool), debugJSON(preToolMatchers[0]))
	}
}

// TestMergeRefusesMalformedHooks: when the user has hand-edited
// settings.json into a shape that doesn't match Claude Code's schema
// (e.g. "hooks" set to a string), merge MUST refuse rather than
// silently overwrite. Silent overwrite would discard whatever the user
// was trying to express — exactly the silent-degradation pattern this
// project is built to surface.
func TestMergeRefusesMalformedHooks(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{
			name: "hooks-is-string",
			in:   map[string]any{"hooks": "this-should-be-an-object"},
			want: "'hooks' is string, want a JSON object",
		},
		{
			name: "event-is-string",
			in:   map[string]any{"hooks": map[string]any{"PreToolUse": "not-an-array"}},
			want: "'hooks.PreToolUse' is string, want a JSON array",
		},
		{
			name: "matcher-is-number",
			in: map[string]any{"hooks": map[string]any{
				"PreToolUse": []any{42},
			}},
			want: "'hooks.PreToolUse[0]' is float64, want a JSON object",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			writeJSON(t, path, tc.in)
			origBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			err = mergeAgentLensHooks(path, "/usr/local/bin/agent-lens-hook")
			if err == nil {
				t.Fatalf("expected error, got nil; file mutated to:\n%s", readBytes(t, path))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err.Error(), tc.want)
			}

			// File must NOT have been modified — error means refuse,
			// not "ate the data and complained on the way out".
			postBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(origBytes) != string(postBytes) {
				t.Errorf("file mutated despite error:\nbefore:\n%s\nafter:\n%s", origBytes, postBytes)
			}
		})
	}
}

func TestCommandIsAgentLensHook(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"/usr/local/bin/agent-lens-hook claude", true},
		{"/Users/dongqiu/go/bin/agent-lens-hook claude", true},
		{"agent-lens-hook claude", true}, // PATH-relative still our binary
		{"/usr/bin/agent-lens-hook-extra report", false},
		{"/usr/bin/their-tool record", false},
		{"", false},
		{"  ", false},
	}
	for _, tc := range cases {
		if got := commandIsAgentLensHook(tc.cmd); got != tc.want {
			t.Errorf("commandIsAgentLensHook(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// helpers --------------------------------------------------------------

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, raw)
	}
	return m
}

func writeJSON(t *testing.T, path string, m map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func debugJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// readBytes is a fatal-on-error file reader used in error-path tests
// where we need to display the file's post-error state in a t.Errorf.
func readBytes(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// canonicalJSON re-parses a settings file and re-serializes it with
// fixed indentation. Used to compare semantic content without being
// fooled by whitespace or number-type round-trip artifacts.
func canonicalJSON(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// jsonEqual reports whether two values produce identical JSON when
// marshaled. More forgiving than reflect.DeepEqual for maps with
// numeric values that crossed an Unmarshal/Marshal boundary.
func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	aj, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(aj) == string(bj)
}
