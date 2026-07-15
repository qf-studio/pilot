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

// TestController_HandleMergeConflict_SourceFileConflictFallsThrough verifies
// that a conflict touching a source file (not just go.mod/go.sum) still falls
// through to the existing closeAndReexecute rung even when a real project
// path is configured and the mechanical rung actually runs the local merge —
// current behavior for this conflict shape is unchanged.
func TestController_HandleMergeConflict_SourceFileConflictFallsThrough(t *testing.T) {
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
		prClosed          bool
		closeCommentBody  string
		pilotLabelAdded   bool
		inProgressRemoved bool
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
			closeCommentBody = body["body"]
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		case r.URL.Path == "/repos/owner/repo/issues/21/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body["labels"] {
				if l == github.LabelPilot {
					pilotLabelAdded = true
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/issues/21/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			inProgressRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/21/labels/pilot-done" && r.Method == http.MethodDelete:
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
		t.Fatalf("Stage = %s, want %s (close-and-reexecute fallback)", pr.Stage, StageFailed)
	}
	if pr.RebaseAttempts != 0 {
		t.Fatalf("RebaseAttempts = %d, want 0 (mechanical resolution never ran to success)", pr.RebaseAttempts)
	}
	if !prClosed {
		t.Fatal("expected PR to be closed via close-and-reexecute fallback")
	}
	if closeCommentBody == "" {
		t.Fatal("expected close-and-reexecute comment to be posted")
	}
	if !pilotLabelAdded || !inProgressRemoved {
		t.Fatal("expected issue restored to dispatch-ready state (pilot label re-added, in-progress removed)")
	}
}
