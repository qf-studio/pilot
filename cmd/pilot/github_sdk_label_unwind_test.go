package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
)

// TestShouldUnwindGithubInProgressLabel_Table is the GH-4961 table-driven
// acceptance test: notifyTaskStartedSDK applies pilot-in-progress BEFORE the
// dispatcher has actually claimed the task (GH-4687 pre-claim ordering). When
// the dispatch that follows drops the pickup — repick backoff, claim lost, or
// any other admission-gate decline surfaced as hr.IsDispatchGated() — and no
// other execution genuinely owns the task, the label just applied is the
// only "in progress" evidence left behind and must be removed again, or the
// issue is wedged forever (every future poll tick skips it). A successful
// dispatch must never trigger the unwind — the happy-path label/comment
// behavior established by GH-4687 must not regress, and no extra label
// round-trip may occur on that path.
func TestShouldUnwindGithubInProgressLabel_Table(t *testing.T) {
	tests := []struct {
		name             string
		notifyAttempted  bool
		hr               *HandlerResult
		dispatcherActive bool
		want             bool
	}{
		{
			name:             "dispatch dropped by repick backoff, no active execution — unwind",
			notifyAttempted:  true,
			hr:               &HandlerResult{Success: false, Error: executor.ErrDispatchGated},
			dispatcherActive: false,
			want:             true,
		},
		{
			name:             "dispatch dropped by claim lost, no active execution — unwind",
			notifyAttempted:  true,
			hr:               &HandlerResult{Success: false, Error: executor.ErrDispatchGated},
			dispatcherActive: false,
			want:             true,
		},
		{
			name:             "dispatch dropped, but a different execution is genuinely active — must not strip",
			notifyAttempted:  true,
			hr:               &HandlerResult{Success: false, Error: executor.ErrDispatchGated},
			dispatcherActive: true,
			want:             false,
		},
		{
			name:             "dispatch succeeds — label applied exactly as today, no unwind",
			notifyAttempted:  true,
			hr:               &HandlerResult{Success: true},
			dispatcherActive: false,
			want:             false,
		},
		{
			name:             "dispatch succeeds while another execution also reports active — still no unwind",
			notifyAttempted:  true,
			hr:               &HandlerResult{Success: true},
			dispatcherActive: true,
			want:             false,
		},
		{
			name:             "label was never applied this call (issue closed) — nothing to unwind",
			notifyAttempted:  false,
			hr:               &HandlerResult{Success: false, Error: executor.ErrDispatchGated},
			dispatcherActive: false,
			want:             false,
		},
		{
			name:             "genuine execution failure (not gated) — must not unwind the label",
			notifyAttempted:  true,
			hr:               &HandlerResult{Success: false, Error: errors.New("execution failed: boom")},
			dispatcherActive: false,
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUnwindGithubInProgressLabel(tt.notifyAttempted, tt.hr, tt.dispatcherActive); got != tt.want {
				t.Errorf("shouldUnwindGithubInProgressLabel(%v, %+v, %v) = %v, want %v",
					tt.notifyAttempted, tt.hr, tt.dispatcherActive, got, tt.want)
			}
		})
	}
}

// TestShouldUnwindGithubInProgressLabel_WrappedGatedError confirms the helper
// consults hr.IsDispatchGated() (errors.Is-based), not a bare equality check,
// so a wrapped executor.ErrDispatchGated — as handleIssueGeneric's own
// hrErr construction can produce via fmt.Errorf("...: %w", ...) elsewhere in
// the codebase — is still recognized as a gated drop.
func TestShouldUnwindGithubInProgressLabel_WrappedGatedError(t *testing.T) {
	hr := &HandlerResult{Success: false, Error: errWrap(executor.ErrDispatchGated)}
	if !hr.IsDispatchGated() {
		t.Fatal("expected wrapped ErrDispatchGated to satisfy IsDispatchGated()")
	}
	if !shouldUnwindGithubInProgressLabel(true, hr, false) {
		t.Error("expected unwind for a wrapped ErrDispatchGated with no active execution")
	}
}

func errWrap(err error) error {
	return &wrappedErr{msg: "dispatch: " + err.Error(), err: err}
}

type wrappedErr struct {
	msg string
	err error
}

func (w *wrappedErr) Error() string { return w.msg }
func (w *wrappedErr) Unwrap() error { return w.err }

// TestGithubHandlerSDK_LabelUnwindWired is a source-level guard (mirrors
// TestGithubHandlerSDK_NotifyTaskStartedWired's established pattern for this
// otherwise-unexercisable function): handleGithubIssueEventSDK must call
// shouldUnwindGithubInProgressLabel AFTER handleIssueGeneric returns (so the
// dispatch outcome is known), and the unwind call itself must go through
// unwindGithubStartedLabel with the same specClient the apply used — never a
// fresh client — so it targets the same repo/token context.
//
// GH-5042/GH-5028: this test used to pin
// `specClient.RemoveLabel(ctx, repoOwner, repoName, issueNum, pilotLabel)` as
// correct — but pilotLabel is the poller's trigger label ("pilot"), not the
// label notifyTaskStartedSDK actually applies (githubSDK.LabelInProgress,
// "pilot-in-progress"; studio-sdk's NotifyTaskStarted never reads the
// triggerLabel it's constructed with). Removing pilotLabel on a dropped
// dispatch pickup strips the label the poller filters its queue by, so a
// queued-but-never-dispatched issue comes back from this "correction"
// invisible to every future poll — the exact live incident. See
// unwindGithubStartedLabel's doc comment and
// TestUnwindGithubStartedLabel_GH5028 for the behavioral regression test.
func TestGithubHandlerSDK_LabelUnwindWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	handleIssueGenericIdx := strings.Index(body, "hr, execErr := handleIssueGeneric(ctx, deps, info, task)")
	unwindIdx := strings.Index(body, "shouldUnwindGithubInProgressLabel(")
	if handleIssueGenericIdx < 0 || unwindIdx < 0 {
		t.Fatal("expected both the handleIssueGeneric call and shouldUnwindGithubInProgressLabel in handleGithubIssueEventSDK")
	}
	if unwindIdx < handleIssueGenericIdx {
		t.Error("shouldUnwindGithubInProgressLabel must be consulted after handleIssueGeneric returns, once the dispatch outcome is known")
	}

	if !strings.Contains(body, "unwindGithubStartedLabel(ctx, specClient, repoOwner, repoName, issueNum)") {
		t.Error("the unwind path must call unwindGithubStartedLabel with the same owner/repo/issue the apply used")
	}
	if strings.Contains(body, "specClient.RemoveLabel(ctx, repoOwner, repoName, issueNum, pilotLabel)") {
		t.Error("GH-5028: the unwind must never remove pilotLabel (the trigger label) directly — that strands a queued-but-never-dispatched issue unpollable")
	}
}
