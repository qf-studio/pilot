package autopilot

import (
	"regexp"
	"strings"
)

// FailureClass classifies a CI check failure as either a genuine code/test/
// lint failure or a CI infrastructure outage — GH-4533. The distinction
// drives handleCIFailed: an infra-classified failure is safe to auto-retry
// (RerunFailedJobs) instead of spawning a fix issue, since there is nothing
// in the PR's own code for Pilot to "fix".
type FailureClass string

const (
	// FailureClassCode is a genuine code/test/lint failure, or any failure
	// whose logs are ambiguous/unavailable — never auto-retried; always goes
	// through the existing fix-issue path. This is also the fail-safe
	// default: unknown, empty, or unfetchable logs classify as code so a
	// classification miss never silently swallows a real failure.
	FailureClassCode FailureClass = "code"
	// FailureClassInfra is a CI infrastructure outage (GitHub Actions runner
	// death, action-download rate limiting, transient 5xx from the Actions
	// backend) unrelated to the PR's code — safe to retry via
	// RerunFailedJobs.
	FailureClassInfra FailureClass = "infra"
)

// realAnnotationRe matches a real compiler/lint annotation line referencing a
// Go source position, e.g. "internal/foo/bar.go:42:6: undefined: foo" (the
// shape both `go vet`/`go build` and golangci-lint/errcheck emit). A log
// containing one of these always classifies as code, even if it also
// contains an infra signature elsewhere (e.g. in runner-setup preamble) —
// the job demonstrably ran far enough to hit real source code.
var realAnnotationRe = regexp.MustCompile(`\.go:\d+:\d+:`)

// classifyCheckFailure inspects one failed check's raw job log and decides
// whether it looks like a CI infrastructure outage (GH-4533, safe to
// auto-retry) or a genuine code/test/lint failure.
//
// The signature set is deliberately conservative (GH-4526/GH-4531 spec):
// only failures that unambiguously look like transient GitHub Actions
// infrastructure trouble classify as infra. Everything else — including
// empty logs (e.g. a log-fetch error, represented by the caller as ""), an
// unrecognized failure, or a log that mixes an infra signature with a real
// annotation line — classifies as code, preserving the pre-GH-4533 behavior
// of always spawning a fix issue.
func classifyCheckFailure(logs string) FailureClass {
	if strings.TrimSpace(logs) == "" {
		return FailureClassCode
	}

	hasInfraSignature := (strings.Contains(logs, "Failed to download action") && strings.Contains(logs, "429")) ||
		(strings.Contains(logs, "##[error]Failed to run:") && strings.Contains(logs, "Unexpected HTTP response: 5")) ||
		strings.Contains(logs, "##[error]The runner has received a shutdown signal") ||
		strings.Contains(logs, "lost communication with the server")

	if !hasInfraSignature {
		return FailureClassCode
	}

	// A real annotation line always wins over an infra signature found
	// elsewhere in the same log — the job actually ran and hit a genuine
	// code failure, even if its setup/teardown output also matched one of
	// the infra signatures above.
	if realAnnotationRe.MatchString(logs) {
		return FailureClassCode
	}

	return FailureClassInfra
}

// classifyPRFailure aggregates classifyCheckFailure across every scoped
// failed check on a SHA (GH-4533): FailureClassInfra only when there is at
// least one failed check and every single one of them classifies infra;
// FailureClassCode otherwise — covering zero failed checks, a pure code
// failure, and a mix of infra and code failures across different checks.
// Fail-safe by construction: a mixed signal never allows an auto-retry.
func classifyPRFailure(checks []FailedCheckLog) FailureClass {
	if len(checks) == 0 {
		return FailureClassCode
	}
	for _, chk := range checks {
		if classifyCheckFailure(chk.Logs) != FailureClassInfra {
			return FailureClassCode
		}
	}
	return FailureClassInfra
}
