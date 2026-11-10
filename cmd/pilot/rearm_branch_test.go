package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

const rearmSHA = "b57d141c0ffee0123456789abcdef0123456789a"

// rearmComment renders the comment the way the producer does: the heading from
// autopilot.RearmCommentMarker and the footer from the producer's own renderer,
// so a drift in either breaks the consumer tests (GH-6191).
func rearmComment(source int, branch string) string {
	return "\U0001F501 **" + autopilot.RearmCommentMarker + "**: its designated fix issue #200 died. Re-armed." +
		autopilot.RenderRearmCommentFooter(branch, 6165, rearmSHA, 1, source)
}

func TestParseRearmFooterFromComments(t *testing.T) {
	tests := []struct {
		name   string
		bodies []string
		ok     bool
		branch string
	}{
		{"latest re-arm comment wins", []string{rearmComment(100, "pilot/GH-1"), "chatter", rearmComment(100, "pilot/GH-2")}, true, "pilot/GH-2"},
		{"other source ignored", []string{rearmComment(101, "pilot/GH-1")}, false, ""},
		{"human quote without marker ignored", []string{"<!-- autopilot-meta branch:pilot/GH-9 pr:1 source:100 -->"}, false, ""},
		{"non-pilot branch ignored", []string{rearmComment(100, "main")}, false, ""},
		{"no comments", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch, pr, sha, ok := parseRearmFooterFromComments(tt.bodies, 100)
			if ok != tt.ok || branch != tt.branch {
				t.Fatalf("got (%q, %d, %q, %v), want branch %q ok %v", branch, pr, sha, ok, tt.branch, tt.ok)
			}
			if ok && (pr != 6165 || sha != rearmSHA) {
				t.Errorf("pr/sha = %d/%q, want 6165/%s", pr, sha, rearmSHA)
			}
		})
	}
}

func TestHasRetryFamilyLabel(t *testing.T) {
	if !hasRetryFamilyLabel([]string{"pilot", "pilot-retry-1"}) || hasRetryFamilyLabel([]string{"pilot", "pilot-failed"}) {
		t.Error("hasRetryFamilyLabel misclassified labels")
	}
}

type fakeCommentLister struct {
	bodies []string
	err    error
}

func (f fakeCommentLister) ListIssueComments(context.Context, string, string, int) ([]*githubSDK.Comment, error) {
	var out []*githubSDK.Comment
	for _, b := range f.bodies {
		out = append(out, &githubSDK.Comment{Body: b})
	}
	return out, f.err
}

// rearmRemoteFixture returns a local clone whose origin carries main and
// pilot/GH-100.
func rearmRemoteFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	remote, local := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(remote, "init", "--bare")
	git(local, "init", "-b", "main")
	git(local, "config", "user.email", "test@test.com")
	git(local, "config", "user.name", "Test User")
	git(local, "remote", "add", "origin", remote)
	if err := os.WriteFile(local+"/a.txt", []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	git(local, "add", ".")
	git(local, "commit", "-m", "initial")
	git(local, "push", "-u", "origin", "main")
	git(local, "push", "origin", "main:refs/heads/pilot/GH-100")
	return local
}

func TestResolveRearmBranch(t *testing.T) {
	local := rearmRemoteFixture(t)
	ctx := context.Background()
	t.Run("branch on remote is reused", func(t *testing.T) {
		b, pr, sha, ok := resolveRearmBranch(ctx, fakeCommentLister{bodies: []string{rearmComment(100, "pilot/GH-100")}}, "o", "r", 100, local, "GH-100")
		if !ok || b != "pilot/GH-100" || pr != 6165 || sha != rearmSHA {
			t.Errorf("got (%q, %d, %q, %v)", b, pr, sha, ok)
		}
	})
	t.Run("branch gone falls back", func(t *testing.T) {
		if _, _, _, ok := resolveRearmBranch(ctx, fakeCommentLister{bodies: []string{rearmComment(100, "pilot/GH-404")}}, "o", "r", 100, local, "GH-100"); ok {
			t.Error("expected fallback for a branch missing on the remote")
		}
	})
	t.Run("comment list error falls back", func(t *testing.T) {
		if _, _, _, ok := resolveRearmBranch(ctx, fakeCommentLister{err: errors.New("boom")}, "o", "r", 100, local, "GH-100"); ok {
			t.Error("expected fallback on a comment-list error")
		}
	})
}

// captureLogs routes the daemon logger to a temp file for the test and returns
// a reader for what was logged.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := logging.Init(&logging.Config{Level: "debug", Format: "text", Output: path}); err != nil {
		t.Fatalf("logging.Init: %v", err)
	}
	t.Cleanup(func() { _ = logging.Init(logging.DefaultConfig()) })
	return func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return string(b)
	}
}

// mutation pin: the producer's rendered comment is not parsed back by the consumer (format drift) -> this test fails
func TestRearmCommentRoundTripsProducerFormat(t *testing.T) {
	body := rearmComment(100, "pilot/GH-6168")
	if !strings.HasSuffix(strings.TrimSpace(body), "-->") {
		t.Fatalf("producer footer must be the last line:\n%s", body)
	}
	branch, pr, sha, ok := parseRearmFooterFromComments([]string{body}, 100)
	if !ok || branch != "pilot/GH-6168" || pr != 6165 || sha != rearmSHA {
		t.Fatalf("consumer read (%q, %d, %q, %v) from the producer's comment:\n%s", branch, pr, sha, ok, body)
	}
	if !strings.Contains(body, rearmCommentMarker) {
		t.Errorf("producer comment lacks the marker the consumer requires:\n%s", body)
	}
}

// mutation pin: the newest re-arm comment is honoured on every later retry, not only the one that follows it -> this test fails
func TestParseRearmFooterFromComments_StaleAfterLaterExecution(t *testing.T) {
	rearm := rearmComment(100, "pilot/GH-2")
	tests := []struct {
		name   string
		bodies []string
		ok     bool
	}{
		{"retry directly after the re-arm", []string{"chatter", rearm}, true},
		{"task already started after the re-arm", []string{rearm, "\U0001F916 **Pilot started working on this issue**\n\nTask ID: `GH-100`"}, false},
		{"another path re-queued the source afterwards", []string{rearm, "PR #7 was closed without merging: reviewer closed it\n\nThe issue has been marked pilot-retry-ready and will be retried automatically."}, false},
		{"a newer re-arm comment is honoured again", []string{rearm, "\U0001F916 **Pilot started working on this issue**", rearmComment(100, "pilot/GH-3")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, ok := parseRearmFooterFromComments(tt.bodies, 100); ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
		})
	}
}

// mutation pin: a re-arm footer without pr: silently falls back to main -> this test fails
func TestResolveRearmBranch_NoPRLogsFallback(t *testing.T) {
	local := rearmRemoteFixture(t)
	read := captureLogs(t)
	noPR := "\U0001F501 **" + autopilot.RearmCommentMarker + "**: died." + autopilot.RenderRearmCommentFooter("pilot/GH-100", 0, "", 1, 100)

	if _, _, _, ok := resolveRearmBranch(context.Background(), fakeCommentLister{bodies: []string{noPR}}, "o", "r", 100, local, "GH-100"); ok {
		t.Fatal("a footer without pr: must keep the from-main path")
	}
	if got := read(); !strings.Contains(got, "re-arm footer carries no PR number, starting from main") {
		t.Errorf("fallback not logged, got %q", got)
	}
}

// mutation pin: the handler wiring (retry-family gate and branch/PR/sha assignment) is not driven by any test -> this test fails
func TestContinueRearmBranch(t *testing.T) {
	local := rearmRemoteFixture(t)
	ctx := context.Background()
	lister := fakeCommentLister{bodies: []string{rearmComment(100, "pilot/GH-100")}}
	const own = "pilot/GH-100x"

	tests := []struct {
		name       string
		labels     []string
		fromPR     int
		owner      string
		wantBranch string
		wantPR     int
		wantSHA    string
	}{
		{"retry-family label continues the re-arm branch", []string{"pilot", "pilot-retry-1"}, 0, "o", "pilot/GH-100", 6165, rearmSHA},
		{"retry-ready label continues the re-arm branch", []string{"pilot-retry-ready"}, 0, "o", "pilot/GH-100", 6165, rearmSHA},
		{"no retry-family label keeps the task branch", []string{"pilot", "pilot-in-progress"}, 0, "o", own, 0, ""},
		{"a body footer that already resolved a branch wins", []string{"pilot-retry-ready"}, 55, "o", own, 55, ""},
		{"unknown repo owner keeps the task branch", []string{"pilot-retry-ready"}, 0, "", own, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, pr, sha := continueRearmBranch(ctx, lister, tt.owner, "r", 100, local, "GH-100", tt.labels, own, tt.fromPR, "")
			if b != tt.wantBranch || pr != tt.wantPR || sha != tt.wantSHA {
				t.Errorf("got (%q, %d, %q), want (%q, %d, %q)", b, pr, sha, tt.wantBranch, tt.wantPR, tt.wantSHA)
			}
		})
	}

	t.Run("handler routes through continueRearmBranch after the body footer", func(t *testing.T) {
		body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")
		footerIdx := strings.Index(body, "resolveAutopilotFixBranch(ev.Labels, ev.Body)")
		rearmIdx := strings.Index(body, "continueRearmBranch(ctx, specClient, repoOwner, repoName, issueNum, projectPath, taskID, liveLabels, branchName, fromPR, fixFromSHA)")
		taskIdx := strings.Index(body, "task := &executor.Task{")
		if footerIdx < 0 || rearmIdx < footerIdx || taskIdx < rearmIdx {
			t.Error("handleGithubIssueEventSDK must call continueRearmBranch between the body-footer resolution and the Task construction")
		}
	})
}

// holdComment renders a pre-PR hold comment the way the producer does: free text
// followed by executor.PrePRHoldMetaFooter, so a drift in either side breaks the
// consumer tests (GH-6233).
func holdComment(source int, branch, sha string) string {
	return "Pilot held this task before opening a PR.\n\n### Where the work is\n\nBranch `" + branch + "` is pushed." +
		executor.PrePRHoldMetaFooter(branch, sha, strconv.Itoa(source))
}

func TestContinueHeldBranch(t *testing.T) {
	local := rearmRemoteFixture(t)
	ctx := context.Background()
	const own = "pilot/GH-100"
	started := "Pilot started working on this issue"

	tests := []struct {
		name       string
		bodies     []string
		err        error
		branch     string
		fromPR     int
		owner      string
		wantBranch string
		wantSHA    string
	}{
		{name: "footer present and branch on remote matches", bodies: []string{holdComment(100, own, rearmSHA)}, branch: own, owner: "o", wantBranch: own, wantSHA: rearmSHA},
		{name: "the latest hold comment wins", bodies: []string{holdComment(100, own, "1111111aaaa"), "noise", holdComment(100, own, rearmSHA)}, branch: own, owner: "o", wantBranch: own, wantSHA: rearmSHA},
		{name: "superseded by a later task start", bodies: []string{holdComment(100, own, rearmSHA), started}, branch: own, owner: "o", wantBranch: own},
		{name: "superseded by a later plain retry", bodies: []string{holdComment(100, own, rearmSHA), "PR #7 was closed without merging"}, branch: own, owner: "o", wantBranch: own},
		{name: "a task start before the hold does not cancel it", bodies: []string{started, holdComment(100, own, rearmSHA)}, branch: own, owner: "o", wantBranch: own, wantSHA: rearmSHA},
		{name: "no hold comment", bodies: []string{"just a comment"}, branch: own, owner: "o", wantBranch: own},
		{name: "footer for another issue", bodies: []string{holdComment(7, own, rearmSHA)}, branch: own, owner: "o", wantBranch: own},
		{name: "footer branch is not the issue's own", bodies: []string{holdComment(100, "pilot/GH-55", rearmSHA)}, branch: own, owner: "o", wantBranch: own},
		{name: "implausible sha", bodies: []string{holdComment(100, own, "zz")}, branch: own, owner: "o", wantBranch: own},
		{name: "footer quoted in a code span is not a footer", bodies: []string{"see `<!-- pilot-hold-meta branch:" + own + " sha:" + rearmSHA + " source:100 -->`"}, branch: own, owner: "o", wantBranch: own},
		{name: "footer not on the last line", bodies: []string{holdComment(100, own, rearmSHA) + "\ntrailing text"}, branch: own, owner: "o", wantBranch: own},
		{name: "a body footer branch already resolved", bodies: []string{holdComment(100, own, rearmSHA)}, branch: "pilot/GH-55", fromPR: 55, owner: "o", wantBranch: "pilot/GH-55"},
		{name: "a re-arm branch already resolved", bodies: []string{holdComment(100, own, rearmSHA)}, branch: "pilot/GH-77", owner: "o", wantBranch: "pilot/GH-77"},
		{name: "unknown repo owner", bodies: []string{holdComment(100, own, rearmSHA)}, branch: own, owner: "", wantBranch: own},
		{name: "comment list error", err: errors.New("boom"), branch: own, owner: "o", wantBranch: own},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sha := continueHeldBranch(ctx, fakeCommentLister{bodies: tt.bodies, err: tt.err}, tt.owner, "r", 100, local, "GH-100", tt.branch, tt.fromPR)
			if b != tt.wantBranch || sha != tt.wantSHA {
				t.Errorf("got (%q, %q), want (%q, %q)", b, sha, tt.wantBranch, tt.wantSHA)
			}
		})
	}

	// The fixture's origin carries pilot/GH-100 only, so issue 101's held branch is gone.
	t.Run("branch gone from the remote falls back and logs the reason", func(t *testing.T) {
		read := captureLogs(t)
		b, sha := continueHeldBranch(ctx, fakeCommentLister{bodies: []string{holdComment(101, "pilot/GH-101", rearmSHA)}}, "o", "r", 101, local, "GH-101", "pilot/GH-101", 0)
		if b != "pilot/GH-101" || sha != "" {
			t.Errorf("got (%q, %q), want the unchanged branch and no sha", b, sha)
		}
		if got := read(); !strings.Contains(got, "held branch gone from origin") {
			t.Errorf("reason not logged, got %q", got)
		}
	})

	t.Run("no hold comment logs one Info line naming the reason", func(t *testing.T) {
		read := captureLogs(t)
		b, sha := continueHeldBranch(ctx, fakeCommentLister{bodies: []string{"just a comment"}}, "o", "r", 100, local, "GH-100", own, 0)
		if b != own || sha != "" {
			t.Errorf("got (%q, %q), want the unchanged branch and no sha", b, sha)
		}
		got := read()
		if !strings.Contains(got, "no hold comment on the issue") || strings.Count(got, "pre-PR hold continuation") != 1 {
			t.Errorf("want exactly one reason line, got %q", got)
		}
	})

	t.Run("handler routes through continueHeldBranch after continueRearmBranch and sets no FromPR", func(t *testing.T) {
		body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")
		rearmIdx := strings.Index(body, "continueRearmBranch(ctx, specClient")
		heldIdx := strings.Index(body, "continueHeldBranch(ctx, specClient, repoOwner, repoName, issueNum, projectPath, taskID, branchName, fromPR)")
		taskIdx := strings.Index(body, "task := &executor.Task{")
		if rearmIdx < 0 || heldIdx < rearmIdx || taskIdx < heldIdx {
			t.Error("handleGithubIssueEventSDK must call continueHeldBranch between continueRearmBranch and the Task construction")
		}
		if !strings.Contains(body, "ContinueBranchSHA:  continueBranchSHA") {
			t.Error("handleGithubIssueEventSDK must hand continueBranchSHA to the Task")
		}
	})
}

// TestHoldContinuation_SupersededCommentWritesNoSHA pins GH-6281 item 4: when the
// poller logs that the hold comment is superseded or invalid, the SHA it hands the
// Task (and so the execution row) is empty, whatever the comment carried.
func TestHoldContinuation_SupersededCommentWritesNoSHA(t *testing.T) {
	local := rearmRemoteFixture(t)
	const own = "pilot/GH-100"
	tests := []struct {
		name   string
		bodies []string
	}{
		{"superseded by a later task start", []string{holdComment(100, own, rearmSHA), "Pilot started working on this issue"}},
		{"invalid sha", []string{holdComment(100, own, "zz")}},
		{"footer for another issue", []string{holdComment(7, own, rearmSHA)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			read := captureLogs(t)
			b, sha := continueHeldBranch(context.Background(), fakeCommentLister{bodies: tt.bodies}, "o", "r", 100, local, "GH-100", own, 0)
			if sha != "" || b != own {
				t.Errorf("got (%q, %q), want the unchanged branch and no sha", b, sha)
			}
			if got := read(); !strings.Contains(got, "hold comment superseded or invalid") || strings.Contains(got, "Continuing the branch a pre-PR hold pushed") {
				t.Errorf("want the superseded-or-invalid line and no continuation line, got %q", got)
			}
		})
	}
}
