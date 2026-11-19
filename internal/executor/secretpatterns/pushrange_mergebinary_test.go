package secretpatterns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		"diff --cc text.txt\n@@@ -1,0 -1,0 +1,1 @@@\n++Binary files differ\n" +
		"diff --git a/g.bin b/g.bin\nBinary files /dev/null and b/g.bin differ\n")
	if got := binaryPaths(patch, true); len(got) != 2 || !got["a.bin"] || !got[`q"x`] {
		t.Errorf("combined only: %v", got)
	}
	if got := binaryPaths(patch, false); len(got) != 3 || !got["g.bin"] {
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
// of a merge with a binary file) carries both submodule arguments.
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
		for _, w := range []string{"--submodule=short", "--ignore-submodules=none"} {
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
