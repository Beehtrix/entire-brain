package base

import (
	"strings"
	"testing"
)

func TestRedactQuotedSecretAssignments(t *testing.T) {
	for _, input := range []string{
		`{"API_KEY":"audit_fake_credential_123456"}`,
		`{"password": "audit fake credential value"}`,
		`PASSWORD='audit fake credential value'`,
		`{"secret": "audit escaped\" credential value"}`,
		`PASSWORD="audit unterminated credential value`,
	} {
		got := RedactText(input)
		for _, secretPart := range []string{"audit", "credential", "value"} {
			if strings.Contains(got, secretPart) {
				t.Errorf("secret fragment survived: input %q, output %q", input, got)
			}
		}
		if !strings.Contains(got, "REDACTED") {
			t.Errorf("missing redaction: %q", got)
		}
	}
}

func TestRedactText(t *testing.T) {
	cases := []struct{ in, mustNotContain, mustContain string }{
		{"export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "[REDACTED"},
		{"Authorization: Bearer abcdef1234567890XYZ", "abcdef1234567890XYZ", "[REDACTED]"},
		{"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcDEF123_-x", "eyJhbGciOiJIUzI1NiJ9", "[REDACTED JWT]"},
		{"API_KEY=supersecretvalue123", "supersecretvalue123", "[REDACTED]"},
		{"ran in /Users/alice/Projects/secret/app", "/Users/alice", "/Users/[redacted]"},
		// macOS is case-insensitive by default (and may be case-sensitive); a
		// lowercase /users/ home path is the same dir and must redact too.
		{"ls /users/example-user/Documents/entire", "example-user", "/users/[redacted]"},
		// Linux home layout.
		{"cat /home/bob/.ssh/config", "/home/bob", "/home/[redacted]"},
		// Quoted home path keeps the surrounding quote (and the subpath shape).
		{`"/Users/carol"`, "carol", `"/Users/[redacted]"`},
		// Path shape is preserved (only the username segment goes).
		{"/Users/dave/go/src", "dave", "/Users/[redacted]/go/src"},
		// Canonical /Users (capital) is a home dir wherever it appears — inside
		// file:// URLs, gitbash /c/Users, sed args, and Windows paths.
		{"git clone file:///Users/example-user/src/cli x", "example-user", "file:///Users/[redacted]"},
		{"grep go-git /c/Users/ExampleUser/cli/go.mod", "ExampleUser", "/c/Users/[redacted]"},
		{`sed 's|x|/Users/example-user/Documents/cli|'`, "example-user", "/Users/[redacted]"},
		{`C:\Users\ExampleUser\cli`, "ExampleUser", `C:\Users\[redacted]`},
		// API/repo paths use lowercase /users/ embedded after a segment -> kept.
		{"gh api users/octocat --jq .name", "", "octocat"},
		{"gh api repos/x/contents/platform/users/components/Grid.tsx", "", "platform/users/components"},
		{"rg users/me/checkpoints api/src", "", "users/me/checkpoints"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----", "MIIabc", "[REDACTED PRIVATE KEY]"},
	}
	for _, c := range cases {
		got := RedactText(c.in)
		if c.mustNotContain != "" && strings.Contains(got, c.mustNotContain) {
			t.Errorf("redactText(%q) still contains secret %q -> %q", c.in, c.mustNotContain, got)
		}
		if !strings.Contains(got, c.mustContain) {
			t.Errorf("redactText(%q) = %q, want it to contain %q", c.in, got, c.mustContain)
		}
	}
}
