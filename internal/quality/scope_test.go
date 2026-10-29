package quality

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const scopeMod = "example.com/m"

// scopeFiles is the tiny module every scope test starts from (11 packages):
//
//	m (root)   leaf <- dep   low <- a, b   testuser (test imports low)
//	viatest (test imports testuser)   mem   exec (test reads ../mem/store.go)
//	zz (sorts last, no imports and no test files)
var scopeFiles = map[string]string{
	"go.mod":               "module " + scopeMod + "\n\ngo 1.21\n",
	"m.go":                 "package m\n",
	"leaf/leaf.go":         "package leaf\n",
	"dep/dep.go":           "package dep\n\nimport _ \"" + scopeMod + "/leaf\"\n",
	"low/low.go":           "package low\n",
	"a/a.go":               "package a\n\nimport _ \"" + scopeMod + "/low\"\n",
	"b/b.go":               "package b\n\nimport _ \"" + scopeMod + "/low\"\n",
	"testuser/t.go":        "package testuser\n",
	"testuser/t_test.go":   "package testuser\n\nimport (\n\t\"testing\"\n\n\t_ \"" + scopeMod + "/low\"\n)\n\nfunc TestX(t *testing.T) {}\n",
	"viatest/v.go":         "package viatest\n",
	"viatest/v_test.go":    "package viatest\n\nimport (\n\t\"testing\"\n\n\t_ \"" + scopeMod + "/testuser\"\n)\n\nfunc TestX(t *testing.T) {}\n",
	"mem/store.go":         "package mem\n",
	"exec/exec.go":         "package exec\n",
	"exec/exec_test.go":    "package exec\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestX(t *testing.T) { _, _ = os.ReadFile(\"../mem/store.go\") }\n",
	"zz/zz.go":             "package zz\n\nconst Z = 1\n",
	"docs/guide.md":        "docs\n",
	".agent/tasks/note.md": "note\n",
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newScopeRepo builds the module in a fresh repo with main committed and a
// feature branch checked out.
func newScopeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	writeFiles(t, dir, scopeFiles)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	git(t, dir, "checkout", "-q", "-b", "feature")
	return dir
}

func imp(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = scopeMod + "/" + n
	}
	return out
}

// The last package in go list order has empty test-import and test-file
// columns; its line must still parse as five columns.
func TestListPackages_LastLineEmptyTrailingColumns(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, scopeFiles)
	pkgs, err := listPackages(context.Background(), dir)
	if err != nil {
		t.Fatalf("listPackages: %v", err)
	}
	var zz *goListPackage
	for _, p := range pkgs {
		if p.importPath == scopeMod+"/zz" {
			zz = p
		}
	}
	if zz == nil {
		t.Fatalf("package %s/zz missing from %d packages", scopeMod, len(pkgs))
	}
	if pkgs[len(pkgs)-1] != zz {
		t.Errorf("zz is not last in go list order: %s", pkgs[len(pkgs)-1].importPath)
	}
	if len(zz.imports) != 0 || len(zz.testImports) != 0 || len(zz.testFiles) != 0 {
		t.Errorf("zz columns not empty: imports=%v testImports=%v testFiles=%v", zz.imports, zz.testImports, zz.testFiles)
	}
}

func TestChangedGoPackages(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(t *testing.T, dir string)
		want       []string
		wantFull   bool
		wantReason string // substring
	}{
		{
			name: "leaf change yields leaf and dependents only",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"leaf/leaf.go": "package leaf\n\nvar X = 1\n"})
				git(t, dir, "commit", "-q", "-am", "leaf")
			},
			want: imp("dep", "leaf"),
		},
		{
			name: "shared low-level package yields its dependents and one test hop",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"low/low.go": "package low\n\nvar X = 1\n"})
				git(t, dir, "commit", "-q", "-am", "low")
			},
			want: imp("a", "b", "low", "testuser"),
		},
		{
			name: "uncommitted edit counts",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"leaf/leaf.go": "package leaf\n\nvar X = 2\n"})
			},
			want: imp("dep", "leaf"),
		},
		{
			name: "untracked new file counts",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"leaf/extra.go": "package leaf\n"})
			},
			want: imp("dep", "leaf"),
		},
		{
			name: "non-Go file under a package directory marks the package",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"leaf/fixture.txt": "x\n"})
			},
			want: imp("dep", "leaf"),
		},
		{
			name: "testdata belongs to the package above it",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"mem/testdata/f.json": "{}\n"})
			},
			want: imp("exec", "mem"),
		},
		{
			name: "deleting a file in a package marks the package",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"leaf/extra.go": "package leaf\n"})
				git(t, dir, "add", "-A")
				git(t, dir, "commit", "-q", "-m", "add")
				if err := os.Remove(filepath.Join(dir, "leaf", "extra.go")); err != nil {
					t.Fatal(err)
				}
			},
			want: imp("dep", "leaf"),
		},
		{
			name: "go.mod change forces the full suite",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"go.mod": "module " + scopeMod + "\n\ngo 1.22\n"})
			},
			wantFull:   true,
			wantReason: "go.mod",
		},
		{
			name: "Makefile change forces the full suite",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"Makefile": "all:\n"})
			},
			wantFull:   true,
			wantReason: "Makefile",
		},
		{
			name: "scripts change forces the full suite",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"scripts/run.sh": "#!/bin/sh\n"})
			},
			wantFull: true,
		},
		{
			name: "file outside any Go package forces the full suite",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"tools/readme.txt": "x\n"})
			},
			wantFull:   true,
			wantReason: "outside any Go package",
		},
		{
			name: "docs-only diff yields an empty set",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"docs/guide.md": "changed\n", ".agent/tasks/new.md": "new\n"})
			},
			wantReason: ScopeReasonDocsOnly,
		},
		{
			name: "claude settings written by the executor are not code",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{".claude/settings.json": "{}\n", ".agent/tasks/new.md": "new\n"})
			},
			wantReason: ScopeReasonDocsOnly,
		},
		{
			name: "docs plus a leaf change scopes to the leaf",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{"docs/guide.md": "changed\n", "leaf/leaf.go": "package leaf\n\nvar X = 3\n"})
			},
			want: imp("dep", "leaf"),
		},
		{
			name: "affected set over 70 percent runs everything",
			mutate: func(t *testing.T, dir string) {
				writeFiles(t, dir, map[string]string{
					"leaf/leaf.go": "package leaf\n\nvar X = 4\n",
					"low/low.go":   "package low\n\nvar X = 4\n",
					"mem/store.go": "package mem\n\nvar X = 4\n",
				})
			},
			wantFull:   true,
			wantReason: "over 70%",
		},
		{
			name:       "no diff at all cannot be scoped",
			mutate:     func(t *testing.T, dir string) {},
			wantFull:   true,
			wantReason: "no changed files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newScopeRepo(t)
			tt.mutate(t, dir)

			got, full, reason := ChangedGoPackages(context.Background(), dir, "main")
			if full != tt.wantFull {
				t.Fatalf("full = %v (reason %q), want %v", full, reason, tt.wantFull)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", reason, tt.wantReason)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("packages = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChangedGoPackages_Fallbacks(t *testing.T) {
	t.Run("empty base branch", func(t *testing.T) {
		dir := newScopeRepo(t)
		writeFiles(t, dir, map[string]string{"leaf/leaf.go": "package leaf\n\nvar X = 1\n"})
		got, full, reason := ChangedGoPackages(context.Background(), dir, "")
		if !full || got != nil || !strings.Contains(reason, "empty base branch") {
			t.Errorf("got (%v, %v, %q), want full with empty base branch reason", got, full, reason)
		}
	})
	t.Run("unknown base branch has no merge base", func(t *testing.T) {
		dir := newScopeRepo(t)
		writeFiles(t, dir, map[string]string{"leaf/leaf.go": "package leaf\n\nvar X = 1\n"})
		_, full, reason := ChangedGoPackages(context.Background(), dir, "nope")
		if !full || !strings.Contains(reason, "no merge base") {
			t.Errorf("got full=%v reason=%q, want full with no merge base", full, reason)
		}
	})
	t.Run("not a git repository", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, scopeFiles)
		_, full, _ := ChangedGoPackages(context.Background(), dir, "main")
		if !full {
			t.Error("expected full outside a git repository")
		}
	})
	t.Run("not a Go module", func(t *testing.T) {
		_, full, reason := ChangedGoPackages(context.Background(), t.TempDir(), "main")
		if !full || !strings.Contains(reason, "Go module") {
			t.Errorf("got full=%v reason=%q", full, reason)
		}
	})
	t.Run("go list failure", func(t *testing.T) {
		dir := newScopeRepo(t)
		writeFiles(t, dir, map[string]string{"leaf/leaf.go": "package leaf\n\nimport _ \"" + scopeMod + "/missing\"\n"})
		_, full, reason := ChangedGoPackages(context.Background(), dir, "main")
		if !full || !strings.Contains(reason, "go list") {
			t.Errorf("got full=%v reason=%q, want full from go list", full, reason)
		}
	})
	t.Run("origin base ref wins over a stale local branch", func(t *testing.T) {
		dir := newScopeRepo(t)
		// origin/main points at the initial commit; local main moves ahead with the leaf change.
		git(t, dir, "update-ref", "refs/remotes/origin/main", "main")
		git(t, dir, "checkout", "-q", "main")
		writeFiles(t, dir, map[string]string{"low/low.go": "package low\n\nvar X = 1\n"})
		git(t, dir, "commit", "-q", "-am", "low on local main")
		git(t, dir, "checkout", "-q", "feature")
		git(t, dir, "merge", "-q", "--ff-only", "main")
		got, full, _ := ChangedGoPackages(context.Background(), dir, "main")
		if full || !reflect.DeepEqual(got, imp("a", "b", "low", "testuser")) {
			t.Errorf("got (%v, %v), want the low-level set against origin/main", got, full)
		}
	})
}

func TestChangedGoPackages_GoModChangeForcesFull(t *testing.T) {
	dir := newScopeRepo(t)
	writeFiles(t, dir, map[string]string{"go.mod": "module " + scopeMod + "\n\ngo 1.22\n"})
	git(t, dir, "commit", "-q", "-am", "bump go")

	got, full, reason := ChangedGoPackages(context.Background(), dir, "main")
	if !full || got != nil {
		t.Fatalf("go.mod change gave (%v, full=%v, %q), want full with no package list", got, full, reason)
	}
}

func TestChangedGoPackages_TestImportsAreOneHopNotTransitive(t *testing.T) {
	dir := newScopeRepo(t)
	writeFiles(t, dir, map[string]string{"low/low.go": "package low\n\nvar X = 1\n"})
	git(t, dir, "commit", "-q", "-am", "low")

	got, full, _ := ChangedGoPackages(context.Background(), dir, "main")
	if full {
		t.Fatal("unexpected full")
	}
	has := func(name string) bool {
		for _, p := range got {
			if p == scopeMod+"/"+name {
				return true
			}
		}
		return false
	}
	if !has("testuser") {
		t.Errorf("testuser's test imports low (one hop) and must be affected, got %v", got)
	}
	if has("viatest") {
		t.Errorf("viatest's test imports testuser, whose own test imports low; that is two test hops and must not be affected, got %v", got)
	}
}

func TestChangedGoPackages_RelativePathReadInTestAddsEdge(t *testing.T) {
	dir := newScopeRepo(t)
	writeFiles(t, dir, map[string]string{"mem/store.go": "package mem\n\nvar X = 1\n"})
	git(t, dir, "commit", "-q", "-am", "mem")

	got, full, _ := ChangedGoPackages(context.Background(), dir, "main")
	if full || !reflect.DeepEqual(got, imp("exec", "mem")) {
		t.Errorf("got (%v, full=%v), want exec affected through its ../mem/store.go read", got, full)
	}
}

func TestChangedGoPackages_RelativeJoinAndRootRules(t *testing.T) {
	dir := newScopeRepo(t)
	writeFiles(t, dir, map[string]string{
		"exec/exec_test.go": "package exec\n\nimport (\n\t\"path/filepath\"\n\t\"testing\"\n)\n\nfunc TestX(t *testing.T) { _ = filepath.Join(\"..\", \"leaf\", \"leaf.go\"); _ = \"../../README.md\" }\n",
	})
	git(t, dir, "commit", "-q", "-am", "join")
	git(t, dir, "branch", "-f", "main", "feature") // the join edit is part of the base

	// exec reads ../leaf via filepath.Join: a leaf change reaches exec too, but
	// a ../../README.md literal must not tie exec to the root package.
	writeFiles(t, dir, map[string]string{"leaf/leaf.go": "package leaf\n\nvar X = 1\n"})
	got, full, _ := ChangedGoPackages(context.Background(), dir, "main")
	if full || !reflect.DeepEqual(got, imp("dep", "exec", "leaf")) {
		t.Errorf("got (%v, full=%v), want dep, exec, leaf", got, full)
	}
	git(t, dir, "checkout", "-q", "--", "leaf/leaf.go")
	writeFiles(t, dir, map[string]string{"m.go": "package m\n\nvar X = 1\n"})
	got, full, _ = ChangedGoPackages(context.Background(), dir, "main")
	if full || !reflect.DeepEqual(got, []string{scopeMod}) {
		t.Errorf("got (%v, full=%v), want only the root package: a ../../README.md literal must not tie exec to it", got, full)
	}
}

func TestMakeTestChanged(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not installed")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		env        []string
		wantGoArgs string // "" means go must not run
		wantOut    string
	}{
		{"docs-only never runs go", []string{"PILOT_CHANGED_FULL=0", "PILOT_CHANGED_PACKAGES=", "PILOT_CHANGED_REASON=docs-only"}, "", "no Go changes"},
		{"full runs everything", []string{"PILOT_CHANGED_FULL=1", "PILOT_CHANGED_PACKAGES=", "PILOT_CHANGED_REASON=go.mod"}, "test -short ./...", ""},
		{"empty list that is not docs-only runs everything", []string{"PILOT_CHANGED_FULL=0", "PILOT_CHANGED_PACKAGES=", "PILOT_CHANGED_REASON=weird"}, "test -short ./...", ""},
		{"no scope exported runs everything", nil, "test -short ./...", ""},
		{"listed packages only", []string{"PILOT_CHANGED_FULL=0", "PILOT_CHANGED_PACKAGES=example.com/m/leaf example.com/m/dep", "PILOT_CHANGED_REASON=affected 2 of 10 packages"}, "test -short example.com/m/leaf example.com/m/dep", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shim := t.TempDir()
			marker := filepath.Join(t.TempDir(), "go-args")
			script := "#!/bin/sh\necho \"$*\" > '" + marker + "'\n"
			if err := os.WriteFile(filepath.Join(shim, "go"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("make", "-s", "-C", repoRoot, "test-changed")
			env := []string{}
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "PILOT_CHANGED_") && !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "MAKEFLAGS=") && !strings.HasPrefix(kv, "MFLAGS=") {
					env = append(env, kv)
				}
			}
			cmd.Env = append(append(env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH")), tt.env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make test-changed: %v\n%s", err, out)
			}
			if tt.wantOut != "" && !strings.Contains(string(out), tt.wantOut) {
				t.Errorf("output %q does not contain %q", out, tt.wantOut)
			}
			args, readErr := os.ReadFile(marker)
			if tt.wantGoArgs == "" {
				if readErr == nil {
					t.Errorf("go ran with %q, want it not to run", strings.TrimSpace(string(args)))
				}
				return
			}
			if readErr != nil {
				t.Fatalf("go did not run: %v\n%s", readErr, out)
			}
			if got := strings.TrimSpace(string(args)); got != tt.wantGoArgs {
				t.Errorf("go args = %q, want %q", got, tt.wantGoArgs)
			}
		})
	}
}
