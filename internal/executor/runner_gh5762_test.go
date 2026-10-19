package executor

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestPushBranchWithRetry_OwnAutoPreservePush (GH-5762): the branch was
// rewritten after the run pushed its auto-preserve commit, so origin sits on
// that commit and the final push is a non-fast-forward. The run may lease on
// the SHA it pushed itself and on no other.
func TestPushBranchWithRetry_OwnAutoPreservePush(t *testing.T) {
	tests := []struct {
		name       string
		pushedWIP  func(s *pushScenario) string // task.pushedWIPSHA
		wantOK     bool
		wantLeases int // lease pushes expected after the plain push
	}{
		{"lease on the SHA this run pushed", func(s *pushScenario) string { return s.remoteHead }, true, 1},
		{"foreign SHA is refused", func(s *pushScenario) string { return s.mainSHA }, false, 0},
		{"no earlier push is refused", func(*pushScenario) string { return "" }, false, 0},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newPushScenario(t, "pilot/GH-5762-"+string(rune('a'+i)), false)
			task := &Task{ID: "GH-5762", Branch: s.branch, pushedWIPSHA: tt.pushedWIP(s)}
			git := NewGitOperations(s.dir)

			err := NewRunnerWithBackend(&mockFixedBackend{}).pushBranchWithRetry(context.Background(), git, task, slog.Default())

			pushes := s.pushes(t)
			if len(pushes) != 1+tt.wantLeases {
				t.Fatalf("push invocations = %q, want %d", pushes, 1+tt.wantLeases)
			}
			for _, p := range pushes {
				for _, f := range strings.Fields(p) {
					if f == "--force" || f == "-f" {
						t.Errorf("push %q uses a plain force", p)
					}
				}
			}
			if tt.wantOK {
				if err != nil {
					t.Fatalf("err = %v, want the lease push to land", err)
				}
				wantLease := "--force-with-lease=refs/heads/" + s.branch + ":" + s.remoteHead
				if !strings.Contains(pushes[1], wantLease) {
					t.Errorf("second push = %q, want %q", pushes[1], wantLease)
				}
				if got := s.originHead(t); got != s.localHead {
					t.Errorf("origin head = %s, want %s", got, s.localHead)
				}
				return
			}
			if err == nil {
				t.Fatal("err = nil, want the foreign-head rejection to fail")
			}
			if got := s.originHead(t); got != s.remoteHead {
				t.Errorf("origin head = %s, want it untouched at %s", got, s.remoteHead)
			}
			// The escalation reason names both SHAs, is deterministic, and is
			// not classified infra (an infra status would be re-picked).
			reason := formatGitStepFailureWithRecovery(context.Background(), git, "push", gitPushRetryAttempts, err, task.ID, task.Branch, s.localHead)
			for _, sha := range []string{s.remoteHead, s.localHead} {
				if !strings.Contains(reason, sha) {
					t.Errorf("reason = %q, want it to name %s", reason, sha)
				}
			}
			if !IsDeterministicFailure(reason) {
				t.Errorf("IsDeterministicFailure(%q) = false, want true", reason)
			}
			if got := TerminalStatus(&ExecutionResult{Error: reason}); got != "failed" {
				t.Errorf("TerminalStatus = %q, want failed (not infra)", got)
			}
		})
	}
}

// TestTerminalStatus_PlainPushFailureStaysInfra (GH-5762): only the
// non-fast-forward-against-task-branch escalation leaves the infra class.
func TestTerminalStatus_PlainPushFailureStaysInfra(t *testing.T) {
	tests := []struct {
		err  string
		want string
	}{
		{"push failed after 3 attempt(s): connection reset", "infra"},
		{"push failed after 3 attempt(s): push rejected, " + nonFastForwardTaskBranchMarker + " pilot/GH-1: remote head abc", "failed"},
	}
	for _, tt := range tests {
		if got := TerminalStatus(&ExecutionResult{Error: tt.err}); got != tt.want {
			t.Errorf("TerminalStatus(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
