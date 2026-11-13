package secretpatterns

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// GH-6284: a cancelled or expired context is the caller giving up, not a
// secret refusal. Both routes through CheckPush (a base that resolves, and the
// fallback with no base) return the context error and never errors.Is
// ErrPushRefused.
func TestCheckPushRange_CancelledContextIsNotARefusal(t *testing.T) {
	f := newPushFixture(t)
	f.commit("new.go", "package x\n", "feat: new")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct{ name, head string }{
		{"head with a base", "pilot/GH-1"},
		{"unresolvable head, no base", "no-such-ref"},
	} {
		err := CheckPush(ctx, f.local, tc.head, PushOptions{})
		if err == nil {
			t.Fatalf("%s: got nil, want the context error", tc.name)
		}
		if errors.Is(err, ErrPushRefused) {
			t.Errorf("%s: a cancelled context became a refusal: %v", tc.name, err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: got %v, want context.Canceled", tc.name, err)
		}
	}
}

// shallowFixture is a depth-1 clone of a remote main that publishes old (a
// registered value) in its first commits. The clone holds a clean local commit
// on pilot/GH-1, and the remote then advanced and the clone fetched it at depth
// 1, so the local history no longer meets origin/main and PushBase finds nothing.
func shallowFixture(t *testing.T, old string) *pushFixture {
	t.Helper()
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	seed := &pushFixture{t: t, local: filepath.Join(root, "seed"), remote: remote}
	if err := os.MkdirAll(seed.local, 0o755); err != nil {
		t.Fatal(err)
	}
	seed.git("init", "-b", "main")
	seed.git("remote", "add", "origin", remote)
	seed.commit("README.md", "hello\n", "init")
	seed.commit("old.md", "token "+old+"\n", "docs: old, long published")
	seed.git("push", "-u", "origin", "main")
	RegisterSecretValues(old)

	f := &pushFixture{t: t, local: filepath.Join(root, "local"), remote: remote}
	if out, err := exec.Command("git", "clone", "--depth", "1", "file://"+remote, f.local).CombinedOutput(); err != nil {
		t.Fatalf("shallow clone: %v\n%s", err, out)
	}
	f.git("checkout", "-b", "pilot/GH-1")
	f.commit("new.go", "package x\n", "feat: clean")

	// The default branch advances, and the clone fetches it shallowly again.
	seed.commit("later.go", "package later\n", "feat: later")
	seed.git("push", "origin", "main")
	f.git("fetch", "--depth", "1", "origin", "main")

	if got := f.git("rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("setup: clone is not shallow (%s)", got)
	}
	if base := PushBase(context.Background(), f.local, "pilot/GH-1", "main"); base != "" {
		t.Fatalf("setup: PushBase = %q, want no merge base for the diverged shallow history", base)
	}
	return f
}

// GH-6284: a depth-1 clone whose remote default branch advanced. The oldest
// unpushed commit is the shallow boundary and its parent is missing, so the diff
// base must be the boundary itself and not the empty tree: a file published long
// ago must not raise a secret alert for a clean commit.
func TestCheckPushRange_ShallowBoundaryIsNotMeasuredFromEmptyTree(t *testing.T) {
	const old = "hunter2-not-a-known-shape"
	f := shallowFixture(t, old)
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{DefaultBranch: "main"}); err != nil {
		t.Fatalf("clean commit on a shallow clone: got %v, want no refusal", err)
	}

	// A secret in the unpushed commit is still found.
	f.commit("leak.md", "token "+old+"\n", "docs: leak")
	var pre *PushRangeError
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{DefaultBranch: "main"}); !errors.As(err, &pre) || pre.File != "leak.md" {
		t.Fatalf("secret in an unpushed commit: got %v, want a hit in leak.md", err)
	}
}

// GH-6318: ending the context between two git calls of the no-base path returns
// the context error and starts no further git call, wherever it ends. The
// cancel comes through the git seam, so each call has its own point; removing
// the context check after any one of them lets another call start.
func TestCheckPushRange_ContextEndedBetweenGitCallsStopsTheNoBasePath(t *testing.T) {
	f := shallowFixture(t, "hunter2-not-a-known-shape")
	opts := PushOptions{DefaultBranch: "main"}

	var calls [][]string
	var cancelAfter int
	var cancel context.CancelFunc
	prev := gitCallDone
	gitCallDone = func(args []string) {
		calls = append(calls, args)
		if cancel != nil && len(calls) == cancelAfter {
			cancel()
		}
	}
	t.Cleanup(func() { gitCallDone = prev })

	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", opts); err != nil {
		t.Fatalf("setup: clean run: %v", err)
	}
	total := len(calls)
	if total < 10 {
		t.Fatalf("setup: the no-base path made %d git calls, want the whole boundary path (%v)", total, calls)
	}

	// The last call is the scan itself: ending the context after it has nothing left to stop.
	for k := 1; k < total; k++ {
		calls, cancelAfter = nil, k
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		err := CheckPush(ctx, f.local, "pilot/GH-1", opts)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("context ended after git call %d (%v): got %v, want context.Canceled", k, calls[k-1], err)
			continue
		}
		if errors.Is(err, ErrPushRefused) {
			t.Errorf("context ended after git call %d (%v): became a refusal: %v", k, calls[k-1], err)
		}
		if len(calls) != k {
			t.Errorf("context ended after git call %d (%v): %d git calls ran, next was %v", k, calls[k-1], len(calls), calls[k:])
		}
	}
}

// GH-6318: a replace ref or a graft made in the session hides a commit from
// every reading git command, while git push sends the real objects. The gate
// reads with GIT_NO_REPLACE_OBJECTS=1, so what it scans is what is pushed.
// mutation pin: the gate's git calls run without GIT_NO_REPLACE_OBJECTS -> both tests fail
func TestCheckPushRange_ReplaceRefDoesNotHideACommit(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.git("checkout", "-b", "clean-twin")
	f.commit("notes.md", "nothing here\n", "docs: notes")
	twin := f.git("rev-parse", "HEAD")
	f.git("checkout", "pilot/GH-1")
	f.commit("notes.md", "token = "+secret+"\n", "docs: notes")
	leaking := f.git("rev-parse", "HEAD")
	f.git("replace", leaking, twin)

	var pre *PushRangeError
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); !errors.As(err, &pre) || pre.File != "notes.md" {
		t.Fatalf("registered value in a commit a replace ref maps to a clean one: got %v, want a hit in notes.md", err)
	}
}

func TestCheckPushRange_ReplaceGraftDoesNotHideAnAddThenRemovePair(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	base := f.git("rev-parse", "HEAD")
	f.commit("notes.md", "token = "+secret+"\n", "docs: add notes")
	f.commit("notes.md", "nothing here\n", "docs: clean notes")
	// The graft cuts the adding commit out of the history the session sees.
	f.git("replace", "--graft", "HEAD", base)

	var pre *PushRangeError
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); !errors.As(err, &pre) || pre.File != "notes.md" {
		t.Fatalf("add-then-remove pair hidden by a replace graft: got %v, want a hit in notes.md", err)
	}
}

// GH-6318: a git older than 2.31 has no rev-parse --path-format, so the shallow
// boundary is silently not recognised there; the gate says so once.
func TestCheckPushRange_WarnOldGit(t *testing.T) {
	tests := []struct {
		out      string
		wantWarn bool
	}{
		{"git version 2.30.9\n", true},
		{"git version 2.25.1\n", true},
		{"git version 1.8.3.1\n", true},
		{"git version 2.31.0\n", false},
		{"git version 2.50.1 (Apple Git-155)\n", false},
		{"git version 3.0.0\n", false},
		{"unparsable\n", false},
	}
	prev := gitVersionOutput
	t.Cleanup(func() {
		gitVersionOutput = prev
		resetWarnOldGit()
	})
	for _, tt := range tests {
		var buf bytes.Buffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		gitVersionOutput = func(context.Context, string) (string, error) { return tt.out, nil }
		resetWarnOldGit()
		warnOldGit(context.Background(), "")
		warnOldGit(context.Background(), "") // once per process
		slog.SetDefault(old)

		got := strings.Count(buf.String(), "level=WARN")
		if tt.wantWarn && got != 1 {
			t.Errorf("%q: %d warnings, want exactly 1", tt.out, got)
		}
		if !tt.wantWarn && got != 0 {
			t.Errorf("%q: %d warnings, want none", tt.out, got)
		}
	}
}

// GH-6318: a version call that fails, or an ended context, must not spend the
// once-guard: the next push still gets its warning.
func TestCheckPushRange_WarnOldGit_FailedVersionCallDoesNotSpendTheGuard(t *testing.T) {
	prev := gitVersionOutput
	t.Cleanup(func() {
		gitVersionOutput = prev
		resetWarnOldGit()
	})
	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)
	resetWarnOldGit()

	gitVersionOutput = func(context.Context, string) (string, error) { return "", errors.New("git: not found") }
	warnOldGit(context.Background(), "")

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	gitVersionOutput = func(context.Context, string) (string, error) { return "git version 2.30.2\n", nil }
	warnOldGit(ended, "")
	if got := strings.Count(buf.String(), "level=WARN"); got != 0 {
		t.Fatalf("%d warnings from a failed call or an ended context, want none", got)
	}

	warnOldGit(context.Background(), "")
	warnOldGit(context.Background(), "")
	if got := strings.Count(buf.String(), "level=WARN"); got != 1 {
		t.Errorf("%d warnings after the guard was kept, want exactly 1:\n%s", got, buf.String())
	}
}

// resetWarnOldGit re-arms the once-guard of warnOldGit.
func resetWarnOldGit() {
	warnOldGitMu.Lock()
	warnOldGitDone = false
	warnOldGitMu.Unlock()
}

// GH-6318: an old git does not fail on --path-format, it prints the option back
// and exits 0. The warning must come from that answer, through the real
// shallow-file lookup, not only from a direct call of warnOldGit.
func TestCheckPushRange_OldGitWarnsOnceThroughTheLookup(t *testing.T) {
	f := newPushFixture(t)
	sha := f.git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(f.local, ".git", "shallow"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A git 2.30: --version says so, and rev-parse echoes the option it does not know.
	shim := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *--path-format=absolute*) printf '%s\\n%s\\n' --path-format=absolute .git/shallow; exit 0 ;;\n" +
		"  *--version*) echo 'git version 2.30.2'; exit 0 ;;\n" +
		"esac\n" +
		"exec " + realGit + " \"$@\"\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	resetWarnOldGit()
	t.Cleanup(resetWarnOldGit)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	for i := 0; i < 2; i++ {
		listed, err := isShallowBoundary(context.Background(), f.local, sha)
		if err != nil || listed {
			t.Fatalf("old git: got listed=%v err=%v, want the boundary check off", listed, err)
		}
	}
	if got := strings.Count(buf.String(), "level=WARN"); got != 1 {
		t.Errorf("%d warnings through the lookup, want exactly 1:\n%s", got, buf.String())
	}
}

// GH-6334: the path of a file the range renames is scanned, and the verdict
// prints it masked.
// mutation pin: do not scan file paths -> this test fails
func TestCheckPushRange_SecretInRenamedPathRefusedAndMasked(t *testing.T) {
	f := newPushFixture(t)
	const secret = "hunter2-not-a-known-shape"
	RegisterSecretValues(secret)
	f.git("mv", "README.md", "readme-"+secret+".md")
	f.git("commit", "-m", "docs: rename")

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %T %v, want *PushRangeError for a pure rename", err, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(pre.File, secret) {
		t.Errorf("the verdict holds the value: %v / %q", err, pre.File)
	}
}

// GH-6343: every git command of the gate carries hooks off, no signature
// display, no gpg program and UTF-8 output; every git log carries
// --no-show-signature. The argv is recorded at the one exec helper, gitCommand.
//
// mutation pin: drop one value from gitScanConfig or one --no-show-signature -> this test fails
func TestCheckPushRange_EveryGateCommandCarriesTheFixedConfig(t *testing.T) {
	f := newPushFixture(t)
	RegisterSecretValues("hunter2-not-a-known-shape")
	f.commit("main.go", "package main\n", "feat: code")
	f.git("commit", "--allow-empty", "-m", "feat: merge base")

	var seen [][]string
	prev := gitCommandSeen
	gitCommandSeen = func(args []string) { seen = append(seen, append([]string(nil), args...)) }
	t.Cleanup(func() { gitCommandSeen = prev })

	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("clean range: %v", err)
	}
	// The no-base path lists commits through more commands.
	f.git("remote", "remove", "origin")
	f.git("update-ref", "-d", "refs/remotes/origin/main")
	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("no-base range: %v", err)
	}

	want := []string{
		"core.hooksPath=" + EmptyHooksDir(),
		"log.showSignature=false",
		"i18n.logOutputEncoding=UTF-8",
		"gpg.program=false",
		"log.showRoot=true",
		"core.quotepath=false",
	}
	var logs int
	for _, args := range seen {
		joined := strings.Join(args, "\x00")
		for _, w := range want {
			if !strings.Contains(joined, "-c\x00"+w+"\x00") {
				t.Errorf("git %v lacks -c %s", args, w)
			}
		}
		for _, a := range args {
			if a == "log" || a == "show" {
				logs++
				if !contains(args, "--no-show-signature") {
					t.Errorf("git %v lacks --no-show-signature", args)
				}
				break
			}
		}
	}
	if logs < 3 || len(seen) < 5 {
		t.Errorf("recorded %d commands (%d log): the gate ran fewer git commands than expected", len(seen), logs)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestCheckPushRange_EmptyHooksDirIsAnEmptyPrivateDirectoryOutsideTheRepository(t *testing.T) {
	dir := EmptyHooksDir()
	if dir != EmptyHooksDir() {
		t.Error("EmptyHooksDir is not stable across calls")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("EmptyHooksDir is %v, want a directory with mode 0700", info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Errorf("EmptyHooksDir holds %d entries (err %v), want none", len(entries), err)
	}
	f := newPushFixture(t)
	if strings.HasPrefix(dir, f.local) {
		t.Errorf("EmptyHooksDir %s is inside the repository %s", dir, f.local)
	}
}

func TestCheckPushRange_EncodingHeaderRefused(t *testing.T) {
	f := newPushFixture(t)
	tree := f.git("rev-parse", "HEAD^{tree}")
	parent := f.git("rev-parse", "HEAD")
	for enc, refused := range map[string]bool{"IBM037": true, "ISO-8859-1": true, "UTF-16": true, "utf-8": false, "UTF8": false, "Utf-8": false} {
		obj := "tree " + tree + "\nparent " + parent + "\nauthor t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\nencoding " + enc + "\n\nclean\n"
		file := filepath.Join(t.TempDir(), "obj")
		if err := os.WriteFile(file, []byte(obj), 0o644); err != nil {
			t.Fatal(err)
		}
		sha := f.git("hash-object", "-t", "commit", "-w", file)
		err := CheckPushRange(context.Background(), f.local, "main", sha)
		var scan *PushScanError
		switch {
		case refused && !errors.As(err, &scan):
			t.Errorf("encoding %s: got %v, want a scan refusal", enc, err)
		case refused && !strings.Contains(err.Error(), "has the encoding "+enc+"; only UTF-8 commit messages are pushed"):
			t.Errorf("encoding %s: refusal text %q", enc, err)
		case !refused && err != nil:
			t.Errorf("encoding %s: got %v, want a pass", enc, err)
		}
	}
}
