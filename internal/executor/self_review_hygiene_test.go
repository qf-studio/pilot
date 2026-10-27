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
	"sync"
	"testing"
	"time"
)

// selfReviewEditBackend is a fake self-review backend: it reads the execution
// path from its options, writes a file there (the self-review session editing
// the clone), then returns the configured result or error.
type selfReviewEditBackend struct {
	mu       sync.Mutex
	gotPath  string
	fileName string
	output   string
	err      error
}

func (b *selfReviewEditBackend) Name() string      { return "mock" }
func (b *selfReviewEditBackend) IsAvailable() bool { return true }
func (b *selfReviewEditBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.gotPath = opts.ProjectPath
	b.mu.Unlock()
	if err := os.WriteFile(filepath.Join(opts.ProjectPath, b.fileName), []byte("self-review edit\n"), 0o644); err != nil {
		return nil, err
	}
	// Also touch a tracked file so both unstaged and untracked paths are covered.
	if err := os.WriteFile(filepath.Join(opts.ProjectPath, "README.md"), []byte("edited by review\n"), 0o644); err != nil {
		return nil, err
	}
	if b.err != nil {
		return nil, b.err
	}
	return &BackendResult{Success: true, Output: b.output}, nil
}

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

func TestSelfReviewTreeHygiene(t *testing.T) {
	killedErr := &ClaudeCodeError{Type: ErrorTypeShutdownTerminated, Message: "Process terminated by SIGKILL after context cancellation (exit code 137)"}

	tests := []struct {
		name        string
		output      string
		err         error
		useWorktree bool
		wantClean   bool
		wantCommits int // commits added on top of the initial one
		wantLog     string
		wantNote    bool
	}{
		{
			name:        "killed review discards edits",
			err:         killedErr,
			wantClean:   true,
			wantCommits: 0,
			wantLog:     "discarded-killed",
			wantNote:    true,
		},
		{
			name:        "REVIEW_FIXED edits are committed",
			output:      "REVIEW_FIXED: wired the config field",
			wantClean:   true,
			wantCommits: 1,
			wantLog:     "committed",
		},
		{
			name:        "failed review discards edits",
			err:         fmt.Errorf("backend exploded"),
			wantClean:   true,
			wantCommits: 0,
			wantLog:     "discarded-failed",
		},
		{
			name:        "passed review with stray edits discards them",
			output:      "REVIEW_PASSED",
			wantClean:   true,
			wantCommits: 0,
			wantLog:     "discarded-unreported",
		},
		{
			name:        "worktree mode skips hygiene",
			err:         killedErr,
			useWorktree: true,
			wantClean:   false,
			wantCommits: 0,
			wantLog:     "",
			wantNote:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)

			backend := &selfReviewEditBackend{fileName: "review_edit.go", output: tt.output, err: tt.err}
			runner := NewRunnerWithBackend(backend)
			runner.skipPreflightChecks = true
			var logBuf bytes.Buffer
			runner.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			task := &Task{
				ID:          "GH-5900",
				Title:       "Add complex feature with multiple components",
				Description: "Implement a complex multi-step feature requiring significant changes",
				ProjectPath: dir,
			}
			state := &progressState{}

			if err := runner.runSelfReview(context.Background(), task, dir, state); err != nil {
				t.Fatalf("runSelfReview returned error: %v", err)
			}
			if backend.gotPath != dir {
				t.Fatalf("fake backend saw ProjectPath %q, want %q", backend.gotPath, dir)
			}

			terminated := runner.afterSelfReview(task, NewGitOperations(dir), state, true, tt.useWorktree)

			status := strings.TrimSpace(gitOut(t, dir, "status", "--porcelain"))
			if tt.wantClean && status != "" {
				t.Errorf("tree not clean after self-review:\n%s", status)
			}
			if !tt.wantClean && status == "" {
				t.Errorf("tree unexpectedly clean; hygiene should not run in worktree mode")
			}
			if diff := strings.TrimSpace(gitOut(t, dir, "diff", "--cached", "--stat")); tt.wantClean && diff != "" {
				t.Errorf("staged diff left behind:\n%s", diff)
			}

			commits := strings.Count(strings.TrimSpace(gitOut(t, dir, "log", "--oneline")), "\n") // lines-1 = commits after init
			if commits != tt.wantCommits {
				t.Errorf("HEAD moved by %d commits, want %d", commits, tt.wantCommits)
			}
			if tt.wantCommits == 1 {
				if msg := gitOut(t, dir, "log", "-1", "--format=%s"); !strings.Contains(msg, "chore(self-review)") {
					t.Errorf("commit message = %q, want chore(self-review) prefix", msg)
				}
				if _, err := os.Stat(filepath.Join(dir, "review_edit.go")); err != nil {
					t.Errorf("committed edit missing from tree: %v", err)
				}
			}

			logs := logBuf.String()
			if tt.wantLog == "" {
				if strings.Contains(logs, "tree hygiene") {
					t.Errorf("hygiene logged in worktree mode:\n%s", logs)
				}
			} else if !strings.Contains(logs, "reason="+tt.wantLog) || !strings.Contains(logs, "files=2") {
				t.Errorf("log missing reason=%s files=2:\n%s", tt.wantLog, logs)
			}

			if tt.wantNote {
				if terminated <= 0 {
					t.Fatalf("terminated = %v, want > 0", terminated)
				}
				note := selfReviewPRNote(terminated)
				if !strings.Contains(note, "Self-review was terminated after ") || !strings.Contains(note, "its edits were discarded.") {
					t.Errorf("PR note = %q", note)
				}
			} else if terminated != 0 {
				t.Errorf("terminated = %v, want 0", terminated)
			}
		})
	}
}

func TestSelfReviewTreeHygiene_KilledReviewDiscardsEdits(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	backend := &selfReviewEditBackend{
		fileName: "partial_edit.go",
		err:      &ClaudeCodeError{Type: ErrorTypeShutdownTerminated, Message: "killed"},
	}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	var logBuf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	task := &Task{ID: "GH-5900", Title: "Add complex feature with multiple components", Description: "Implement a complex multi-step feature requiring significant changes", ProjectPath: dir}
	state := &progressState{}
	if err := runner.runSelfReview(context.Background(), task, dir, state); err != nil {
		t.Fatalf("runSelfReview: %v", err)
	}
	if !state.selfReview.killed {
		t.Fatalf("outcome.killed = false, want true")
	}
	runner.afterSelfReview(task, NewGitOperations(dir), state, true, false)

	if out := strings.TrimSpace(gitOut(t, dir, "status", "--porcelain")); out != "" {
		t.Errorf("edits survived a killed self-review:\n%s", out)
	}
	if !strings.Contains(logBuf.String(), "discarded-killed") {
		t.Errorf("log lacks discarded-killed line:\n%s", logBuf.String())
	}
}

func TestSelfReviewTreeHygiene_WorktreeModeSkipped(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	backend := &selfReviewEditBackend{fileName: "wt_edit.go", output: "REVIEW_FIXED: x"}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	var logBuf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	task := &Task{ID: "GH-5900", Title: "Add complex feature with multiple components", Description: "Implement a complex multi-step feature requiring significant changes", ProjectPath: dir}
	state := &progressState{}
	if err := runner.runSelfReview(context.Background(), task, dir, state); err != nil {
		t.Fatalf("runSelfReview: %v", err)
	}
	runner.afterSelfReview(task, NewGitOperations(dir), state, true, true /* useWorktree */)

	if out := strings.TrimSpace(gitOut(t, dir, "status", "--porcelain")); out == "" {
		t.Errorf("hygiene ran in worktree mode: tree is clean")
	}
	if n := strings.Count(strings.TrimSpace(gitOut(t, dir, "log", "--oneline")), "\n"); n != 0 {
		t.Errorf("hygiene committed in worktree mode (%d extra commits)", n)
	}
	if strings.Contains(logBuf.String(), "tree hygiene") {
		t.Errorf("hygiene logged in worktree mode:\n%s", logBuf.String())
	}
}

func TestSelfReviewTreeHygiene_NotRunWhenSkipped(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("preexisting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewRunnerWithBackend(&selfReviewEditBackend{fileName: "x.go"})
	task := &Task{ID: "GH-5900", ProjectPath: dir}

	// Self-review skipped (executed=false): leave the tree alone.
	if got := runner.afterSelfReview(task, NewGitOperations(dir), &progressState{}, false, false); got != 0 {
		t.Errorf("terminated = %v, want 0", got)
	}
	// Review never reached the backend (ran=false): leave the tree alone too.
	if got := runner.afterSelfReview(task, NewGitOperations(dir), &progressState{}, true, false); got != 0 {
		t.Errorf("terminated = %v, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Errorf("pre-existing file removed: %v", err)
	}
}

func TestSelfReviewWasTerminated(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"shutdown_terminated", context.Background(), &ClaudeCodeError{Type: ErrorTypeShutdownTerminated}, true},
		{"timeout type", context.Background(), &ClaudeCodeError{Type: ErrorTypeTimeout}, true},
		{"expired review ctx", expired, fmt.Errorf("boom"), true},
		{"deadline error", context.Background(), fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"api error", context.Background(), &ClaudeCodeError{Type: ErrorTypeAPIError}, false},
		{"plain error", context.Background(), fmt.Errorf("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selfReviewWasTerminated(tt.ctx, tt.err); got != tt.want {
				t.Errorf("selfReviewWasTerminated = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveSelfReviewTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  *BackendConfig
		want time.Duration
	}{
		{"nil config claude-code default", nil, 2 * time.Minute},
		{"unset claude-code default", &BackendConfig{Type: BackendTypeClaudeCode}, 2 * time.Minute},
		{"unset opencode default", &BackendConfig{Type: BackendTypeOpenCode}, 10 * time.Minute},
		{"set applies to claude-code", &BackendConfig{Type: BackendTypeClaudeCode, SelfReviewTimeout: 7 * time.Minute}, 7 * time.Minute},
		{"set applies to opencode", &BackendConfig{Type: BackendTypeOpenCode, SelfReviewTimeout: 7 * time.Minute}, 7 * time.Minute},
		{"negative falls back to default", &BackendConfig{Type: BackendTypeClaudeCode, SelfReviewTimeout: -time.Second}, 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewRunnerWithBackend(&selfReviewEditBackend{})
			runner.config = tt.cfg
			if got := runner.selfReviewTimeout(); got != tt.want {
				t.Errorf("selfReviewTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveSelfReviewTimeout_ConfiguredOverridesDefault(t *testing.T) {
	runner := NewRunnerWithBackend(&selfReviewEditBackend{})
	runner.config = &BackendConfig{Type: BackendTypeClaudeCode, SelfReviewTimeout: 7 * time.Minute}
	if got := runner.selfReviewTimeout(); got != 7*time.Minute {
		t.Errorf("selfReviewTimeout() = %v, want 7m (configured value must override the 2m default)", got)
	}
	runner.config.SelfReviewTimeout = 0
	if got := runner.selfReviewTimeout(); got != 2*time.Minute {
		t.Errorf("selfReviewTimeout() unset = %v, want 2m", got)
	}
}
