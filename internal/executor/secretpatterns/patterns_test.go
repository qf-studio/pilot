package secretpatterns

import "testing"

func TestPatterns_NonEmptyAndParsed(t *testing.T) {
	patterns := Patterns()
	if len(patterns) == 0 {
		t.Fatal("expected at least one pattern from patterns.txt")
	}
	for _, p := range patterns {
		if p == "" {
			t.Error("Patterns() returned an empty pattern string")
		}
	}
}

func TestRegexes_CompileAndMatchKnownShapes(t *testing.T) {
	regexes := Regexes()
	if len(regexes) != len(Patterns()) {
		t.Fatalf("expected %d compiled regexes, got %d", len(Patterns()), len(regexes))
	}

	cases := []string{
		"ghp_" + repeat("a1B2c3", 6), // 36 chars
		"pdl_live_abcdefghijklmnopqrstuvwxyz",
		"AKIAABCDEFGHIJKLMNOP",
		"123456789:AA" + repeat("a", 33), // Telegram bot token
	}
	for _, c := range cases {
		matched := false
		for _, re := range regexes {
			if re.MatchString(c) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("expected %q to match at least one secret pattern", c)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestRedact(t *testing.T) {
	telegram := "123456789:AA" + repeat("a", 33)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"telegram token in URL", "Get https://api.telegram.org/bot" + telegram + "/getMe: EOF", "Get https://api.telegram.org/bot" + Redacted + "/getMe: EOF"},
		{"github token", "auth ghp_" + repeat("a1B2c3", 6) + " failed", "auth " + Redacted + " failed"},
		{"two secrets", telegram + " " + telegram, Redacted + " " + Redacted},
		{"no secret", "plain log line 12345:short", "plain log line 12345:short"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
