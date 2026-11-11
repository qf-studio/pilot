package autopilot

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapterhealth"
	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// newCIUnreadableFixture is the outage fixture with a token that can read
// neither check runs, Actions runs nor commit statuses, and an adapter health
// registry wired in (GH-6304).
func newCIUnreadableFixture(t *testing.T) (*ciOutageFixture, *adapterhealth.Registry) {
	t.Helper()
	f := newCIOutageFixture(t, nil)
	f.c.ciMonitor.SetStepLogClient(ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, f.srvURL))
	reg := adapterhealth.NewRegistry()
	f.c.SetAdapterHealth(reg)
	f.setMode(ciOutageUnreadable)
	return f, reg
}

func githubAdapterStatus(t *testing.T, reg *adapterhealth.Registry) (adapterhealth.Status, bool) {
	t.Helper()
	for _, st := range reg.Snapshot() {
		if st.Name == "github" {
			return st, true
		}
	}
	return adapterhealth.Status{}, false
}

func TestWaitingCI_TokenCannotReadCI_CommentsOnceAndHolds(t *testing.T) {
	f, _ := newCIUnreadableFixture(t)

	// The first poll holds at once: no five-error threshold, one comment.
	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if !f.pr.APIOutageHoldActive {
		t.Fatal("APIOutageHoldActive = false after the first unreadable poll, want true")
	}
	if got := f.comments.Load(); got != 1 {
		t.Fatalf("comments after first poll = %d, want 1", got)
	}

	// Many more polls, well past the outage budget: still one comment, still
	// waiting for CI, never labelled pilot-failed.
	for i := 0; i < 60; i++ {
		f.now = f.pr.APIOutageNextPoll
		f.tick()
	}
	if f.now.Sub(f.pr.APIOutageSince) <= f.c.apiOutageBudget() {
		t.Fatalf("test did not run past the %s budget (ran %s)", f.c.apiOutageBudget(), f.now.Sub(f.pr.APIOutageSince))
	}
	if got := f.comments.Load(); got != 1 {
		t.Errorf("comments after many polls = %d, want 1", got)
	}
	if f.pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", f.pr.Stage, StageWaitingCI)
	}
	if f.pr.TerminalLabel != "" || f.pr.Error != "" {
		t.Errorf("TerminalLabel = %q, Error = %q, want both empty", f.pr.TerminalLabel, f.pr.Error)
	}
	if !f.pr.CIUnreadableNotified {
		t.Error("CIUnreadableNotified = false, want true")
	}

	// The dedup flag survives a restart.
	saved, err := f.store.GetPRState("owner/repo", 42)
	if err != nil || saved == nil {
		t.Fatalf("GetPRState = %v, %v", saved, err)
	}
	if !saved.CIUnreadableNotified {
		t.Error("persisted CIUnreadableNotified = false, want true")
	}
}

func TestWaitingCI_TokenCannotReadCI_CommentAndAlert(t *testing.T) {
	f, _ := newCIUnreadableFixture(t)

	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}

	body, _ := f.commentBody.Load().(string)
	for _, want := range []string{
		"Pilot cannot read the CI result for this pull request with the configured GitHub token (missing: actions runs, commit statuses).",
		"Add Actions (Read) and Commit statuses (Read) to the fine-grained token, or use a classic token with the repo scope.",
		"Pilot checks again when the token changes.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment %q does not contain %q", body, want)
		}
	}

	evs := f.outageEvents()
	if len(evs) != 1 {
		t.Fatalf("ci_poll_api_outage alerts = %d, want 1", len(evs))
	}
	if got := evs[0].Metadata["reason"]; got != "token_cannot_read_ci" {
		t.Errorf("alert reason = %q, want token_cannot_read_ci", got)
	}

	// Later polls do not repeat the alert.
	for i := 0; i < 5; i++ {
		f.now = f.pr.APIOutageNextPoll
		f.tick()
	}
	if n := len(f.outageEvents()); n != 1 {
		t.Errorf("ci_poll_api_outage alerts after more polls = %d, want 1", n)
	}
}

func TestWaitingCI_TokenCannotReadCI_AdapterDegradedThenRecovers(t *testing.T) {
	f, reg := newCIUnreadableFixture(t)

	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	st, ok := githubAdapterStatus(t, reg)
	if !ok {
		t.Fatal("github adapter missing from the snapshot")
	}
	if st.Healthy {
		t.Error("github adapter Healthy = true while the token cannot read CI")
	}
	if !strings.Contains(st.LastError, "CI unreadable") || !strings.Contains(st.LastError, "actions runs") {
		t.Errorf("LastError = %q, want the CI unreadable message", st.LastError)
	}
	if st.RestartCount != 0 {
		t.Errorf("RestartCount = %d, want 0", st.RestartCount)
	}

	// The token is fixed: the next due poll reads CI, the hold ends and the
	// adapter is healthy again.
	f.setMode(ciOutageGreen)
	f.now = f.pr.APIOutageNextPoll
	f.tick()

	if f.pr.APIOutageHoldActive {
		t.Error("hold still active after a successful read")
	}
	st, ok = githubAdapterStatus(t, reg)
	if ok && (!st.Healthy || st.LastError != "") {
		t.Errorf("github adapter after a successful read = %+v, want healthy with no error", st)
	}
}

func TestWaitingCI_TokenCannotReadCI_CommentRetriedAfterFailedPost(t *testing.T) {
	f, _ := newCIUnreadableFixture(t)
	f.commentFail.Store(true)

	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if f.pr.CIUnreadableNotified || f.comments.Load() != 0 {
		t.Fatalf("notified = %v, comments = %d after a failed post, want false and 0", f.pr.CIUnreadableNotified, f.comments.Load())
	}

	f.commentFail.Store(false)
	f.now = f.pr.APIOutageNextPoll
	f.tick()
	if !f.pr.CIUnreadableNotified || f.comments.Load() != 1 {
		t.Errorf("notified = %v, comments = %d on the next poll, want true and 1", f.pr.CIUnreadableNotified, f.comments.Load())
	}
}

// An ordinary outage (5xx) keeps the GH-6056 behaviour: no hold before the
// fifth error, no comment, no degraded adapter, and the PR fails at the budget.
func TestWaitingCI_OrdinaryOutageUnchangedByUnreadableHandling(t *testing.T) {
	f := newCIOutageFixture(t, nil)
	reg := adapterhealth.NewRegistry()
	f.c.SetAdapterHealth(reg)

	for i := 1; i < apiOutageFailureThreshold; i++ {
		if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR %d: %v", i, err)
		}
		if f.pr.APIOutageHoldActive {
			t.Fatalf("hold active after only %d errors", i)
		}
	}
	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("fifth ProcessPR: %v", err)
	}
	if !f.pr.APIOutageHoldActive {
		t.Fatal("hold not active after the fifth error")
	}

	for i := 0; i < 40 && f.pr.Stage == StageWaitingCI; i++ {
		f.now = f.pr.APIOutageNextPoll
		f.tick()
	}
	if f.pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s once the budget elapsed", f.pr.Stage, StageFailed)
	}
	if f.pr.TerminalLabel == "" {
		t.Error("TerminalLabel empty, want the pilot-failed label for an ordinary outage")
	}
	if got := f.comments.Load(); got != 0 {
		t.Errorf("comments = %d, want 0 for an ordinary outage", got)
	}
	if _, ok := githubAdapterStatus(t, reg); ok {
		t.Error("github adapter degraded by an ordinary outage")
	}
	for _, e := range f.outageEvents() {
		if _, has := e.Metadata["reason"]; has {
			t.Errorf("ordinary outage alert carries reason %q", e.Metadata["reason"])
		}
	}
}

func TestPRState_CIUnreadableNotifiedPersists(t *testing.T) {
	store := newTestStateStore(t)
	pr := &PRState{
		PRNumber:             42,
		PRURL:                "https://github.com/owner/repo/pull/42",
		Stage:                StageWaitingCI,
		CIUnreadableNotified: true,
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	got, err := store.GetPRState("owner/repo", 42)
	if err != nil || got == nil || !got.CIUnreadableNotified {
		t.Fatalf("GetPRState = %+v, %v, want CIUnreadableNotified", got, err)
	}
	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil || len(all) != 1 || !all[0].CIUnreadableNotified {
		t.Fatalf("LoadAllPRStates = %+v, %v, want CIUnreadableNotified", all, err)
	}
	if !got.snapshot().CIUnreadableNotified {
		t.Error("snapshot() dropped CIUnreadableNotified")
	}
}
