package autopilot

import (
	"strings"
	"testing"
)

// TestClassifyCheckFailure_TableDriven exercises the conservative infra
// signature set from GH-4526/GH-4531/GH-4533: only unambiguous CI
// infrastructure signatures classify infra; everything else — including
// empty logs and mixed infra+code signals — classifies code (fail-safe).
func TestClassifyCheckFailure_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		logs string
		want FailureClass
	}{
		{
			name: "action download rate limited 429",
			logs: `Run actions/checkout@v4
##[error]Failed to download action 'https://api.github.com/repos/actions/checkout/tarball/v4'. Error: Response status code does not indicate success: 429 (Too Many Requests).`,
			want: FailureClassInfra,
		},
		{
			name: "golangci-lint transient 504",
			logs: `Run golangci-lint run ./...
##[error]Failed to run: exit status 1
##[error]Failed to run: golangci-lint: Unexpected HTTP response: 504 Gateway Timeout while fetching linter cache`,
			want: FailureClassInfra,
		},
		{
			name: "runner shutdown signal",
			logs: `Run go test ./...
##[error]The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.`,
			want: FailureClassInfra,
		},
		{
			name: "lost communication with server",
			logs: `Run go build ./...
Error: The operation was canceled.
lost communication with the server. Please check for any issues with the network or your GitHub Actions service.`,
			want: FailureClassInfra,
		},
		{
			name: "real errcheck lint failure is code",
			logs: `Run golangci-lint run ./...
internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
	if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
	   ^
##[error]Process completed with exit code 1.`,
			want: FailureClassCode,
		},
		{
			name: "real go vet annotation is code",
			logs: `Run go vet ./...
internal/autopilot/metrics.go:42:6: undefined: foo
##[error]Process completed with exit code 2.`,
			want: FailureClassCode,
		},
		{
			name: "mixed infra signature and real annotation is code",
			logs: `Run actions/checkout@v4
##[error]Failed to download action 'https://api.github.com/repos/actions/checkout/tarball/v4'. Error: Response status code does not indicate success: 429 (Too Many Requests).
Run golangci-lint run ./...
internal/autopilot/controller.go:99:1: undefined: bar
##[error]Process completed with exit code 1.`,
			want: FailureClassCode,
		},
		{
			name: "empty logs classify as code",
			logs: "",
			want: FailureClassCode,
		},
		{
			name: "whitespace-only logs classify as code",
			logs: "   \n\t  ",
			want: FailureClassCode,
		},
		{
			name: "unrecognized failure classifies as code",
			logs: `Run npm test
Test suite failed to run
##[error]Process completed with exit code 1.`,
			want: FailureClassCode,
		},
		{
			name: "429 without action-download context is code",
			logs: `Run curl https://example.com/health
429 Too Many Requests
##[error]Process completed with exit code 1.`,
			want: FailureClassCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCheckFailure(tt.logs)
			if got != tt.want {
				t.Errorf("classifyCheckFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClassifyPRFailure_TableDriven covers aggregation across multiple
// scoped failed checks on one SHA (GH-4533): infra only when there is at
// least one failed check and every single one classifies infra.
func TestClassifyPRFailure_TableDriven(t *testing.T) {
	infraLog := `##[error]Failed to download action 'https://api.github.com/repos/actions/checkout/tarball/v4'. Error: Response status code does not indicate success: 429 (Too Many Requests).`
	codeLog := `internal/autopilot/controller.go:1234:6: Error return value is not checked (errcheck)`

	tests := []struct {
		name   string
		checks []FailedCheckLog
		want   FailureClass
	}{
		{
			// GH-4779: zero gathered evidence must never fall back to
			// FailureClassCode — that's the exact gap that let the
			// 2026-08-06 outage close a correct PR (#4770) with nothing to
			// point at. FailureClassUnknown routes callers to the
			// non-destructive escalate-and-hold path instead.
			name:   "no failed checks is unknown (zero evidence)",
			checks: nil,
			want:   FailureClassUnknown,
		},
		{
			name: "all checks infra",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, Logs: infraLog},
				{CheckName: "test", JobID: 2, Logs: infraLog},
			},
			want: FailureClassInfra,
		},
		{
			name: "one code check among infra checks is code",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, Logs: infraLog},
				{CheckName: "test", JobID: 2, Logs: codeLog},
			},
			want: FailureClassCode,
		},
		{
			name: "log fetch error (empty logs) among checks is code",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, Logs: infraLog},
				{CheckName: "test", JobID: 2, Logs: ""},
			},
			want: FailureClassCode,
		},
		{
			name: "single infra check is infra",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, Logs: infraLog},
			},
			want: FailureClassInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPRFailure(tt.checks)
			if got != tt.want {
				t.Errorf("classifyPRFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

// billingAnnotation is the real GitHub Actions check-run output text
// (GH-4591 live incident 2026-07-28) surfaced when an org billing problem
// blocks a job from starting at all.
const billingAnnotation = "The job was not started because recent account payments have failed or your spending limit needs to be increased."

// TestIsJobsNeverStartedInfra_TableDriven covers GH-4591's jobs-never-started
// billing-refusal detector in isolation: either an annotation keyword match
// or a known-zero step count is sufficient; an unknown step count must never
// be treated as zero.
func TestIsJobsNeverStartedInfra_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		chk  FailedCheckLog
		want bool
	}{
		{
			name: "annotation text matches billing refusal phrase",
			chk:  FailedCheckLog{CheckName: "build", AnnotationText: billingAnnotation},
			want: true,
		},
		{
			name: "annotation text matches spending limit phrasing case-insensitively",
			chk:  FailedCheckLog{CheckName: "build", AnnotationText: "Your Spending Limit needs review."},
			want: true,
		},
		{
			name: "steps known and zero",
			chk:  FailedCheckLog{CheckName: "build", StepsKnown: true, StepsCount: 0},
			want: true,
		},
		{
			name: "steps known and non-zero is not infra",
			chk:  FailedCheckLog{CheckName: "build", StepsKnown: true, StepsCount: 4},
			want: false,
		},
		{
			name: "steps unknown (zero value) must not be treated as zero steps",
			chk:  FailedCheckLog{CheckName: "build"},
			want: false,
		},
		{
			name: "no annotation and no step info is not infra",
			chk:  FailedCheckLog{CheckName: "build", Logs: "some unrelated log text"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJobsNeverStartedInfra(tt.chk)
			if got != tt.want {
				t.Errorf("isJobsNeverStartedInfra() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestClassifyPRFailure_JobsNeverStarted_TableDriven covers the GH-4591
// acceptance scenarios end to end through classifyPRFailure: a synthetic
// billing-refused run (zero steps + annotation) classifies infra_billing; a
// real test failure (steps executed, one failing) classifies code; and a
// mixed run (one billing-refused check alongside one genuine code failure)
// classifies code — a mixed signal must never allow an auto-retry, same
// fail-safe rule as the pre-existing runner-infra aggregation.
func TestClassifyPRFailure_JobsNeverStarted_TableDriven(t *testing.T) {
	codeLog := `internal/autopilot/controller.go:1234:6: Error return value is not checked (errcheck)`

	tests := []struct {
		name   string
		checks []FailedCheckLog
		want   FailureClass
	}{
		{
			name: "billing-refused run: all jobs zero steps with annotation classifies infra_billing",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, AnnotationText: billingAnnotation, StepsKnown: true, StepsCount: 0},
				{CheckName: "test", JobID: 2, AnnotationText: billingAnnotation, StepsKnown: true, StepsCount: 0},
			},
			want: FailureClassInfraBilling,
		},
		{
			name: "billing-refused run: zero steps alone (no annotation captured) still classifies infra_billing",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, StepsKnown: true, StepsCount: 0},
				{CheckName: "test", JobID: 2, StepsKnown: true, StepsCount: 0},
			},
			want: FailureClassInfraBilling,
		},
		{
			name: "real test failure: steps executed, one failing classifies code",
			checks: []FailedCheckLog{
				{CheckName: "test", JobID: 1, Logs: codeLog, StepsKnown: true, StepsCount: 6},
			},
			want: FailureClassCode,
		},
		{
			name: "mixed run: billing-refused check alongside genuine code failure classifies code",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, AnnotationText: billingAnnotation, StepsKnown: true, StepsCount: 0},
				{CheckName: "test", JobID: 2, Logs: codeLog, StepsKnown: true, StepsCount: 6},
			},
			want: FailureClassCode,
		},
		{
			name: "mixed infra-family run: runner-infra check alongside billing-refused check classifies infra_billing",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, Logs: `##[error]Failed to download action 'https://api.github.com/repos/actions/checkout/tarball/v4'. Error: Response status code does not indicate success: 429 (Too Many Requests).`},
				{CheckName: "test", JobID: 2, AnnotationText: billingAnnotation, StepsKnown: true, StepsCount: 0},
			},
			want: FailureClassInfraBilling,
		},
		{
			// Regression guard: an incomplete/stale jobs-API response (e.g. a
			// job lookup that succeeds but returns no step breakdown) must
			// not misclassify a genuine code failure as billing just because
			// StepsCount reads 0 — the real compiler annotation in the log is
			// definitive proof the job actually ran.
			name: "real annotation wins over a StepsCount=0 false positive",
			checks: []FailedCheckLog{
				{CheckName: "lint", JobID: 1, Logs: codeLog, StepsKnown: true, StepsCount: 0},
			},
			want: FailureClassCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPRFailure(tt.checks)
			if got != tt.want {
				t.Errorf("classifyPRFailure() = %q, want %q", got, tt.want)
			}
			if got.IsInfra() != (tt.want != FailureClassCode) {
				t.Errorf("IsInfra() = %v inconsistent with want %q", got.IsInfra(), tt.want)
			}
		})
	}
}

// TestClassifyCheckFailureFull_StructuralSignals is GH-4779's core regression
// suite: structural, non-prose signals must classify infra even when the log
// text matches none of TASK-418's four hardcoded signatures — the exact gap
// that let the 2026-08-06 GitHub Actions outage misclassify PR #4770 as a
// code failure.
func TestClassifyCheckFailureFull_StructuralSignals(t *testing.T) {
	tests := []struct {
		name       string
		chk        FailedCheckLog
		wantClass  FailureClass
		wantSignal classificationSignal
	}{
		{
			// The 2026-08-06 incident's exact shape: the failing step is
			// GitHub's own synthetic "Set up job", the log prose is
			// "Failed to resolve action download info. Error: Service
			// Unavailable" (none of TASK-418's four signatures), and the
			// conclusion is the ordinary "failure" — not one of the newer
			// startup_failure/stale conclusions. Structural step-name
			// evidence alone must be enough.
			name: "2026-08-06 outage replay: synthetic Set up job step, unrecognized prose, conclusion failure",
			chk: FailedCheckLog{
				CheckName:       "build",
				JobID:           1,
				Conclusion:      "failure",
				FailingStepName: "Set up job",
				// StepsCount=1 (only the synthetic "Set up job" step ever
				// ran) deliberately keeps this fixture out of
				// isJobsNeverStartedInfra's StepsCount==0 billing-refusal
				// check — that shape is for jobs with an empty step array
				// altogether. This incident's job got as far as GitHub's own
				// synthetic step and died there, which is exactly the
				// broader isStructuralInfra RepoStepsExecuted==0 signal.
				StepsKnown:        true,
				StepsCount:        1,
				RepoStepsExecuted: 0,
				Logs:              "##[error]Failed to resolve action download info. Error: Service Unavailable",
			},
			wantClass:  FailureClassInfra,
			wantSignal: signalStructural,
		},
		{
			name: "zero repo-defined steps executed, failing step unresolved",
			chk: FailedCheckLog{
				CheckName:         "test",
				JobID:             2,
				Conclusion:        "failure",
				StepsKnown:        true,
				StepsCount:        2, // both synthetic ("Set up job", "Complete job"); no repo step ran
				RepoStepsExecuted: 0,
				Logs:              "some unrecognized runner-provisioning error",
			},
			wantClass:  FailureClassInfra,
			wantSignal: signalStructural,
		},
		{
			name: "conclusion startup_failure with no useful logs",
			chk: FailedCheckLog{
				CheckName:  "lint",
				JobID:      3,
				Conclusion: conclusionStartupFailure,
			},
			wantClass:  FailureClassInfra,
			wantSignal: signalStructural,
		},
		{
			name: "conclusion stale with no useful logs",
			chk: FailedCheckLog{
				CheckName:  "e2e",
				JobID:      4,
				Conclusion: conclusionStale,
			},
			wantClass:  FailureClassInfra,
			wantSignal: signalStructural,
		},
		{
			// A repo-defined step actually ran and executed steps are
			// non-zero, so a structural signal must not fire even though
			// the conclusion is "failure" and the log is unrecognized —
			// falls through to the fail-safe prose default of code.
			name: "repo steps executed, unrecognized log, conclusion failure classifies code",
			chk: FailedCheckLog{
				CheckName:         "test",
				JobID:             5,
				Conclusion:        "failure",
				FailingStepName:   "go test ./...",
				StepsKnown:        true,
				StepsCount:        4,
				RepoStepsExecuted: 3,
				Logs:              "--- FAIL: TestSomething\nassertion failed",
			},
			wantClass:  FailureClassCode,
			wantSignal: signalNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClass, gotSignal := classifyCheckFailureFull(tt.chk)
			if gotClass != tt.wantClass {
				t.Errorf("classifyCheckFailureFull() class = %q, want %q", gotClass, tt.wantClass)
			}
			if gotSignal != tt.wantSignal {
				t.Errorf("classifyCheckFailureFull() signal = %q, want %q", gotSignal, tt.wantSignal)
			}
		})
	}
}

// TestNewVerdict_ConstructionRules is the TASK-459 Phase 1 contract test:
// an evidence-free construction path must always yield FailureClassUnknown,
// regardless of the class requested, and every destructive class survives
// construction only when non-empty evidence is supplied. Scope and source
// must round-trip unchanged in both cases.
func TestNewVerdict_ConstructionRules(t *testing.T) {
	tests := []struct {
		name         string
		class        FailureClass
		evidence     string
		source       string
		scope        string
		wantClass    FailureClass
		wantEvidence string
	}{
		{
			name:         "empty evidence downgrades FailureClassCode to Unknown",
			class:        FailureClassCode,
			evidence:     "",
			source:       "classifyPRFailure",
			scope:        "qf-studio/pilot",
			wantClass:    FailureClassUnknown,
			wantEvidence: "",
		},
		{
			name:         "empty evidence downgrades FailureClassInfra to Unknown",
			class:        FailureClassInfra,
			evidence:     "",
			source:       "classifyPRFailure",
			scope:        "qf-studio/pilot",
			wantClass:    FailureClassUnknown,
			wantEvidence: "",
		},
		{
			name:         "empty evidence downgrades FailureClassInfraBilling to Unknown",
			class:        FailureClassInfraBilling,
			evidence:     "",
			source:       "classifyPRFailure",
			scope:        "qf-studio/pilot",
			wantClass:    FailureClassUnknown,
			wantEvidence: "",
		},
		{
			name:         "FailureClassCode with evidence is retained",
			class:        FailureClassCode,
			evidence:     "internal/autopilot/controller.go:1234:6: undefined: foo",
			source:       "classifyCheckFailureFull",
			scope:        "qf-studio/pilot",
			wantClass:    FailureClassCode,
			wantEvidence: "internal/autopilot/controller.go:1234:6: undefined: foo",
		},
		{
			name:         "FailureClassInfra with evidence is retained",
			class:        FailureClassInfra,
			evidence:     "Failed to download action: 429 (Too Many Requests)",
			source:       "classifyCheckFailureFull",
			scope:        "qf-studio/pilot",
			wantClass:    FailureClassInfra,
			wantEvidence: "Failed to download action: 429 (Too Many Requests)",
		},
		{
			name:         "FailureClassInfraBilling with evidence is retained",
			class:        FailureClassInfraBilling,
			evidence:     "spending limit reached",
			source:       "isJobsNeverStartedInfra",
			scope:        "qf-studio/pilot-canary-sandbox",
			wantClass:    FailureClassInfraBilling,
			wantEvidence: "spending limit reached",
		},
		{
			name:         "explicit FailureClassUnknown with evidence keeps Unknown class but retains evidence text",
			class:        FailureClassUnknown,
			evidence:     "diagnostic note",
			source:       "classifyPRFailure",
			scope:        "qf-studio/pilot",
			wantClass:    FailureClassUnknown,
			wantEvidence: "diagnostic note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerdict(tt.class, tt.evidence, tt.source, tt.scope)
			if v.Class() != tt.wantClass {
				t.Errorf("NewVerdict().Class() = %q, want %q", v.Class(), tt.wantClass)
			}
			if v.Evidence() != tt.wantEvidence {
				t.Errorf("NewVerdict().Evidence() = %q, want %q", v.Evidence(), tt.wantEvidence)
			}
			if v.Source() != tt.source {
				t.Errorf("NewVerdict().Source() = %q, want %q", v.Source(), tt.source)
			}
			if v.Scope() != tt.scope {
				t.Errorf("NewVerdict().Scope() = %q, want %q", v.Scope(), tt.scope)
			}
		})
	}
}

// TestNewUnknownVerdict always yields FailureClassUnknown with empty
// evidence, regardless of caller input — there is no evidence to pass, by
// construction, since this constructor exists precisely for the
// zero-gathered-evidence case (GH-4779). Scope and source still round-trip.
func TestNewUnknownVerdict(t *testing.T) {
	tests := []struct {
		name   string
		source string
		scope  string
	}{
		{name: "typical scope", source: "classifyPRFailure", scope: "qf-studio/pilot"},
		{name: "different project scope", source: "checkRequiredChecks", scope: "qf-studio/pilot-canary-sandbox"},
		{name: "empty source and scope still yields Unknown", source: "", scope: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewUnknownVerdict(tt.source, tt.scope)
			if v.Class() != FailureClassUnknown {
				t.Errorf("NewUnknownVerdict().Class() = %q, want %q", v.Class(), FailureClassUnknown)
			}
			if v.Evidence() != "" {
				t.Errorf("NewUnknownVerdict().Evidence() = %q, want empty", v.Evidence())
			}
			if v.Source() != tt.source {
				t.Errorf("NewUnknownVerdict().Source() = %q, want %q", v.Source(), tt.source)
			}
			if v.Scope() != tt.scope {
				t.Errorf("NewUnknownVerdict().Scope() = %q, want %q", v.Scope(), tt.scope)
			}
		})
	}
}

// TestVerdict_EvidenceFreeNeverAuthorizesDestructiveClass is the explicit
// regression test for the TASK-459 invariant stated on the Verdict doc
// comment: sweeping every non-Unknown FailureClass value through NewVerdict
// with empty evidence must always yield Unknown, never the requested
// destructive class. This is the single test that would fail first if a
// future edit accidentally let a destructive class slip through without
// evidence.
func TestVerdict_EvidenceFreeNeverAuthorizesDestructiveClass(t *testing.T) {
	destructiveClasses := []FailureClass{FailureClassCode, FailureClassInfra, FailureClassInfraBilling}
	for _, class := range destructiveClasses {
		t.Run(string(class), func(t *testing.T) {
			v := NewVerdict(class, "", "test-source", "test-scope")
			if v.Class() != FailureClassUnknown {
				t.Errorf("NewVerdict(%q, \"\", ...).Class() = %q, want %q (evidence-free must never authorize a destructive class)",
					class, v.Class(), FailureClassUnknown)
			}
			if v.Evidence() != "" {
				t.Errorf("NewVerdict(%q, \"\", ...).Evidence() = %q, want empty", class, v.Evidence())
			}
		})
	}
}

// TestVerdict_ZeroValue is the TASK-459 Phase 2 regression test for PR#4802
// review finding 1: a zero-value Verdict (var v Verdict, or any accidental
// Verdict{} composite literal) must read as FailureClassUnknown with empty
// evidence — never as some other class a naive `class != FailureClassUnknown`
// gate would treat as authorized, since the zero value's unexported class
// field is "" (neither FailureClassUnknown's string value nor any
// destructive class).
func TestVerdict_ZeroValue(t *testing.T) {
	var v Verdict
	if got := v.Class(); got != FailureClassUnknown {
		t.Errorf("zero-value Verdict.Class() = %q, want %q", got, FailureClassUnknown)
	}
	if got := v.Evidence(); got != "" {
		t.Errorf("zero-value Verdict.Evidence() = %q, want empty", got)
	}
	if got := v.Source(); got != "" {
		t.Errorf("zero-value Verdict.Source() = %q, want empty", got)
	}
	if got := v.Scope(); got != "" {
		t.Errorf("zero-value Verdict.Scope() = %q, want empty", got)
	}
	if v.AuthorizesDestructive() {
		t.Error("zero-value Verdict.AuthorizesDestructive() = true, want false (finding-1 regression: a zero-value Verdict must never authorize a destructive action)")
	}
}

// TestVerdict_AuthorizesDestructive is the TASK-459 Phase 2 contract test
// for the single shared gate every destructive rung in the CI-failure path
// consumes: true only for a non-Unknown class carrying non-empty evidence,
// false for every other combination a Verdict can actually be constructed
// in — including the finding-1 zero value, which NewVerdict/NewUnknownVerdict
// can never produce but a bare `Verdict{}` composite literal still can.
func TestVerdict_AuthorizesDestructive(t *testing.T) {
	tests := []struct {
		name string
		v    Verdict
		want bool
	}{
		{
			name: "zero value never authorizes (finding 1)",
			v:    Verdict{},
			want: false,
		},
		{
			name: "NewUnknownVerdict never authorizes",
			v:    NewUnknownVerdict("classifyPRFailure", "qf-studio/pilot"),
			want: false,
		},
		{
			name: "evidence-free NewVerdict request never authorizes",
			v:    NewVerdict(FailureClassCode, "", "classifyPRFailure", "qf-studio/pilot"),
			want: false,
		},
		{
			name: "evidenced FailureClassCode authorizes",
			v:    NewVerdict(FailureClassCode, "ci:code(real_annotation)", "classifyPRFailure", "qf-studio/pilot"),
			want: true,
		},
		{
			name: "evidenced FailureClassInfra authorizes",
			v:    NewVerdict(FailureClassInfra, "ci:infra(structural)", "classifyPRFailure", "qf-studio/pilot"),
			want: true,
		},
		{
			name: "evidenced FailureClassInfraBilling authorizes",
			v:    NewVerdict(FailureClassInfraBilling, "ci:infra_billing(billing)", "classifyPRFailure", "qf-studio/pilot"),
			want: true,
		},
		{
			name: "explicit Unknown with evidence still never authorizes",
			v:    NewVerdict(FailureClassUnknown, "diagnostic note", "classifyPRFailure", "qf-studio/pilot"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.AuthorizesDestructive(); got != tt.want {
				t.Errorf("AuthorizesDestructive() = %v, want %v (class=%q evidence=%q)", got, tt.want, tt.v.Class(), tt.v.Evidence())
			}
		})
	}
}

// TestCIFailureVerdictEvidence_NamesConcreteChecks verifies
// ciFailureVerdictEvidence (via newCIFailureVerdict) names the specific
// failed check(s)/signal(s) that produced class, not a restatement of the
// class itself — the evidence-quality requirement from TASK-459 Phase 2's
// task doc ("not a generic 'checks failed' restatement of the verdict
// itself").
func TestCIFailureVerdictEvidence_NamesConcreteChecks(t *testing.T) {
	t.Run("infra aggregate names the infra-classified check", func(t *testing.T) {
		checks := []FailedCheckLog{
			{CheckName: "build", Logs: "##[error]Failed to run: step\nUnexpected HTTP response: 503"},
		}
		v := newCIFailureVerdict(classifyPRFailure(checks), checks, "qf-studio/pilot")
		if v.Class() != FailureClassInfra {
			t.Fatalf("class = %q, want %q", v.Class(), FailureClassInfra)
		}
		if !strings.Contains(v.Evidence(), "build") {
			t.Errorf("Evidence() = %q, want it to name the failing check %q", v.Evidence(), "build")
		}
		if v.Evidence() == string(FailureClassInfra) {
			t.Errorf("Evidence() = %q, must not be a bare restatement of the class", v.Evidence())
		}
	})

	t.Run("code aggregate names only the check(s) that tipped it to code", func(t *testing.T) {
		checks := []FailedCheckLog{
			{CheckName: "lint", Logs: "internal/foo.go:12:3: undefined: bar"},
			{CheckName: "build", Logs: "##[error]Failed to run: step\nUnexpected HTTP response: 503"},
		}
		v := newCIFailureVerdict(classifyPRFailure(checks), checks, "qf-studio/pilot")
		if v.Class() != FailureClassCode {
			t.Fatalf("class = %q, want %q", v.Class(), FailureClassCode)
		}
		if !strings.Contains(v.Evidence(), "lint") {
			t.Errorf("Evidence() = %q, want it to name the code-classified check %q", v.Evidence(), "lint")
		}
		if strings.Contains(v.Evidence(), "build") {
			t.Errorf("Evidence() = %q, must not name the infra-classified check that did not tip the aggregate", v.Evidence())
		}
	})

	t.Run("zero checks constructs via NewUnknownVerdict", func(t *testing.T) {
		v := newCIFailureVerdict(classifyPRFailure(nil), nil, "qf-studio/pilot")
		if v.Class() != FailureClassUnknown {
			t.Fatalf("class = %q, want %q", v.Class(), FailureClassUnknown)
		}
		if v.Evidence() != "" {
			t.Errorf("Evidence() = %q, want empty", v.Evidence())
		}
		if v.AuthorizesDestructive() {
			t.Error("AuthorizesDestructive() = true, want false for zero-gathered-evidence verdict")
		}
	})
}
