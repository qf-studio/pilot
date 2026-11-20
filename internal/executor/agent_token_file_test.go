package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// TASK-651: the read token file of a run_as_user run. None of these tests
// needs root or GitHub: the provider is a fake and sudo is a mock script.

// setAgentReadTokenProviderForTest installs provider for one test.
func setAgentReadTokenProviderForTest(t *testing.T, provider AgentReadTokenProvider) {
	t.Helper()
	prev := GetAgentReadTokenProvider()
	SetAgentReadTokenProvider(provider)
	t.Cleanup(func() { SetAgentReadTokenProvider(prev) })
}

// fakeReadTokenProvider hands out tokens that expire just after the refresh
// margin, so the refresh runs within the test.
type fakeReadTokenProvider struct {
	mu     sync.Mutex
	calls  int
	repos  []string
	tokens []string // tokens to hand out in order; the last repeats with a counter
	expiry time.Duration
	err    error
}

func (p *fakeReadTokenProvider) provide(_ context.Context, repo string) (string, time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.repos = append(p.repos, repo)
	if p.err != nil {
		return "", time.Time{}, p.err
	}
	token := fmt.Sprintf("test-read-token-%d", p.calls)
	if p.calls <= len(p.tokens) {
		token = p.tokens[p.calls-1]
	}
	return token, time.Now().Add(agentTokenRefreshMargin + p.expiry), nil
}

func (p *fakeReadTokenProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// tokenTestRunAs sets the option with a group the daemon user is a member of
// (another than the primary one when there is one), lowers the retry wait and
// gives os.TempDir a fresh directory. It returns the settings and that
// directory.
func tokenTestRunAs(t *testing.T, set bool) (*runAsSettings, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	prevWait := agentTokenRetryWait
	agentTokenRetryWait = 10 * time.Millisecond
	t.Cleanup(func() { agentTokenRetryWait = prevWait })
	if !set {
		setRunAsStateForTest(t, nil, "")
		return nil, tmp
	}
	s := testRunAsSettings(t)
	s.gid = os.Getegid()
	if groups, err := os.Getgroups(); err == nil {
		for _, g := range groups {
			if g != os.Getegid() {
				s.gid = g
				break
			}
		}
	}
	setRunAsStateForTest(t, s, "")
	return s, tmp
}

// tokenDirs lists the token directories in tmp.
func tokenDirs(t *testing.T, tmp string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(tmp, "pilot-ghtoken-*"))
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func readTokenFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	return string(raw)
}

func TestAgentTokenFileModeAndRefresh(t *testing.T) {
	t.Run("modes, group and refresh; nothing after the end of the run", func(t *testing.T) {
		s, tmp := tokenTestRunAs(t, true)
		p := &fakeReadTokenProvider{expiry: 150 * time.Millisecond}
		setAgentReadTokenProviderForTest(t, p.provide)

		f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
		if f == nil {
			t.Fatal("no token file with the option set and a provider")
		}
		defer f.Stop()
		if got := tokenDirs(t, tmp); len(got) != 1 || got[0] != f.dir {
			t.Fatalf("token dirs = %v, want [%s]", got, f.dir)
		}
		if f.path != filepath.Join(f.dir, "github-token") {
			t.Errorf("token file = %s, want github-token in %s", f.path, f.dir)
		}
		if f.Env() != ghguard.EnvTokenFile+"="+f.path || ghguard.EnvTokenFile != "PILOT_GH_TOKEN_FILE" {
			t.Errorf("Env() = %q", f.Env())
		}
		di, err := os.Stat(f.dir)
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm() != 0o750 {
			t.Errorf("token dir mode = %o, want 750", di.Mode().Perm())
		}
		fi, err := os.Stat(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o640 {
			t.Errorf("token file mode = %o, want 640", fi.Mode().Perm())
		}
		if gid := fileGID(t, f.dir); gid != s.gid {
			t.Errorf("token dir gid = %d, want %d", gid, s.gid)
		}
		if gid := fileGID(t, f.path); gid != s.gid {
			t.Errorf("token file gid = %d, want %d", gid, s.gid)
		}
		if got := readTokenFile(t, f.path); got != "test-read-token-1" {
			t.Errorf("token file = %q, want test-read-token-1", got)
		}

		deadline := time.Now().Add(5 * time.Second)
		for readTokenFile(t, f.path) == "test-read-token-1" {
			if time.Now().After(deadline) {
				t.Fatal("the token file was not refreshed before the expiry")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if fi, err := os.Stat(f.path); err != nil {
			t.Errorf("refreshed token file: %v", err)
		} else if fi.Mode().Perm() != 0o640 {
			t.Errorf("refreshed token file mode = %o, want 640", fi.Mode().Perm())
		}
		p.mu.Lock()
		for _, r := range p.repos {
			if r != "qf-studio/pilot" {
				t.Errorf("provider got repo %q, want qf-studio/pilot", r)
			}
		}
		p.mu.Unlock()

		f.Stop()
		f.Stop() // a second Stop is harmless
		if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
			t.Errorf("token dir still there after the end of the run: %v", err)
		}
		calls := p.callCount()
		time.Sleep(400 * time.Millisecond)
		if got := p.callCount(); got != calls {
			t.Errorf("provider called %d times after the end of the run", got-calls)
		}
	})

	t.Run("provider error: no file, run goes on, warning cut to 200 characters", func(t *testing.T) {
		_, tmp := tokenTestRunAs(t, true)
		long := "test-provider-failure " + strings.Repeat("x", 500)
		p := &fakeReadTokenProvider{err: errors.New(long)}
		setAgentReadTokenProviderForTest(t, p.provide)
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))

		if f := startAgentTokenFile(context.Background(), log, "qf-studio/pilot"); f != nil {
			f.Stop()
			t.Fatal("a provider error gave a token file")
		}
		if got := tokenDirs(t, tmp); len(got) != 0 {
			t.Errorf("token dirs after a provider error: %v", got)
		}
		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("warning not logged: %v (%q)", err, buf.String())
		}
		if rec["level"] != "WARN" {
			t.Errorf("level = %v, want WARN", rec["level"])
		}
		msg, _ := rec["error"].(string)
		if len([]rune(msg)) > 200 || msg != long[:200] {
			t.Errorf("warning error = %q (%d characters), want the first 200 characters", msg, len([]rune(msg)))
		}
		if strings.Count(buf.String(), "x") > 200 {
			t.Errorf("warning holds more than 200 characters of the error: %q", buf.String())
		}
	})

	t.Run("a link at the temporary name fails the write and its target stays", func(t *testing.T) {
		tokenTestRunAs(t, true)
		for _, existing := range []bool{true, false} {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "target")
			if existing {
				if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(dir, "github-token.tmp")); err != nil {
				t.Fatal(err)
			}
			if err := writeAgentTokenFile(dir, "test-read-token"); err == nil {
				t.Errorf("existing target %v: the write through a link did not fail", existing)
			}
			raw, err := os.ReadFile(target)
			switch {
			case existing && (err != nil || string(raw) != "original"):
				t.Errorf("link target = %q, %v, want original", raw, err)
			case !existing && err == nil:
				t.Errorf("the write created the link target: %q", raw)
			}
			if _, err := os.Stat(filepath.Join(dir, "github-token")); err == nil {
				t.Error("a token file came from a failed write")
			}
			if _, err := os.Lstat(filepath.Join(dir, "github-token.tmp")); err != nil {
				t.Errorf("the failed write removed the link: %v", err)
			}
		}
	})

	t.Run("a token with quotes, $, a backslash and a leading dash is byte for byte", func(t *testing.T) {
		tokenTestRunAs(t, true)
		token := `-test'read"token$HOME\x` + "`id`"
		p := &fakeReadTokenProvider{tokens: []string{token}, expiry: time.Hour}
		setAgentReadTokenProviderForTest(t, p.provide)
		f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
		if f == nil {
			t.Fatal("no token file")
		}
		defer f.Stop()
		if got := readTokenFile(t, f.path); got != token {
			t.Errorf("token file = %q, want %q", got, token)
		}
	})

	t.Run("option empty or no provider: no directory", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			set      bool
			provider bool
		}{{"option empty", false, true}, {"no provider", true, false}} {
			_, tmp := tokenTestRunAs(t, tc.set)
			p := &fakeReadTokenProvider{expiry: time.Hour}
			if tc.provider {
				setAgentReadTokenProviderForTest(t, p.provide)
			} else {
				setAgentReadTokenProviderForTest(t, nil)
			}
			if f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot"); f != nil {
				f.Stop()
				t.Errorf("%s: a token file", tc.name)
			}
			if got := tokenDirs(t, tmp); len(got) != 0 {
				t.Errorf("%s: token dirs %v", tc.name, got)
			}
			if p.callCount() != 0 {
				t.Errorf("%s: provider called", tc.name)
			}
		}
	})

	t.Run("the agent process: path in its env, the token in no env value or argument; one directory for two backend calls", func(t *testing.T) {
		stubRunAsGOOS(t)
		const token = "-test'agent\"read$token\\"
		for _, tc := range []struct {
			name string
			err  error
		}{{"token", nil}, {"provider error", errors.New("test-provider-failure")}} {
			_, tmp := tokenTestRunAs(t, true)
			p := &fakeReadTokenProvider{tokens: []string{token}, expiry: time.Hour, err: tc.err}
			setAgentReadTokenProviderForTest(t, p.provide)

			rec := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\0' \"$@\" >> '%[1]s/argv'\nenv >> '%[1]s/env'\n"+
				"if [ -n \"$PILOT_GH_TOKEN_FILE\" ]; then cat \"$PILOT_GH_TOKEN_FILE\" > '%[1]s/seen'; fi\nexit 0\n", rec)
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(script), 0o755); err != nil { //nolint:gosec // mock must be executable
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			projectPath := t.TempDir()
			r := &Runner{log: slog.Default(), agentTokenRepoLookup: func(path string) (string, bool) {
				if path == projectPath {
					return "qf-studio/pilot", true
				}
				return "", false
			}}
			ctx := context.Background()
			tokenFile := r.startRunAgentTokenFile(ctx, &Task{ID: "GH-6596", ProjectPath: projectPath})
			if tokenFile != nil {
				ctx = withAgentTokenFile(ctx, tokenFile)
			}
			b := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: "claude-model-cli"})
			b.SetGhGuard(true, "")
			for i := 0; i < 2; i++ { // the first process and a retry of the run
				_, _ = b.Execute(ctx, ExecuteOptions{
					ProjectPath: t.TempDir(), Prompt: "p", SourceRepo: "qf-studio/pilot", SourceIssueID: "6587",
				})
			}
			if got := p.callCount(); got != 1 {
				t.Errorf("%s: provider called %d times for a run of two backend calls, want 1", tc.name, got)
			}
			if tc.err == nil {
				if got := tokenDirs(t, tmp); len(got) != 1 {
					t.Errorf("%s: token dirs during the run = %v, want one", tc.name, got)
				}
			}
			if tokenFile != nil {
				tokenFile.Stop()
			}

			argv, err := os.ReadFile(filepath.Join(rec, "argv"))
			if err != nil {
				t.Fatalf("%s: the agent process did not start: %v", tc.name, err)
			}
			env, err := os.ReadFile(filepath.Join(rec, "env"))
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range strings.Split(string(argv), "\x00") {
				if strings.Contains(a, token) || strings.Contains(a, "test-read-token") {
					t.Errorf("%s: an argument holds the token: %q", tc.name, a)
				}
			}
			var tokenFiles []string
			for _, kv := range strings.Split(string(env), "\n") {
				if strings.Contains(kv, token) || strings.Contains(kv, "test-read-token") {
					t.Errorf("%s: an env value holds the token: %q", tc.name, kv)
				}
				if v, ok := strings.CutPrefix(kv, "PILOT_GH_TOKEN_FILE="); ok {
					tokenFiles = append(tokenFiles, v)
				}
			}
			seen, seenErr := os.ReadFile(filepath.Join(rec, "seen"))
			if tc.err != nil {
				if tokenFile != nil || len(tokenFiles) != 0 || seenErr == nil {
					t.Errorf("%s: token files %q in the env, seen %q", tc.name, tokenFiles, seen)
				}
				continue
			}
			if len(tokenFiles) != 2 || tokenFiles[0] != tokenFiles[1] || filepath.Base(tokenFiles[0]) != "github-token" {
				t.Fatalf("%s: PILOT_GH_TOKEN_FILE of the two processes = %q, want one github-token twice", tc.name, tokenFiles)
			}
			if string(seen) != token {
				t.Errorf("%s: the agent read %q from the token file, want %q", tc.name, seen, token)
			}
			if _, err := os.Stat(filepath.Dir(tokenFiles[0])); !os.IsNotExist(err) {
				t.Errorf("%s: token dir still there after the run: %v", tc.name, err)
			}
			if got := tokenDirs(t, tmp); len(got) != 0 {
				t.Errorf("%s: token dirs after the run: %v", tc.name, got)
			}
			calls := p.callCount()
			time.Sleep(50 * time.Millisecond)
			if p.callCount() != calls {
				t.Errorf("%s: provider called after the run", tc.name)
			}
		}
	})

	t.Run("a backend process outside a run makes no token file", func(t *testing.T) {
		stubRunAsGOOS(t)
		_, tmp := tokenTestRunAs(t, true)
		p := &fakeReadTokenProvider{expiry: time.Hour}
		setAgentReadTokenProviderForTest(t, p.provide)
		rec := t.TempDir()
		bin := t.TempDir()
		script := fmt.Sprintf("#!/bin/sh\nenv > '%s/env'\nexit 0\n", rec)
		if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(script), 0o755); err != nil { //nolint:gosec // mock must be executable
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		b := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: "claude-model-cli"})
		b.SetGhGuard(true, "")
		_, _ = b.Execute(context.Background(), ExecuteOptions{ProjectPath: t.TempDir(), Prompt: "p", SourceRepo: "qf-studio/pilot"})
		env, err := os.ReadFile(filepath.Join(rec, "env"))
		if err != nil {
			t.Fatalf("the agent process did not start: %v", err)
		}
		if strings.Contains(string(env), "PILOT_GH_TOKEN_FILE=") || p.callCount() != 0 || len(tokenDirs(t, tmp)) != 0 {
			t.Errorf("a token file outside a run: provider calls %d, dirs %v", p.callCount(), tokenDirs(t, tmp))
		}
	})
}

// repoTokenProvider hands out "test-read-token-for-<repo>".
type repoTokenProvider struct {
	mu    sync.Mutex
	repos []string
}

func (p *repoTokenProvider) provide(_ context.Context, repo string) (string, time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.repos = append(p.repos, repo)
	return "test-read-token-for-" + repo, time.Now().Add(time.Hour), nil
}

// TestAgentTokenRepoOfTheProjectEntry: the repository of the read token is
// that of the project config entry of the task path. Of two entries with the
// same repository name under two owners each run gets the token of its own;
// a task that names the repository of the other owner gets none and one Warn
// line names both.
func TestAgentTokenRepoOfTheProjectEntry(t *testing.T) {
	tokenTestRunAs(t, true)
	p := &repoTokenProvider{}
	setAgentReadTokenProviderForTest(t, p.provide)
	entries := map[string]string{"/srv/a/name": "OWNER-A/NAME", "/srv/b/name": "OWNER-B/NAME", "/srv/c": ""}
	var buf bytes.Buffer
	r := &Runner{log: slog.New(slog.NewJSONHandler(&buf, nil)), agentTokenRepoLookup: func(path string) (string, bool) {
		repo, ok := entries[path]
		return repo, ok
	}}

	for _, tc := range []struct {
		name, path, taskRepo, want string
	}{
		{"owner A, no full name on the task", "/srv/a/name", "", "test-read-token-for-OWNER-A/NAME"},
		{"owner B, no full name on the task", "/srv/b/name", "", "test-read-token-for-OWNER-B/NAME"},
		{"owner A, full name of another letter case", "/srv/a/name", "owner-a/name", "test-read-token-for-OWNER-A/NAME"},
		{"owner B, full name of owner A", "/srv/b/name", "OWNER-A/NAME", ""},
		{"no GitHub repository in the config", "/srv/c", "", ""},
		{"no config entry", "/srv/none", "OWNER-A/NAME", ""},
	} {
		buf.Reset()
		p.mu.Lock()
		p.repos = nil
		p.mu.Unlock()
		f := r.startRunAgentTokenFile(context.Background(), &Task{ID: "GH-6596", ProjectPath: tc.path, SourceRepo: tc.taskRepo})
		if tc.want == "" {
			if f != nil {
				f.Stop()
				t.Errorf("%s: a token file", tc.name)
			}
			if len(p.repos) != 0 {
				t.Errorf("%s: provider asked for %v", tc.name, p.repos)
			}
			continue
		}
		if f == nil {
			t.Errorf("%s: no token file", tc.name)
			continue
		}
		if got := readTokenFile(t, f.path); got != tc.want {
			t.Errorf("%s: token file = %q, want %q", tc.name, got, tc.want)
		}
		f.Stop()
	}

	buf.Reset()
	if f := r.startRunAgentTokenFile(context.Background(), &Task{ID: "GH-6596", ProjectPath: "/srv/b/name", SourceRepo: "OWNER-A/NAME"}); f != nil {
		f.Stop()
		t.Fatal("a token file for a task of the other owner")
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %q, want one Warn line", lines)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["level"] != "WARN" || rec["task_repo"] != "OWNER-A/NAME" || rec["project_repo"] != "OWNER-B/NAME" {
		t.Errorf("warning = %v, want WARN naming task_repo OWNER-A/NAME and project_repo OWNER-B/NAME", rec)
	}
}

// TestAgentTokenFileExclusiveCreate: a file at the temporary name makes the
// write fail and stays as it was.
func TestAgentTokenFileExclusiveCreate(t *testing.T) {
	tokenTestRunAs(t, true)
	dir := t.TempDir()
	tmp := filepath.Join(dir, "github-token.tmp")
	if err := os.WriteFile(tmp, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentTokenFile(dir, "test-read-token"); err == nil {
		t.Error("the write over a file at the temporary name did not fail")
	}
	if raw, err := os.ReadFile(tmp); err != nil || string(raw) != "original" {
		t.Errorf("file at the temporary name = %q, %v, want original", raw, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "github-token")); err == nil {
		t.Error("a token file came from a failed write")
	}
}

// recordAgentTokenWaits replaces the timer of the refresh with one that fires
// at once and sends each wait to the returned channel.
func recordAgentTokenWaits(t *testing.T) <-chan time.Duration {
	t.Helper()
	waits := make(chan time.Duration, 64)
	prev := agentTokenTimer
	agentTokenTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		c := make(chan time.Time, 1)
		select {
		case waits <- d:
			c <- time.Now()
		default: // the test read enough: block until Stop
		}
		return c, func() bool { return true }
	}
	t.Cleanup(func() { agentTokenTimer = prev })
	return waits
}

func nextWait(t *testing.T, waits <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case d := <-waits:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no wait of the refresh")
		return 0
	}
}

// TestAgentTokenFileRefreshMargin: a token that expires in 20 minutes is
// refreshed after about 5 minutes (a margin of 15), not after 19.
func TestAgentTokenFileRefreshMargin(t *testing.T) {
	tokenTestRunAs(t, true)
	waits := recordAgentTokenWaits(t)
	gate := make(chan struct{})
	var calls atomic.Int32
	setAgentReadTokenProviderForTest(t, func(ctx context.Context, _ string) (string, time.Time, error) {
		if calls.Add(1) > 1 {
			select { // hold the refresh so only the first wait is seen
			case <-gate:
			case <-ctx.Done():
			}
		}
		return "test-read-token", time.Now().Add(20 * time.Minute), nil
	})
	f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
	if f == nil {
		t.Fatal("no token file")
	}
	defer f.Stop()
	defer close(gate)
	if d := nextWait(t, waits); d > 5*time.Minute+5*time.Second || d < 4*time.Minute {
		t.Errorf("refresh of a token that expires in 20 minutes after %v, want about 5m", d)
	}
}

// TestAgentTokenFileRefreshErrorWaitsGrow: after each failed refresh the wait
// doubles from 30 seconds up to 5 minutes; a success resets it.
func TestAgentTokenFileRefreshErrorWaitsGrow(t *testing.T) {
	tokenTestRunAs(t, true)
	waits := recordAgentTokenWaits(t)
	var calls atomic.Int32
	setAgentReadTokenProviderForTest(t, func(context.Context, string) (string, time.Time, error) {
		n := calls.Add(1)
		if n == 1 || n == 9 {
			return "test-read-token", time.Now().Add(time.Hour), nil
		}
		return "", time.Time{}, errors.New("test-provider-failure")
	})
	f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
	if f == nil {
		t.Fatal("no token file")
	}
	defer f.Stop()
	if d := nextWait(t, waits); d < 40*time.Minute {
		t.Errorf("first wait %v, want the wait to the margin", d)
	}
	for i, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		if d := nextWait(t, waits); d != want {
			t.Errorf("wait after %d provider errors = %v, want %v", i+1, d, want)
		}
	}
	if d := nextWait(t, waits); d < 40*time.Minute {
		t.Errorf("wait after a success = %v, want the wait to the margin", d)
	}
}

// TestAgentTokenFileStopDuringProviderCall: Stop cancels the context of a
// provider call in flight and returns within 3 seconds, also when the
// provider does not return.
func TestAgentTokenFileStopDuringProviderCall(t *testing.T) {
	for _, honorsCtx := range []bool{true, false} {
		tokenTestRunAs(t, true)
		recordAgentTokenWaits(t)
		release := make(chan struct{})
		inFlight := make(chan struct{})
		cancelled := make(chan struct{})
		var calls atomic.Int32
		setAgentReadTokenProviderForTest(t, func(ctx context.Context, _ string) (string, time.Time, error) {
			if calls.Add(1) == 1 {
				return "test-read-token", time.Now().Add(time.Hour), nil
			}
			close(inFlight)
			if honorsCtx {
				<-ctx.Done()
				close(cancelled)
				return "", time.Time{}, ctx.Err()
			}
			<-release
			return "", time.Time{}, errors.New("test-provider-failure")
		})
		f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
		if f == nil {
			t.Fatal("no token file")
		}
		select {
		case <-inFlight:
		case <-time.After(5 * time.Second):
			t.Fatal("no provider call of the refresh")
		}
		start := time.Now()
		f.Stop()
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("honors ctx %v: Stop returned after %v, want within 3s", honorsCtx, d)
		}
		if honorsCtx {
			select {
			case <-cancelled:
			default:
				t.Error("Stop did not cancel the context of the provider call")
			}
		}
		if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
			t.Errorf("honors ctx %v: token dir still there after Stop: %v", honorsCtx, err)
		}
		close(release)
	}
}

// TestAgentTokenFileCutError: the cut counts characters, not bytes.
func TestAgentTokenFileCutError(t *testing.T) {
	short := errors.New("short")
	if got := cutAgentTokenError(short); got != "short" {
		t.Errorf("cut(short) = %q", got)
	}
	long := errors.New(strings.Repeat("é", 300))
	if got := cutAgentTokenError(long); len([]rune(got)) != 200 {
		t.Errorf("cut(long) has %d characters, want 200", len([]rune(got)))
	}
}
