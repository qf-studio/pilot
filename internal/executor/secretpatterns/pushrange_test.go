package secretpatterns

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushFixture is a work tree with a bare remote, main already pushed.
type pushFixture struct {
	t      *testing.T
	local  string
	remote string
}

func (f *pushFixture) git(args ...string) string {
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

func (f *pushFixture) commit(file, content, msg string) {
	f.t.Helper()
	path := filepath.Join(f.local, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", file)
	f.git("commit", "-m", msg)
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	root := t.TempDir()
	f := &pushFixture{t: t, local: filepath.Join(root, "local"), remote: filepath.Join(root, "remote.git")}
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", f.remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	if err := os.MkdirAll(f.local, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git("init", "-b", "main")
	f.git("remote", "add", "origin", f.remote)
	f.commit("README.md", "hello\n", "init")
	f.git("push", "-u", "origin", "main")
	f.git("checkout", "-b", "pilot/GH-1")
	return f
}

// telegramShaped builds a bot-token-shaped string at run time, so this file
// holds no literal that scripts/check-secret-patterns.sh would flag.
func telegramShaped() string {
	return "1234567890" + ":AA" + strings.Repeat("b", 33)
}

func TestCheckPushRange_RegisteredValueRefused(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.commit("docs/notes.md", "token = "+secret+"\n", "docs: notes")

	err := CheckPush(context.Background(), f.local, "pilot/GH-1")
	if err == nil {
		t.Fatal("registered value in a new commit: got nil error")
	}
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %T %v, want *PushRangeError", err, err)
	}
	if !errors.Is(err, ErrPushRefused) {
		t.Error("error does not match ErrPushRefused")
	}
	if pre.File != "docs/notes.md" || pre.Kind != KindConfiguredValue {
		t.Errorf("got file %q kind %q", pre.File, pre.Kind)
	}
	if want := f.git("rev-parse", "--short=12", "HEAD"); pre.Commit != want {
		t.Errorf("commit %q, want %q", pre.Commit, want)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error holds the value: %v", err)
	}
	if want := "push refused: commit " + pre.Commit + " adds a secret value in docs/notes.md"; err.Error() != want {
		t.Errorf("error %q, want %q", err.Error(), want)
	}
}

func TestCheckPushRange_ShapeRefused(t *testing.T) {
	f := newPushFixture(t)
	tok := telegramShaped()
	f.commit(".agent/system/notes.md", "bot: "+tok+"\n", "docs: notes")

	err := CheckPush(context.Background(), f.local, "pilot/GH-1")
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want *PushRangeError", err)
	}
	if pre.File != ".agent/system/notes.md" {
		t.Errorf("file %q", pre.File)
	}
	if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), tok[:10]) {
		t.Errorf("error holds the token: %v", err)
	}
}

func TestCheckPushRange_CleanRangePasses(t *testing.T) {
	f := newPushFixture(t)
	RegisterSecretValues("hunter2-not-a-known-shape")
	f.commit("main.go", "package main\n", "feat: code")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1"); err != nil {
		t.Fatalf("clean range: %v", err)
	}
}

func TestCheckPushRange_SecretInCommitMessage(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.commit("a.txt", "x\n", "feat: a\n\nbecause "+secret)

	err := CheckPush(context.Background(), f.local, "pilot/GH-1")
	var pre *PushRangeError
	if !errors.As(err, &pre) || pre.File != CommitMessageFile {
		t.Fatalf("got %v, want a commit-message hit", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error holds the value: %v", err)
	}
}

// A token committed and removed by a later commit is gone from the net diff,
// but the first commit still reaches the remote.
func TestCheckPushRange_AddedThenRemovedStillRefused(t *testing.T) {
	f := newPushFixture(t)
	f.commit("notes.md", "bot: "+telegramShaped()+"\n", "docs: notes")
	first := f.git("rev-parse", "--short=12", "HEAD")
	f.commit("notes.md", "bot: redacted\n", "fix: remove token")

	err := CheckPush(context.Background(), f.local, "pilot/GH-1")
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want *PushRangeError", err)
	}
	if pre.Commit != first {
		t.Errorf("commit %q, want the adding commit %q", pre.Commit, first)
	}
}

// A secret already in base is not this push's doing.
func TestCheckPushRange_SecretOnlyInBaseIgnored(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	f.git("checkout", "main")
	f.commit("old.md", secret+"\n", "docs: old")
	f.git("push", "origin", "main")
	f.git("checkout", "pilot/GH-1")
	f.git("rebase", "main")
	f.commit("new.go", "package x\n", "feat: new")
	RegisterSecretValues(secret)

	if err := CheckPush(context.Background(), f.local, "pilot/GH-1"); err != nil {
		t.Fatalf("secret only in base: %v", err)
	}
}

func TestCheckPushRange_AllowlistedFileSkipsShapesNotValues(t *testing.T) {
	f := newPushFixture(t)
	f.commit("internal/testutil/tokens.go", "// "+telegramShaped()+"\n", "docs: example")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1"); err != nil {
		t.Fatalf("shape in an allowlisted file: %v", err)
	}

	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.commit("internal/testutil/tokens.go", "// "+secret+"\n", "docs: real value")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1"); err == nil {
		t.Fatal("registered value in an allowlisted file must still be refused")
	}
}

func TestCheckPushRange_NoRemoteScansWholeHistory(t *testing.T) {
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	dir := t.TempDir()
	f := &pushFixture{t: t, local: dir}
	f.git("init", "-b", "main")
	f.commit("a.md", "bot: "+telegramShaped()+"\n", "init")
	if err := CheckPush(context.Background(), dir, "HEAD"); err == nil {
		t.Fatal("no remote refs: want the whole history scanned")
	}
}

func TestCheckPushRange_BadRefFailsClosed(t *testing.T) {
	f := newPushFixture(t)
	err := CheckPushRange(context.Background(), f.local, "no-such-ref", "HEAD")
	if err == nil || errors.Is(err, ErrPushRefused) {
		t.Fatalf("unresolvable base: got %v, want a plain error", err)
	}
}
