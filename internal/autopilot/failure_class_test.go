package autopilot

import "testing"

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
			name:   "no failed checks is code",
			checks: nil,
			want:   FailureClassCode,
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
