package secretpatterns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// runtimeValue builds a fake value of n characters at run time: a fake token
// of internal/testutil, then a count, so that no 8-character part of it is a
// word the refusal text could hold by chance.
func runtimeValue(n int) string {
	var b strings.Builder
	b.WriteString(testutil.FakeGitHubToken)
	for i := 0; b.Len() < n; i++ {
		b.WriteString("-" + strconv.Itoa(i*7919))
	}
	return b.String()[:n]
}

// leakedPart returns the first part of 8 or more characters of value that is
// in text, or "".
func leakedPart(text, value string) string {
	for i := 0; i+8 <= len(value); i++ {
		if strings.Contains(text, value[i:i+8]) {
			return value[i : i+8]
		}
	}
	return ""
}

// writeCommit writes a raw commit object with the given parents, committer
// date and extra header lines after the committer, and returns its SHA.
func (f *pushFixture) writeCommit(parents []string, date int64, author string, extra ...string) string {
	f.t.Helper()
	obj := "tree " + f.git("rev-parse", "HEAD^{tree}") + "\n"
	for _, p := range parents {
		obj += "parent " + p + "\n"
	}
	if author == "" {
		author = "t"
	}
	stamp := strconv.FormatInt(date, 10) + " +0000"
	obj += "author " + author + " <t@example.com> " + stamp + "\ncommitter t <t@example.com> " + stamp + "\n"
	for _, e := range extra {
		obj += e + "\n"
	}
	obj += "\nclean\n"
	file := filepath.Join(f.t.TempDir(), "obj")
	if err := os.WriteFile(file, []byte(obj), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return f.git("hash-object", "-t", "commit", "-w", file)
}

// headerValueRows are the values of the header tables: registered unless
// shape is set.
var headerValueRows = []struct {
	name  string
	value func() string
	shape bool
}{
	{"registered 20", func() string { return runtimeValue(20) }, false},
	{"registered 40", func() string { return runtimeValue(40) }, false},
	{"registered 41", func() string { return runtimeValue(41) }, false},
	{"registered 80", func() string { return runtimeValue(80) }, false},
	{"shape longer than 40", telegramShaped, true},
	{"registered with a space", func() string { return runtimeValue(20) + " " + runtimeValue(30) }, false},
	{"registered with a colon", func() string { return runtimeValue(20) + ":" + runtimeValue(30) }, false},
}

// mutation pin: print the name of an unknown header line -> this test fails
func TestCheckPushRange_HeaderNameIsNeverPrinted(t *testing.T) {
	for _, tc := range headerValueRows {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			v := tc.value()
			if !tc.shape {
				RegisterSecretValues(v)
			}
			if Redact(v) == v {
				t.Fatalf("fixture: %q is not a secret", v)
			}
			// The value is the whole header line: its name, or its name and
			// value when it holds a space.
			sha := f.writeCommit([]string{f.git("rev-parse", "HEAD")}, 1700000000, "", v)

			err := CheckPushRange(context.Background(), f.local, "main", sha)
			var pre *PushRangeError
			if !errors.As(err, &pre) {
				t.Fatalf("got %v, want a *PushRangeError", err)
			}
			if want := CommitHeaderFile("", 5); pre.File != want {
				t.Errorf("file %q, want %q", pre.File, want)
			}
			if !strings.Contains(err.Error(), "header line 5") {
				t.Errorf("error %q does not hold the label header line 5", err)
			}
			if part := leakedPart(err.Error(), v); part != "" {
				t.Errorf("error %q holds %q of the value", err, part)
			}
		})
	}
}

// mutation pin: cut before the redaction in checkCommitEncodings -> this test fails
func TestCheckPushRange_EncodingValueIsNeverPrinted(t *testing.T) {
	for _, tc := range headerValueRows {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			v := tc.value()
			if !tc.shape {
				RegisterSecretValues(v)
			}
			sha := f.writeCommit([]string{f.git("rev-parse", "HEAD")}, 1700000000, "", "encoding "+v)

			err := CheckPushRange(context.Background(), f.local, "main", sha)
			var scan *PushScanError
			if !errors.As(err, &scan) {
				t.Fatalf("got %v, want a *PushScanError", err)
			}
			if !strings.Contains(err.Error(), "has an encoding header that is not UTF-8;") {
				t.Errorf("error %q does not hold the fixed text", err)
			}
			if part := leakedPart(err.Error(), v); part != "" {
				t.Errorf("error %q holds %q of the value", err, part)
			}
		})
	}
}

func TestCheckPushRange_EncodingNameRules(t *testing.T) {
	for enc, printed := range map[string]bool{
		"ISO-8859-1":            true,
		"x_mac.roman:2":         true,
		strings.Repeat("e", 40): true,
		strings.Repeat("e", 41): false,
		"ISO 8859":              false,
		"latin/1":               false,
	} {
		f := newPushFixture(t)
		sha := f.writeCommit([]string{f.git("rev-parse", "HEAD")}, 1700000000, "", "encoding "+enc)
		err := CheckPushRange(context.Background(), f.local, "main", sha)
		if err == nil {
			t.Fatalf("encoding %q: got nil, want a refusal", enc)
		}
		if got := strings.Contains(err.Error(), "has the encoding "+enc+";"); got != printed {
			t.Errorf("encoding %q: printed %v, want %v: %v", enc, got, printed, err)
		}
	}
}

func TestCheckPushRange_AuthorHitNamesAuthor(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	sha := f.writeCommit([]string{f.git("rev-parse", "HEAD")}, 1700000000, "dev "+v)

	err := CheckPushRange(context.Background(), f.local, "main", sha)
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want a *PushRangeError", err)
	}
	if want := CommitHeaderFile("author", 3); pre.File != want || want != "the commit header author" {
		t.Errorf("file %q, want %q", pre.File, want)
	}
	if part := leakedPart(err.Error(), v); part != "" {
		t.Errorf("error %q holds %q of the value", err, part)
	}
}

func TestCommitHeaderFile_ContinuationCountsUnderItsLine(t *testing.T) {
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	head := []byte("tree t\nauthor a\nx-block first\n second " + v + "\n third\nx-after clean\n")
	name, line, _, hit := scanCommitHeader(head)
	if !hit || CommitHeaderFile(name, line) != "the commit header line 3" {
		t.Errorf("got %q line %d hit %v, want line 3", name, line, hit)
	}
}

// mutation pin: rev-list --reverse without --topo-order -> this test fails
func TestCheckPushRange_MergeWithSkewNamesTheParent(t *testing.T) {
	f := newPushFixture(t)
	v := runtimeValue(30)
	RegisterSecretValues(v)
	base := f.git("rev-parse", "HEAD")
	// parent is dated after its child; the merge lists both, so a date walk
	// reaches the parent before the child.
	parent := f.writeCommit([]string{base}, 1900000000, "", "x-note "+v)
	child := f.writeCommit([]string{parent}, 1700000000, "", "x-note "+v)
	merge := f.writeCommit([]string{child, parent}, 1600000000, "")

	err := CheckPushRange(context.Background(), f.local, "main", merge)
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want a *PushRangeError", err)
	}
	if !strings.HasPrefix(parent, pre.Commit) {
		t.Errorf("refusal names %s, want the parent %s (child %s)", pre.Commit, parent[:12], child[:12])
	}
}
