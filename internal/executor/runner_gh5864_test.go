package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRecordingGh puts a fake `gh` on PATH. `gh pr list` answers openPRJSON,
// `gh issue comment` appends its arguments to the returned log file.
func writeRecordingGh(t *testing.T, openPRJSON string) (commentLog string) {
	t.Helper()
	fakeBin := t.TempDir()
	commentLog = filepath.Join(fakeBin, "comments.log")
	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  \"issue comment\") echo \"$@\" >> " + commentLog + " ;;\n" +
		"  \"pr list\") echo '" + openPRJSON + "' ;;\n" +
		"  *) echo '[]' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil { //nolint:gosec // test wrapper must be executable
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return commentLog
}

// TestPushBranchWithRetry_InheritedHeadLease (GH-5864): a previous run of the
// same task left a commit on pilot/GH-N and opened no PR, so the re-pick's push
// is a non-fast-forward against a head this run never pushed. The dispatcher
// recorded that head; the run may lease on exactly it, once, and only while no
// open PR has the branch as its head.
func TestPushBranchWithRetry_InheritedHeadLease(t *testing.T) {
	const noPR, openPR = `[]`, `[{"url":"https://github.com/o/r/pull/9"}]`
	tests := []struct {
		name      string
		inherited func(s *pushScenario) string // task.inheritedHeadSHA
		prJSON    string
		wantOK    bool
	}{
		{"stale head, no open PR: lease", func(s *pushScenario) string { return s.remoteHead }, noPR, true},
		{"stale head, open PR on the branch: escalate", func(s *pushScenario) string { return s.remoteHead }, openPR, false},
		{"inherited SHA is not origin's head: escalate", func(s *pushScenario) string { return s.mainSHA }, noPR, false},
		{"nothing inherited: escalate", func(*pushScenario) string { return "" }, noPR, false},
	}
	prev := inheritedHeadOpenPRCheck
	inheritedHeadOpenPRCheck = func(ctx context.Context, git *GitOperations, branch string) (string, error) {
		return git.FindOpenPRByBranch(ctx, branch)
	}
	t.Cleanup(func() { inheritedHeadOpenPRCheck = prev })

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newPushScenario(t, "pilot/GH-5864-"+string(rune('a'+i)), false)
			commentLog := writeRecordingGh(t, tt.prJSON)
			task := &Task{ID: "GH-5864", Branch: s.branch, ProjectPath: s.dir, inheritedHeadSHA: tt.inherited(s)}
			git := NewGitOperations(s.dir)

			err := NewRunnerWithBackend(&mockFixedBackend{}).pushBranchWithRetry(context.Background(), git, task, slog.Default())

			pushes := s.pushes(t)
			comments, _ := os.ReadFile(commentLog)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("err = %v, want the lease push to land", err)
				}
				if len(pushes) != 2 {
					t.Fatalf("push invocations = %q, want a plain push then one lease push", pushes)
				}
				wantLease := "--force-with-lease=refs/heads/" + s.branch + ":" + s.remoteHead
				if !strings.Contains(pushes[1], wantLease) {
					t.Errorf("second push = %q, want %q", pushes[1], wantLease)
				}
				if got := s.originHead(t); got != s.localHead {
					t.Errorf("origin head = %s, want %s", got, s.localHead)
				}
				if n := strings.Count(string(comments), "issue comment"); n != 1 || !strings.Contains(string(comments), s.remoteHead) {
					t.Errorf("comments = %q, want exactly one naming %s", comments, s.remoteHead)
				}
				if task.inheritedHeadSHA != "" {
					t.Errorf("inheritedHeadSHA = %q, want it cleared after the replace", task.inheritedHeadSHA)
				}
				return
			}
			if err == nil {
				t.Fatal("err = nil, want the non-fast-forward to escalate")
			}
			if len(pushes) != 1 {
				t.Errorf("push invocations = %q, want only the plain push", pushes)
			}
			if got := s.originHead(t); got != s.remoteHead {
				t.Errorf("origin head = %s, want it untouched at %s", got, s.remoteHead)
			}
			if len(comments) != 0 {
				t.Errorf("comments = %q, want none", comments)
			}
			if !strings.Contains(err.Error(), nonFastForwardTaskBranchMarker) {
				t.Errorf("err = %q, want the %q escalation", err, nonFastForwardTaskBranchMarker)
			}
		})
	}
}
