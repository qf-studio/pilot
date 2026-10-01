package typesafe

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	// Secret-shaped values are assembled at runtime so no realistic literal
	// sits in the source (GitHub push protection).
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"key=value", "token=abc123secret", "[redacted]"},
		{"key=value in sentence", "run it with api_key=zzz123 then verify", "run it with [redacted] then verify"},
		{"key=value mid-path keeps filename (GH-5557)", "cmd/token=abc123secret/x.go", "cmd/[redacted]/x.go"},
		{"slash inside value is part of the value (GH-5566)", "token=abc/def", "[redacted]"},
		{"url value fully redacted (GH-5566)", "token=https://x.io/abc", "[redacted]"},
		{"aws-style base64 secret with slashes fully redacted (GH-5566)", "aws_secret_access_key=" + strings.Repeat("A", 12) + "/" + strings.Repeat("b", 13) + "/" + strings.Repeat("C", 13), "[redacted]"},
		{"slash value in sentence keeps trailing prose (GH-5566)", "set token=abc/def then rerun", "set [redacted] then rerun"},
		{"path-context incident span keeps filename (GH-5557)", "internal/executor/token=abc123secret/dependency_detector.go", "internal/executor/[redacted]/dependency_detector.go"},
		{"backticked key=value keeps closing backtick and paren (GH-5557)", "(set `token=abc123secret`) and rerun", "(set `[redacted]`) and rerun"},
		{"quoted value redacted", `api_key="zzz123" next`, `[redacted]" next`},
		{"password colon", "password: hunter22", "[redacted]"},
		{"sk prefix", "use sk-" + strings.Repeat("a", 24) + " here", "use [redacted] here"},
		{"ghp prefix", "gh " + "ghp_" + strings.Repeat("b", 30), "gh [redacted]"},
		{"aws", "AKIA" + strings.Repeat("A", 16), "[redacted]"},
		{"slack", "xoxb-" + strings.Repeat("1", 12) + "-" + strings.Repeat("c", 10), "[redacted]"},
		{"jwt", "eyJ" + strings.Repeat("a", 10) + ".eyJ" + strings.Repeat("b", 10) + "." + strings.Repeat("c", 10), "[redacted]"},
		{"40 hex", "commit " + strings.Repeat("ab12", 10), "commit [redacted]"},
		{"plain text untouched", "go test ./internal/foo passes; TestBar fails after deleting line 4", "go test ./internal/foo passes; TestBar fails after deleting line 4"},
		{"short hex untouched", "sha abc1234", "sha abc1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactSecrets(tt.in); got != tt.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCapItem(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter", "abc", 5, "abc"},
		{"exact", "abc", 3, "abc"},
		{"truncated", "abcdef", 3, "abc"},
		{"runes not bytes", "héllo wörld", 5, "héllo"},
		{"zero", "abc", 0, ""},
		{"negative", "abc", -1, ""},
		{"empty", "", 4, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capItem(tt.in, tt.n); got != tt.want {
				t.Errorf("capItem = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCapItem_600(t *testing.T) {
	if got := capItem(strings.Repeat("x", 1000), 600); len([]rune(got)) != 600 {
		t.Errorf("len = %d", len([]rune(got)))
	}
}

func TestRedactAndCap(t *testing.T) {
	got := RedactAndCap("keep token=abc123secret tail", 600)
	if got != "keep [redacted] tail" {
		t.Errorf("RedactAndCap = %q", got)
	}
	if got := RedactAndCap("abcdef", 3); got != "abc" {
		t.Errorf("cap = %q, want abc", got)
	}
}

func TestRedactAndCap_SlashInsideValueIsRedacted(t *testing.T) {
	// 40-char standard-base64 value (AWS secret access key shape) with two slashes.
	secret := strings.Repeat("A", 10) + "/" + strings.Repeat("b", 14) + "/" + strings.Repeat("C", 14)
	if len(secret) != 40 {
		t.Fatalf("fixture len = %d, want 40", len(secret))
	}
	for _, in := range []string{
		"token=abc/def",
		"token=https://x.io/abc",
		"AWS_SECRET_ACCESS_KEY=" + secret,
	} {
		got := RedactAndCap(in, 600)
		if got != "[redacted]" {
			t.Errorf("RedactAndCap(%q) = %q, want fully redacted", in, got)
		}
	}
	if got := RedactAndCap("x token=abc/def y", 600); strings.Contains(got, "def") {
		t.Errorf("tail after slash leaked: %q", got)
	}
}

func TestContainsSecret(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"cmd/token=abc123secret/x.go", true},
		{"internal/" + strings.Repeat("ab12", 10) + "/x.go", true},
		{"internal/executor/runner.go", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ContainsSecret(tt.in); got != tt.want {
			t.Errorf("ContainsSecret(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
