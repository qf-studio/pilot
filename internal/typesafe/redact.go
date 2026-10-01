package typesafe

import (
	"regexp"
	"strings"
)

const redactedPlaceholder = "[redacted]"

// secretPatterns mirror the redaction set of Navigator's judge: key=value
// shapes, sk-, ghp_ (and sibling GitHub prefixes), AKIA, xox, JWT, 40-hex.
// Order matters: JWTs and prefixed tokens go before the generic shapes.
var secretPatterns = []*regexp.Regexp{
	// JWT: three base64url segments, first two start with eyJ.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	// key=value / key: value where the key name looks secret-shaped. The value
	// is bounded to the token (an optional opening quote, then a run that stops
	// at whitespace, backtick, quote or closing bracket) so surrounding prose
	// survives; a backticked pair keeps its closing backtick.
	// Decision: "/" ends the value only in path context, i.e. when the pair is
	// directly preceded by "/" (alternative 1: `cmd/token=abc/x.go` redacts to
	// `cmd/[redacted]/x.go`, filename intact). Everywhere else (alternative 2)
	// "/" belongs to the value, so AWS-style base64 secrets and URLs are
	// redacted whole. RE2 has no lookbehind, so alternative 1 consumes the
	// leading "/" and redactSecrets restores it.
	regexp.MustCompile(`(?i)/[\w.-]*(?:token|secret|passwd|password|api[_-]?key|apikey|credential|auth)[\w.-]*\s*[=:]\s*["']?[^\s` + "`" + `'")\]}>/]+` +
		`|\b[\w.-]*(?:token|secret|passwd|password|api[_-]?key|apikey|credential|auth)[\w.-]*\s*[=:]\s*["']?[^\s` + "`" + `'")\]}>]+`),
	// OpenAI / Anthropic style.
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	// GitHub tokens.
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	// AWS access key id.
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// Slack tokens.
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	// 40-hex (git SHA-1 shaped secrets, legacy tokens).
	regexp.MustCompile(`\b[0-9a-fA-F]{40}\b`),
}

// redactSecrets replaces every substring shaped like a credential with
// "[redacted]". Only redacted text may leave the machine.
func redactSecrets(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if strings.HasPrefix(m, "/") {
				// Path-context key=value match: keep the leading separator.
				return "/" + redactedPlaceholder
			}
			return redactedPlaceholder
		})
	}
	return s
}

// ContainsSecret reports whether s contains any credential-shaped span, i.e.
// whether redactSecrets would change it.
func ContainsSecret(s string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// capItem truncates s to at most n characters (runes).
func capItem(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// RedactAndCap redacts credential-shaped spans from s and then caps the result
// at maxChars characters. It is the only way text should enter a System One
// state: redact first so a secret cut in half by the cap cannot survive.
func RedactAndCap(s string, maxChars int) string {
	return capItem(redactSecrets(s), maxChars)
}
