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
	// FailureClassUnknown is GH-4779's zero-evidence guard: classifyPRFailure
	// returns this — never FailureClassCode — when the CI aggregate already
	// reported CIFailure but per-check evidence gathering came back with
	// nothing to classify (the check-runs list API call itself failed, or no
	// in-scope check run's conclusion actually matched what the aggregate
	// used to decide CIFailure). This used to silently default to
	// FailureClassCode, which is how the 2026-08-06 GitHub Actions outage
	// closed a correct PR (#4770) on logs autopilot never actually looked
	// at. FailureClassCode must require positive evidence of a genuine
	// repo-code failure; FailureClassUnknown is the honest "we don't know"
	// verdict, and callers must route it to a non-destructive hold
	// (escalateAndHold), never ClosePullRequest or a spawned fix issue.
	FailureClassUnknown FailureClass = "unknown"
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

// conclusionStartupFailure and conclusionStale are GitHub Actions check-run
// conclusions absent from studio-sdk's Conclusion* constants (studio-sdk
// vendors only success/failure/neutral/cancelled/timed_out/action_required/
// skipped as of this writing — GH-4779) — declared here as local literals
// per the task's scope fence rather than blocking on an SDK release. Both
// are GitHub-side signals, never a genuine code failure:
//   - startup_failure: the job failed to even start (workflow/runner
//     provisioning problem on GitHub's side) — no repo code ever ran.
//   - stale: GitHub gave up trying to get a final status update for the
//     check run (e.g. a runner or webhook delivery died mid-run) and marked
//     it stale rather than leave it pending forever.
const (
	conclusionStartupFailure = "startup_failure"
	conclusionStale          = "stale"
)

// syntheticStepNames are the setup/teardown steps GitHub Actions injects
// into every job's step list, bracketing the repo-defined steps a workflow
// actually specifies (GH-4779). A job whose failing step is one of these
// died during GitHub's own provisioning/teardown — it never reached any
// step the repo defines, regardless of what the log prose says.
var syntheticStepNames = map[string]bool{
	"set up job":    true,
	"set up runner": true,
	"complete job":  true,
}

// isSyntheticStepName reports whether name is one of GitHub's synthetic
// setup/teardown step names, case-insensitively. An empty name (failing step
// unresolved, e.g. no StepLogClient configured) is never synthetic.
func isSyntheticStepName(name string) bool {
	return syntheticStepNames[strings.ToLower(strings.TrimSpace(name))]
}

// isStructuralInfra reports whether chk carries a structural fact —
// independent of log wording — that definitively marks it as CI
// infrastructure trouble rather than a genuine code failure (GH-4779,
// evaluated in classifyCheckFailureFull before any prose-signature
// matching):
//  1. the failing step GitHub actually resolved is one of its own synthetic
//     setup/teardown steps (FailingStepName), or
//  2. the job's step breakdown is known and zero repo-defined
//     (non-synthetic) steps ever executed — broader than
//     isJobsNeverStartedInfra's raw StepsCount==0 check, which only catches
//     a job with an empty step array altogether; this also catches a job
//     that got as far as GitHub's synthetic "Set up job" step and then died
//     before reaching anything the workflow itself defines, or
//  3. the check run's own top-level conclusion is startup_failure or stale
//     — GitHub-only outcomes that never represent a repo-code failure.
//
// This is the fix for the 2026-08-06 GitHub Actions outage (pilot#4779):
// TASK-418's four hardcoded log-prose signatures matched none of that
// incident's wording ("resolve action download info" / "Service
// Unavailable"), but the job's failing step was GitHub's own synthetic "Set
// up job" — a structural fact no amount of new prose signatures can keep up
// with across future incidents.
func isStructuralInfra(chk FailedCheckLog) bool {
	if isSyntheticStepName(chk.FailingStepName) {
		return true
	}
	if chk.StepsKnown && chk.RepoStepsExecuted == 0 {
		return true
	}
	if chk.Conclusion == conclusionStartupFailure || chk.Conclusion == conclusionStale {
		return true
	}
	return false
}

// classificationSignal names which detection tier produced a
// classifyCheckFailureFull verdict (GH-4779) — logged by handleCIFailed so a
// future incident is diagnosable from logs alone without re-deriving the
// decision by hand.
type classificationSignal string

const (
	signalRealAnnotation classificationSignal = "real_annotation" // definitive code evidence, wins outright
	signalBilling        classificationSignal = "billing"         // GH-4591 jobs-never-started shape
	signalStructural     classificationSignal = "structural"      // GH-4779 synthetic step / zero repo steps / startup_failure|stale
	signalProse          classificationSignal = "prose"           // TASK-418's legacy hardcoded log signatures
	signalNone           classificationSignal = "none"            // no positive signal either way; fail-safe code
)

// classifyCheckFailureFull is classifyCheckFailure extended with GH-4591's
// jobs-never-started billing-refusal shape and GH-4779's structural signals,
// which classifyCheckFailure alone cannot see since it only has the (often
// empty, for these shapes) job log to work from. Also returns which signal
// tier produced the verdict, purely for diagnostic logging.
//
// Evaluation order:
//  1. A real compiler/lint annotation in the log wins outright — definitive
//     proof the job actually started and ran far enough to hit real source
//     code, regardless of what any other signal reports (e.g. an
//     incomplete/stale jobs-API response reading StepsCount/RepoStepsExecuted
//     as 0). This mirrors classifyCheckFailure's own realAnnotationRe
//     override.
//  2. GH-4591's billing-refusal shape (isJobsNeverStartedInfra) — checked
//     ahead of the more general structural signals below so a genuine
//     billing refusal keeps reporting the specific FailureClassInfraBilling
//     rather than being swallowed by the generic zero-repo-steps signal,
//     which would also match it.
//  3. GH-4779's structural signals (isStructuralInfra) — synthetic failing
//     step, zero repo-defined steps executed, or a startup_failure/stale
//     conclusion. None of these depend on log wording.
//  4. classifyCheckFailure's legacy log-prose signature matching, as the
//     last resort fallback.
func classifyCheckFailureFull(chk FailedCheckLog) (FailureClass, classificationSignal) {
	if realAnnotationRe.MatchString(chk.Logs) {
		return classifyCheckFailure(chk.Logs), signalRealAnnotation
	}
	if isJobsNeverStartedInfra(chk) {
		return FailureClassInfraBilling, signalBilling
	}
	if isStructuralInfra(chk) {
		return FailureClassInfra, signalStructural
	}
	class := classifyCheckFailure(chk.Logs)
	if class == FailureClassInfra {
		return class, signalProse
	}
	return class, signalNone
}

// classifyPRFailure aggregates classifyCheckFailureFull across every scoped
// failed check on a SHA (GH-4533, extended by GH-4591 and GH-4779): an
// infra-family classification (FailureClassInfra or FailureClassInfraBilling)
// only when there is at least one failed check and every single one of them
// classifies infra-family; FailureClassCode when at least one check is a
// genuine code failure — a mixed signal never allows an auto-retry, fail-safe
// by construction.
//
// FailureClassUnknown (GH-4779) is the zero-evidence case: no failed checks
// were gathered at all, even though the caller only reaches classifyPRFailure
// after CI's own aggregate status already reported CIFailure. That gap means
// evidence gathering itself came back empty — the check-runs list API call
// failed, or no in-scope check run's conclusion matched what the aggregate
// used to decide CIFailure — not that CI actually passed. Returning
// FailureClassCode here (the pre-GH-4779 behavior) let a PR be closed and a
// fix issue spawned with literally nothing to point at; FailureClassUnknown
// forces callers onto the non-destructive escalate-and-hold path instead.
//
// When every check is infra-family and at least one is specifically a
// billing refusal, the aggregate reports FailureClassInfraBilling (the more
// actionable, specific signal) rather than the generic FailureClassInfra.
func classifyPRFailure(checks []FailedCheckLog) FailureClass {
	if len(checks) == 0 {
		return FailureClassUnknown
	}
	sawBilling := false
	for _, chk := range checks {
		class, _ := classifyCheckFailureFull(chk)
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

// Verdict is the typed, evidence-carrying result of a classification that
// authorizes (or withholds) an irreversible or operator-costly autopilot
// action — TASK-459 Phase 1. `.agent/system/irreversible-actions.md`
// inventories every call site (PR close, branch delete, fix-issue spawn,
// retry-budget burn, merge, ledger cancel/supersede writes) that Phases 2-3
// migrate to consume a Verdict instead of a raw FailureClass, bare string,
// or nil-check; Phase 1 only defines the contract, no call site is migrated
// here.
//
// Invariant: an Unknown or evidence-free Verdict must never authorize a
// destructive action. This is enforced structurally, not by convention —
// the fields are unexported, so a Verdict can only be produced by the
// constructor functions below, and NewVerdict silently downgrades any
// class to FailureClassUnknown whenever the caller supplies no Evidence.
// There is deliberately no separate boolean "hasEvidence" flag and no
// confidence score to thread through call sites or tune: the presence of a
// non-empty Evidence string is itself the only signal, so a Verdict cannot
// claim FailureClassCode/Infra/InfraBilling while silently carrying nothing
// to back it up.
type Verdict struct {
	class    FailureClass
	evidence string
	source   string
	scope    string
}

// NewVerdict constructs a Verdict for a positively-evidenced classification.
// evidence must describe the specific fact that produced class — e.g. the
// matched log signature, the failed check name, or the recorded ledger
// status — not a generic "checks failed" restatement of the verdict itself.
// If evidence is empty, NewVerdict ignores the requested class entirely and
// returns the same zero-evidence FailureClassUnknown verdict NewUnknownVerdict
// would (this is the construction rule that makes an evidence-free
// destructive verdict impossible to produce).
//
// source names the function/subsystem that performed the classification
// (e.g. "classifyPRFailure", "isStructuralInfra") — for logging/diagnosis
// only, never consulted by decision logic. scope is the project this
// verdict applies to, conventionally an "owner/repo" string matching
// Controller.repoKey() — a verdict's authority is project-scoped because
// evidence gathering itself is (a project's required_checks allowlist can
// make a signal decorative for that project only; see
// irreversible-actions.md § authoritative/decorative).
func NewVerdict(class FailureClass, evidence, source, scope string) Verdict {
	if evidence == "" {
		return NewUnknownVerdict(source, scope)
	}
	return Verdict{class: class, evidence: evidence, source: source, scope: scope}
}

// NewUnknownVerdict constructs the explicit zero-evidence verdict: class is
// always FailureClassUnknown regardless of what the caller might otherwise
// have concluded. Use this when a classification path deliberately has
// nothing to point at (GH-4779's zero-gathered-evidence case) rather than
// calling NewVerdict with an empty evidence string — both produce an
// identical Verdict, but this constructor names the intent at the call
// site.
func NewUnknownVerdict(source, scope string) Verdict {
	return Verdict{class: FailureClassUnknown, source: source, scope: scope}
}

// Class returns the verdict's failure classification. Callers gating a
// destructive action must treat FailureClassUnknown as "do not act" —
// never as a synonym for FailureClassCode.
func (v Verdict) Class() FailureClass { return v.class }

// Evidence returns the positive fact backing this verdict. Always empty for
// FailureClassUnknown; always non-empty for every other class, guaranteed
// by construction (NewVerdict downgrades to Unknown rather than allow a
// destructive class through with empty evidence).
func (v Verdict) Evidence() string { return v.evidence }

// Source names the subsystem/function that produced this verdict, for
// logging and incident diagnosis — not decision logic.
func (v Verdict) Source() string { return v.source }

// Scope is the project this verdict applies to (conventionally
// "owner/repo", matching Controller.repoKey()). A verdict must only
// authorize an action within its own scope — evidence gathered for one
// project's required_checks configuration says nothing about another's.
func (v Verdict) Scope() string { return v.scope }
