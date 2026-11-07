package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

const rearmTestSHA = "b57d141c0ffee0123456789abcdef0123456789a"

func newRearmTestController(t *testing.T, srv *ownerDeathGHServer) (*Controller, *fakeBorrowedBranchLookup) {
	t.Helper()
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ts.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	c.SetAlertsEngine(&fakeAlertSink{})
	reg := &fakeBorrowedBranchLookup{}
	c.SetBorrowedBranchLookup(reg)
	return c, reg
}

func failedSource(num int) *github.Issue {
	return &github.Issue{
		Number: num,
		State:  github.StateOpen,
		Labels: []github.Label{{Name: github.LabelPilot}, {Name: github.LabelInProgress}, {Name: github.LabelFailed}},
	}
}

func TestRearmDeadOwnerSource_CarriesBranchFooter(t *testing.T) {
	srv := newOwnerDeathGHServer()
	source := failedSource(100)
	srv.issues[100] = source
	c, reg := newRearmTestController(t, srv)

	carry := &rearmCarry{branch: "pilot/GH-150", pr: 6165, sha: rearmTestSHA, iteration: 1}
	c.rearmDeadOwnerSource(context.Background(), source, "its designated fix issue #200 died (declined)", carry)

	if len(srv.comments) != 1 {
		t.Fatalf("expected 1 comment, got %v", srv.comments)
	}
	got := srv.comments[0]
	for _, want := range []string{
		"Retry continues branch `pilot/GH-150` at `b57d141`; the closed PR #6165 holds the review history.",
		"<!-- autopilot-meta branch:pilot/GH-150 pr:6165 iteration:2 source:100 sha:" + rearmTestSHA + " -->",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("re-arm comment missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "-->") {
		t.Errorf("footer must be the last line of the comment:\n%s", got)
	}
	if len(reg.recorded) != 1 || reg.recorded[0] != (recordedBorrowedBranch{taskID: "GH-100", branch: "pilot/GH-150", fromPR: 6165}) {
		t.Errorf("registry entry for the source = %+v", reg.recorded)
	}
}

func TestRearmDeadOwnerSource_NoCarryKeepsReasonOnlyComment(t *testing.T) {
	srv := newOwnerDeathGHServer()
	source := failedSource(100)
	srv.issues[100] = source
	c, reg := newRearmTestController(t, srv)

	c.rearmDeadOwnerSource(context.Background(), source, "its designated fix issue #200 died (declined)", nil)

	if len(srv.comments) != 1 || strings.Contains(srv.comments[0], "autopilot-meta") {
		t.Errorf("no-carry re-arm must not render a footer, got %v", srv.comments)
	}
	if len(reg.recorded) != 0 {
		t.Errorf("no-carry re-arm must not touch the registry, got %+v", reg.recorded)
	}
}

func TestParseRearmCarry(t *testing.T) {
	tests := []struct {
		name string
		body string
		want rearmCarry
		ok   bool
	}{
		{"full footer", "x\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:3 source:100 sha:" + rearmTestSHA + " -->",
			rearmCarry{branch: "pilot/GH-100", pr: 7, sha: rearmTestSHA, iteration: 3}, true},
		{"no sha", "<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 -->",
			rearmCarry{branch: "pilot/GH-100", pr: 7, iteration: 1}, true},
		{"no footer", "plain body", rearmCarry{}, false},
		{"non-pilot branch", "<!-- autopilot-meta branch:main pr:7 -->", rearmCarry{}, false},
		{"traversal branch", "<!-- autopilot-meta branch:pilot/../x pr:7 -->", rearmCarry{}, false},
		{"bad sha dropped", "<!-- autopilot-meta branch:pilot/GH-1 pr:7 sha:zzzzzzz -->",
			rearmCarry{branch: "pilot/GH-1", pr: 7}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRearmCarry(tt.body)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseRearmCarry = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestReactToDeadFixIssue_TransfersBranchToSource(t *testing.T) {
	srv := newOwnerDeathGHServer()
	srv.issues[100] = failedSource(100)
	c, reg := newRearmTestController(t, srv)

	dead := &github.Issue{Number: 200, State: github.StateClosed,
		Body: "Fixes CI.\n\n<!-- autopilot-meta branch:pilot/GH-150 pr:6165 iteration:1 source:100 sha:" + rearmTestSHA + " -->"}
	c.reactToDeadFixIssue(context.Background(), dead, "declined at preflight: stale assertions")

	if len(reg.forgotten) != 1 || reg.forgotten[0] != "GH-200" {
		t.Errorf("dead fix issue entry must still be forgotten, got %v", reg.forgotten)
	}
	if len(reg.recorded) != 1 || reg.recorded[0].taskID != "GH-100" || reg.recorded[0].branch != "pilot/GH-150" {
		t.Fatalf("source registry entry missing, got %+v", reg.recorded)
	}
	if len(srv.comments) != 1 || !strings.Contains(srv.comments[0], "iteration:2 source:100 sha:"+rearmTestSHA) {
		t.Errorf("re-arm comment footer missing, got %v", srv.comments)
	}
}

// TestReactToDeadFixIssue_ReplayWorktreeHeadIsDeadBranchSha replays the
// 2026-11-06 incident shape: a declined fix issue whose branch holds 28 files
// of work re-arms its source, and the retry's worktree — built from the re-arm
// comment alone, the way the dispatcher reads it (handlers.go
// resolveRearmBranch), never from the in-memory registry — lands on the dead
// issue's commit.
//
// mutation pin: replay reads branch and sha from the re-arm comment, not the fake registry -> this test fails if the comment footer is not rendered
func TestReactToDeadFixIssue_ReplayWorktreeHeadIsDeadBranchSha(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	remote, local := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(remote, "init", "--bare")
	git(local, "init", "-b", "main")
	git(local, "config", "user.email", "test@test.com")
	git(local, "config", "user.name", "Test User")
	git(local, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(local, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	git(local, "add", ".")
	git(local, "commit", "-m", "initial")
	git(local, "push", "-u", "origin", "main")
	git(local, "checkout", "-b", "pilot/GH-100")
	for i := 0; i < 28; i++ {
		if err := os.WriteFile(filepath.Join(local, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(local, "add", ".")
	git(local, "commit", "-m", "work")
	sha := git(local, "rev-parse", "HEAD")
	git(local, "push", "-u", "origin", "pilot/GH-100")
	git(local, "checkout", "main")
	git(local, "branch", "-D", "pilot/GH-100")

	srv := newOwnerDeathGHServer()
	srv.issues[100] = failedSource(100)
	c, reg := newRearmTestController(t, srv)
	dead := &github.Issue{Number: 200, State: github.StateClosed,
		Body: "Fixes CI.\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:6165 iteration:1 source:100 sha:" + sha + " -->"}
	c.reactToDeadFixIssue(ctx, dead, "declined at preflight")

	// The retry: branch and sha both from the comment footer. The fix issue's
	// branch is the source's own, so the dispatcher gets no registry entry.
	if len(reg.recorded) != 0 {
		t.Errorf("self-branch carry must not be registered, got %+v", reg.recorded)
	}
	if len(srv.comments) != 1 {
		t.Fatalf("expected the re-arm comment, got %v", srv.comments)
	}
	bm := regexp.MustCompile(`branch:(\S+) `).FindStringSubmatch(srv.comments[0])
	m := regexp.MustCompile(`sha:([0-9a-f]+) -->`).FindStringSubmatch(srv.comments[0])
	if len(bm) < 2 || len(m) < 2 {
		t.Fatalf("no branch/sha in re-arm comment: %s", srv.comments[0])
	}
	branch := bm[1]
	base := executor.NewGitOperations(local).ResolveFixContinuationBaseRef(ctx, branch, m[1])
	path, cleanup, err := executor.CreateWorktreeWithBranch(ctx, local, "gh6183-replay", branch, base)
	if err != nil {
		t.Fatalf("CreateWorktreeWithBranch: %v", err)
	}
	defer cleanup()

	if head := git(path, "rev-parse", "HEAD"); head != sha {
		t.Errorf("retry worktree HEAD = %s, want dead issue's commit %s", head, sha)
	}
	if n := len(strings.Fields(git(path, "diff", "--name-only", "main...HEAD"))); n != 28 {
		t.Errorf("changed files vs main = %d, want 28", n)
	}
}

// mutation pin: parseRearmCarry takes a quoted footer anywhere in the body -> this test fails
func TestParseRearmCarry_OnlyTrustsLastLineFooter(t *testing.T) {
	real := "<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 source:100 -->"
	tests := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{"quoted in CI log before the real footer", "log: <!-- autopilot-meta branch:pilot/GH-666 pr:1 -->\n\n" + real, "pilot/GH-100", true},
		{"quoted mid-body, no real footer", "log:\n<!-- autopilot-meta branch:pilot/GH-666 pr:1 -->\n\nmore text", "", false},
		{"inside a fenced block", "```\n<!-- autopilot-meta branch:pilot/GH-666 pr:1 -->\n```\nend", "", false},
		{"inside an inline code span", "see `<!-- autopilot-meta branch:pilot/GH-666 pr:1 -->`", "", false},
		{"pr: from a quoted block is not borrowed", "<!-- autopilot-meta pr:99 -->\n\n<!-- autopilot-meta branch:pilot/GH-100 iteration:1 -->", "pilot/GH-100", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRearmCarry(tt.body)
			if ok != tt.ok || got.branch != tt.want {
				t.Fatalf("parseRearmCarry = %+v, %v; want branch %q ok %v", got, ok, tt.want, tt.ok)
			}
			if tt.name == "pr: from a quoted block is not borrowed" && got.pr != 0 {
				t.Errorf("pr = %d, want 0", got.pr)
			}
		})
	}
}

// mutation pin: the registry entry is recorded for a self-branch carry (GH-N -> pilot/GH-N) -> this test fails
func TestRearmDeadOwnerSource_SelfBranchCarryIsNotRegistered(t *testing.T) {
	srv := newOwnerDeathGHServer()
	source := failedSource(100)
	srv.issues[100] = source
	c, reg := newRearmTestController(t, srv)

	c.rearmDeadOwnerSource(context.Background(), source, "its designated fix issue #200 died", &rearmCarry{branch: "pilot/GH-100", pr: 6165, iteration: 1})

	if len(reg.recorded) != 0 {
		t.Errorf("self-entry must not be recorded (the dispatcher skips it), got %+v", reg.recorded)
	}
	if len(srv.comments) != 1 || !strings.Contains(srv.comments[0], "branch:pilot/GH-100 pr:6165") {
		t.Errorf("the footer must still be rendered, got %v", srv.comments)
	}
}

// mutation pin: the external-close scan re-arm carries no branch (sibling path of reactToDeadFixIssue) -> this test fails
func TestNotifyExternalClose_DeadOwnerRearmCarriesBranch(t *testing.T) {
	const (
		prNumber    = 5301
		issueNumber = 5302
		fixIssueNum = 5303
	)
	deadBody := "Fixes CI.\n\n<!-- autopilot-meta branch:pilot/GH-5350 pr:5301 iteration:1 source:5302 sha:" + rearmTestSHA + " -->"

	var mu sync.Mutex
	var comments []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5302" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(github.Issue{Number: issueNumber, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/5303" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(github.Issue{Number: fixIssueNum, State: github.StateClosed, Body: deadBody})
		case r.URL.Path == "/repos/owner/repo/issues/5302/comments" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			comments = append(comments, body["body"])
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/repos/owner/repo/issues/5302/labels" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	store := newTestStateStore(t)
	key := spawnedFixDedupKey(prNumber, FailureCIPreMerge, []string{"lint"})
	if _, err := store.ClaimSpawnedFix("owner/repo", key); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSpawnedFixIssue("owner/repo", key, fixIssueNum); err != nil {
		t.Fatal(err)
	}
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
	c.SetStateStore(store)
	c.SetAlertsEngine(&fakeAlertSink{})
	reg := &fakeBorrowedBranchLookup{}
	c.SetBorrowedBranchLookup(reg)

	c.notifyExternalClose(context.Background(), &PRState{PRNumber: prNumber, IssueNumber: issueNumber})

	mu.Lock()
	defer mu.Unlock()
	if len(comments) != 1 {
		t.Fatalf("expected one comment, got %v", comments)
	}
	got := comments[0]
	if !strings.Contains(got, RearmCommentMarker) {
		t.Errorf("comment must carry the re-arm marker the dispatcher trusts:\n%s", got)
	}
	want := "<!-- autopilot-meta branch:pilot/GH-5350 pr:5301 iteration:2 source:5302 sha:" + rearmTestSHA + " -->"
	if !strings.HasSuffix(strings.TrimSpace(got), want) {
		t.Errorf("footer must be the comment's last line, want suffix %q in:\n%s", want, got)
	}
	if len(reg.recorded) != 1 || reg.recorded[0].taskID != "GH-5302" || reg.recorded[0].branch != "pilot/GH-5350" {
		t.Errorf("registry entry = %+v", reg.recorded)
	}
}
