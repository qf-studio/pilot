package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// GH-6652: the restore of a mutation line resolves the path again before each
// write and in the check, never writes through a link at the path, the git
// repair reads the file name as a literal path, and the mode keeps the setuid,
// setgid and sticky bits.

// setMutationWriteModeForTest replaces mutationWriteFile with fn for the test;
// fn gets the 1-based number of the write and the write it replaces.
func setMutationWriteModeForTest(t *testing.T, fn func(n int, path string, data []byte, mode os.FileMode, write func() error) error) {
	t.Helper()
	prev := mutationWriteFile
	var mu sync.Mutex
	n := 0
	mutationWriteFile = func(ctx context.Context, path string, data []byte, mode os.FileMode) error {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		return fn(i, path, data, mode, func() error { return prev(ctx, path, data, mode) })
	}
	t.Cleanup(func() { mutationWriteFile = prev })
}

// dirEntries returns name=content for each entry of dir.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name()+"="+readTestFile(t, filepath.Join(dir, e.Name())))
	}
	return out
}

func TestMutationRestoreStaysInTheWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
	}{
		{"the command replaces the parent directory by a link outside, the file is there", true},
		{"the command replaces the parent directory by a link outside, the file is not there", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
				outside := t.TempDir()
				if tc.present {
					writeTestFile(t, filepath.Join(outside, "main.go"), "outside\n")
				}
				want := dirEntries(t, outside)
				pkg := filepath.Join(e.dir, "pkg")
				seed(func() {
					writeTestFile(t, filepath.Join(pkg, "main.go"), restoreTestOriginal)
					seedToolchainMarker(t, e.dir, "go.mod")
				})
				runner := newRecordingEvidenceRunner(t)
				runner.during = func(context.Context) error {
					e.asAgent(func() {
						if err := os.Rename(pkg, filepath.Join(e.dir, "pkg-moved")); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, pkg); err != nil {
							t.Fatal(err)
						}
					})
					return nil
				}
				items := []AcceptanceItem{
					ClassifyAcceptanceItem("delete line 4 in pkg/main.go -> TestMain fails"),
					ClassifyAcceptanceItem("`go test ./pkg/` passes; paste the output into the PR body"),
				}

				results := RunAcceptanceEvidenceItems(t.Context(), runner, e.dir, items, defaultTestAllowedCommands)

				if got := dirEntries(t, outside); strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("the directory outside the workspace = %q, want it unchanged %q", got, want)
				}
				var lc *mutationLeftChangedError
				if len(results) != 1 || !errors.As(results[0].Err, &lc) {
					t.Fatalf("results = %+v, want the run to stop on the mutation line", results)
				}
				if !lc.LeftWorkspace || !strings.Contains(lc.Error(), "the path left the workspace") {
					t.Errorf("Err = %q, want it to say the path left the workspace", lc.Error())
				}
				if runner.count() != 1 {
					t.Errorf("commands ran %d times, want 1", runner.count())
				}
			})
		})
	}

	t.Run("a link put at the path before the restore write is not followed", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			outside := filepath.Join(t.TempDir(), "outside.go")
			writeTestFile(t, outside, "outside\n")
			var main string
			seed(func() { main = seedMutationDir(t, e.dir) })
			setMutationWriteModeForTest(t, func(n int, path string, _ []byte, _ os.FileMode, write func() error) error {
				if n == 2 {
					e.asAgent(func() {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, path); err != nil {
							t.Fatal(err)
						}
					})
				}
				return write()
			})

			res := runMutationItem(t.Context(), newRecordingEvidenceRunner(t), e.dir, ClassifyAcceptanceItem(restoreTestMutationLine), defaultTestAllowedCommands)

			if got := readTestFile(t, outside); got != "outside\n" {
				t.Errorf("the file outside the workspace = %q, want it unchanged", got)
			}
			if res.Err != nil {
				t.Errorf("Err = %v, want the file restored", res.Err)
			}
			e.asAgent(func() { assertRegularFile(t, main, 0o644, restoreTestOriginal) })
		})
	})

	t.Run("the git repair of a*.go keeps an uncommitted change of ab.go", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			const abChanged = "package ab // uncommitted\n"
			star := filepath.Join(e.dir, "a*.go")
			ab := filepath.Join(e.dir, "ab.go")
			seed(func() {
				seedMutationGitRepo(t, e.dir)
				writeTestFile(t, star, restoreTestOriginal)
				writeTestFile(t, ab, "package ab\n")
				seedToolchainMarker(t, e.dir, "go.mod")
				runGit(t, e.dir, "add", ".")
				runGit(t, e.dir, "commit", "-m", "seed")
				writeTestFile(t, ab, abChanged)
			})
			setMutationWriteModeForTest(t, func(n int, _ string, _ []byte, _ os.FileMode, write func() error) error {
				if n == 1 {
					return write()
				}
				return errRestoreTestWrite
			})
			checkouts := countCheckoutForTest(t)

			res := runMutationItem(t.Context(), newRecordingEvidenceRunner(t), e.dir, ClassifyAcceptanceItem("delete line 4 in a*.go -> TestMain fails"), defaultTestAllowedCommands)

			if n := checkouts.Load(); n != 1 {
				t.Errorf("git checkout ran %d times, want 1", n)
			}
			if res.Err != nil {
				t.Errorf("Err = %v, want the checkout to repair a*.go", res.Err)
			}
			if got := readTestFile(t, ab); got != abChanged {
				t.Errorf("ab.go = %q, want its uncommitted change %q", got, abChanged)
			}
			e.asAgent(func() { assertRegularFile(t, star, 0o644, restoreTestOriginal) })
		})
	})

	t.Run("a file of mode 4755 has mode 4755 at the end", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			var main string
			seed(func() {
				main = seedMutationDir(t, e.dir)
				if err := os.Chmod(main, 0o755|os.ModeSetuid); err != nil {
					t.Fatal(err)
				}
			})
			if fi, err := os.Lstat(main); err != nil || fi.Mode()&os.ModeSetuid == 0 {
				t.Skipf("the file system of the test drops the setuid bit (%v)", err)
			}

			res := runMutationItem(t.Context(), newRecordingEvidenceRunner(t), e.dir, ClassifyAcceptanceItem(restoreTestMutationLine), defaultTestAllowedCommands)

			if res.Err != nil {
				t.Errorf("Err = %v, want the file restored", res.Err)
			}
			e.asAgent(func() {
				fi, err := os.Lstat(main)
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode() & mutationModeBits; got != 0o755|os.ModeSetuid {
					t.Errorf("main.go has mode %v, want %v", got, 0o755|os.ModeSetuid)
				}
				if got := readTestFile(t, main); got != restoreTestOriginal {
					t.Errorf("main.go = %q, want the first content", got)
				}
			})
		})
	})
}
