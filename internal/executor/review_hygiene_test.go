package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestReviewPhaseTreeHygiene drives the hygiene pass the way the pre-PR fix
// round does: the session left an untracked file and an edit to a tracked one,
// and the outcome says how the session ended.
func TestReviewPhaseTreeHygiene(t *testing.T) {
	tests := []struct {
		name         string
		out          reviewPhaseOutcome
		wantAction   hygieneAction
		wantCommits  int // commits added on top of the initial one
		wantLog      string
		wantFileKept bool
	}{
		{
			name:       "killed session discards edits",
			out:        reviewPhaseOutcome{ran: true, failed: true, killed: true},
			wantAction: hygieneDiscarded,
			wantLog:    "discarded-killed",
		},
		{
			name:         "REVIEW_FIXED edits are committed",
			out:          reviewPhaseOutcome{ran: true, fixed: true},
			wantAction:   hygieneCommitted,
			wantCommits:  1,
			wantLog:      "committed",
			wantFileKept: true,
		},
		{
			name:       "failed session discards edits",
			out:        reviewPhaseOutcome{ran: true, failed: true},
			wantAction: hygieneDiscarded,
			wantLog:    "discarded-failed",
		},
		{
			name:       "unreported edits are discarded",
			out:        reviewPhaseOutcome{ran: true},
			wantAction: hygieneDiscarded,
			wantLog:    "discarded-unreported",
		},
		{
			name:       "backend never invoked leaves the tree alone",
			out:        reviewPhaseOutcome{},
			wantAction: hygieneNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			if err := os.WriteFile(filepath.Join(dir, "review_edit.go"), []byte("review edit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("edited by review\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			runner := NewRunnerWithBackend(&guardRecordingBackend{})
			var logBuf bytes.Buffer
			runner.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			task := &Task{ID: "GH-5900", ProjectPath: dir}

			if got := runner.reviewPhaseTreeHygiene(task, NewGitOperations(dir), reviewFixPhase, tt.out); got != tt.wantAction {
				t.Errorf("action = %q, want %q", got, tt.wantAction)
			}

			status := strings.TrimSpace(gitOut(t, dir, "status", "--porcelain"))
			if tt.wantAction == hygieneNone {
				if status == "" {
					t.Error("tree was cleaned although the phase never ran")
				}
				if strings.Contains(logBuf.String(), "tree hygiene") {
					t.Errorf("hygiene logged for a phase that never ran:\n%s", logBuf.String())
				}
				return
			}
			if status != "" {
				t.Errorf("tree not clean after the review phase:\n%s", status)
			}
			if commits := strings.Count(strings.TrimSpace(gitOut(t, dir, "log", "--oneline")), "\n"); commits != tt.wantCommits {
				t.Errorf("HEAD moved by %d commits, want %d", commits, tt.wantCommits)
			}
			if _, err := os.Stat(filepath.Join(dir, "review_edit.go")); (err == nil) != tt.wantFileKept {
				t.Errorf("review_edit.go kept = %v, want %v", err == nil, tt.wantFileKept)
			}
			if tt.wantCommits == 1 {
				if msg := gitOut(t, dir, "log", "-1", "--format=%s"); !strings.Contains(msg, "chore(review-fix)") {
					t.Errorf("commit message = %q, want chore(review-fix) prefix", msg)
				}
			}
			if logs := logBuf.String(); !strings.Contains(logs, "reason="+tt.wantLog) || !strings.Contains(logs, "files=2") {
				t.Errorf("log missing reason=%s files=2:\n%s", tt.wantLog, logs)
			}
		})
	}
}

func TestReviewWasTerminated(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"terminated by signal", context.Background(), &ClaudeCodeError{Type: ErrorTypeShutdownTerminated}, true},
		{"timeout type", context.Background(), &ClaudeCodeError{Type: ErrorTypeTimeout}, true},
		{"expired review ctx", expired, fmt.Errorf("boom"), true},
		{"deadline error", context.Background(), fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"api error", context.Background(), &ClaudeCodeError{Type: ErrorTypeAPIError}, false},
		{"plain error", context.Background(), fmt.Errorf("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewWasTerminated(tt.ctx, tt.err); got != tt.want {
				t.Errorf("reviewWasTerminated = %v, want %v", got, tt.want)
			}
		})
	}
}
