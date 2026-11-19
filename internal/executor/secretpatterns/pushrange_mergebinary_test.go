package secretpatterns

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mergeAdding makes a merge commit on the current branch that adds name with
// content: a side branch and an own commit, then a --no-ff merge that adds the
// file to the merge itself.
func (f *pushFixture) mergeAdding(name, content string) {
	f.t.Helper()
	cur := f.git("rev-parse", "--abbrev-ref", "HEAD")
	f.git("checkout", "-b", "side-"+cur)
	f.commit("side.txt", "side\n", "side")
	f.git("checkout", cur)
	f.commit("own.txt", "own\n", "own")
	f.git("merge", "--no-ff", "--no-commit", "side-"+cur)
	f.writeWorkFile(name, content)
	f.git("add", name)
	f.git("commit", "-m", "merge side")
}

// mutation pin: remove the second scan (firstParentPatch) from scanCommits -> every refusal row fails
func TestPushRangeMergeAddsBinaryFile(t *testing.T) {
	rows := []struct {
		name    string
		trunk   bool // no base resolves: the remote has no default branch ref
		setup   func(f *pushFixture)
		content func(v string) string
		remove  bool
	}{
		{"NUL byte, deleted later", false, func(f *pushFixture) {},
			func(v string) string { return "\x00\x01 key " + v + "\n" }, true},
		{"committed .gitattributes binary, deleted later", false, func(f *pushFixture) {
			f.commit(".gitattributes", "data.txt binary\n", "attributes")
		}, func(v string) string { return "key " + v + "\n" }, true},
		{"info/attributes -diff, deleted later", false, func(f *pushFixture) {
			p := f.git("rev-parse", "--path-format=absolute", "--git-path", "info/attributes")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("data.txt -diff\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, func(v string) string { return "key " + v + "\n" }, true},
		{"NUL byte, kept, no base", true, func(f *pushFixture) {},
			func(v string) string { return "\x00\x01 key " + v + "\n" }, false},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			var f *pushFixture
			if tc.trunk {
				f = newTrunkFixture(t, "x")
			} else {
				f = newPushFixture(t)
			}
			v := runtimeValue(30)
			RegisterSecretValues(v)
			tc.setup(f)
			f.mergeAdding("data.txt", tc.content(v))
			if out := f.git("log", "-1", "-p", "--cc", "--text", "--format="); !strings.Contains(out, "Binary files differ") {
				t.Fatalf("setup: the combined diff does not print the file as binary:\n%s", out)
			}
			if tc.remove {
				f.git("rm", "-q", "data.txt")
				f.git("commit", "-m", "remove data")
			}
			if tc.trunk {
				if got := PushBase(context.Background(), f.local, "pilot/GH-1", ""); got != "" {
					t.Fatalf("setup: a base resolved (%q)", got)
				}
			}
			err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
			assertRefusedUnprinted(t, err, v)
			if pre := err.(*PushRangeError); pre.File != "data.txt" {
				t.Errorf("File = %q, want data.txt", pre.File)
			}
		})
	}

	t.Run("clean binary file passes", func(t *testing.T) {
		f := newPushFixture(t)
		RegisterSecretValues(runtimeValue(30))
		f.mergeAdding("data.bin", "\x00\x01\x02 clean\n")
		if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
			t.Fatalf("clean binary file of a merge: %v", err)
		}
	})
}

func TestBinaryPaths(t *testing.T) {
	patch := []byte("diff --cc a.bin\nindex 0000000,0000000..1111111\nBinary files differ\n" +
		"diff --cc \"q\\\"x\"\nBinary files differ\n" +
		"diff --combined c.bin\nBinary files differ\n" +
		"diff --cc text.txt\n@@@ -1,0 -1,0 +1,1 @@@\n++Binary files differ\n" +
		"diff --git a/g.bin b/g.bin\nBinary files /dev/null and b/g.bin differ\n")
	if got := binaryPaths(patch, true); len(got) != 3 || !got["a.bin"] || !got[`q"x`] || !got["c.bin"] {
		t.Errorf("combined only: %v", got)
	}
	if got := binaryPaths(patch, false); len(got) != 4 || !got["g.bin"] {
		t.Errorf("all blocks: %v", got)
	}
}

// A first-parent read that fails is a refusal, not a pass.
func TestFirstParentPatchFailsClosed(t *testing.T) {
	f := newPushFixture(t)
	_, err := firstParentPatch(context.Background(), f.local, strings.Repeat("0", 40), map[string]bool{"x": true})
	var scan *PushScanError
	if !errors.As(err, &scan) || scan.Reason != "a binary file of a merge commit could not be read" {
		t.Fatalf("got %T %v, want the binary-file PushScanError", err, err)
	}
}

// mergeOfBase pushes a base commit that writes baseFile with baseContent to
// main, then merges main into the work branch, which has an own commit. The
// merge adds data.bin with binContent (it differs from both parents) and the
// files of extra.
func (f *pushFixture) mergeOfBase(baseFile, baseContent, binContent string, extra map[string]string) {
	f.t.Helper()
	f.commit("own.txt", "own\n", "own")
	f.git("checkout", "main")
	f.commit(baseFile, baseContent, "base")
	f.git("push", "origin", "main")
	f.git("checkout", "pilot/GH-1")
	f.git("merge", "--no-ff", "--no-commit", "main")
	f.writeWorkFile("data.bin", binContent)
	f.git("add", "data.bin")
	for name, content := range extra {
		f.writeWorkFile(name, content)
		f.git("add", name)
	}
	f.git("commit", "-m", "merge main")
	if out := f.git("log", "-1", "-p", "--cc", "--text", "--format="); !strings.Contains(out, "Binary files differ") {
		f.t.Fatalf("setup: the combined diff does not print data.bin as binary:\n%s", out)
	}
}

// GH-6572: the first-parent read of a merge of the published base holds the
// whole delta of the base. It reads only the binary paths of the combined
// diff, and its blocks are added to the combined patch.
//
// mutation pin: let the second patch replace the combined patch -> the "text value the merge adds" row fails
// mutation pin: remove the pathspecs from the second scan -> the "value in the base" and "base over the size limit" rows fail
func TestPushRangeMergeOfBaseWithBinaryFile(t *testing.T) {
	t.Run("value in the base, clean binary file passes", func(t *testing.T) {
		f := newPushFixture(t)
		v := runtimeValue(30)
		RegisterSecretValues(v)
		f.mergeOfBase("base.txt", "key "+v+"\n", "\x00\x01 merged clean\n", nil)
		if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
			t.Fatalf("merge of the published base: %v", err)
		}
	})

	t.Run("value in the binary file refused", func(t *testing.T) {
		f := newPushFixture(t)
		v := runtimeValue(30)
		RegisterSecretValues(v)
		f.mergeOfBase("base.txt", "base\n", "\x00\x01 key "+v+"\n", nil)
		err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
		assertRefusedUnprinted(t, err, v)
		if pre := err.(*PushRangeError); pre.File != "data.bin" {
			t.Errorf("File = %q, want data.bin", pre.File)
		}
	})

	t.Run("text value the merge adds, deleted later, refused", func(t *testing.T) {
		f := newPushFixture(t)
		v := runtimeValue(30)
		RegisterSecretValues(v)
		f.mergeOfBase("base.txt", "base\n", "\x00\x01 merged clean\n", map[string]string{"evil.txt": "key " + v + "\n"})
		f.git("rm", "-q", "evil.txt")
		f.git("commit", "-m", "remove evil")
		err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
		assertRefusedUnprinted(t, err, v)
		if pre := err.(*PushRangeError); pre.File != "evil.txt" {
			t.Errorf("File = %q, want evil.txt", pre.File)
		}
	})

	t.Run("base over the size limit, clean binary file passes", func(t *testing.T) {
		old := maxCommitScanBytes
		maxCommitScanBytes = 4 << 10
		t.Cleanup(func() { maxCommitScanBytes = old })
		f := newPushFixture(t)
		RegisterSecretValues(runtimeValue(30))
		f.mergeOfBase("big.txt", strings.Repeat("published base line\n", 1000), "\x00\x01 merged clean\n", nil)
		if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
			t.Fatalf("merge of a large published base: %v", err)
		}
	})
}

// A binary path git prints quoted reaches the first-parent read as one
// :(literal) pathspec in its plain form, and its value is found there.
//
// mutation pin: pass the path without :(literal) -> this test fails
func TestFirstParentPatchQuotedPathIsOneLiteralPathspec(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	const name = `q"x [y].bin`
	f.mergeAdding(name, "\x00\x01 key "+v+"\n")
	f.git("rm", "-q", name)
	f.git("commit", "-m", "remove data")

	var pathspecs [][]string
	prev := gitCommandSeen
	gitCommandSeen = func(args []string) {
		if contains(args, "--diff-merges=first-parent") {
			i := slices.Index(args, "--")
			if i < 0 {
				i = len(args) - 1
			}
			pathspecs = append(pathspecs, append([]string(nil), args[i+1:]...))
		}
	}
	t.Cleanup(func() { gitCommandSeen = prev })

	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	assertRefusedUnprinted(t, err, v)
	if pre := err.(*PushRangeError); pre.File != name {
		t.Errorf("File = %q, want %q", pre.File, name)
	}
	if len(pathspecs) != 1 || !slices.Equal(pathspecs[0], []string{":(literal)" + name}) {
		t.Errorf("pathspecs of the first-parent read = %q, want [%q]", pathspecs, ":(literal)"+name)
	}
}

// A git before 2.31 fails on --diff-merges: the refusal names the version,
// and the old-git warning is logged.
func TestFirstParentPatchOldGitNamesTheVersion(t *testing.T) {
	f := newPushFixture(t)
	RegisterSecretValues(runtimeValue(30))
	f.mergeAdding("data.bin", "\x00\x01\x02 clean\n")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *--diff-merges*) echo 'fatal: unrecognized argument: --diff-merges=first-parent' >&2; exit 128 ;;\n" +
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
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prevLog)

	err = CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	var scan *PushScanError
	if !errors.As(err, &scan) || !strings.Contains(scan.Reason, "git 2.31 or later is needed") {
		t.Fatalf("got %T %v, want the binary-file PushScanError that names git 2.31", err, err)
	}
	if got := strings.Count(buf.String(), "level=WARN"); got != 1 {
		t.Errorf("%d old-git warnings, want exactly 1:\n%s", got, buf.String())
	}
}

// A binary path the first-parent read prints no block for is a refusal, not
// a pass: here GIT_LITERAL_PATHSPECS makes the :(literal) pathspec match no file.
//
// mutation pin: drop the check that each binary path has a block -> this test fails
func TestFirstParentPatchMissingBlockRefused(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	f.mergeAdding("data.bin", "\x00\x01 key "+v+"\n")
	f.git("rm", "-q", "data.bin")
	f.git("commit", "-m", "remove data")
	ctx := WithGitEnv(context.Background(), append(os.Environ(), "GIT_LITERAL_PATHSPECS=1"))
	err := CheckPush(ctx, f.local, "pilot/GH-1", PushOptions{})
	var scan *PushScanError
	if !errors.As(err, &scan) || scan.Reason != errMergeBinaryUnread.Reason {
		t.Fatalf("got %T %v, want the binary-file PushScanError", err, err)
	}
}

// The first-parent read has the size limit of the per-commit scan: a binary
// file above it is refused, though the combined diff prints one line for it.
func TestFirstParentPatchOverTheSizeLimitRefused(t *testing.T) {
	old := maxCommitScanBytes
	maxCommitScanBytes = 4 << 10
	t.Cleanup(func() { maxCommitScanBytes = old })
	f := newPushFixture(t)
	RegisterSecretValues(runtimeValue(30))
	f.mergeAdding("data.bin", "\x00\x01"+strings.Repeat("clean binary line\n", 400))
	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	var scan *PushScanError
	if !errors.As(err, &scan) || !strings.Contains(scan.Reason, "too large to scan") {
		t.Fatalf("got %T %v, want the too-large PushScanError", err, err)
	}
}

// The submodule rows of TestPushRangeSubmodulePathAnyConfig through scanCommits
// alone, so the arguments of git log -p are pinned without the net diff.
//
// mutation pin: remove --ignore-submodules=none from the git log -p alone -> the ignoreSubmodules row fails
// mutation pin: remove --submodule=short from the git log -p alone -> the diff.submodule row fails
func TestScanCommitsSubmodulePathAnyConfig(t *testing.T) {
	for _, tc := range []struct{ name, key, val string }{
		{"diff.submodule=log", "diff.submodule", "log"},
		{"diff.ignoreSubmodules=all", "diff.ignoreSubmodules", "all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			v := runtimeValue(30)
			RegisterSecretValues(v)
			f.git("config", tc.key, tc.val)
			base := f.git("rev-parse", "HEAD")
			f.addGitlink(v)
			assertRefusedUnprinted(t, scanCommits(context.Background(), f.local, []string{base + "..HEAD"}, PushOptions{}), v)
		})
	}
}

// Each scan command (the net diff, git log -p --cc and the first-parent read
// of a merge with a binary file) carries --text and both submodule arguments.
//
// mutation pin: remove --text from the first-parent read -> this test fails
func TestEveryScanCommandCarriesTheSubmoduleArguments(t *testing.T) {
	f := newPushFixture(t)
	RegisterSecretValues(runtimeValue(30))
	f.mergeAdding("data.bin", "\x00\x01\x02 clean\n")

	var seen [][]string
	prev := gitCommandSeen
	gitCommandSeen = func(args []string) { seen = append(seen, append([]string(nil), args...)) }
	t.Cleanup(func() { gitCommandSeen = prev })

	if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
		t.Fatalf("clean range: %v", err)
	}
	found := map[string]bool{}
	for _, args := range seen {
		var kind string
		switch {
		case contains(args, "diff"):
			kind = "diff"
		case contains(args, "log") && contains(args, "--cc"):
			kind = "log --cc"
		case contains(args, "log") && contains(args, "--diff-merges=first-parent"):
			kind = "log first-parent"
		default:
			continue
		}
		found[kind] = true
		for _, w := range []string{"--text", "--submodule=short", "--ignore-submodules=none"} {
			if !contains(args, w) {
				t.Errorf("%s: git %v lacks %s", kind, args, w)
			}
		}
	}
	for _, k := range []string{"diff", "log --cc", "log first-parent"} {
		if !found[k] {
			t.Errorf("the gate ran no %s command", k)
		}
	}
}

// The cut of a long path runs after its redaction: a cut first would leave
// the end of the value in the printed part.
//
// mutation pin: cut the path before Redact in printablePath -> this test fails
func TestRefusalCutsThePathAfterTheRedaction(t *testing.T) {
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	err := newPushRangeError("abc", strings.Repeat("p", 300)+v+strings.Repeat("q", 185), KindConfiguredValue)
	if part := leakedPart(err.Error(), v); part != "" {
		t.Errorf("the refusal holds %q of the value: %q", part, err.Error())
	}
}
