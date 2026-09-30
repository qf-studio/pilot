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
