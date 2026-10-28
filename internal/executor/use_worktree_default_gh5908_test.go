package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEffectiveUseWorktree(t *testing.T) {
	tests := []struct {
		name string
		cfg  *BackendConfig
		want bool
	}{
		{"nil config", nil, true},
		{"unset field", &BackendConfig{}, true},
		{"explicit true", &BackendConfig{UseWorktree: boolPtr(true)}, true},
		{"explicit false", &BackendConfig{UseWorktree: boolPtr(false)}, false},
		{"default config", DefaultBackendConfig(), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveUseWorktree(); got != tt.want {
				t.Errorf("EffectiveUseWorktree() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEffectiveUseWorktree_NilIsTrue pins the GH-5908 default: a config that
// never mentions use_worktree isolates tasks in a worktree.
func TestEffectiveUseWorktree_NilIsTrue(t *testing.T) {
	cfg := &BackendConfig{}
	if cfg.UseWorktree != nil {
		t.Fatalf("zero-value BackendConfig.UseWorktree = %v, want nil", *cfg.UseWorktree)
	}
	if !cfg.EffectiveUseWorktree() {
		t.Error("EffectiveUseWorktree() = false for nil UseWorktree, want true (worktree isolation is the default)")
	}
}

// TestRunnerUsesWorktreeByDefault drives Execute with a nil-valued
// use_worktree and a branch task, and checks the backend ran in a worktree.
// The explicit-false row proves direct mode still runs in the project clone.
func TestRunnerUsesWorktreeByDefault(t *testing.T) {
	tests := []struct {
		name         string
		useWorktree  *bool
		wantWorktree bool
	}{
		{"nil (unset) isolates in a worktree", nil, true},
		{"explicit true isolates in a worktree", boolPtr(true), true},
		{"explicit false edits the clone in place", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo, remoteRepo := setupTestRepoWithRemote(t)
			defer func() { _ = os.RemoveAll(localRepo) }()
			defer func() { _ = os.RemoveAll(remoteRepo) }()

			backend := &retryPathRecordingBackend{}
			runner := NewRunnerWithBackend(backend)
			runner.config = &BackendConfig{UseWorktree: tt.useWorktree}
			runner.SetSkipPreflightChecks(true)
			runner.SetRecordingEnabled(false)

			task := &Task{
				ID:          "GH-5908",
				Title:       "worktree is the default",
				Description: "the backend never commits; only the path it ran in matters",
				ProjectPath: localRepo,
				Branch:      "pilot/GH-5908-default",
				CreatePR:    true,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, _ = runner.Execute(ctx, task)

			paths := backend.paths()
			if len(paths) == 0 {
				t.Fatal("backend was never invoked")
			}
			inWorktree := strings.Contains(paths[0], "pilot-worktree-")
			if inWorktree != tt.wantWorktree {
				t.Errorf("first backend call ran in %q; in worktree = %v, want %v", paths[0], inWorktree, tt.wantWorktree)
			}
			if !tt.wantWorktree && paths[0] != localRepo {
				t.Errorf("direct mode ran in %q, want project clone %q", paths[0], localRepo)
			}
		})
	}
}
