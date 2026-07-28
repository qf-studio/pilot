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
	// FailureClassInfraBilling is GH-4591's jobs-never-started shape:
	// GitHub Actions refused to even start the job because of an org billing
	// problem (payment failure or spending limit reached). Handled
	// identically to FailureClassInfra — safe to retry, never closes the PR
	// or spawns a fix issue — but kept as a distinct value so metrics/alerts
	// can name billing specifically instead of folding it into the generic
	// runner-infra count.
	FailureClassInfraBilling FailureClass = "infra_billing"
)

// IsInfra reports whether c is any infra-family classification — safe to
// auto-retry, and never grounds for closing the PR or spawning a fix issue.
// GH-4591 split the single FailureClassInfra value into two (generic
// runner-infra death vs. billing refusal); callers that only care about
// "is this our problem or GitHub's" should branch on IsInfra() rather than
// comparing against FailureClassInfra directly.
func (c FailureClass) IsInfra() bool {
	return c == FailureClassInfra || c == FailureClassInfraBilling
}

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

// billingRefusalRe matches the check-run output text GitHub emits when
// Actions refuses to even start a job because of an org billing problem —
// payment failure or spending limit reached (GH-4591, live incident
// 2026-07-28: pilot-canary-sandbox#106 closed and fix issue #107 spawned for
// a PR whose content was never actually tested; pointer#213/#214 hit the
// same shape). Unlike the runner-infra signatures above, a job in this state
// has zero job logs to search — no step ever ran — so this matches against
// the check run's own Output.Summary/Output.Text instead of GetJobLogs
// output.
var billingRefusalRe = regexp.MustCompile(`(?i)(was not started|payments? have failed|spending limit)`)

// isJobsNeverStartedInfra reports whether chk looks like GH-4591's
// jobs-never-started shape. Either detection signal is sufficient on its
// own:
//  1. the check run's own output text contains a billing-refusal phrase, or
//  2. the job's step breakdown is known (StepsKnown) and empty (StepsCount
//     == 0) — a job that never started can't be blamed on any code change,
//     regardless of whether GitHub's output text happened to be captured.
//
// StepsKnown deliberately gates signal 2: an unresolved jobs-API lookup
// (StepLogClient unset, or the lookup itself erroring) must never be
// silently treated as "definitely zero steps" — that would misclassify an
// ordinary runner-infra failure (whose step count is simply unknown here,
// not zero) as a billing refusal.
func isJobsNeverStartedInfra(chk FailedCheckLog) bool {
	if billingRefusalRe.MatchString(chk.AnnotationText) {
		return true
	}
	return chk.StepsKnown && chk.StepsCount == 0
}

// classifyCheckFailureFull is classifyCheckFailure extended with GH-4591's
// jobs-never-started billing-refusal shape, which classifyCheckFailure alone
// cannot see since it only has the (empty) job log to work from.
//
// A real compiler/lint annotation in the log is checked first and, if
// present, wins outright — exactly like classifyCheckFailure's own
// realAnnotationRe override — because it is definitive proof the job
// actually started and ran far enough to hit real source code, regardless of
// what StepsCount/AnnotationText happen to report (e.g. an incomplete jobs-
// API response). Only once that's ruled out does the billing-refusal check
// run: a job that never started is unambiguously not-code regardless of what
// an empty/near-empty log happens to contain otherwise.
func classifyCheckFailureFull(chk FailedCheckLog) FailureClass {
	if realAnnotationRe.MatchString(chk.Logs) {
		return classifyCheckFailure(chk.Logs)
	}
	if isJobsNeverStartedInfra(chk) {
		return FailureClassInfraBilling
	}
	return classifyCheckFailure(chk.Logs)
}

// classifyPRFailure aggregates classifyCheckFailureFull across every scoped
// failed check on a SHA (GH-4533, extended by GH-4591): an infra-family
// classification (FailureClassInfra or FailureClassInfraBilling) only when
// there is at least one failed check and every single one of them
// classifies infra-family; FailureClassCode otherwise — covering zero failed
// checks, a pure code failure, and a mix of infra and code failures across
// different checks. Fail-safe by construction: a mixed signal never allows
// an auto-retry. When every check is infra-family and at least one is
// specifically a billing refusal, the aggregate reports
// FailureClassInfraBilling (the more actionable, specific signal) rather
// than the generic FailureClassInfra.
func classifyPRFailure(checks []FailedCheckLog) FailureClass {
	if len(checks) == 0 {
		return FailureClassCode
	}
	sawBilling := false
	for _, chk := range checks {
		class := classifyCheckFailureFull(chk)
		if !class.IsInfra() {
			return FailureClassCode
		}
		if class == FailureClassInfraBilling {
			sawBilling = true
		}
	}
	if sawBilling {
		return FailureClassInfraBilling
	}
	return FailureClassInfra
}
