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
	"net/url"
	"regexp"
	"sort"
	"strconv"
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
	// secretValues holds the registered exact values and their encoded
	// variants, longest first.
	secretValues [][]byte
	// secretDistinct holds the plain values behind secretValues, so the count
	// reports secrets, not variants.
	secretDistinct = map[string]bool{}
)

// RegisterSecretValues adds exact secret values to a process-wide set that
// Redact and RedactBytes mask before applying the shape patterns. It exists
// for configured secrets whose shape no pattern knows (GH-6249).
//
// Values shorter than 8 characters, unexpanded "${VAR}" references and values
// that are a substring of the redaction marker are ignored. The JSON-escaped
// form of a value is registered alongside it, since recordings and stored
// output carry text inside JSON strings, and so are the URL-encoded forms
// (url.QueryEscape, url.PathEscape) when they differ from the plain value
// (GH-6266). Safe for concurrent use.
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
		if len(v) < minSecretValueLen || strings.HasPrefix(v, "${") || strings.Contains(Redacted, v) {
			continue
		}
		secretDistinct[v] = true
		add(v)
		if enc, err := json.Marshal(v); err == nil {
			add(string(enc[1 : len(enc)-1]))
		}
		if q := url.QueryEscape(v); q != v {
			add(q)
		}
		if p := url.PathEscape(v); p != v {
			add(p)
		}
	}
	sort.SliceStable(secretValues, func(i, j int) bool {
		return len(secretValues[i]) > len(secretValues[j])
	})
}

// SecretValueCount reports how many distinct secrets are registered; the
// encoded variants of one value count once.
func SecretValueCount() int {
	secretValuesMu.RLock()
	defer secretValuesMu.RUnlock()
	return len(secretDistinct)
}

// ResetSecretValuesForTest clears the registered values. Tests only.
func ResetSecretValuesForTest() {
	secretValuesMu.Lock()
	secretValues = nil
	secretDistinct = map[string]bool{}
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
	var spans [][2]int
	for _, v := range secretValues {
		for from := 0; from < len(p); {
			i := bytes.Index(p[from:], v)
			if i < 0 {
				break
			}
			spans = append(spans, [2]int{from + i, from + i + len(v)})
			// Step one byte, not len(v): overlapping occurrences of one
			// value must merge too.
			from += i + 1
		}
	}
	secretValuesMu.RUnlock()
	p = maskSpans(p, spans)

	redactOnce.Do(func() { redactRegexes = Regexes() })
	for _, re := range redactRegexes {
		if re.Match(p) {
			p = re.ReplaceAll(p, []byte(Redacted))
		}
	}
	return p
}

// maskSpans replaces the union of spans in p with Redacted in one pass, so two
// registered values that overlap in the text leave no tail of the second one.
// It returns p itself when spans is empty.
func maskSpans(p []byte, spans [][2]int) []byte {
	if len(spans) == 0 {
		return p
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := make([]byte, 0, len(p))
	pos := 0
	cur := spans[0]
	flush := func() {
		out = append(out, p[pos:cur[0]]...)
		out = append(out, Redacted...)
		pos = cur[1]
	}
	for _, s := range spans[1:] {
		if s[0] <= cur[1] {
			if s[1] > cur[1] {
				cur[1] = s[1]
			}
			continue
		}
		flush()
		cur = s
	}
	flush()
	return append(out, p[pos:]...)
}

// KindConfiguredValue is the Match.Kind of a registered secret value.
const KindConfiguredValue = "configured_value"

// Match is one secret found by FindMatches. Start and End are byte offsets
// into the searched slice. Kind is KindConfiguredValue for a registered value,
// else the pattern's literal prefix ("ghp", "xoxb", "AKIA"), or "pattern_<n>"
// (n = line index among Patterns()) when the pattern starts with a character
// class.
type Match struct {
	Kind       string
	Start, End int
}

var (
	kindsOnce sync.Once
	kindNames []string
)

// patternKind derives a metric-safe label from a pattern's literal prefix.
func patternKind(pattern string, idx int) string {
	end := strings.IndexAny(pattern, "[({.*+?\\|^$")
	if end < 0 {
		end = len(pattern)
	}
	prefix := strings.TrimRight(pattern[:end], "-_:")
	if prefix == "" {
		return "pattern_" + strconv.Itoa(idx)
	}
	return prefix
}

// FindMatches returns every registered-value and pattern match in p, without
// altering it. It is the read-only counterpart of RedactBytes: the leak
// scanner uses it on raw bytes from disk, so it must not assume the writer
// redacted anything (GH-6250).
func FindMatches(p []byte) []Match {
	var out []Match

	secretValuesMu.RLock()
	for _, v := range secretValues {
		for from := 0; from < len(p); {
			i := bytes.Index(p[from:], v)
			if i < 0 {
				break
			}
			out = append(out, Match{Kind: KindConfiguredValue, Start: from + i, End: from + i + len(v)})
			from += i + len(v)
		}
	}
	secretValuesMu.RUnlock()

	redactOnce.Do(func() { redactRegexes = Regexes() })
	kindsOnce.Do(func() {
		for i, pat := range Patterns() {
			kindNames = append(kindNames, patternKind(pat, i))
		}
	})
	for i, re := range redactRegexes {
		for _, loc := range re.FindAllIndex(p, -1) {
			out = append(out, Match{Kind: kindNames[i], Start: loc[0], End: loc[1]})
		}
	}
	return out
}
