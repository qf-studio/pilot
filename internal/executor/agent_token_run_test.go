package executor

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
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
	runner.SetAgentTokenRepoLookup(func(path string) (string, bool) {
		if path == e.repo {
			return "OWNER-A/NAME", true
		}
		return "", false
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

// agentTokenWarnLines returns the log lines of out that say a run gets no
// read token.
func agentTokenWarnLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "no GitHub read token for the agent") {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// TestAgentTokenNoProviderWarnsOncePerRun (TASK-651): with the option set and
// no read token provider installed, a run whose backend runs twice logs one
// Warn line that says so, and no backend call gets a token file.
func TestAgentTokenNoProviderWarnsOncePerRun(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	setAgentReadTokenProviderForTest(t, nil)

	backend := &tokenFileRecordingBackend{}
	runner := NewRunnerWithBackend(backend)
	logs := &syncBuffer{}
	runner.log = slog.New(slog.NewJSONHandler(logs, nil))
	runner.config = &BackendConfig{}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.retrier = NewRetrier(&RetryConfig{
		Enabled:  true,
		APIError: &RetryStrategy{MaxAttempts: 1, InitialBackoff: time.Millisecond, BackoffMultiplier: 1},
	})
	runner.SetAgentTokenRepoLookup(func(path string) (string, bool) { return "OWNER-A/NAME", true })

	task := &Task{ID: "GH-6621", Title: "t", Description: "d", ProjectPath: e.repo, Branch: "pilot/GH-6621", CreatePR: true, BaseBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = runner.Execute(ctx, task)

	paths, _ := backend.seen()
	if len(paths) < 2 {
		t.Fatalf("the backend ran %d time(s), want at least 2 (a failure and the retry)", len(paths))
	}
	for i, path := range paths {
		if path != "" {
			t.Errorf("backend call %d got token file %q, want none", i+1, path)
		}
	}
	recs := agentTokenWarnLines(t, logs.String())
	if len(recs) != 1 {
		t.Fatalf("no-token log lines = %v, want one", recs)
	}
	if rec := recs[0]; rec["level"] != "WARN" || rec["task_id"] != "GH-6621" ||
		!strings.Contains(rec["msg"].(string), "no read token provider") {
		t.Errorf("warning = %v, want WARN for GH-6621 saying no read token provider is installed", rec)
	}
	if dirs := tokenDirs(t, tmp); len(dirs) != 0 {
		t.Errorf("token directories after the run: %q", dirs)
	}
}

// TestAgentTokenNoTokenReasons (TASK-651): with the option set, each way a
// run gets no token logs one Warn line that says why and names the path;
// with the option empty no line.
func TestAgentTokenNoTokenReasons(t *testing.T) {
	p := &repoTokenProvider{}
	entries := map[string]string{"/srv/a": "OWNER-A/NAME", "/srv/c": ""}
	lookup := func(path string) (string, bool) {
		repo, ok := entries[path]
		return repo, ok
	}
	for _, tc := range []struct {
		name     string
		option   bool
		provider AgentReadTokenProvider
		lookup   AgentTokenRepoLookup
		path     string
		reason   string // "" for no line
	}{
		{"option empty", false, nil, nil, "/srv/none", ""},
		{"no provider", true, nil, lookup, "/srv/a", "no read token provider"},
		{"no lookup", true, p.provide, nil, "/srv/a", "no repository lookup"},
		{"no project config entry", true, p.provide, lookup, "/srv/none", "no project config entry has the path"},
		{"no GitHub repository in the entry", true, p.provide, lookup, "/srv/c", "has no GitHub repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenTestRunAs(t, tc.option)
			setAgentReadTokenProviderForTest(t, tc.provider)
			var buf syncBuffer
			r := &Runner{log: slog.New(slog.NewJSONHandler(&buf, nil)), agentTokenRepoLookup: tc.lookup}
			if f := r.startRunAgentTokenFile(context.Background(), &Task{ID: "GH-6621", ProjectPath: tc.path}); f != nil {
				f.Stop()
				t.Fatal("a token file")
			}
			recs := agentTokenWarnLines(t, buf.String())
			if tc.reason == "" {
				if out := strings.TrimSpace(buf.String()); out != "" {
					t.Errorf("log = %q, want no line", out)
				}
				return
			}
			if len(recs) != 1 {
				t.Fatalf("no-token log lines = %v, want one", recs)
			}
			rec := recs[0]
			if rec["level"] != "WARN" || !strings.Contains(rec["msg"].(string), tc.reason) || rec["project_path"] != tc.path {
				t.Errorf("warning = %v, want WARN with %q naming project_path %s", rec, tc.reason, tc.path)
			}
		})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.repos) != 0 {
		t.Errorf("provider asked for %v", p.repos)
	}
}
