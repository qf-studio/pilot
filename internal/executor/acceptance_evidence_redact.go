package executor

import (
	"os"
	"regexp"
	"strings"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-5437: PR #5436's evidence runner pasted a command's combined
// stdout+stderr into the PR body verbatim. Since the commands themselves
// come from issue text (an untrusted, publicly-writable input on a public
// repo) and originally ran with the daemon's full process environment, an
// item containing `env` or `cat ~/.pilot/config.yaml` published every
// credential the daemon holds. redactOutputForEvidence is the last line of
// defense applied to every captured output before it is embedded: it scrubs
// (a) the *values* of parent-process env vars whose *names* look
// secret-shaped, and (b) any substring shaped like a real provider secret,
// regardless of where it came from.

// secretEnvNameRe matches env var names that plausibly hold a credential.
// Matched by name (not value) against the daemon's own environment — GH-5437
// spec: "(?i)token|key|secret|password|credential".
var secretEnvNameRe = regexp.MustCompile(`(?i)token|key|secret|password|credential`)

// minRedactableSecretLen is the shortest env-var value redactOutputForEvidence
// will treat as a secret worth scrubbing. Without a floor, a secret-named env
// var holding a short/trivial value (e.g. "1", "true", "") would turn into a
// blanket find-and-replace over common substrings in legitimate command
// output, redacting things that were never a credential.
const minRedactableSecretLen = 6

// redactedPlaceholder replaces every matched secret span.
const redactedPlaceholder = "[REDACTED]"

// redactOutputForEvidence scrubs output before it is embedded in the PR
// body (GH-5437). Applied to every captured command/mutation-test output,
// evidence or not-verified alike, never the raw acceptance-item text itself
// (that's issue-author-controlled prose, not command output).
func redactOutputForEvidence(output string) string {
	if output == "" {
		return output
	}
	output = redactParentSecretEnvValues(output)
	output = redactKnownSecretShapes(output)
	return output
}

// redactParentSecretEnvValues replaces every occurrence of a secret-named
// parent-process env var's value with redactedPlaceholder.
func redactParentSecretEnvValues(output string) string {
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" || len(value) < minRedactableSecretLen {
			continue
		}
		if secretEnvNameRe.MatchString(name) {
			output = strings.ReplaceAll(output, value, redactedPlaceholder)
		}
	}
	return output
}

// redactKnownSecretShapes replaces every substring matching one of the
// repo's canonical secret patterns (secretpatterns package — the same
// source scripts/check-secret-patterns.sh uses) with redactedPlaceholder.
func redactKnownSecretShapes(output string) string {
	for _, re := range secretpatterns.Regexes() {
		output = re.ReplaceAllString(output, redactedPlaceholder)
	}
	return output
}
