package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_HandleMergeConflict_MechanicalResolutionSucceeds is an
// integration-style GH-4328 test reproducing the original incident: two
// sibling branches each add a different dependency, so the conflict surface
// is exactly go.mod/go.sum. handleMergeConflict should resolve it mechanically
// via attemptMechanicalConflictResolution, push the fix, and advance the PR to
// StageWaitingCI — never reaching closeAndReexecute.
func TestController_HandleMergeConflict_MechanicalResolutionSucceeds(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	newLocalReplaceModule(t, local, "depa", "depa")
	newLocalReplaceModule(t, local, "depb", "depb")

	writeFixtureFile(t, local, "go.mod", strings.Join([]string{
		"module fixture",
		"",
		"go 1.25",
		"",
		"replace example.com/depa => ./localdeps/depa",
		"",
		"replace example.com/depb => ./localdeps/depb",
		"",
	}, "\n"))
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "scaffold local replace modules")
	runFixtureGit(t, local, "push", "origin", "main")

	runFixtureGit(t, local, "checkout", "-b", "feature/dep-a")
	appendFixtureFile(t, local, "go.mod", "require example.com/depa v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "usea.go", "package fixture\n\nimport \"example.com/depa\"\n\nvar UseDepA = depa.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depa")
	runFixtureGit(t, local, "push", "origin", "feature/dep-a")

	runFixtureGit(t, local, "checkout", "main")
	appendFixtureFile(t, local, "go.mod", "require example.com/depb v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "useb.go", "package fixture\n\nimport \"example.com/depb\"\n\nvar UseDepB = depb.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depb")
	runFixtureGit(t, local, "push", "origin", "main")

	var prClosed, commentPosted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/55/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/55/comments" && r.Method == http.MethodPost:
			commentPosted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxRebaseAttempts = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[55] = &PRState{
		PRNumber:    55,
		PRURL:       "https://github.com/owner/repo/pull/55",
		IssueNumber: 20,
		BranchName:  "feature/dep-a",
		HeadSHA:     "deadbeef",
		Stage:       StageMerging,
		CreatedAt:   time.Now(),
	}
	c.mu.Unlock()

	if err := c.handleMergeConflict(ctx, c.activePRs[55]); err != nil {
		t.Fatalf("handleMergeConflict: %v", err)
	}

	pr, ok := c.GetPRState(55)
	if !ok {
		t.Fatal("PR 55 not found in activePRs")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s (mechanical resolution should succeed)", pr.Stage, StageWaitingCI)
	}
	if pr.RebaseAttempts != 1 {
		t.Fatalf("RebaseAttempts = %d, want 1", pr.RebaseAttempts)
	}
	if prClosed {
		t.Fatal("PR should not have been closed — mechanical resolution should have succeeded")
	}
	if commentPosted {
		t.Fatal("no close-and-reexecute comment should have been posted")
	}

	logOut := runFixtureGit(t, local, "log", "-1", "--format=%s", "origin/feature/dep-a")
	if strings.TrimSpace(logOut) != mechanicalResolutionCommitMessage {
		t.Fatalf("expected top commit on origin/feature/dep-a to be %q, got %q", mechanicalResolutionCommitMessage, strings.TrimSpace(logOut))
	}
}

// TestController_HandleMergeConflict_SourceFileConflictEscalatesInsteadOfClosing
// verifies that a conflict touching a source file (not just go.mod/go.sum)
// no longer falls through to closeAndReexecute (GH-4459): once the local
// merge replay determines the conflict surface and it isn't go.mod/go.sum-only,
// the PR must be held via escalateAndHold instead of closed — closing it here
// throws away in-flight work for a conflict shape no automatic rung can ever
// resolve.
func TestController_HandleMergeConflict_SourceFileConflictEscalatesInsteadOfClosing(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "feature/x")
	writeFixtureFile(t, local, "main.go", "package fixture\n\nfunc X() int { return 1 }\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add X returning 1")
	runFixtureGit(t, local, "push", "origin", "feature/x")

	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "main.go", "package fixture\n\nfunc X() int { return 2 }\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "change X to return 2")
	runFixtureGit(t, local, "push", "origin", "main")

	var (
		prClosed        bool
		branchDeleted   bool
		escalateComment string
		issueCreated    bool
		labelsAdded     []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/56/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/56" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/56/comments" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			escalateComment = body["body"]
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		case r.URL.Path == "/repos/owner/repo/issues/21/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 999})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[56] = &PRState{
		PRNumber:    56,
		PRURL:       "https://github.com/owner/repo/pull/56",
		IssueNumber: 21,
		BranchName:  "feature/x",
		HeadSHA:     "deadbeef",
		Stage:       StageMerging,
		CreatedAt:   time.Now(),
	}
	c.mu.Unlock()

	if err := c.handleMergeConflict(ctx, c.activePRs[56]); err != nil {
		t.Fatalf("handleMergeConflict: %v", err)
	}

	pr, ok := c.GetPRState(56)
	if !ok {
		t.Fatal("PR 56 not found in activePRs")
	}
	if pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s (escalateAndHold)", pr.Stage, StageFailed)
	}
	if pr.RebaseAttempts != 0 {
		t.Fatalf("RebaseAttempts = %d, want 0 (mechanical resolution never ran to success)", pr.RebaseAttempts)
	}
	if prClosed {
		t.Fatal("PR must NOT be closed for a non-go.mod/go.sum conflict — escalateAndHold holds it instead (GH-4459)")
	}
	if c.consumeSelfClosedMarker(56) {
		t.Fatal("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if branchDeleted {
		t.Fatal("branch must NOT be deleted when the PR is held via escalateAndHold")
	}
	if issueCreated {
		t.Fatal("no re-execution issue should be created when the PR is held via escalateAndHold")
	}
	if pr.Error != "auto-rebase failed" {
		t.Fatalf("Error = %q, want %q", pr.Error, "auto-rebase failed")
	}
	if escalateComment == "" || !strings.Contains(escalateComment, "main.go") {
		t.Fatalf("expected escalateAndHold comment to name the conflicted file, got: %q", escalateComment)
	}
	found := false
	for _, l := range labelsAdded {
		if l == "needs-manual-rebase" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected needs-manual-rebase label on the issue, got labels: %v", labelsAdded)
	}
}
