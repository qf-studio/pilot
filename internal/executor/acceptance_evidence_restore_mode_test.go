package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6638: the check after the restore of a mutation line compares the type,
// the permission bits and the content of the file; the restore never writes
// through a link; the git repair keeps an uncommitted change and gets the
// environment of the evidence commands; a panic of the runner still restores.

// mutationModeEnv is the task directory of one form of the option: asAgent
// runs fn as the session of the agent would (the workspace writable).
type mutationModeEnv struct {
	dir     string
	asAgent func(fn func())
}

// mutationModeEnvs are the option empty and the option set (the mock sudo of
// newAgentFilesFixture). seed runs before the workspace is locked.
func runMutationModeEnvs(t *testing.T, test func(t *testing.T, e mutationModeEnv, seed func(fn func()))) {
	t.Run("option empty", func(t *testing.T) {
		dir := t.TempDir()
		test(t, mutationModeEnv{dir: dir, asAgent: func(fn func()) { fn() }}, func(fn func()) { fn() })
	})
	t.Run("option set", func(t *testing.T) {
		f := newAgentFilesFixture(t, "")
		test(t, mutationModeEnv{dir: f.ws, asAgent: func(fn func()) { f.asAgent(t, fn) }}, func(fn func()) {
			fn()
			f.lock(t)
		})
	})
}

// writeThroughEvidence is the real write of the seam: as the agent user with
// the option set.
func writeThroughEvidence(path string, data []byte) error {
	return evidenceWriteFile(context.Background(), path, data, 0o600)
}

// countCheckoutForTest counts the calls of mutationGitCheckout.
func countCheckoutForTest(t *testing.T) *atomic.Int32 {
	t.Helper()
	prev := mutationGitCheckout
	var n atomic.Int32
	mutationGitCheckout = func(ctx context.Context, dir, rel string) error {
		n.Add(1)
		return prev(ctx, dir, rel)
	}
	t.Cleanup(func() { mutationGitCheckout = prev })
	return &n
}

// assertRegularFile fails unless path is a regular file with perm and content.
func assertRegularFile(t *testing.T, path string, perm os.FileMode, content string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("%s is %s, want a regular file", filepath.Base(path), fi.Mode().Type())
		return
	}
	if fi.Mode().Perm() != perm {
		t.Errorf("%s has mode %o, want %o", filepath.Base(path), fi.Mode().Perm(), perm)
	}
	if got := readTestFile(t, path); got != content {
		t.Errorf("%s = %q, want %q", filepath.Base(path), got, content)
	}
}

func seedMutationGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test User")
}

func TestMutationRestoreChecksTypeAndMode(t *testing.T) {
	item := ClassifyAcceptanceItem(restoreTestMutationLine)

	for _, tc := range []struct {
		name    string
		outside string
	}{
		{"the command replaces the file by a link to a file with the first content", restoreTestOriginal},
		{"the command replaces the file by a link to another file", "outside\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
				outside := filepath.Join(t.TempDir(), "outside.go")
				writeTestFile(t, outside, tc.outside)
				var main string
				seed(func() { main = seedMutationDir(t, e.dir) })
				runner := newRecordingEvidenceRunner(t)
				runner.during = func(context.Context) error {
					e.asAgent(func() {
						if err := os.Remove(main); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, main); err != nil {
							t.Fatal(err)
						}
					})
					return nil
				}

				res := runMutationItem(t.Context(), runner, e.dir, item, defaultTestAllowedCommands)

				if res.Err != nil {
					t.Errorf("Err = %v, want the restore to make the file again", res.Err)
				}
				e.asAgent(func() { assertRegularFile(t, main, 0o644, restoreTestOriginal) })
				if got := readTestFile(t, outside); got != tc.outside {
					t.Errorf("the file outside the workspace = %q, want it unchanged %q", got, tc.outside)
				}
			})
		})
	}

	t.Run("a file of mode 0755 that the command replaces through a rename keeps 0755", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			var main string
			seed(func() {
				main = seedMutationDir(t, e.dir)
				if err := os.Chmod(main, 0o755); err != nil {
					t.Fatal(err)
				}
			})
			runner := newRecordingEvidenceRunner(t)
			runner.during = func(context.Context) error {
				e.asAgent(func() {
					tmp := filepath.Join(e.dir, "main.go.tmp")
					if err := os.WriteFile(tmp, []byte(restoreTestOriginal), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(tmp, 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(tmp, main); err != nil {
						t.Fatal(err)
					}
				})
				return nil
			}

			res := runMutationItem(t.Context(), runner, e.dir, item, defaultTestAllowedCommands)

			if res.Err != nil {
				t.Errorf("Err = %v, want the restore to bring the mode back", res.Err)
			}
			e.asAgent(func() { assertRegularFile(t, main, 0o755, restoreTestOriginal) })
		})
	})

	t.Run("a path that is not a regular file is not mutated", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			main := filepath.Join(e.dir, "main.go")
			seed(func() {
				seedToolchainMarker(t, e.dir, "go.mod")
				if err := syscall.Mkfifo(main, 0o644); err != nil {
					t.Fatal(err)
				}
			})
			runner := newRecordingEvidenceRunner(t)

			res := runMutationItem(t.Context(), runner, e.dir, item, defaultTestAllowedCommands)

			if res.Err != nil || res.MutationOutcome != nil || runner.count() != 0 {
				t.Errorf("result = %+v, commands %d: want a Not-verified line and no run", res, runner.count())
			}
			if want := "main.go is not a regular file"; !strings.HasPrefix(res.NotVerifiedReason, want) {
				t.Errorf("reason = %q, want prefix %q", res.NotVerifiedReason, want)
			}
		})
	})

	t.Run("an uncommitted change and a write that fails after the first: no git checkout, the run stops", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			const committed = "package main\n"
			var main string
			seed(func() {
				seedMutationGitRepo(t, e.dir)
				main = filepath.Join(e.dir, "main.go")
				writeTestFile(t, main, committed)
				seedToolchainMarker(t, e.dir, "go.mod")
				runGit(t, e.dir, "add", ".")
				runGit(t, e.dir, "commit", "-m", "seed")
				writeTestFile(t, main, restoreTestOriginal)
			})
			setMutationWriteForTest(t, func(n int, path string, data []byte) error {
				if n == 1 {
					return writeThroughEvidence(path, data)
				}
				return errRestoreTestWrite
			})
			checkouts := countCheckoutForTest(t)
			runner := newRecordingEvidenceRunner(t)

			results := RunAcceptanceEvidenceItems(t.Context(), runner, e.dir, restoreTestItems(), defaultTestAllowedCommands)

			if n := checkouts.Load(); n != 0 {
				t.Errorf("git checkout ran %d times, want none over an uncommitted change", n)
			}
			var lc *mutationLeftChangedError
			if len(results) != 1 || !errors.As(results[0].Err, &lc) {
				t.Fatalf("results = %+v, want the run to stop on the mutation line", results)
			}
			if runner.count() != 1 {
				t.Errorf("commands ran %d times, want 1", runner.count())
			}
			if got := readTestFile(t, main); got == committed {
				t.Errorf("main.go holds the index version: the uncommitted change is lost")
			}
		})
	})

	t.Run("the filter program of the checkout does not see a token of the daemon", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", testutil.FakeGitHubToken)
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			envOut := filepath.Join(t.TempDir(), "filter-env")
			var main string
			seed(func() {
				seedMutationGitRepo(t, e.dir)
				runGit(t, e.dir, "config", "filter.leak.smudge", "env > '"+envOut+"'; cat")
				runGit(t, e.dir, "config", "filter.leak.clean", "cat")
				writeTestFile(t, filepath.Join(e.dir, ".gitattributes"), "main.go filter=leak\n")
				main = seedMutationDir(t, e.dir)
				runGit(t, e.dir, "add", ".")
				runGit(t, e.dir, "commit", "-m", "seed")
				if err := os.Remove(envOut); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			})
			setMutationWriteForTest(t, func(n int, path string, data []byte) error {
				if n == 1 {
					return writeThroughEvidence(path, data)
				}
				return errRestoreTestWrite
			})
			checkouts := countCheckoutForTest(t)

			res := runMutationItem(t.Context(), newRecordingEvidenceRunner(t), e.dir, item, defaultTestAllowedCommands)

			if res.Err != nil {
				t.Errorf("Err = %v, want the checkout to repair the file", res.Err)
			}
			if n := checkouts.Load(); n != 1 {
				t.Errorf("git checkout ran %d times, want 1", n)
			}
			e.asAgent(func() { assertRegularFile(t, main, 0o644, restoreTestOriginal) })
			env := readTestFile(t, envOut)
			if !strings.Contains(env, "PATH=") {
				t.Fatalf("the filter did not run: %q", env)
			}
			for _, bad := range []string{"GITHUB_TOKEN", testutil.FakeGitHubToken} {
				if strings.Contains(env, bad) {
					t.Errorf("the filter program got %s:\n%s", bad, env)
				}
			}
		})
	})

	t.Run("a runner that panics leaves the first content", func(t *testing.T) {
		runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
			var main string
			seed(func() { main = seedMutationDir(t, e.dir) })
			runner := funcAcceptanceCommandRunner(func(context.Context, string, string) (string, error) {
				panic("runner panic 6638")
			})

			got := func() (p any) {
				defer func() { p = recover() }()
				runMutationItem(t.Context(), runner, e.dir, item, defaultTestAllowedCommands)
				return nil
			}()

			if got != "runner panic 6638" {
				t.Errorf("recovered %v, want the panic of the runner to go on", got)
			}
			e.asAgent(func() { assertRegularFile(t, main, 0o644, restoreTestOriginal) })
		})
	})
}
