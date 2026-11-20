package executor

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// tokenFileRecordingBackend records the read token file each backend call of
// a run gets on its context, and what the file holds then. The first call
// fails with a retryable error so the smart retry runs the backend again.
type tokenFileRecordingBackend struct {
	mu     sync.Mutex
	paths  []string
	tokens []string
}

func (b *tokenFileRecordingBackend) Name() string      { return "token-file-recording" }
func (b *tokenFileRecordingBackend) IsAvailable() bool { return true }
func (b *tokenFileRecordingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	path, token := "", ""
	if f := agentTokenFileFrom(ctx); f != nil {
		path = f.path
		if raw, err := os.ReadFile(f.path); err == nil {
			token = string(raw)
		}
	}
	b.mu.Lock()
	b.paths = append(b.paths, path)
	b.tokens = append(b.tokens, token)
	n := len(b.paths)
	b.mu.Unlock()
	if n == 1 {
		return nil, gh5718BackendErr{}
	}
	return &BackendResult{Success: true, Output: "no changes made"}, nil
}

func (b *tokenFileRecordingBackend) seen() (paths, tokens []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.paths...), append([]string(nil), b.tokens...)
}

// TestAgentTokenRunWiringThroughExecute (TASK-651): the read token file
// belongs to the run, made by executeWithOptions. A run whose backend fails
// once and is tried again asks the provider once, hands each backend call
// the same token file, and leaves no token directory at the end. Mutation pin:
// remove the startRunAgentTokenFile block of executeWithOptions (runner.go)
// and this fails.
func TestAgentTokenRunWiringThroughExecute(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	p := &repoTokenProvider{}
	setAgentReadTokenProviderForTest(t, p.provide)

	backend := &tokenFileRecordingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.retrier = NewRetrier(&RetryConfig{
		Enabled:  true,
		APIError: &RetryStrategy{MaxAttempts: 1, InitialBackoff: time.Millisecond, BackoffMultiplier: 1},
	})
	runner.SetAgentTokenRepoLookup(func(path string) string {
		if path == e.repo {
			return "OWNER-A/NAME"
		}
		return ""
	})

	task := &Task{ID: "GH-6596", Title: "t", Description: "d", ProjectPath: e.repo, Branch: "pilot/GH-6596", CreatePR: true, BaseBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = runner.Execute(ctx, task)

	paths, tokens := backend.seen()
	// The smart retry of the failure runs the backend a second time; the
	// retry of a run with no commit runs it a third time, in the same run.
	if len(paths) < 2 {
		t.Fatalf("the backend ran %d time(s), want at least 2 (a failure and the retry)", len(paths))
	}
	for i, path := range paths {
		if path == "" {
			t.Errorf("backend call %d got no token file", i+1)
		} else if path != paths[0] {
			t.Errorf("backend call %d got token file %q, want the one of call 1, %q", i+1, path, paths[0])
		}
		if want := "test-read-token-for-OWNER-A/NAME"; tokens[i] != want {
			t.Errorf("backend call %d: token file holds %q, want %q", i+1, tokens[i], want)
		}
	}
	p.mu.Lock()
	repos := append([]string(nil), p.repos...)
	p.mu.Unlock()
	if len(repos) != 1 || repos[0] != "OWNER-A/NAME" {
		t.Errorf("provider asked for %q, want once for OWNER-A/NAME", repos)
	}
	if dirs := tokenDirs(t, tmp); len(dirs) != 0 {
		t.Errorf("token directories left after the run: %q", dirs)
	}
}
