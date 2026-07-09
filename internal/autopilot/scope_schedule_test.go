package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
	"github.com/robfig/cron/v3"
)

// scheduleTickServer builds a fake GitHub server covering everything
// scheduleReleaseTick needs: releases/latest + tags for version resolution,
// compare for the commit train, and pulls/<n> for member-PR verification.
// prs maps candidate PR numbers to their merged state; a number absent from
// the map 404s (unverifiable, dropped from members).
func scheduleTickServer(t *testing.T, lastTag string, commits []*github.Commit, prs map[int]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: lastTag})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"commits": commits})
		case strings.Contains(r.URL.Path, "/pulls/"):
			var num int
			_, _ = fmtSscanIssueNum(strings.Replace(r.URL.Path, "/pulls/", "/issues/", 1), &num)
			merged, ok := prs[num]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.PullRequest{Number: num, Merged: merged})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// newScheduleController builds a controller with Trigger "on_schedule" wired
// to a test GitHub server and a fresh in-memory state store.
func newScheduleController(t *testing.T, serverURL, schedule string) (*Controller, *StateStore) {
	t.Helper()
	stateStore := newTestStateStore(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, serverURL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_schedule",
		TagPrefix: "v",
		Schedule:  schedule,
	}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	if c.releaser == nil {
		t.Fatal("releaser not initialized — check ReleaseConfig wiring")
	}
	c.SetStateStore(stateStore)
	return c, stateStore
}

// TestScheduleReleaseTick_EnqueuesTrainWithResolvedMembers covers the happy
// path: commits since the last tag include two squash-merge PR suffixes and
// one direct commit with no suffix; the enqueued row keys on
// "train:<RFC3339>" and lists only the two resolvable, verified-merged
// members (GH-3993).
func TestScheduleReleaseTick_EnqueuesTrainWithResolvedMembers(t *testing.T) {
	c1 := makeCommit("feat: add thing (#101)")
	c1.SHA = "sha1"
	c2 := makeCommit("fix: another thing (#102)")
	c2.SHA = "sha2"
	c3 := makeCommit("chore: direct commit, no PR")
	c3.SHA = "sha3"

	server := scheduleTickServer(t, "v1.0.0", []*github.Commit{c1, c2, c3}, map[int]bool{101: true, 102: true})
	defer server.Close()

	c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")

	scheduledAt := time.Date(2026, 7, 10, 21, 0, 0, 0, time.UTC)
	c.scheduleReleaseTick(context.Background(), scheduledAt)

	row, err := stateStore.GetScopeRelease("owner/repo", "train:2026-07-10T21:00:00Z")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row == nil {
		t.Fatal("expected a train row to be enqueued")
	}
	if want := []int{101, 102}; !reflect.DeepEqual(row.MemberPRs, want) {
		t.Errorf("members = %v, want %v", row.MemberPRs, want)
	}
	if want := "Release train 2026-07-10 21:00"; row.ScopeTitle != want {
		t.Errorf("title = %q, want %q", row.ScopeTitle, want)
	}
}

// TestScheduleReleaseTick_DropsUnverifiablePRNumber covers a commit whose
// "(#N)" suffix does not resolve to a merged PR (deleted/never-existed) —
// it is dropped from the member list, but a co-occurring resolvable member
// still enqueues the train (GH-3993).
func TestScheduleReleaseTick_DropsUnverifiablePRNumber(t *testing.T) {
	c1 := makeCommit("feat: add thing (#201)")
	c1.SHA = "sha1"
	c2 := makeCommit("fix: ghost pr (#999)")
	c2.SHA = "sha2"

	server := scheduleTickServer(t, "v1.0.0", []*github.Commit{c1, c2}, map[int]bool{201: true})
	defer server.Close()

	c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")

	scheduledAt := time.Date(2026, 7, 10, 21, 0, 0, 0, time.UTC)
	c.scheduleReleaseTick(context.Background(), scheduledAt)

	row, err := stateStore.GetScopeRelease("owner/repo", "train:2026-07-10T21:00:00Z")
	if err != nil || row == nil {
		t.Fatalf("expected a train row, err=%v row=%v", err, row)
	}
	if want := []int{201}; !reflect.DeepEqual(row.MemberPRs, want) {
		t.Errorf("members = %v, want %v", row.MemberPRs, want)
	}
}

// TestScheduleReleaseTick_EmptyCompare_NoRow covers "empty week": no commits
// since the last tag means no row and no noise (GH-3993 acceptance criteria).
func TestScheduleReleaseTick_EmptyCompare_NoRow(t *testing.T) {
	server := scheduleTickServer(t, "v1.0.0", nil, nil)
	defer server.Close()

	c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")

	scheduledAt := time.Date(2026, 7, 10, 21, 0, 0, 0, time.UTC)
	c.scheduleReleaseTick(context.Background(), scheduledAt)

	rows, err := stateStore.ListScopeReleases("owner/repo", "pending", "releasing", "done", "failed")
	if err != nil {
		t.Fatalf("ListScopeReleases failed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows for an empty compare, got %d", len(rows))
	}
}

// TestScheduleReleaseTick_DirectCommitOnlyTrain_NoRow covers the v1
// limitation: commits exist but none carry a resolvable PR suffix (an
// all-direct-commit train) — skipped with a WARN rather than enqueued,
// since the carrier requires a real merged PR to anchor on (GH-3993).
func TestScheduleReleaseTick_DirectCommitOnlyTrain_NoRow(t *testing.T) {
	c1 := makeCommit("chore: direct commit, no PR suffix")
	c1.SHA = "sha1"

	server := scheduleTickServer(t, "v1.0.0", []*github.Commit{c1}, nil)
	defer server.Close()

	c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")

	scheduledAt := time.Date(2026, 7, 10, 21, 0, 0, 0, time.UTC)
	c.scheduleReleaseTick(context.Background(), scheduledAt)

	rows, err := stateStore.ListScopeReleases("owner/repo", "pending", "releasing", "done", "failed")
	if err != nil {
		t.Fatalf("ListScopeReleases failed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows for a direct-commit-only train, got %d", len(rows))
	}
}

// TestAutoMerger_SquashTitleSuffix_ResolvesAsTrainMember is a regex
// round-trip: it builds a squash-merge commit title via AutoMerger.MergePR's
// actual production title-construction path, then feeds a commit carrying
// that title into resolveTrainMemberPRs and confirms it resolves as a member
// PR — verifying the two call sites (auto_merger.go's title builder and
// scope_schedule.go's trainPRSuffixRe) now agree on format (GH-4150).
func TestAutoMerger_SquashTitleSuffix_ResolvesAsTrainMember(t *testing.T) {
	var capturedTitle string
	mergeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/555/merge" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			capturedTitle = body["commit_title"]
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer mergeServer.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, mergeServer.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.MergeMethod = github.MergeMethodSquash

	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)
	if err := merger.MergePR(context.Background(), &PRState{
		PRNumber: 555,
		PRTitle:  "feat(train): resolve member PRs",
	}); err != nil {
		t.Fatalf("MergePR() error = %v", err)
	}
	if capturedTitle == "" {
		t.Fatal("commit_title was not captured by the fake merge server")
	}

	commit := makeCommit(capturedTitle)
	scheduleServer := scheduleTickServer(t, "v1.0.0", nil, map[int]bool{555: true})
	defer scheduleServer.Close()

	c, _ := newScheduleController(t, scheduleServer.URL, "0 21 * * FRI")

	members := c.resolveTrainMemberPRs(context.Background(), []*github.Commit{commit})
	if want := []int{555}; !reflect.DeepEqual(members, want) {
		t.Errorf("resolveTrainMemberPRs(%q) = %v, want %v", capturedTitle, members, want)
	}
}

// TestRecoverMissedTrainTick covers the three missed-tick recovery gates:
// a genuine miss within lookback fires the tick once; an already-enqueued
// slot is a no-op; a miss older than the lookback is left for manual
// re-trigger (GH-3993). None of these sleep on a real cron — they call
// recoverMissedTrainTick directly against a fixed schedule.
func TestRecoverMissedTrainTick(t *testing.T) {
	loc := time.UTC
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("0 21 * * FRI")
	if err != nil {
		t.Fatalf("parse schedule: %v", err)
	}

	t.Run("no prior row, commits exist, within lookback runs tick once", func(t *testing.T) {
		c1 := makeCommit("feat: add thing (#301)")
		c1.SHA = "sha1"
		server := scheduleTickServer(t, "v1.0.0", []*github.Commit{c1}, map[int]bool{301: true})
		defer server.Close()

		c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 7 * 24 * time.Hour

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		rows, err := stateStore.ListScopeReleases("owner/repo", "pending")
		if err != nil {
			t.Fatalf("ListScopeReleases: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected exactly 1 recovered row, got %d", len(rows))
		}
	})

	t.Run("row already exists for the missed slot is a no-op", func(t *testing.T) {
		var compareHits int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/compare/") {
				atomic.AddInt64(&compareHits, 1)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 7 * 24 * time.Hour

		prevScheduled := previousScheduledTime(schedule, time.Now().In(loc))
		if err := stateStore.EnqueueScopeRelease("owner/repo", trainScopeKey(prevScheduled), "Release train existing", []int{1}); err != nil {
			t.Fatalf("EnqueueScopeRelease: %v", err)
		}

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		if got := atomic.LoadInt64(&compareHits); got != 0 {
			t.Errorf("expected no compare-commits call when a row already exists for the slot, got %d", got)
		}
	})

	t.Run("miss older than lookback is a no-op", func(t *testing.T) {
		var compareHits int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/compare/") {
				atomic.AddInt64(&compareHits, 1)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		c, _ := newScheduleController(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 1 * time.Millisecond // any past scheduled slot is older than this

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		if got := atomic.LoadInt64(&compareHits); got != 0 {
			t.Errorf("expected no compare-commits call for a miss older than the lookback window, got %d", got)
		}
	})
}

// TestPreviousScheduledTime_HonorsLocation verifies the same cron expression
// resolves to different absolute instants depending on the location the
// reference time is evaluated in — the "Timezone: schedule evaluated in
// configured location" requirement (GH-3993).
func TestPreviousScheduledTime_HonorsLocation(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("0 21 * * FRI")
	if err != nil {
		t.Fatalf("parse schedule: %v", err)
	}

	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Europe/Berlin tzdata unavailable: %v", err)
	}

	ref := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC) // a Saturday, comfortably after either zone's Friday 21:00

	prevUTC := previousScheduledTime(schedule, ref.In(time.UTC))
	prevBerlin := previousScheduledTime(schedule, ref.In(berlin))

	if prevUTC.IsZero() || prevBerlin.IsZero() {
		t.Fatal("expected both evaluations to resolve to a non-zero previous scheduled time")
	}
	if prevUTC.Equal(prevBerlin) {
		t.Errorf("expected different absolute instants for UTC vs Europe/Berlin schedule evaluation, both = %v", prevUTC)
	}
	if h := prevBerlin.In(berlin).Hour(); h != 21 {
		t.Errorf("Berlin previous scheduled hour = %d, want 21", h)
	}
	if h := prevUTC.In(time.UTC).Hour(); h != 21 {
		t.Errorf("UTC previous scheduled hour = %d, want 21", h)
	}
}

// trainReleaseServer builds a fake GitHub server for driving handleReleasing
// on a "train:" scope carrier: branch/check-runs for post-merge CI plumbing,
// releases/tags for version resolution, a combined compare response serving
// both CompareStatus (reachability guard) and CompareCommits (train commit
// source), and /git/refs for tag creation.
func trainReleaseServer(t *testing.T, commits []*github.Commit, gitRefPosts *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "mainsha"}})
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/compare/"):
			// Serves both CompareStatus (reachability guard reads .status) and
			// CompareCommits (train commit source reads .commits) from one route.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "identical", "commits": commits})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			atomic.AddInt64(gitRefPosts, 1)
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/releases/tags/v1.1.0"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.1.0", HTMLURL: "https://github.com/owner/repo/releases/tag/v1.1.0"})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestHandleReleasing_TrainScope_UsesCompareCommitsNotMemberUnion covers the
// controller.go handleReleasing branch: a "train:" scope carrier's commit
// source is CompareCommits(lastTag -> HeadSHA), not a member-PR commit
// union, and cuts exactly one tag (GH-3993).
func TestHandleReleasing_TrainScope_UsesCompareCommitsNotMemberUnion(t *testing.T) {
	commit := makeCommit("feat: add a thing (#55)")
	commit.SHA = "train-sha-1"

	var gitRefPosts int64
	server := trainReleaseServer(t, []*github.Commit{commit}, &gitRefPosts)
	defer server.Close()

	stateStore := newTestStateStore(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{
		Enabled:       true,
		Trigger:       "on_schedule",
		TagPrefix:     "v",
		RequireCI:     true,
		Schedule:      "0 21 * * FRI",
		VerifyRelease: boolPtr(false),
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	prState := &PRState{
		PRNumber:       999,
		PRURL:          "https://github.com/owner/repo/pull/999",
		ScopeKey:       "train:2026-07-10T21:00:00Z",
		ScopeTitle:     "Release train 2026-07-10 21:00",
		ScopeMemberPRs: []int{55},
		PostMergeSHA:   "train-sha-1",
		Stage:          StageReleasing,
	}

	if err := c.handleReleasing(context.Background(), prState); err != nil {
		t.Fatalf("handleReleasing failed: %v", err)
	}

	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git ref posts = %d, want 1", got)
	}
	if prState.Stage == StageFailed {
		t.Errorf("prState unexpectedly failed: %s", prState.Error)
	}
	if prState.HeadSHA != "train-sha-1" {
		t.Errorf("HeadSHA = %q, want the PostMergeSHA (train-sha-1)", prState.HeadSHA)
	}
}
