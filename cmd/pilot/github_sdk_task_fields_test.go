package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestFetchGithubIssueForSDKTask_ReturnsStateAndUser is a regression test for GH-4050:
// the SDK-poller path (handleGithubIssueEventSDK) must fetch the real issue so State and
// author flow into the executor.Task the same way the legacy in-tree path
// (handleGitHubIssueWithResult) sets them. Before this fix, sdkcore.IssueEvent's lack of a
// State field meant task.State was always "" on the SDK path, silently bypassing the
// epic.go parent-done gate (GH-201 regression).
func TestFetchGithubIssueForSDKTask_ReturnsStateAndUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(githubSDK.Issue{
			Number: 4050,
			State:  "closed",
			User:   githubSDK.User{Login: "alice", Email: "alice@example.com"},
		})
	}))
	defer srv.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)

	issue := fetchGithubIssueForSDKTask(context.Background(), client, "o", "r", 4050, "GH-4050")
	if issue == nil {
		t.Fatal("fetchGithubIssueForSDKTask() = nil, want a populated issue")
	}
	if issue.State != "closed" {
		t.Errorf("issue.State = %q, want %q", issue.State, "closed")
	}
	if issue.User.Login != "alice" {
		t.Errorf("issue.User.Login = %q, want %q", issue.User.Login, "alice")
	}
}

// TestFetchGithubIssueForSDKTask_ErrorReturnsNil verifies the fetch is best-effort: a fetch
// failure (e.g. transient API error) must not crash task construction, mirroring the
// tolerance already used by the spec-guard path elsewhere in this file.
func TestFetchGithubIssueForSDKTask_ErrorReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)

	if issue := fetchGithubIssueForSDKTask(context.Background(), client, "o", "r", 4050, "GH-4050"); issue != nil {
		t.Errorf("fetchGithubIssueForSDKTask() = %+v, want nil on fetch error", issue)
	}
}

// TestResolveGitHubMemberIDByLogin_NilAdapterReturnsEmpty documents the fail-open behavior
// resolveGitHubMemberIDByLogin shares with resolveGitHubMemberID: with no team adapter
// configured (the default in this test binary), RBAC resolution returns "" rather than
// panicking or erroring, on both the legacy and SDK-poller paths (GH-4050).
func TestResolveGitHubMemberIDByLogin_NilAdapterReturnsEmpty(t *testing.T) {
	if teamAdapter != nil {
		t.Skip("teamAdapter unexpectedly configured in test binary")
	}
	if got := resolveGitHubMemberIDByLogin("alice", "alice@example.com"); got != "" {
		t.Errorf("resolveGitHubMemberIDByLogin() = %q, want \"\" with nil teamAdapter", got)
	}
}

// TestGithubHandlerSDK_FieldsWired is a source-level regression guard scoped to the
// handleGithubIssueEventSDK function body (GH-4050): the constructed executor.Task must
// carry Labels, State, MemberID, AcceptanceCriteria, and FromPR, mirroring the legacy
// in-tree handleGitHubIssueWithResult. This is the exact bug class that regressed
// unnoticed — the legacy path had no equivalent wiring test either.
func TestGithubHandlerSDK_FieldsWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	for _, want := range []string{
		"Labels:",
		"ev.Labels",
		"State:",
		"issueState",
		"MemberID:",
		"memberID",
		"AcceptanceCriteria:",
		"github.ExtractAcceptanceCriteria(ev.Body)",
		"FromPR:",
		"fromPR",
		"fetchGithubIssueForSDKTask(",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleGithubIssueEventSDK body must contain %q (GH-4050: task.Labels/State/MemberID/AcceptanceCriteria/FromPR must be populated)", want)
		}
	}
}

// TestGithubSDKTask_NoDecomposeLabelStaysSingleTask reproduces the GH-3994 forensic
// scenario (GH-4050): a human adds the no-decompose label ~1h before re-dispatch, but
// because handleGithubIssueEventSDK never set task.Labels, the no-decompose gate at
// decompose.go:135/206 silently no-oped and the retry still decomposed into subtasks.
// This test asserts that a Task built the way the fixed SDK-poller path builds it (Labels
// populated from the event) is skipped by TaskDecomposer regardless of its complexity or
// description length.
func TestGithubSDKTask_NoDecomposeLabelStaysSingleTask(t *testing.T) {
	longDescription := strings.Repeat("implement a complex multi-file refactor across several packages with new interfaces and extensive test coverage. ", 10)

	task := &executor.Task{
		ID:          "GH-3994",
		Title:       "Large refactor",
		Description: longDescription,
		Labels:      []string{"no-decompose"}, // GH-4050: now flows from ev.Labels
	}

	decomposer := executor.NewTaskDecomposer(&executor.DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	})

	result := decomposer.Decompose(task)
	if result.Decomposed {
		t.Fatalf("Decompose() = decomposed with subtasks=%d, want a single task (no-decompose label must gate this)", len(result.Subtasks))
	}
	if result.Reason != "skipped: no-decompose label" {
		t.Errorf("Decompose().Reason = %q, want %q", result.Reason, "skipped: no-decompose label")
	}
	if len(result.Subtasks) != 1 || result.Subtasks[0] != task {
		t.Errorf("Decompose().Subtasks = %v, want the single original task", result.Subtasks)
	}
}

// TestGithubSDKTask_EmptyLabelsWouldHaveDecomposed is the negative control for
// TestGithubSDKTask_NoDecomposeLabelStaysSingleTask: it documents that WITHOUT Labels
// populated (the pre-GH-4050 bug), the same task is not protected by the no-decompose gate
// and proceeds to complexity-based decomposition — this is exactly the fleet-wide failure
// mode GH-4050 fixed.
func TestGithubSDKTask_EmptyLabelsWouldHaveDecomposed(t *testing.T) {
	longDescription := strings.Repeat("implement a complex multi-file refactor across several packages with new interfaces and extensive test coverage. ", 10)

	task := &executor.Task{
		ID:          "GH-3994",
		Title:       "Large refactor",
		Description: longDescription,
		Labels:      nil, // simulates the pre-fix constructor, which never set Labels
	}

	decomposer := executor.NewTaskDecomposer(&executor.DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	})

	result := decomposer.Decompose(task)
	if result.Reason == "skipped: no-decompose label" {
		t.Error("Decompose() skipped via the no-decompose label with Labels=nil; the label gate must NOT fire on empty Labels")
	}
}
