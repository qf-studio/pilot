package autopilot

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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

	carry := &rearmCarry{branch: "pilot/GH-100", pr: 6165, sha: rearmTestSHA, iteration: 1}
	c.rearmDeadOwnerSource(context.Background(), source, "its designated fix issue #200 died (declined)", carry)

	if len(srv.comments) != 1 {
		t.Fatalf("expected 1 comment, got %v", srv.comments)
	}
	got := srv.comments[0]
	for _, want := range []string{
		"Retry continues branch `pilot/GH-100` at `b57d141`; the closed PR #6165 holds the review history.",
		"<!-- autopilot-meta branch:pilot/GH-100 pr:6165 iteration:2 source:100 sha:" + rearmTestSHA + " -->",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("re-arm comment missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "-->") {
		t.Errorf("footer must be the last line of the comment:\n%s", got)
	}
	if len(reg.recorded) != 1 || reg.recorded[0] != (recordedBorrowedBranch{taskID: "GH-100", branch: "pilot/GH-100", fromPR: 6165}) {
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
		Body: "Fixes CI.\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:6165 iteration:1 source:100 sha:" + rearmTestSHA + " -->"}
	c.reactToDeadFixIssue(context.Background(), dead, "declined at preflight: stale assertions")

	if len(reg.forgotten) != 1 || reg.forgotten[0] != "GH-200" {
		t.Errorf("dead fix issue entry must still be forgotten, got %v", reg.forgotten)
	}
	if len(reg.recorded) != 1 || reg.recorded[0].taskID != "GH-100" || reg.recorded[0].branch != "pilot/GH-100" {
		t.Fatalf("source registry entry missing, got %+v", reg.recorded)
	}
	if len(srv.comments) != 1 || !strings.Contains(srv.comments[0], "iteration:2 source:100 sha:"+rearmTestSHA) {
		t.Errorf("re-arm comment footer missing, got %v", srv.comments)
	}
}

// TestReactToDeadFixIssue_ReplayWorktreeHeadIsDeadBranchSha replays the
// 2026-11-06 incident shape: a declined fix issue whose branch holds 28 files
// of work re-arms its source, and the retry's worktree — built from the
// registry entry and the comment footer alone — lands on the dead issue's
// commit. Skipping the registry transfer leaves nothing to build from.
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

	// The retry: branch from the registry, sha from the comment footer.
	if len(reg.recorded) != 1 {
		t.Fatalf("no registry entry for the source after the re-arm: %+v", reg.recorded)
	}
	branch := reg.recorded[0].branch
	m := regexp.MustCompile(`sha:([0-9a-f]+) -->`).FindStringSubmatch(srv.comments[0])
	if len(m) < 2 {
		t.Fatalf("no sha in re-arm comment: %s", srv.comments[0])
	}
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
