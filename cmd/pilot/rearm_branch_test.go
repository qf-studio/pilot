package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

const rearmSHA = "b57d141c0ffee0123456789abcdef0123456789a"

func rearmComment(source int, branch string) string {
	return "\U0001F501 **Owner-death recovery**: its designated fix issue #200 died. Re-armed.\n\n" +
		"Retry continues branch `" + branch + "` at `b57d141`; the closed PR #6165 holds the review history.\n\n" +
		"<!-- autopilot-meta branch:" + branch + " pr:6165 iteration:2 source:" + strconv.Itoa(source) + " sha:" + rearmSHA + " -->"
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

func TestResolveRearmBranch(t *testing.T) {
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
