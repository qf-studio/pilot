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

// gh3928HumanPR builds a human-authored (non pilot/*) merged PR fixture for the
// TestScanRecentlyMergedPRs_HumanMerges table below.
func gh3928HumanPR(mergedAt, base string) github.PullRequest {
	return github.PullRequest{
		Number:         900,
		Head:           github.PRRef{Ref: "feat/human-thing", SHA: "humansha"},
		Base:           github.PRRef{Ref: base},
		HTMLURL:        "https://github.com/owner/repo/pull/900",
		Title:          "feat: human thing",
		Merged:         true,
		MergedAt:       mergedAt,
		MergeCommitSHA: "humanmergesha",
	}
}

func gh3928Server(t *testing.T, prs []*github.PullRequest, tags []*github.Tag) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags"):
			w.WriteHeader(http.StatusOK)
			if tags == nil {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_ = json.NewEncoder(w).Encode(tags)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func gh3928Controller(t *testing.T, serverURL string, tagHuman bool) *Controller {
	t.Helper()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, serverURL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:        true,
		Trigger:        "on_merge",
		TagPrefix:      "v",
		TagHumanMerges: tagHuman,
	}
	cfg.MergedPRScanWindow = 30 * time.Minute
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(newTestStateStore(t))
	return c
}

// TestScanRecentlyMergedPRs_HumanMerges verifies GH-3928: release.tag_human_merges
// opts ScanRecentlyMergedPRs into considering merged PRs whose head branch is
// NOT pilot/* for release tagging, gated behind the opt-in flag and the
// default-branch requirement, without touching Pilot-only side effects.
func TestScanRecentlyMergedPRs_HumanMerges(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	staleMergedAt := time.Now().Add(-45 * time.Minute).UTC().Format(time.RFC3339)

	t.Run("flag off: human PR not registered", func(t *testing.T) {
		pr := gh3928HumanPR(recentMergedAt, "main")
		server := gh3928Server(t, []*github.PullRequest{&pr}, nil)

		c := gh3928Controller(t, server.URL, false)
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
		}
		c.mu.RLock()
		_, tracked := c.activePRs[900]
		c.mu.RUnlock()
		if tracked {
			t.Error("human PR registered with tag_human_merges=false; today's default behavior must be unchanged")
		}
	})

	t.Run("flag on: human PR merged to default branch in-window registered", func(t *testing.T) {
		pr := gh3928HumanPR(recentMergedAt, "main")
		server := gh3928Server(t, []*github.PullRequest{&pr}, nil)

		c := gh3928Controller(t, server.URL, true)
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
		}
		c.mu.RLock()
		prState, tracked := c.activePRs[900]
		c.mu.RUnlock()
		if !tracked {
			t.Fatal("human PR not registered with tag_human_merges=true")
		}
		if prState.Stage != StageReleasing {
			t.Errorf("stage = %v, want StageReleasing", prState.Stage)
		}
		if prState.IssueNumber != 0 {
			t.Errorf("IssueNumber = %d, want 0 (human branch has no pilot/GH-N)", prState.IssueNumber)
		}
		if prState.HeadSHA != pr.MergeCommitSHA {
			t.Errorf("HeadSHA = %q, want merge commit SHA %q", prState.HeadSHA, pr.MergeCommitSHA)
		}
	})

	t.Run("flag on: human PR to non-default base skipped", func(t *testing.T) {
		pr := gh3928HumanPR(recentMergedAt, "develop")
		server := gh3928Server(t, []*github.PullRequest{&pr}, nil)

		c := gh3928Controller(t, server.URL, true)
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
		}
		c.mu.RLock()
		_, tracked := c.activePRs[900]
		c.mu.RUnlock()
		if tracked {
			t.Error("human PR merged into a non-default base branch was registered; must be skipped")
		}
	})

	t.Run("flag on: already-tagged human PR skipped", func(t *testing.T) {
		pr := gh3928HumanPR(recentMergedAt, "main")
		existingTag := &github.Tag{Name: "v1.0.0", Commit: struct {
			SHA string `json:"sha"`
		}{SHA: pr.MergeCommitSHA}}
		server := gh3928Server(t, []*github.PullRequest{&pr}, []*github.Tag{existingTag})

		c := gh3928Controller(t, server.URL, true)
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
		}
		c.mu.RLock()
		_, tracked := c.activePRs[900]
		c.mu.RUnlock()
		if tracked {
			t.Error("already-tagged human PR was registered; must be skipped")
		}
	})

	t.Run("flag on: human PR merged outside scan window skipped", func(t *testing.T) {
		pr := gh3928HumanPR(staleMergedAt, "main")
		server := gh3928Server(t, []*github.PullRequest{&pr}, nil)

		c := gh3928Controller(t, server.URL, true)
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
		}
		c.mu.RLock()
		_, tracked := c.activePRs[900]
		c.mu.RUnlock()
		if tracked {
			t.Error("human PR merged outside the scan window was registered; must be skipped")
		}
	})

	t.Run("flag on: pilot PR side effects fire, human PR side effects do not", func(t *testing.T) {
		pilotPR := github.PullRequest{
			Number:         901,
			Head:           github.PRRef{Ref: "pilot/GH-500", SHA: "pilotsha"},
			Base:           github.PRRef{Ref: "main"},
			HTMLURL:        "https://github.com/owner/repo/pull/901",
			Title:          "feat: pilot thing",
			Merged:         true,
			MergedAt:       recentMergedAt,
			MergeCommitSHA: "pilotmergesha",
		}
		humanPR := gh3928HumanPR(recentMergedAt, "main")

		server := gh3928Server(t, []*github.PullRequest{&pilotPR, &humanPR}, nil)

		c := gh3928Controller(t, server.URL, true)
		evalMock := &mockEvalStore{}
		c.SetEvalStore(evalMock)

		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
		}

		c.mu.RLock()
		_, pilotTracked := c.activePRs[901]
		_, humanTracked := c.activePRs[900]
		c.mu.RUnlock()
		if !pilotTracked {
			t.Error("pilot PR not registered")
		}
		if !humanTracked {
			t.Error("human PR not registered")
		}

		if got := c.Metrics().Snapshot().PRsMerged; got != 1 {
			t.Errorf("PRsMerged = %d, want 1 (pilot PR only — human merges must not pollute merge metrics)", got)
		}
		if len(evalMock.selfHealed) != 1 {
			t.Errorf("selfHealed entries = %d, want 1 (pilot PR only — human merges must not trigger self-heal)", len(evalMock.selfHealed))
		}
	})
}

// TestHandleReleasing_HumanPR_NoIssueComment verifies GH-3928: a scanner-registered
// human PR carries IssueNumber==0, and handleReleasing's escalation-comment path
// (which only fires for IssueNumber > 0) must not attempt to comment on a
// nonexistent issue — on both the escalation and happy paths.
func TestHandleReleasing_HumanPR_NoIssueComment(t *testing.T) {
	var issueCommentPosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			issueCommentPosts++
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			// Diverged SHA: branch tip never matches the PR's head SHA, driving
			// handleReleasing down the escalation path (guardReleaseSHAReachable fails).
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "unrelated-main-tip-sha"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "diverged"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{
		PRNumber:    902,
		HeadSHA:     "humanmergesha",
		Stage:       StageReleasing,
		IssueNumber: 0, // GH-3928: human PRs never have an associated issue
	}
	c.mu.Lock()
	c.activePRs[902] = prState
	c.mu.Unlock()

	if err := c.handleReleasing(context.Background(), prState); err != nil {
		t.Fatalf("handleReleasing returned error: %v", err)
	}

	if issueCommentPosts != 0 {
		t.Errorf("POST .../issues/*/comments called %d times, want 0 for IssueNumber==0", issueCommentPosts)
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %v, want StageFailed (unreachable SHA escalates)", prState.Stage)
	}
}
