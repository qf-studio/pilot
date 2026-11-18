package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// TASK-651: the secret gate scans the header lines of each commit object
// (author, committer, encoding, extra headers), not only the message (%B) and
// the diff. Option empty: the push of the fixture runs the gate in the
// repository the session wrote.

// commitAs commits file on the branch of f with the given author name.
func (f *secretGateFixture) commitAs(author, file, content, msg string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.local, file), []byte(content), 0o644); err != nil { //nolint:gosec // test file
		f.t.Fatal(err)
	}
	f.run("add", file)
	cmd := exec.Command("git", "commit", "-q", "-m", msg)
	cmd.Dir = f.local
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+author, "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("commit: %v\n%s", err, out)
	}
}

// commitWithExtraHeader writes a commit object on top of HEAD with the tree of
// HEAD and the header line extra after the committer (git hash-object -t
// commit -w) and moves the branch to it.
func (f *secretGateFixture) commitWithExtraHeader(extra string) string {
	f.t.Helper()
	tree := f.run("rev-parse", "HEAD^{tree}")
	parent := f.run("rev-parse", "HEAD")
	obj := "tree " + tree + "\nparent " + parent +
		"\nauthor t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
		extra + "\n\ndocs: a commit with an extra header\n"
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = f.local
	cmd.Stdin = strings.NewReader(obj)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("hash-object: %v\n%s", err, out)
	}
	sha := strings.TrimSpace(string(out))
	f.run("update-ref", "refs/heads/pilot/GH-9", sha)
	f.run("reset", "-q", "--hard", sha)
	return sha
}

// assertHeaderRefusal checks a refusal of the gate for the header line label
// (a known header name or "line N") of the commit sha: the SHA and the label
// in the error, never the value, no push, one alert.
func (f *secretGateFixture) assertHeaderRefusal(err error, sha, name string) {
	f.t.Helper()
	if !IsPushRefused(err) {
		f.t.Fatalf("push of a secret in the %s header: %v, want a refusal", name, err)
	}
	var pre *secretpatterns.PushRangeError
	if !errors.As(err, &pre) {
		f.t.Fatalf("not a secret hit: %v", err)
	}
	if pre.File != secretpatterns.CommitHeaderFile(name) || !strings.HasPrefix(sha, pre.Commit) {
		f.t.Errorf("refusal names commit %s and %q, want %s and %q", pre.Commit, pre.File, sha[:12], secretpatterns.CommitHeaderFile(name))
	}
	if !strings.Contains(err.Error(), pre.Commit) || !strings.Contains(err.Error(), "header "+name) {
		f.t.Errorf("error text %q does not name the commit and the header", err)
	}
	if strings.Contains(err.Error(), secretGateValue) {
		f.t.Errorf("error holds the value: %v", err)
	}
	if strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		f.t.Error("the branch reached the remote")
	}
	if n := len(f.sink.events); n != 1 {
		f.t.Errorf("%d alerts, want 1", n)
	}
}

// mutation pin: scan %B only (no scanCommitHeader in readBatchObjects) -> this test fails
func TestSecretGate_RegisteredValueInAuthorNameRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commitAs("dev "+secretGateValue, "notes.md", "clean\n", "docs: notes")
	sha := f.run("rev-parse", "HEAD")

	err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
	f.assertHeaderRefusal(err, sha, "author")
}

func TestSecretGate_RegisteredValueInExtraHeaderRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	sha := f.commitWithExtraHeader("x-note " + secretGateValue)

	err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
	f.assertHeaderRefusal(err, sha, "line 5") // an extra header name is not printed
}

func TestSecretGate_ValueInMultiLineHeaderRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	sha := f.commitWithExtraHeader("x-block first line\n second " + secretGateValue + "\n third")

	err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
	f.assertHeaderRefusal(err, sha, "line 5")
}

func TestSecretGate_CleanCommitHeadersPass(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commitAs("A Developer", "notes.md", "clean\n", "docs: notes")
	f.commitWithExtraHeader("x-note a clean value\n with a second line")

	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("push of commits with clean headers: %v", err)
	}
	if !strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Error("the branch did not reach the remote")
	}
	if n := len(f.sink.events); n != 0 {
		t.Errorf("%d alerts for clean headers, want 0", n)
	}
}

// The amend of the co-author strip runs no clean filter of the repository: a
// filter that writes a marker file would run as the daemon user.
func TestSecretGate_CoAuthorAmendRunsNoCleanFilter(t *testing.T) {
	f := newSecretGateFixture(t)
	f.commit(".gitattributes", "*.txt filter=mark\n", "chore: attributes")
	f.commit("data.txt", "data\n", "feat: data\n\n"+claudeCoAuthorTrailer)
	marker := filepath.Join(t.TempDir(), "clean-filter-ran")
	f.run("config", "filter.mark.clean", "touch '"+marker+"'; cat")
	f.run("config", "filter.mark.smudge", "cat")
	// A changed stat of the tracked file: a refresh of the index would run the
	// filter to hash it again.
	if err := os.WriteFile(filepath.Join(f.local, "data.txt"), []byte("data\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	later := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(f.local, "data.txt"), later, later); err != nil {
		t.Fatal(err)
	}

	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if msgs := f.branchMessages(); strings.Contains(msgs, "anthropic.com") {
		t.Fatalf("the amend did not strip the trailer:\n%s", msgs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the clean filter of the repository ran in the amend of the trailer strip")
	}
}
