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

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
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

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
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
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("clean range: %v", err)
	}
}

func TestCheckPushRange_SecretInCommitMessage(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.commit("a.txt", "x\n", "feat: a\n\nbecause "+secret)

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
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

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
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

	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("secret only in base: %v", err)
	}
}

func TestCheckPushRange_AllowlistedFileSkipsShapesNotValues(t *testing.T) {
	f := newPushFixture(t)
	f.commit("internal/testutil/tokens.go", "// "+telegramShaped()+"\n", "docs: example")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("shape in an allowlisted file: %v", err)
	}

	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.commit("internal/testutil/tokens.go", "// "+secret+"\n", "docs: real value")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err == nil {
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
	if err := CheckPush(context.Background(), dir, "HEAD", PushOptions{}); err == nil {
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

// newTrunkFixture is a work tree whose default branch is "trunk" and whose
// clone has no origin/HEAD (the remote was added, not cloned). trunk carries an
// old commit with a secret that was already pushed.
func newTrunkFixture(t *testing.T, oldSecret string) *pushFixture {
	t.Helper()
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	root := t.TempDir()
	f := &pushFixture{t: t, local: filepath.Join(root, "local"), remote: filepath.Join(root, "remote.git")}
	if out, err := exec.Command("git", "init", "--bare", "-b", "trunk", f.remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	if err := os.MkdirAll(f.local, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git("init", "-b", "trunk")
	f.git("remote", "add", "origin", f.remote)
	f.commit("README.md", "hello\n", "init")
	f.commit("old.md", oldSecret+"\n", "docs: old")
	f.git("push", "-u", "origin", "trunk")
	f.git("checkout", "-b", "pilot/GH-1")
	return f
}

// GH-6272: a repo whose default branch is not main/master and that has no
// origin/HEAD resolves its base from the configured default branch.
func TestCheckPushRange_ConfiguredDefaultBranchBase(t *testing.T) {
	const old = "hunter2-not-a-known-shape"
	f := newTrunkFixture(t, old)
	f.commit("new.go", "package x\n", "feat: new")
	want := f.git("rev-parse", "origin/trunk")

	if got := PushBase(context.Background(), f.local, "pilot/GH-1", "trunk"); got != want {
		t.Fatalf("PushBase with default branch trunk = %q, want %q", got, want)
	}
	if got := PushBase(context.Background(), f.local, "pilot/GH-1", ""); got != "" {
		t.Errorf("PushBase without a default branch resolved %q; origin/main, origin/master and origin/HEAD do not exist", got)
	}

	RegisterSecretValues(old)
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{DefaultBranch: "trunk"}); err != nil {
		t.Fatalf("old pushed commit must not be scanned: %v", err)
	}
	f.commit("new.md", "token "+old+"\n", "docs: new")
	var pre *PushRangeError
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{DefaultBranch: "trunk"}); !errors.As(err, &pre) || pre.File != "new.md" {
		t.Fatalf("new commit with the value: got %v, want a hit in new.md", err)
	}
}

// With no base at all the gate scans only the commits that are on no remote
// ref, instead of the whole history.
func TestCheckPushRange_NoBaseScansOnlyUnpushedCommits(t *testing.T) {
	const old = "hunter2-not-a-known-shape"
	f := newTrunkFixture(t, old)
	RegisterSecretValues(old)
	f.commit("new.go", "package x\n", "feat: new")

	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("no base, old pushed secret: got %v, want only the new commit scanned", err)
	}
	f.commit("new.md", "token "+old+"\n", "docs: new")
	var pre *PushRangeError
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); !errors.As(err, &pre) || pre.File != "new.md" {
		t.Fatalf("got %v, want a hit in new.md", err)
	}
	// Everything already on a remote ref: nothing to publish.
	f.git("checkout", "trunk")
	if err := CheckPush(context.Background(), f.local, "trunk", PushOptions{}); err != nil {
		t.Fatalf("head on a remote ref: %v", err)
	}
}

// When the base cannot be computed the refusal says so and is not a secret hit.
func TestCheckPushRange_BaseUncomputableRefusesWithoutSecretAlert(t *testing.T) {
	f := newTrunkFixture(t, "x")
	err := CheckPush(context.Background(), f.local, "no-such-ref", PushOptions{})
	var scan *PushScanError
	if !errors.As(err, &scan) {
		t.Fatalf("got %T %v, want *PushScanError", err, err)
	}
	var pre *PushRangeError
	if errors.As(err, &pre) {
		t.Error("a base failure must not look like a secret hit")
	}
	if !errors.Is(err, ErrPushRefused) || !strings.Contains(err.Error(), "base could not be computed") {
		t.Errorf("error %q must be a refusal naming the base", err)
	}
}

// An added line "++ <token>" appears in the diff as "+++ <token>"; it is a
// line of the file, not a file header.
func TestCheckPushRange_AddedPlusPlusLineIsScanned(t *testing.T) {
	f := newPushFixture(t)
	tok := telegramShaped()
	f.commit("notes.md", "first\n++ "+tok+"\n", "docs: notes")

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want *PushRangeError", err)
	}
	if pre.File != "notes.md" {
		t.Errorf("file %q, want notes.md (the added line must not rename the file)", pre.File)
	}
	if strings.Contains(err.Error(), tok) {
		t.Errorf("error holds the token: %v", err)
	}
}

// The "++ " line must not rename the file for the allowlist either: a shape in
// an allowlisted file stays allowed, a shape in another file is not excused by
// a "++ tokens.go" line.
func TestCheckPushRange_PlusPlusLineDoesNotBorrowAllowlistedName(t *testing.T) {
	f := newPushFixture(t)
	f.commit("notes.md", "++ b/internal/testutil/tokens.go\nbot: "+telegramShaped()+"\n", "docs: notes")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err == nil {
		t.Fatal("a '++ b/<allowlisted path>' line excused a shape in another file")
	}
}

func TestCheckPushRange_BinaryFileIsScanned(t *testing.T) {
	f := newPushFixture(t)
	f.commit("blob.bin", "\x00\x01\x02 bot: "+telegramShaped()+" \x00\n", "chore: blob")
	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	var pre *PushRangeError
	if !errors.As(err, &pre) || pre.File != "blob.bin" {
		t.Fatalf("got %v, want a hit in blob.bin", err)
	}
}

func TestCheckPushRange_OversizedCommitRefused(t *testing.T) {
	old := maxCommitScanBytes
	maxCommitScanBytes = 1 << 10
	t.Cleanup(func() { maxCommitScanBytes = old })

	f := newPushFixture(t)
	f.commit("big.txt", strings.Repeat("0123456789abcdef\n", 400), "chore: big") // 6.8 KB: over the per-commit cap, under 5x
	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	var scan *PushScanError
	if !errors.As(err, &scan) || !errors.Is(err, ErrPushRefused) {
		t.Fatalf("got %v, want a *PushScanError refusal", err)
	}
	if !strings.Contains(err.Error(), "too large to scan") {
		t.Errorf("error %q must name the size", err)
	}
	// Above the net-diff window as well.
	f.commit("bigger.txt", strings.Repeat("0123456789abcdef\n", 2000), "chore: bigger")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); !errors.Is(err, ErrPushRefused) {
		t.Fatalf("net diff over the cap: got %v", err)
	}
}

func TestCheckPushRange_AllowPathsGlob(t *testing.T) {
	f := newPushFixture(t)
	tok := telegramShaped()
	f.commit("fixtures/deep/token.txt", "bot: "+tok+"\n", "test: fixture")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err == nil {
		t.Fatal("shape in fixtures/ without allow_paths must be refused")
	}
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{AllowPaths: []string{"fixtures/**"}}); err != nil {
		t.Fatalf("shape in an allow_paths file: %v", err)
	}
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.commit("fixtures/deep/real.txt", secret+"\n", "test: real")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{AllowPaths: []string{"fixtures/**"}}); err == nil {
		t.Fatal("a registered value is refused even in an allow_paths file")
	}
}

func TestCheckPushRange_MatchPathGlob(t *testing.T) {
	tests := []struct {
		pattern, file string
		want          bool
	}{
		{"fixtures/**", "fixtures/a/b.txt", true},
		{"fixtures/**", "other/fixtures/a.txt", false},
		{"**/testdata/*.json", "a/b/testdata/x.json", true},
		{"**/testdata/*.json", "testdata/x.json", true},
		{"**/testdata/*.json", "testdata/sub/x.json", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/sub/a.md", false},
		{"tokens.go", "internal/testutil/tokens.go", true},
		{"*.example", "deploy/app.example", true},
		{"./docs/a.md", "docs/a.md", true},
		{"", "docs/a.md", false},
	}
	for _, tt := range tests {
		if got := MatchPathGlob(tt.pattern, tt.file); got != tt.want {
			t.Errorf("MatchPathGlob(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
		}
	}
}

// mutation pin: the no-base fallback diffs from the oldest unpushed commit's parent and refuses a remote branch's old secret that a merge brought in -> this test fails
func TestCheckPushRange_NoBaseMergeOfPushedBranchIsNotNew(t *testing.T) {
	const old = "hunter2-not-a-known-shape"
	f := newTrunkFixture(t, "x")
	f.git("checkout", "-b", "feat-b")
	f.commit("b.md", "token "+old+"\n", "docs: b")
	f.git("push", "-u", "origin", "feat-b")
	f.git("checkout", "pilot/GH-1")
	f.commit("new.go", "package x\n", "feat: new")
	f.git("merge", "--no-ff", "feat-b", "-m", "merge feat-b")
	RegisterSecretValues(old)

	if got := PushBase(context.Background(), f.local, "pilot/GH-1", ""); got != "" {
		t.Fatalf("setup: a base resolved (%q); the fallback is not under test", got)
	}
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("secret of an already pushed branch, merged in: got %v, want it not refused as new", err)
	}

	// A secret the merge itself introduces (a conflict resolution) is still refused.
	f.git("checkout", "-b", "pilot/GH-2", "trunk")
	f.commit("c.txt", "one\n", "feat: c")
	f.git("checkout", "-b", "side", "trunk")
	f.commit("c.txt", "two\n", "feat: c side")
	f.git("checkout", "pilot/GH-2")
	f.commit("c.txt", "three\n", "feat: c head")
	if out, err := exec.Command("git", "-C", f.local, "merge", "side").CombinedOutput(); err == nil {
		t.Fatalf("setup: merge did not conflict: %s", out)
	}
	if err := os.WriteFile(filepath.Join(f.local, "c.txt"), []byte("token "+old+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git("add", "c.txt")
	f.git("commit", "-m", "merge side")
	var pre *PushRangeError
	if err := CheckPush(context.Background(), f.local, "pilot/GH-2", PushOptions{}); !errors.As(err, &pre) || pre.File != "c.txt" {
		t.Fatalf("secret in a merge's conflict resolution: got %v, want a hit in c.txt", err)
	}
}
