package redact

import (
	"strings"
	"testing"
)

func TestRedactPatterns(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantPattern string // substring that should appear in output
		wantCount   int
	}{
		{
			name: "pem-rsa-private",
			in: `Here's the key:
-----BEGIN RSA PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ
-----END RSA PRIVATE KEY-----
hope that helps`,
			wantPattern: "[REDACTED:pem-private-key]",
			wantCount:   1,
		},
		{
			name:        "pem-ed25519-private",
			in:          "Key: -----BEGIN ED25519 PRIVATE KEY-----\nABCD\n-----END ED25519 PRIVATE KEY-----",
			wantPattern: "[REDACTED:pem-private-key]",
			wantCount:   1,
		},
		{
			name:        "aws-access-key",
			in:          "use AKIAIOSFODNN7EXAMPLE for s3",
			wantPattern: "[REDACTED:aws-access-key-id]",
			wantCount:   1,
		},
		{
			name:        "github-pat",
			in:          "set token=ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789Ab",
			wantPattern: "[REDACTED:github-token]",
			wantCount:   1,
		},
		{
			name:        "slack-bot",
			in:          "use xoxb-12345-67890-12345-abcdef0123456789abcdef",
			wantPattern: "[REDACTED:slack-token]",
			wantCount:   1,
		},
		{
			name:        "keyword-api-key",
			in:          `config: api_key: "abcdefghij1234567890_-"`,
			wantPattern: "[REDACTED:keyword-secret]",
			wantCount:   1,
		},
		{
			name:        "keyword-password-equals",
			in:          `password=Sup3rSecret_Pa55w0rd!Long`,
			wantPattern: "[REDACTED:keyword-secret]",
			wantCount:   1,
		},
		{
			name:        "auth-header-bearer",
			in:          "Authorization: Bearer abcdef1234567890ghijkl0987654321",
			wantPattern: "[REDACTED:auth-header]",
			wantCount:   1,
		},
		{
			name:        "auth-header-basic",
			in:          "Authorization: Basic dXNlcm5hbWU6cGFzc3dvcmRfd2l0aF9lbm91Z2hfYnl0ZXM=",
			wantPattern: "[REDACTED:auth-header]",
			wantCount:   1,
		},
		{
			name: "multiple-patterns-coexist",
			in: `key1: ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789Ab
also AKIAIOSFODNN7EXAMPLE`,
			wantPattern: "[REDACTED:",
			wantCount:   2,
		},
		// Edge: an AWS-shaped string inside a PEM block should be
		// redacted ONCE (pem catches the whole block). Verifies pattern
		// ordering avoids double-redacting.
		{
			name: "pem-wraps-other-secret",
			in: `-----BEGIN RSA PRIVATE KEY-----
content with AKIAIOSFODNN7EXAMPLE inside
-----END RSA PRIVATE KEY-----`,
			wantPattern: "[REDACTED:pem-private-key]",
			wantCount:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, count := Redact(tc.in)
			if !strings.Contains(got, tc.wantPattern) {
				t.Errorf("output missing %q\nin:  %s\nout: %s", tc.wantPattern, tc.in, got)
			}
			if count != tc.wantCount {
				t.Errorf("count = %d, want %d\nin:  %s\nout: %s", count, tc.wantCount, tc.in, got)
			}
			// The original secret bytes must not survive in the output
			// (this is the whole point — easy to forget if a future
			// change uses a non-replacing transform).
			for _, leaked := range []string{
				"MIIEvQIBAD",                 // PEM body
				"AKIAIOSFODNN7",              // AWS prefix
				"ghp_aBcDeFgHi",              // GH PAT prefix
				"xoxb-12345-67890",           // Slack prefix
			} {
				if strings.Contains(got, leaked) && !strings.Contains(tc.in, leaked) == false {
					// Only flag if the secret appeared in input AND survived in output
					if strings.Contains(tc.in, leaked) && strings.Contains(got, leaked) {
						t.Errorf("secret %q leaked through redaction\nout: %s", leaked, got)
					}
				}
			}
		})
	}
}

// TestRedactNoFalsePositives ensures the v0.1 patterns don't redact
// content that happens to look key-shaped. False positives in audit
// reports erode user trust in the redactor — they assume things were
// secrets that weren't.
func TestRedactNoFalsePositives(t *testing.T) {
	cases := []string{
		// Plain English with a keyword but no secret-shaped value
		"the password is in the manual",
		"please set your api_key when you're ready",
		// Short strings under min length
		`token: "short"`,
		// AWS-prefix chars in a sentence (4-letter prefix only counts
		// when followed by 16 base32 chars)
		"the AKIA service is available",
		// PEM-like text with no actual key body (no full BEGIN/END pair)
		"see the BEGIN PRIVATE KEY format docs",
		// Code with bearer keyword but no value following
		"// bearer tokens are in the header",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, count := Redact(in)
			if count != 0 {
				t.Errorf("got %d redactions on benign input\nin:  %s\nout: %s", count, in, got)
			}
		})
	}
}

// TestRedactEmptyInput: edge case — empty string should pass through
// without any allocation overhead or pattern walking.
func TestRedactEmptyInput(t *testing.T) {
	got, count := Redact("")
	if got != "" || count != 0 {
		t.Errorf("Redact(\"\") = (%q, %d), want (\"\", 0)", got, count)
	}
}
