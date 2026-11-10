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
	if err := f.git.PushToMain(context.Background()); !IsPushRefused(err) {
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
