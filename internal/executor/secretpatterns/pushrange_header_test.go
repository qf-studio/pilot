package secretpatterns

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TASK-651: the refusal of a hit in a commit header names the commit and the
// place, never a part of the value: an unknown header name is printed as
// "line N", an encoding value only when it is a plain charset name the
// redaction leaves as it is.

// runtimeValue builds an n-character value at run time that no part of a
// refusal text (fixed words, a hex SHA) can hold 8 characters of.
func runtimeValue(n int) string {
	return strings.Repeat("kQ7wZ", n/5+1)[:n]
}

// writeCommit writes the raw commit object obj (git hash-object -t commit -w)
// and returns its SHA.
func (f *pushFixture) writeCommit(obj string) string {
	f.t.Helper()
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = f.local
	cmd.Stdin = strings.NewReader(obj)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("hash-object: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// headerCommit writes a commit on top of HEAD with the tree of HEAD and the
// header line extra after the committer.
func (f *pushFixture) headerCommit(extra string) string {
	f.t.Helper()
	return f.writeCommit("tree " + f.git("rev-parse", "HEAD^{tree}") + "\nparent " + f.git("rev-parse", "HEAD") +
		"\nauthor t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
		extra + "\n\nclean\n")
}

// assertNoPartOf fails when text holds a substring of 8 or more characters of v.
func assertNoPartOf(t *testing.T, text, v string) {
	t.Helper()
	for i := 0; i+8 <= len(v); i++ {
		if strings.Contains(text, v[i:i+8]) {
			t.Errorf("refusal %q holds %q, a part of the value", text, v[i:i+8])
			return
		}
	}
}

// headerValueRows are the values of the header name and the encoding tables.
// register is false for a shape value: the patterns find it.
func headerValueRows() []struct {
	name     string
	value    string
	register bool
} {
	return []struct {
		name     string
		value    string
		register bool
	}{
		{"registered value of 20", runtimeValue(20), true},
		{"registered value of 40", runtimeValue(40), true},
		{"registered value of 41", runtimeValue(41), true},
		{"registered value of 80", runtimeValue(80), true},
		{"shape value longer than 40", telegramShaped(), false},
		{"registered value with a space", runtimeValue(12) + " " + runtimeValue(12), true},
		{"registered value with a colon", runtimeValue(12) + ":" + runtimeValue(12), true},
	}
}

// mutation pin: print the name of an unknown header line (headerLabel returns
// name) -> this test fails
func TestCheckPushRange_HeaderNameIsNeverPrinted(t *testing.T) {
	for _, tc := range headerValueRows() {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			if tc.register {
				RegisterSecretValues(tc.value)
			}
			extra := tc.value + " note"
			if strings.Contains(tc.value, " ") {
				extra = tc.value // the value is the whole header line
			}
			sha := f.headerCommit(extra)

			err := CheckPushRange(context.Background(), f.local, "main", sha)
			var pre *PushRangeError
			if !errors.As(err, &pre) {
				t.Fatalf("got %v, want a *PushRangeError", err)
			}
			if want := CommitHeaderFile("line 5"); pre.File != want {
				t.Errorf("file %q, want %q", pre.File, want)
			}
			if !strings.Contains(err.Error(), "header line 5") {
				t.Errorf("refusal %q does not name the header line", err)
			}
			assertNoPartOf(t, err.Error(), tc.value)
		})
	}
}

// mutation pin: cut before the redaction in checkCommitEncodings
// (Redact(printableValue(enc))) -> this test fails
func TestCheckPushRange_EncodingValueIsNeverPrinted(t *testing.T) {
	for _, tc := range headerValueRows() {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			if tc.register {
				RegisterSecretValues(tc.value)
			}
			sha := f.headerCommit("encoding " + tc.value)

			err := CheckPushRange(context.Background(), f.local, "main", sha)
			if !errors.Is(err, ErrPushRefused) {
				t.Fatalf("got %v, want a refusal", err)
			}
			assertNoPartOf(t, err.Error(), tc.value)
		})
	}
}

func TestCheckPushRange_EncodingLabel(t *testing.T) {
	ResetSecretValuesForTest()
	t.Cleanup(ResetSecretValuesForTest)
	RegisterSecretValues(runtimeValue(20))
	for enc, want := range map[string]string{
		"IBM037":                "the encoding IBM037",
		"ISO-8859-1":            "the encoding ISO-8859-1",
		"x_mac.roman:2":         "the encoding x_mac.roman:2",
		strings.Repeat("a", 40): "the encoding " + strings.Repeat("a", 40),
		strings.Repeat("a", 41): "an encoding header that is not UTF-8",
		"ISO 8859":              "an encoding header that is not UTF-8",
		"ISO/8859":              "an encoding header that is not UTF-8",
		runtimeValue(20):        "an encoding header that is not UTF-8",
		"x" + runtimeValue(20):  "an encoding header that is not UTF-8",
	} {
		if got := encodingLabel(enc); got != want {
			t.Errorf("encodingLabel(%q) = %q, want %q", enc, got, want)
		}
	}
}

func TestCheckPushRange_KnownHeaderNameIsPrinted(t *testing.T) {
	f := newPushFixture(t)
	value := runtimeValue(20)
	RegisterSecretValues(value)
	sha := f.writeCommit("tree " + f.git("rev-parse", "HEAD^{tree}") + "\nparent " + f.git("rev-parse", "HEAD") +
		"\nauthor dev " + value + " <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n\nclean\n")

	err := CheckPushRange(context.Background(), f.local, "main", sha)
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want a *PushRangeError", err)
	}
	if pre.File != CommitHeaderFile("author") || !strings.Contains(err.Error(), "the commit header author") {
		t.Errorf("refusal %q, want it to name the author header", err)
	}
	assertNoPartOf(t, err.Error(), value)
}

func TestCheckPushRange_ContinuationLineCountsUnderItsHeader(t *testing.T) {
	f := newPushFixture(t)
	value := runtimeValue(20)
	RegisterSecretValues(value)
	sha := f.headerCommit("x-block first\n second " + value + "\n third")

	err := CheckPushRange(context.Background(), f.local, "main", sha)
	var pre *PushRangeError
	if !errors.As(err, &pre) || pre.File != CommitHeaderFile("line 5") {
		t.Fatalf("got %v, want a refusal for the commit header line 5", err)
	}
}

// mutation pin: rev-list --reverse without --topo-order in checkCommitObjects
// -> this test fails (the child P1 is named)
func TestCheckPushRange_HeaderHitAcrossMergeNamesTheParent(t *testing.T) {
	f := newPushFixture(t)
	value := runtimeValue(20)
	RegisterSecretValues(value)
	tree := f.git("rev-parse", "HEAD^{tree}")
	commit := func(date string, extra string, parents ...string) string {
		obj := "tree " + tree + "\n"
		for _, p := range parents {
			obj += "parent " + p + "\n"
		}
		obj += "author t <t@example.com> " + date + " +0000\ncommitter t <t@example.com> " + date + " +0000\n"
		if extra != "" {
			obj += extra + "\n"
		}
		return f.writeCommit(obj + "\nclean\n")
	}
	// The parent q is dated after its children p1 and p2: by date alone q is
	// listed between p2 and p1.
	q := commit("2000000000", "x-note "+value, f.git("rev-parse", "HEAD"))
	p1 := commit("1700000000", "x-note "+value, q)
	p2 := commit("1700000100", "", q)
	m := commit("1700000200", "", p1, p2)

	err := CheckPushRange(context.Background(), f.local, "main", m)
	var pre *PushRangeError
	if !errors.As(err, &pre) {
		t.Fatalf("got %v, want a *PushRangeError", err)
	}
	if !strings.HasPrefix(q, pre.Commit) {
		t.Errorf("refusal names %s, want the parent %s (child %s)", pre.Commit, q[:12], p1[:12])
	}
}
