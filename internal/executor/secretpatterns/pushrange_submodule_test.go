package secretpatterns

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addGitlink stages a gitlink (mode 160000) at path and commits it.
func (f *pushFixture) addGitlink(path string) {
	f.t.Helper()
	f.git("update-index", "--add", "--cacheinfo", "160000,"+f.git("rev-parse", "HEAD")+","+path)
	f.git("commit", "-m", "add submodule entry")
}

func (f *pushFixture) writeWorkFile(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.local, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// mutation pin: remove --ignore-submodules=none from the two scan commands -> the
// ignoreSubmodules and .gitmodules rows fail
// mutation pin: remove --submodule=short from the two scan commands -> the
// diff.submodule rows fail
func TestPushRangeSubmodulePathAnyConfig(t *testing.T) {
	ignoreAll := func(v string) string {
		return "[submodule \"sub\"]\n\tpath = " + v + "\n\turl = ./sub\n\tignore = all\n"
	}
	rows := []struct {
		name  string
		setup func(f *pushFixture, v string)
		mid   string
	}{
		{"diff.submodule=log", func(f *pushFixture, v string) { f.git("config", "diff.submodule", "log") }, ""},
		{"diff.submodule=diff", func(f *pushFixture, v string) { f.git("config", "diff.submodule", "diff") }, ""},
		{"diff.ignoreSubmodules=all", func(f *pushFixture, v string) { f.git("config", "diff.ignoreSubmodules", "all") }, ""},
		{"uncommitted .gitmodules ignore=all", func(f *pushFixture, v string) { f.writeWorkFile(".gitmodules", ignoreAll(v)) }, ""},
		// The .gitmodules commit is pushed to main first: in the range its content
		// holds the value and the push would be refused for that file alone.
		{"committed .gitmodules ignore=all", func(f *pushFixture, v string) {
			f.commit(".gitmodules", ignoreAll(v), "add gitmodules")
			f.git("push", "origin", "HEAD:main")
		}, ""},
		{"no config", func(f *pushFixture, v string) {}, ""},
		{"path with a double quote", func(f *pushFixture, v string) { f.git("config", "diff.submodule", "log") }, `"&`},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			v := quotedNameValue(tc.mid)
			RegisterSecretValues(v)
			tc.setup(f, v)
			f.addGitlink(v)
			err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
			assertRefusedUnprinted(t, err, v)
			if pre := err.(*PushRangeError); pre.File == ".gitmodules" {
				t.Errorf("refused for .gitmodules, want the gitlink path")
			}
		})
	}
}

func TestPushRangeLongPathIsCut(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	f.addGitlink(v + strings.Repeat("x", 5000-len(v)))
	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	assertRefusedUnprinted(t, err, v)
	if n := len(err.Error()); n >= 400 {
		t.Errorf("the refusal is %d bytes, want under 400", n)
	}
}

func TestPrintablePath(t *testing.T) {
	long := strings.Repeat("a", 250)
	for _, tc := range []struct{ name, in, want string }{
		{"short", "dir/file.txt", "dir/file.txt"},
		{"exactly the limit", long[:maxPrintedPath], long[:maxPrintedPath]},
		{"cut keeps the end", long + "END", "..." + long[:maxPrintedPath-3] + "END"},
		{"control bytes", "a\x01b\x7fc", "a?b?c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := printablePath(tc.in); got != tc.want {
				t.Errorf("printablePath = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty file with a quote in its name, added by a merge commit, shows only
// in the "diff --cc" header of git log -p --cc.
func TestScanCommitsMergeHeaderQuotedName(t *testing.T) {
	f := newPushFixture(t)
	v := quotedNameValue(`"&`)
	RegisterSecretValues(v)
	base := f.git("rev-parse", "HEAD")
	f.git("checkout", "-b", "side")
	f.commit("side.txt", "side\n", "side")
	f.git("checkout", "pilot/GH-1")
	f.commit("own.txt", "own\n", "own")
	f.git("merge", "--no-ff", "--no-commit", "side")
	f.writeWorkFile(v, "")
	f.git("add", v)
	f.git("commit", "-m", "merge")
	err := scanCommits(context.Background(), f.local, []string{base + "..HEAD"}, PushOptions{})
	assertRefusedUnprinted(t, err, v)
}

// A 100 % copy has a "copy to" line and no ---/+++ lines and no hunk.
func TestCopyToNameIsScanned(t *testing.T) {
	f := newPushFixture(t)
	v := quotedNameValue(`"&`)
	RegisterSecretValues(v)
	f.git("config", "diff.renames", "copies")
	base := f.git("rev-parse", "HEAD")
	f.writeWorkFile(v, "hello\n")
	f.writeWorkFile("README.md", "changed\n")
	f.git("add", "-A")
	f.git("commit", "-m", "copy")
	if out := f.git("show", "--format=", "-C", "--stat", "HEAD"); !strings.Contains(out, "=>") {
		t.Skipf("git does not detect the copy:\n%s", out)
	}
	assertRefusedUnprinted(t, scanCommits(context.Background(), f.local, []string{base + "..HEAD"}, PushOptions{}), v)
	assertRefusedUnprinted(t, CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}), v)
}

func TestPathWithByte0x7fIsPrintedEscaped(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	f.commit("a\x7fb.txt", "x "+v+"\n", "add secret")
	err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
	pre, ok := err.(*PushRangeError)
	if !ok {
		t.Fatalf("got %T %v, want *PushRangeError", err, err)
	}
	if pre.File != "a?b.txt" {
		t.Errorf("File = %q, want %q", pre.File, "a?b.txt")
	}
}
