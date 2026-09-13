// Package secretpatterns is the single source of truth for regex patterns
// that identify realistic-looking secrets (API keys, tokens, etc.) in text.
//
// It exists so two independent consumers never drift apart (GH-5437):
//  1. scripts/check-secret-patterns.sh, which blocks commits/CI on tracked
//     files containing a realistic secret pattern (the original TASK-41
//     incident: 9 branches blocked for hours by GitHub push protection).
//  2. internal/executor's acceptance-evidence output redaction, which must
//     scrub the same shapes out of command output before it is pasted into
//     a public PR body.
//
// patterns.txt is the canonical list; changing detection coverage means
// editing that file only, not this one.
package secretpatterns

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed patterns.txt
var patternsFile string

// Patterns returns the canonical secret-pattern regex strings, in file
// order, skipping blank lines and comment lines (leading "#").
func Patterns() []string {
	lines := strings.Split(patternsFile, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Regexes compiles Patterns() into Go regexps.
//
// Panics on a malformed pattern: patterns.txt is a checked-in file
// maintained by this repo, not user input, so a compile failure is a
// programmer error that a test (patterns_test.go) catches immediately —
// not a runtime condition callers need to handle defensively.
func Regexes() []*regexp.Regexp {
	patterns := Patterns()
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		res = append(res, regexp.MustCompile(p))
	}
	return res
}
