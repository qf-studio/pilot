package main

import (
	"strings"
	"testing"
)

// TestLabelLifecycleDeadManTrackerName_PerRepo verifies GH-4866's per-repo
// keying: two different repos must resolve to two different tracker names,
// and the same repo must always resolve to the same name (so
// alerts.Engine.RegisterDeadManTracker's memoize-by-name shares one set of
// counters for it across the startup registration in poller_github.go and
// the event-time lookup in handlers.go).
func TestLabelLifecycleDeadManTrackerName_PerRepo(t *testing.T) {
	nameA := labelLifecycleDeadManTrackerName("qf-studio/pilot")
	nameB := labelLifecycleDeadManTrackerName("qf-studio/other-repo")

	if nameA == nameB {
		t.Fatalf("expected distinct tracker names for distinct repos, got %q for both", nameA)
	}
	if got := labelLifecycleDeadManTrackerName("qf-studio/pilot"); got != nameA {
		t.Errorf("expected stable tracker name for the same repo, got %q want %q", got, nameA)
	}
	if !strings.Contains(nameA, "qf-studio/pilot") {
		t.Errorf("expected tracker name to embed the repo, got %q", nameA)
	}
}

// TestGithubHandlerSDK_LabelLifecycleTrackerPerRepo is a source-level guard
// (mirrors TestGithubHandlerSDK_NotifyTaskStartedWired's established pattern
// for this otherwise-unexercisable function): handleGithubIssueEventSDK must
// register the label-lifecycle dead-man tracker under the per-repo name
// (labelLifecycleDeadManTrackerName(repoOwner+"/"+repoName)), not a bare
// global name — GH-4866 found the prior global tracker let one repo's real
// failures get diluted by every other repo's successes on the same counter.
func TestGithubHandlerSDK_LabelLifecycleTrackerPerRepo(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	if !strings.Contains(body, "labelLifecycleDeadManTrackerName(repoOwner+\"/\"+repoName)") {
		t.Error("handleGithubIssueEventSDK must register the label-lifecycle dead-man tracker under the per-repo name labelLifecycleDeadManTrackerName(repoOwner+\"/\"+repoName), not a global name (GH-4866)")
	}
}

// TestStartGithubSDKPollerForRepo_RegistersPerRepoLabelLifecycleTracker is a
// source-level guard: startGithubSDKPollerForRepo must register the
// label-lifecycle tracker at poller startup (hoisted, mirroring the
// self-review tracker registered in the same function) using the per-repo
// name keyed off target.repoFullName, so the tracker exists (and is
// discoverable, e.g. via `pilot doctor`) before the first event ever lands.
func TestStartGithubSDKPollerForRepo_RegistersPerRepoLabelLifecycleTracker(t *testing.T) {
	body := githubFuncBody(t, "poller_github.go", "func startGithubSDKPollerForRepo(")

	if !strings.Contains(body, "labelLifecycleDeadManTrackerName(target.repoFullName)") {
		t.Error("startGithubSDKPollerForRepo must register the label-lifecycle dead-man tracker at startup under labelLifecycleDeadManTrackerName(target.repoFullName), mirroring the self-review tracker registration in the same function (GH-4866)")
	}

	selfReviewIdx := strings.Index(body, "executor.SelfReviewDeadManTrackerName")
	labelLifecycleIdx := strings.Index(body, "labelLifecycleDeadManTrackerName(target.repoFullName)")
	if selfReviewIdx < 0 || labelLifecycleIdx < 0 {
		t.Fatal("expected both the self-review and label-lifecycle tracker registrations in startGithubSDKPollerForRepo")
	}
}
