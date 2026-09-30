package typesafe

import "regexp"

const redactedPlaceholder = "[redacted]"

// secretPatterns mirror the redaction set of Navigator's judge: key=value
// shapes, sk-, ghp_ (and sibling GitHub prefixes), AKIA, xox, JWT, 40-hex.
// Order matters: JWTs and prefixed tokens go before the generic shapes.
var secretPatterns = []*regexp.Regexp{
	// JWT: three base64url segments, first two start with eyJ.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	// key=value / key: value where the key name looks secret-shaped.
	regexp.MustCompile(`(?i)\b[\w.-]*(?:token|secret|passwd|password|api[_-]?key|apikey|credential|auth)[\w.-]*\s*[=:]\s*\S+`),
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
		s = re.ReplaceAllString(s, redactedPlaceholder)
	}
	return s
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
