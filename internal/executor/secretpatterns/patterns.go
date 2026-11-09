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
	"bytes"
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
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

// Redacted replaces every match of a secret pattern in Redact.
const Redacted = "<redacted>"

var (
	redactOnce    sync.Once
	redactRegexes []*regexp.Regexp
)

// minSecretValueLen is the shortest value RegisterSecretValues accepts.
// Shorter strings ("true", "8080", "dev") would shred ordinary text when
// masked wherever they occur. Slightly above the acceptance-evidence env
// floor (minRedactableSecretLen in internal/executor), because this set is
// applied to every log line, row and recording.
const minSecretValueLen = 8

var (
	secretValuesMu sync.RWMutex
	// secretValues holds the registered exact values, longest first, so a
	// value that contains another registered value is masked whole.
	secretValues [][]byte
)

// RegisterSecretValues adds exact secret values to a process-wide set that
// Redact and RedactBytes mask before applying the shape patterns. It exists
// for configured secrets whose shape no pattern knows (GH-6249).
//
// Values shorter than 8 characters and unexpanded "${VAR}" references are
// ignored. The JSON-escaped form of a value is registered alongside it, since
// recordings and stored output carry text inside JSON strings. Safe for
// concurrent use.
func RegisterSecretValues(values ...string) {
	secretValuesMu.Lock()
	defer secretValuesMu.Unlock()
	known := make(map[string]bool, len(secretValues))
	for _, v := range secretValues {
		known[string(v)] = true
	}
	add := func(v string) {
		if v != "" && !known[v] {
			known[v] = true
			secretValues = append(secretValues, []byte(v))
		}
	}
	for _, v := range values {
		if len(v) < minSecretValueLen || strings.HasPrefix(v, "${") {
			continue
		}
		add(v)
		if enc, err := json.Marshal(v); err == nil {
			add(string(enc[1 : len(enc)-1]))
		}
	}
	sort.SliceStable(secretValues, func(i, j int) bool {
		return len(secretValues[i]) > len(secretValues[j])
	})
}

// SecretValueCount reports how many distinct values are registered
// (JSON-escaped variants of a value count separately).
func SecretValueCount() int {
	secretValuesMu.RLock()
	defer secretValuesMu.RUnlock()
	return len(secretValues)
}

// ResetSecretValuesForTest clears the registered values. Tests only.
func ResetSecretValuesForTest() {
	secretValuesMu.Lock()
	secretValues = nil
	secretValuesMu.Unlock()
}

// Redact replaces each registered secret value, then each match of a
// canonical secret pattern, in s with Redacted. The compiled patterns are
// cached; text without a secret is returned unchanged.
func Redact(s string) string {
	return string(RedactBytes([]byte(s)))
}

// RedactBytes is Redact for a byte slice. It returns p itself when nothing
// matches, so the common no-secret case does not allocate.
func RedactBytes(p []byte) []byte {
	secretValuesMu.RLock()
	for _, v := range secretValues {
		if bytes.Contains(p, v) {
			p = bytes.ReplaceAll(p, v, []byte(Redacted))
		}
	}
	secretValuesMu.RUnlock()

	redactOnce.Do(func() { redactRegexes = Regexes() })
	for _, re := range redactRegexes {
		if re.Match(p) {
			p = re.ReplaceAll(p, []byte(Redacted))
		}
	}
	return p
}
