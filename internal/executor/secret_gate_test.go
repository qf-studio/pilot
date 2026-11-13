package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

type secretGateFixture struct {
	t      *testing.T
	local  string
	remote string
	git    *GitOperations
	sink   *fakeAlertProcessor
}

func (f *secretGateFixture) run(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.local
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *secretGateFixture) commit(file, content, msg string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.local, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.run("add", file)
	f.run("commit", "-m", msg)
}

// remoteBranches lists the branches the bare remote holds.
func (f *secretGateFixture) remoteBranches() string {
	f.t.Helper()
	out, err := exec.Command("git", "-C", f.remote, "branch", "--list").CombinedOutput()
	if err != nil {
		f.t.Fatalf("list remote branches: %v\n%s", err, out)
	}
	return string(out)
}

func newSecretGateFixture(t *testing.T) *secretGateFixture {
	t.Helper()
	secretpatterns.ResetSecretValuesForTest()
	resetAlertedCommits()
	sink := &fakeAlertProcessor{}
	SetSecretGateAlertProcessor(sink)
	t.Cleanup(func() {
		secretpatterns.ResetSecretValuesForTest()
		SetSecretGateAlertProcessor(nil)
	})

	root := t.TempDir()
	f := &secretGateFixture{t: t, local: filepath.Join(root, "local"), remote: filepath.Join(root, "remote.git"), sink: sink}
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", f.remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	if err := os.MkdirAll(f.local, 0o755); err != nil {
		t.Fatal(err)
	}
	f.run("init", "-b", "main")
	// The daemon's git commands (the co-author amend) inherit the test process's
	// environment, not f.run's identity; a CI runner has no git identity of its
	// own, so the repository carries one (GH-6358).
	f.run("config", "user.name", "t")
	f.run("config", "user.email", "t@example.com")
	f.run("remote", "add", "origin", f.remote)
	f.commit("README.md", "hello\n", "init")
	f.run("push", "-u", "origin", "main")
	f.run("checkout", "-b", "pilot/GH-9")
	f.git = NewGitOperations(f.local)
	return f
}

const secretGateValue = "hunter2-not-a-known-shape"

func TestSecretGate_PushRefusedForRegisteredValue(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")

	err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
	if err == nil {
		t.Fatal("push of a registered value succeeded")
	}
	if !IsPushRefused(err) {
		t.Fatalf("error is not a push refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "push refused: commit ") || !strings.Contains(err.Error(), "adds a secret value in notes.md") {
		t.Errorf("unexpected error text: %v", err)
	}
	if strings.Contains(err.Error(), secretGateValue) {
		t.Errorf("error holds the value: %v", err)
	}
	if strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Errorf("branch reached the remote:\n%s", f.remoteBranches())
	}

	if len(f.sink.events) != 1 {
		t.Fatalf("got %d alert events, want 1", len(f.sink.events))
	}
	ev := f.sink.events[0]
	if ev.Type != AlertEventTypeSecurityEvent || ev.Source != SecurityEventSourceSecretGate {
		t.Errorf("event type %q source %q", ev.Type, ev.Source)
	}
	if ev.Error != err.Error() {
		t.Errorf("alert text %q differs from the error %q", ev.Error, err.Error())
	}
	for k, v := range ev.Metadata {
		if strings.Contains(v, secretGateValue) {
			t.Errorf("metadata %s holds the value", k)
		}
	}
}

func TestSecretGate_PushRefusedForShape(t *testing.T) {
	f := newSecretGateFixture(t)
	tok := "1234567890" + ":AA" + strings.Repeat("c", 33)
	f.commit("notes.md", "bot "+tok+"\n", "docs: notes")

	err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
	if !IsPushRefused(err) {
		t.Fatalf("got %v, want a push refusal", err)
	}
	if strings.Contains(err.Error(), tok) {
		t.Errorf("error holds the token: %v", err)
	}
	if strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Error("branch reached the remote")
	}
}

func TestSecretGate_CleanPushGoesThrough(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("main.go", "package main\n", "feat: code")

	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("clean push: %v", err)
	}
	if !strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Error("clean branch did not reach the remote")
	}
	if len(f.sink.events) != 0 {
		t.Errorf("clean push raised %d alerts", len(f.sink.events))
	}
}

func TestSecretGate_LeasePushAndPushToMainRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("notes.md", secretGateValue+"\n", "docs: notes")

	if err := f.git.PushForceWithLease(context.Background(), "pilot/GH-9", strings.Repeat("0", 40), "GH-9"); !IsPushRefused(err) {
		t.Errorf("lease push: got %v, want a push refusal", err)
	}
	f.run("checkout", "main")
	f.run("merge", "--ff-only", "pilot/GH-9")
	if err := f.git.PushToMain(context.Background(), "GH-9"); !IsPushRefused(err) {
		t.Errorf("default-branch push: got %v, want a push refusal", err)
	}
	if got := f.run("--git-dir", f.remote, "rev-list", "--count", "main"); got != "1" {
		t.Errorf("remote main has %s commits, want 1", got)
	}
}

// The retry loop must give up on the first refusal.
func TestSecretGate_PushBranchWithRetryDoesNotRetry(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("notes.md", secretGateValue+"\n", "docs: notes")

	r := &Runner{}
	task := &Task{ID: "GH-9", Branch: "pilot/GH-9"}
	err := r.pushBranchWithRetry(context.Background(), f.git, task, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !IsPushRefused(err) {
		t.Fatalf("got %v, want a push refusal", err)
	}
	if len(f.sink.events) != 1 {
		t.Errorf("gate ran %d times, want 1 (no retry)", len(f.sink.events))
	}
}

func TestSecretGate_CreatePRMasksTitleAndBody(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)

	fakeBin := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "gh-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\necho https://github.com/o/r/pull/1\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	url, err := f.git.CreatePR(context.Background(), "feat(x): add "+secretGateValue, "evidence: "+secretGateValue+" done", "main")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if url == "" {
		t.Error("no PR URL")
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), secretGateValue) {
		t.Errorf("gh received the value:\n%s", got)
	}
	if !strings.Contains(string(got), secretpatterns.Redacted) {
		t.Errorf("gh args hold no mask:\n%s", got)
	}
}

// lateRepo registers gate settings for a project path that is not a git
// repository yet, then makes it one (a clone or init that finishes after the
// config load). The settings are { DefaultBranch: "trunk", AllowPaths: fixtures/** }.
func lateRepo(t *testing.T) string {
	t.Helper()
	ResetSecretGateConfigForTest()
	t.Cleanup(ResetSecretGateConfigForTest)
	dir := t.TempDir()
	SetSecretGateProject(dir, SecretGateProject{DefaultBranch: "trunk", AllowPaths: []string{"fixtures/**"}})

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "trunk", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	run("init", "-b", "trunk")
	run("remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	run("push", "-u", "origin", "trunk")
	run("checkout", "-b", "pilot/GH-9")
	return dir
}

// mutation pin: CheckSecretGate reads the project's settings from the process-wide registry, which drops a path that was not a git repository at load time, so its allow_paths never apply -> this test fails
func TestSecretGate_ProjectRegisteredBeforeRepoExistsKeepsAllowPaths(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	dir := lateRepo(t)
	shaped := "1234567890" + ":AA" + strings.Repeat("d", 33)
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixtures", "x.txt"), []byte(shaped+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "fixtures/x.txt"}, {"commit", "-m", "test: fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if err := CheckSecretGate(context.Background(), dir, "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("a secret shape in an allow_paths file must pass: %v", err)
	}
}

// mutation pin: project gate settings registered before the path is a git repository are dropped with no log, so the default branch never applies -> this test fails
func TestSecretGate_LateRepositoryDefaultBranchReachesWorktrees(t *testing.T) {
	var warned int
	prev := slog.Default()
	slog.SetDefault(slog.New(countingHandler{msg: "Secret gate: project path is not a git repository yet; its settings apply once it is", n: &warned}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	dir := lateRepo(t)
	if warned != 1 {
		t.Errorf("registering a non-repository path logged %d warnings, want 1", warned)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "worktree", "add", "-b", "pilot/GH-10", wt, "pilot/GH-9")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	opts := secretGateOptions(context.Background(), wt)
	if opts.DefaultBranch != "trunk" {
		t.Errorf("DefaultBranch = %q for a worktree of the project, want %q", opts.DefaultBranch, "trunk")
	}
	if len(opts.AllowPaths) != 1 || opts.AllowPaths[0] != "fixtures/**" {
		t.Errorf("AllowPaths = %v, want [fixtures/**]", opts.AllowPaths)
	}
	// Once resolved the entry is keyed by the repository; nothing is left pending.
	secretGateProjMu.RLock()
	pending := len(secretGatePending)
	secretGateProjMu.RUnlock()
	if pending != 0 {
		t.Errorf("%d entries still pending after the repository resolved", pending)
	}
}
