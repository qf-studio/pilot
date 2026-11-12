package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-6334: five ways to publish a secret past the push gate through the
// repository the session can write (a textconv driver, log.showRoot, a shadow of
// the branch name, push.followTags, a secret in a file path). Each test pushes
// through Push on real temporary repositories and ends with nothing new on the
// bare remote.

// remoteState is the bare remote's refs and commit count: a push that changes
// neither published nothing.
func (f *secretGateFixture) remoteState() string {
	f.t.Helper()
	refs, err := exec.Command("git", "-C", f.remote, "for-each-ref").CombinedOutput()
	if err != nil {
		f.t.Fatalf("for-each-ref: %v\n%s", err, refs)
	}
	count, err := exec.Command("git", "-C", f.remote, "rev-list", "--all", "--count").CombinedOutput()
	if err != nil {
		f.t.Fatalf("rev-list: %v\n%s", err, count)
	}
	return string(refs) + "commits=" + string(count)
}

// assertPushRefusedNothingPublished is pushRefusedNothingPublished for a test
// that does not read the error.
func (f *secretGateFixture) assertPushRefusedNothingPublished(before string) {
	f.t.Helper()
	_ = f.pushRefusedNothingPublished(before)
}

// pushRefusedNothingPublished pushes pilot/GH-9 and requires a refusal that
// left the remote as it was; it returns the refusal.
func (f *secretGateFixture) pushRefusedNothingPublished(before string) error {
	f.t.Helper()
	err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
	if err == nil {
		f.t.Fatal("push succeeded, want a refusal")
	}
	if !IsPushRefused(err) {
		f.t.Fatalf("error is not a push refusal: %v", err)
	}
	if after := f.remoteState(); after != before {
		f.t.Errorf("the remote changed after a refused push:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	return err
}

// textconvDriver writes a textconv script that prints clean text and touches a
// marker file, and registers it as the driver diff.pilot.
func (f *secretGateFixture) textconvDriver() (marker string) {
	f.t.Helper()
	dir := f.t.TempDir()
	marker = filepath.Join(dir, "textconv-ran")
	script := filepath.Join(dir, "conv.sh")
	body := "#!/bin/sh\ntouch " + marker + "\necho clean\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.run("config", "diff.pilot.textconv", script)
	return marker
}

func (f *secretGateFixture) gitDir() string {
	f.t.Helper()
	return filepath.Join(f.local, f.run("rev-parse", "--git-dir"))
}

// mutation pin: remove --no-textconv -> this test fails
func TestSecretGate_TextconvDriverInInfoAttributesIsIgnored(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.textconvDriver()
	if err := os.WriteFile(filepath.Join(f.gitDir(), "info", "attributes"), []byte("*.md diff=pilot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
}

// mutation pin: remove --no-textconv -> this test fails
func TestSecretGate_TextconvDriverInCommittedGitattributesIsIgnored(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.textconvDriver()
	f.commit(".gitattributes", "*.md diff=pilot\n", "chore: attributes")
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
}

// mutation pin: remove --no-textconv -> this test fails
func TestSecretGate_TextconvDriverInAttributesFileConfigIsIgnored(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.textconvDriver()
	attrs := filepath.Join(t.TempDir(), "attrs")
	if err := os.WriteFile(attrs, []byte("*.md diff=pilot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run("config", "core.attributesFile", attrs)
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
}

// mutation pin: remove --no-textconv -> this test fails
func TestSecretGate_TextconvCommandDoesNotRun(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	marker := f.textconvDriver()
	if err := os.WriteFile(filepath.Join(f.gitDir(), "info", "attributes"), []byte("*.md diff=pilot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the textconv command ran: git executed a command the session chose")
	}
}

// mutation pin: remove the log.showRoot argument -> this test fails
func TestSecretGate_OrphanRootCommitIsShownWhenShowRootIsFalse(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.run("config", "log.showRoot", "false")
	// An orphan root commit that holds the secret, merged, then deleted by a later commit.
	f.run("checkout", "--orphan", "orphan")
	f.run("rm", "-rf", "-q", ".")
	f.commit("root.md", "token = "+secretGateValue+"\n", "docs: root")
	f.run("checkout", "pilot/GH-9")
	f.run("merge", "--allow-unrelated-histories", "--no-edit", "orphan")
	f.run("rm", "-q", "root.md")
	f.run("commit", "-m", "chore: remove root")
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
}

// mutation pin: scan the short branch name again -> this test fails
func TestSecretGate_ShadowOfBranchNameInGitDirIsNotScanned(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	// $GIT_DIR/pilot/GH-9 holding a clean SHA wins over refs/heads/pilot/GH-9 for a short name.
	clean := f.run("rev-parse", "main")
	shadow := filepath.Join(f.gitDir(), "pilot", "GH-9")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte(clean+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
}

// mutation pin: scan the short branch name again -> this test fails
func TestSecretGate_ScannedCommitIsTheCommitGitSends(t *testing.T) {
	f := newSecretGateFixture(t)
	f.commit("notes.md", "clean\n", "docs: notes")
	clean := f.run("rev-parse", "main")
	shadow := filepath.Join(f.gitDir(), "pilot", "GH-9")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte(clean+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var scannedHead string
	prev := checkPushForGate
	checkPushForGate = func(ctx context.Context, dir, head string, opts secretpatterns.PushOptions) error {
		scannedHead = head
		return prev(ctx, dir, head, opts)
	}
	t.Cleanup(func() { checkPushForGate = prev })

	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("clean push: %v", err)
	}
	branch := f.run("rev-parse", "refs/heads/pilot/GH-9")
	scanned := f.run("rev-parse", "--verify", "-q", scannedHead)
	if !strings.HasPrefix(scannedHead, "refs/heads/") {
		t.Errorf("the gate scans %q, want a full ref under refs/heads", scannedHead)
	}
	sent := strings.TrimSpace(mustGit(t, f.remote, "rev-parse", "refs/heads/pilot/GH-9"))
	if scanned != branch || sent != branch {
		t.Errorf("scanned %s, git sent %s, branch is %s: the gate and the push must agree", scanned, sent, branch)
	}
	if scanned == clean {
		t.Errorf("the gate scanned the shadow commit %s", clean)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// mutation pin: remove --no-follow-tags -> this test fails
func TestSecretGate_AnnotatedTagIsNotFollowedWhenFollowTagsIsTrue(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.run("config", "push.followTags", "true")
	f.commit("notes.md", "clean\n", "docs: notes")
	f.run("tag", "-a", "v1", "-m", "release "+secretGateValue)

	// The commits are clean, so the branch push itself passes.
	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("push of clean commits: %v", err)
	}
	if tags := mustGit(t, f.remote, "tag", "--list"); strings.TrimSpace(tags) != "" {
		t.Errorf("the annotated tag reached the remote: %q", tags)
	}
	if !strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Errorf("the branch did not reach the remote:\n%s", f.remoteBranches())
	}
}

// mutation pin: do not scan file paths -> this test fails
func TestSecretGate_SecretInFileNameAloneIsRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("token-"+secretGateValue+".txt", "clean\n", "docs: file")
	before := f.remoteState()

	err := f.pushRefusedNothingPublished(before)
	if strings.Contains(err.Error(), secretGateValue) {
		t.Errorf("error holds the value: %v", err)
	}
}

// mutation pin: print the path unmasked -> this test fails
func TestSecretGate_SecretInFileNameAndContentIsNotPrinted(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("token-"+secretGateValue+".md", "token = "+secretGateValue+"\n", "docs: file")
	before := f.remoteState()

	var logs bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	err := f.pushRefusedNothingPublished(before)
	if strings.Contains(err.Error(), secretGateValue) {
		t.Errorf("error holds the value: %v", err)
	}
	if strings.Contains(logs.String(), secretGateValue) {
		t.Errorf("log holds the value:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "Push refused by the secret gate") {
		t.Errorf("no refusal log line captured:\n%s", logs.String())
	}
	if len(f.sink.events) != 1 {
		t.Fatalf("got %d alert events, want 1", len(f.sink.events))
	}
	ev := f.sink.events[0]
	if strings.Contains(ev.Error, secretGateValue) {
		t.Errorf("alert text holds the value: %q", ev.Error)
	}
	for k, v := range ev.Metadata {
		if strings.Contains(v, secretGateValue) {
			t.Errorf("alert metadata %s holds the value", k)
		}
	}
}
