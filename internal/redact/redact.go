// Package redact applies pattern-based redaction to free-text fields
// (prompts, thinking content, assistant decisions) before they reach
// the ingest server. Per SPEC §12: "rule-based first, model-assisted
// later". This implements the rule-based half.
//
// v0.1 patterns cover the highest-confidence catastrophic-leak cases:
// PEM private keys, AWS access keys, GitHub tokens, Slack tokens, and
// generic high-entropy secrets next to common keyword identifiers.
// PII (emails, phone numbers, SSN-like patterns) is INTENTIONALLY out
// of scope — the false-positive rate at v0.1 doesn't justify the noise
// it would add to audit reports.
//
// Replacement strategy: substitute with `[REDACTED:type]` so audit
// readers can see something WAS there of that kind, without the
// content. Preserves byte-count signal but hides material.
package redact

import (
	"regexp"
)

// Pattern is a named regex + label. Order in the patterns slice
// determines apply order; longer / more specific patterns first to
// avoid generic-keyword catching content already wrapped by a more
// specific match.
type pattern struct {
	name string
	re   *regexp.Regexp
}

var patterns = []pattern{
	// PEM-encoded private keys. Catches RSA / EC / ED25519 / generic.
	// Multi-line; (?s) flag makes `.` match newlines for the body.
	{
		name: "pem-private-key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	},
	// AWS access key IDs. The 4-letter prefix set is documented by
	// AWS; the 16-char body is base32 alphabet.
	{
		name: "aws-access-key-id",
		re:   regexp.MustCompile(`\b(?:AKIA|AGPA|AROA|AIDA|ANPA|ANVA|ASIA)[0-9A-Z]{16}\b`),
	},
	// GitHub personal-access tokens (ghp_), OAuth (gho_), server
	// tokens (ghs_), user-to-server (ghu_), refresh (ghr_). v2 PATs
	// are at least 36 chars after the prefix.
	{
		name: "github-token",
		re:   regexp.MustCompile(`\bgh[oprsu]_[A-Za-z0-9]{36,}\b`),
	},
	// Slack bot/user tokens. Format xoxb-/xoxp-/xoxa-/xoxr-/xoxs-
	// followed by digit-dash-segments.
	{
		name: "slack-token",
		re:   regexp.MustCompile(`\bxox[abprs]-[0-9]+-[0-9]+-[0-9]+-[a-fA-F0-9]+\b`),
	},
	// HTTP Authorization header values: "Bearer <token>" /
	// "Basic <base64>". Separate from keyword-secret because the
	// separator is whitespace, not `:` or `=`.
	{
		name: "auth-header",
		re:   regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[A-Za-z0-9_\-/.+=]{16,}\b`),
	},
	// Generic secret-with-keyword: "api_key": "...", password=...,
	// token: ... . Requires the right-hand side be ≥16 chars of
	// secret-shaped alphabet (alphanumeric + a few symbols) AND a
	// keyword on the left. Higher false-positive risk than the
	// vendor-specific patterns above, so kept conservative.
	{
		name: "keyword-secret",
		re:   regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|password|passwd|access[_-]?token|x-api-key)\s*[:=]\s*["']?[A-Za-z0-9_\-/+=]{16,}["']?`),
	},
}

// Redact applies the v0.1 rule set. Returns the redacted text and the
// number of pattern matches replaced. Caller may surface the count to
// audit readers (e.g. "X secrets redacted" badge) so users know this
// happened — silent redaction would be the wrong default for an audit
// tool.
func Redact(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	var total int
	out := text
	for _, p := range patterns {
		matches := p.re.FindAllStringIndex(out, -1)
		if len(matches) == 0 {
			continue
		}
		total += len(matches)
		out = p.re.ReplaceAllString(out, "[REDACTED:"+p.name+"]")
	}
	return out, total
}
