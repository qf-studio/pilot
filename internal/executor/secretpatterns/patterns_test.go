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
