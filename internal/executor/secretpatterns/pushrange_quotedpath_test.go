package secretpatterns

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// quotedNameValue builds a registered value at run time that holds mid: git
// prints a file name with this value in its quoted form when mid holds a '"',
// a '\' or a control byte.
func quotedNameValue(mid string) string {
	return runtimeValue(14) + mid + "-" + runtimeValue(30)[5:]
}

// quotedNameRows are the values of the file name tables. The JSON form of the
// first two differs from the git form ('&' and '<' become a Unicode escape),
// and so does the one of a control byte (git writes \001, JSON \u0001).
var quotedNameRows = []struct {
	name string
	mid  string
}{
	{"quote and ampersand", `"&`},
	{"backslash and less-than", `\<`},
	{"byte 0x01", "\x01"},
	{"byte 0x07", "\x07"},
	{"tab", "\t"},
	{"newline", "\n"},
	{"non-ASCII", "é—ü"},
}

// assertRefusedUnprinted checks err is a *PushRangeError that holds no part of
// 8 or more characters of v.
func assertRefusedUnprinted(t *testing.T, err error, v string) {
	t.Helper()
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %T %v, want *PushRangeError", err, err)
	}
	if part := leakedPart(err.Error(), v); part != "" {
		t.Errorf("the refusal holds %q of the value: %q", part, err.Error())
	}
	if part := leakedPart(pre.File, v); part != "" {
		t.Errorf("File holds %q of the value: %q", part, pre.File)
	}
}

// mutation pin: scan the quoted text of the path -> the "quote and ampersand" row fails
func TestCheckPushRange_QuotedFileNameIsScannedPlain(t *testing.T) {
	for _, tc := range quotedNameRows {
		for _, inContent := range []bool{false, true} {
			name := tc.name
			if inContent {
				name += " in content too"
			}
			t.Run(name, func(t *testing.T) {
				f := newPushFixture(t)
				v := quotedNameValue(tc.mid)
				RegisterSecretValues(v)
				content := "clean\n"
				if inContent {
					content = "x " + v + "\n"
				}
				f.commit(v, content, "add file")
				assertRefusedUnprinted(t, CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}), v)
			})
		}
	}
}

func TestCheckPushRange_RenameToQuotedFileName(t *testing.T) {
	for _, tc := range quotedNameRows {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			v := quotedNameValue(tc.mid)
			RegisterSecretValues(v)
			f.git("mv", "README.md", v)
			f.git("commit", "-m", "docs: rename")
			assertRefusedUnprinted(t, CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}), v)
		})
	}
}

// A normal path, and a quoted one, pass with clean content and are named in
// their plain form (control bytes as "?") when the content holds a secret.
func TestCheckPushRange_PathNamedPlain(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"dir name/file x.txt", "dir name/file x.txt"},
		{"café/naïve.txt", "café/naïve.txt"},
		{`q"uo\te.txt`, `q"uo\te.txt`},
		{"ta\tb.txt", "ta?b.txt"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			f := newPushFixture(t)
			v := runtimeValue(30)
			RegisterSecretValues(v)
			f.commit(tc.path, "clean\n", "add file")
			if err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{}); err != nil {
				t.Fatalf("clean content: got %v, want nil", err)
			}
			f.commit(tc.path, "x "+v+"\n", "add secret")
			err := CheckPush(context.Background(), f.local, "pilot/GH-1", PushOptions{})
			var pre *PushRangeError
			if !errors.As(err, &pre) {
				t.Fatalf("got %T %v, want *PushRangeError", err, err)
			}
			if pre.File != tc.want {
				t.Errorf("File = %q, want %q", pre.File, tc.want)
			}
		})
	}
}

func TestUnquoteGitPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain/path.txt", "plain/path.txt"},
		{`b/x"y`, `b/x"y`},
		{`"`, `"`},
		{`""`, ""},
		{`"a\"b"`, `a"b`},
		{`"a\\b"`, `a\b`},
		{`"\a\b\f\n\r\t\v"`, "\a\b\f\n\r\t\v"},
		{`"x\001y\177\303\251"`, "x\x01y\x7f\xc3\xa9"},
		{`"a\qb"`, `"a\qb"`},
		{`"a\"`, `"a\"`},
		{`"a\01"`, `"a\01"`},
		{`"a\400"`, `"a\400"`},
		{`"a\08x"`, `"a\08x"`},
	} {
		if got := unquoteGitPath(tc.in); got != tc.want {
			t.Errorf("unquoteGitPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiffGitNewPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a/x.txt b/x.txt", "x.txt"},
		{"a/x b/y b/x b/y", "y"},
		{`"a/x\"y" "b/x\"y"`, `x"y`},
		{`a/old.txt "b/n\\\"ew"`, `n\"ew`},
		{`"a/x\ty" "b/x y\" \"b/z"`, `x y" "b/z`},
	} {
		if got := diffGitNewPath(tc.in); got != tc.want {
			t.Errorf("diffGitNewPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// mutation pin: scanCommits without --topo-order -> this test fails
func TestCheckPushRange_MergeWithSkewNamesTheParentOfTheMessage(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	base := f.git("rev-parse", "HEAD")
	// parent is dated after its child; a date walk reaches it first, so the
	// reversed list puts the child first.
	parent := f.writeCommitMessage([]string{base}, 1900000000, "", "note "+v)
	child := f.writeCommitMessage([]string{parent}, 1700000000, "", "note "+v)
	merge := f.writeCommit([]string{child, parent}, 1600000000, "")

	err := CheckPushRange(context.Background(), f.local, "main", merge)
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want a *PushRangeError", err)
	}
	if pre.File != CommitMessageFile || !strings.HasPrefix(parent, pre.Commit) {
		t.Errorf("refusal names %s in %q, want the parent %s in the message (child %s)", pre.Commit, pre.File, parent[:12], child[:12])
	}
}

// mutation pin: checkCommitEncodings without --reverse (the chain row) or
// without --topo-order (the merge row) -> this test fails
func TestCheckPushRange_EncodingNamesTheOldestCommit(t *testing.T) {
	for _, tc := range []struct {
		name              string
		parentAt, childAt int64
		mergeAt           int64
	}{
		{"chain", 1600000000, 1700000000, 0},
		{"merge with a parent dated after its child", 1900000000, 1700000000, 1600000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			base := f.git("rev-parse", "HEAD")
			parent := f.writeCommit([]string{base}, tc.parentAt, "", "encoding ISO-8859-1")
			child := f.writeCommit([]string{parent}, tc.childAt, "", "encoding ISO-8859-1")
			head := child
			if tc.mergeAt != 0 {
				head = f.writeCommit([]string{child, parent}, tc.mergeAt, "")
			}

			err := CheckPushRange(context.Background(), f.local, "main", head)
			var pse *PushScanError
			if !errors.As(err, &pse) {
				t.Fatalf("got %v, want a *PushScanError", err)
			}
			if !strings.Contains(pse.Reason, "commit "+parent[:12]) {
				t.Errorf("refusal %q, want the parent %s (child %s)", pse.Reason, parent[:12], child[:12])
			}
		})
	}
}
