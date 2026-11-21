package executor

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6606: the mutation check of the acceptance evidence never leaves a
// changed file. A failed write or restore is repaired, or the run stops before
// the pre-PR review, its fix rounds and the push.

const restoreTestOriginal = "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

const restoreTestMutationLine = "delete line 4 in main.go -> TestMain fails"

// setMutationWriteForTest replaces mutationWriteFile with fn for the test. fn
// gets the 1-based number of the write.
func setMutationWriteForTest(t *testing.T, fn func(n int, path string, data []byte) error) {
	t.Helper()
	prev := mutationWriteFile
	var mu sync.Mutex
	n := 0
	mutationWriteFile = func(_ context.Context, _, path string, data []byte, _ os.FileMode) error {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		return fn(i, path, data)
	}
	t.Cleanup(func() { mutationWriteFile = prev })
}

// failCheckoutForTest makes mutationGitCheckout fail for the test.
func failCheckoutForTest(t *testing.T) {
	t.Helper()
	prev := mutationGitCheckout
	mutationGitCheckout = func(context.Context, string, string) error { return errors.New("checkout refused") }
	t.Cleanup(func() { mutationGitCheckout = prev })
}

// writeOK is the plain write of the seam.
func writeOK(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }

var errRestoreTestWrite = errors.New("sudo: a write failed")

// seedMutationDir writes main.go and go.mod into dir.
func seedMutationDir(t *testing.T, dir string) string {
	t.Helper()
	main := filepath.Join(dir, "main.go")
	writeTestFile(t, main, restoreTestOriginal)
	seedToolchainMarker(t, dir, "go.mod")
	return main
}

// recordingEvidenceRunner records each command and answers a mutation run
// with a caught failure.
type recordingEvidenceRunner struct {
	mu       sync.Mutex
	commands []string
	// during runs while the command runs.
	during  func(ctx context.Context) error
	exitErr error
}

func newRecordingEvidenceRunner(t *testing.T) *recordingEvidenceRunner {
	t.Helper()
	return &recordingEvidenceRunner{exitErr: exitOneError(t)}
}

func (r *recordingEvidenceRunner) RunCommand(ctx context.Context, _, command string) (string, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	if r.during != nil {
		if err := r.during(ctx); err != nil {
			return "", err
		}
	}
	return "--- FAIL: TestMain (0.00s)\nFAIL\n", r.exitErr
}

func (r *recordingEvidenceRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.commands)
}

// assertNoDirInReasons fails when a reason names dir.
func assertNoDirInReasons(t *testing.T, dir string, results []AcceptanceEvidenceResult) {
	t.Helper()
	forms := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		forms = append(forms, r)
	}
	for _, res := range results {
		for _, f := range forms {
			if strings.Contains(res.NotVerifiedReason, f) {
				t.Errorf("reason %q holds the task directory %s", res.NotVerifiedReason, f)
			}
		}
	}
}

func restoreTestItems() []AcceptanceItem {
	return []AcceptanceItem{
		ClassifyAcceptanceItem(restoreTestMutationLine),
		ClassifyAcceptanceItem("`go test ./pkg/` passes; paste the output into the PR body"),
	}
}

func TestMutationRestoreFailureStopsTheRun(t *testing.T) {
	t.Run("the restore write fails once: the second write repairs the file and the run goes on", func(t *testing.T) {
		dir := t.TempDir()
		main := seedMutationDir(t, dir)
		setMutationWriteForTest(t, func(n int, path string, data []byte) error {
			if n == 2 {
				return errRestoreTestWrite
			}
			return writeOK(path, data)
		})
		failCheckoutForTest(t)
		runner := newRecordingEvidenceRunner(t)

		results := RunAcceptanceEvidenceItems(t.Context(), runner, dir, restoreTestItems(), defaultTestAllowedCommands)

		if got := readTestFile(t, main); got != restoreTestOriginal {
			t.Errorf("main.go = %q, want the first content", got)
		}
		if len(results) != 2 || runner.count() != 2 {
			t.Fatalf("results %d, commands %d: want the next line to run", len(results), runner.count())
		}
		if results[0].Err != nil || results[0].MutationOutcome == nil || len(results[0].MutationOutcome.FailingTests) == 0 {
			t.Errorf("mutation result = %+v, want a caught mutation", results[0])
		}
		assertNoDirInReasons(t, dir, results)
	})

	t.Run("each restore write fails: git checkout repairs the file and the run goes on", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init", "-b", "main")
		runGit(t, dir, "config", "user.email", "test@test.com")
		runGit(t, dir, "config", "user.name", "Test User")
		main := seedMutationDir(t, dir)
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-m", "seed")
		setMutationWriteForTest(t, func(n int, path string, data []byte) error {
			if n == 1 {
				return writeOK(path, data)
			}
			return errRestoreTestWrite
		})
		runner := newRecordingEvidenceRunner(t)

		results := RunAcceptanceEvidenceItems(t.Context(), runner, dir, restoreTestItems(), defaultTestAllowedCommands)

		if got := readTestFile(t, main); got != restoreTestOriginal {
			t.Errorf("main.go = %q, want the first content", got)
		}
		if len(results) != 2 || runner.count() != 2 {
			t.Fatalf("results %d, commands %d: want the next line to run", len(results), runner.count())
		}
		if results[0].Err != nil {
			t.Errorf("Err = %v, want the checkout to repair the file", results[0].Err)
		}
		assertNoDirInReasons(t, dir, results)
	})

	t.Run("the writes and the checkout fail: the file is reported, the next line does not run, the run fails with no push", testMutationLeftChangedStopsExecute)

	t.Run("a changed write that cuts the file and fails: the repair brings back the first content", func(t *testing.T) {
		dir := t.TempDir()
		main := seedMutationDir(t, dir)
		setMutationWriteForTest(t, func(n int, path string, data []byte) error {
			if n == 1 {
				if err := os.Truncate(path, 0); err != nil {
					return err
				}
				return errRestoreTestWrite
			}
			return writeOK(path, data)
		})
		failCheckoutForTest(t)
		runner := newRecordingEvidenceRunner(t)

		results := RunAcceptanceEvidenceItems(t.Context(), runner, dir, restoreTestItems(), defaultTestAllowedCommands)

		if got := readTestFile(t, main); got != restoreTestOriginal {
			t.Errorf("main.go = %q, want the first content", got)
		}
		if len(results) != 2 || results[0].Err != nil {
			t.Fatalf("results = %+v, want two lines and no Err", results)
		}
		if want := "could not write mutated main.go"; !strings.HasPrefix(results[0].NotVerifiedReason, want) {
			t.Errorf("reason = %q, want prefix %q", results[0].NotVerifiedReason, want)
		}
		assertNoDirInReasons(t, dir, results)
	})

	t.Run("a cancel during the command: the cancel reason and the first content", func(t *testing.T) {
		dir := t.TempDir()
		main := seedMutationDir(t, dir)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		runner := newRecordingEvidenceRunner(t)
		runner.during = func(context.Context) error {
			cancel()
			return nil // the output of a caught mutation, from a killed command
		}

		results := RunAcceptanceEvidenceItems(ctx, runner, dir, restoreTestItems()[:1], defaultTestAllowedCommands)

		if got := readTestFile(t, main); got != restoreTestOriginal {
			t.Errorf("main.go = %q, want the first content", got)
		}
		if len(results) != 1 {
			t.Fatalf("results = %d, want 1", len(results))
		}
		if results[0].MutationOutcome != nil {
			t.Errorf("MutationOutcome = %+v, want none for a cancelled command", results[0].MutationOutcome)
		}
		if results[0].NotVerifiedReason != evidenceCancelledReason {
			t.Errorf("reason = %q, want %q", results[0].NotVerifiedReason, evidenceCancelledReason)
		}
	})

	t.Run("no reason text holds the directory of the test", func(t *testing.T) {
		dir := t.TempDir()
		item := ClassifyAcceptanceItem("delete line 4 in missing.go -> TestMain fails")
		seedToolchainMarker(t, dir, "go.mod")
		results := RunAcceptanceEvidenceItems(t.Context(), newRecordingEvidenceRunner(t), dir, []AcceptanceItem{item}, defaultTestAllowedCommands)
		if len(results) != 1 || results[0].NotVerifiedReason == "" {
			t.Fatalf("results = %+v, want one Not-verified line", results)
		}
		assertNoDirInReasons(t, dir, results)
		if got := relativeToTaskDir(dir, "open "+filepath.Join(dir, "a.go")+": failed in "+dir); got != "open a.go: failed in ." {
			t.Errorf("relativeToTaskDir = %q", got)
		}
	})
}

// testMutationLeftChangedStopsExecute drives Execute: the evidence before the
// pre-PR review leaves main.go changed, so the paste-output line does not run,
// the review does not run, nothing is pushed and the result is the failure.
func testMutationLeftChangedStopsExecute(t *testing.T) {
	cmd := &sequenceEvidenceRunner{outputs: []string{"--- FAIL: TestMain (0.00s)\nFAIL\n"}, errs: []error{exitOneError(t)}}
	e := newEvidenceExecuteEnv(t, PrePRReviewModeShadow, approveWithNotes(), cmd)
	dir := e.task.ProjectPath
	seedMutationDir(t, dir)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "seed")
	e.task.AcceptanceCriteria = append([]string{restoreTestMutationLine}, e.task.AcceptanceCriteria...)
	creator := &bodyCapturingPRCreator{}
	e.task.SourceAdapter = "linear"
	e.runner.SetPRCreator(creator)
	logs := &syncBuffer{}
	e.runner.log = slog.New(slog.NewTextHandler(logs, nil))

	setMutationWriteForTest(t, func(n int, path string, data []byte) error {
		if n == 1 {
			return writeOK(path, data)
		}
		return errRestoreTestWrite
	})
	failCheckoutForTest(t)

	result, err := e.runner.Execute(context.Background(), e.task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	const want = "acceptance evidence left main.go changed; the run stops before the review and the push"
	if result.Success || result.Error != want {
		t.Errorf("result success=%v error=%q, want the failure %q", result.Success, result.Error, want)
	}
	if strings.Contains(result.Error, dir) {
		t.Errorf("error %q holds the task directory", result.Error)
	}
	if n := cmd.count(); n != 1 {
		t.Errorf("evidence commands ran %d times, want 1 (the next line must not run)", n)
	}
	if n := e.runner.prePRReviewer.(*fakePrePRReviewer).calls(); n != 0 {
		t.Errorf("pre-PR review ran %d times, want 0", n)
	}
	creator.mu.Lock()
	created := len(creator.bodies)
	creator.mu.Unlock()
	if created != 0 || result.PRUrl != "" {
		t.Errorf("a PR was created (%d calls, url %q)", created, result.PRUrl)
	}
	if out := gitOutput(t, dir, "ls-remote", "origin", e.task.Branch); strings.TrimSpace(out) != "" {
		t.Errorf("the branch was pushed: %s", out)
	}
	line := logs.String()
	if !strings.Contains(line, "level=ERROR") || !strings.Contains(line, "task_id="+e.task.ID) || !strings.Contains(line, "file=main.go") {
		t.Errorf("no Error line with the task id and the relative file:\n%s", line)
	}
	if !strings.Contains(line, evidenceLeftChangedBeforeReviewLog) {
		t.Errorf("the Error line does not say the run stops before the review and the push:\n%s", line)
	}
}

// TestMutationLeftChangedAtPRCreation is the fixture of
// testMutationLeftChangedStopsExecute with the review mode off (GH-6617): the
// evidence runs at PR creation, after the push, and leaves main.go changed. The
// run fails with a text that names the file, no PR is created, and the Error
// line does not claim the run stops before the push.
func TestMutationLeftChangedAtPRCreation(t *testing.T) {
	cmd := &sequenceEvidenceRunner{outputs: []string{"--- FAIL: TestMain (0.00s)\nFAIL\n"}, errs: []error{exitOneError(t)}}
	e := newEvidenceExecuteEnv(t, PrePRReviewModeOff, approveWithNotes(), cmd)
	dir := e.task.ProjectPath
	seedMutationDir(t, dir)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "seed")
	e.task.AcceptanceCriteria = append([]string{restoreTestMutationLine}, e.task.AcceptanceCriteria...)
	creator := &bodyCapturingPRCreator{}
	e.task.SourceAdapter = "linear"
	e.runner.SetPRCreator(creator)
	logs := &syncBuffer{}
	e.runner.log = slog.New(slog.NewTextHandler(logs, nil))

	setMutationWriteForTest(t, func(n int, path string, data []byte) error {
		if n == 1 {
			return writeOK(path, data)
		}
		return errRestoreTestWrite
	})
	failCheckoutForTest(t)

	result, err := e.runner.Execute(context.Background(), e.task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	want := "acceptance evidence left main.go changed; the branch " + e.task.Branch + " is pushed, the run fails, no PR is created"
	if result.Success || result.Error != want {
		t.Errorf("result success=%v error=%q, want the failure %q", result.Success, result.Error, want)
	}
	if strings.Contains(result.Error, dir) {
		t.Errorf("error %q holds the task directory", result.Error)
	}
	if n := cmd.count(); n != 1 {
		t.Errorf("evidence commands ran %d times, want 1 (the next line must not run)", n)
	}
	creator.mu.Lock()
	created := len(creator.bodies)
	creator.mu.Unlock()
	if created != 0 || result.PRUrl != "" {
		t.Errorf("a PR was created (%d calls, url %q)", created, result.PRUrl)
	}
	line := logs.String()
	if !strings.Contains(line, "level=ERROR") || !strings.Contains(line, "task_id="+e.task.ID) || !strings.Contains(line, "file=main.go") {
		t.Errorf("no Error line with the task id and the relative file:\n%s", line)
	}
	if !strings.Contains(line, evidenceLeftChangedAtPRCreationLog) {
		t.Errorf("the Error line does not say that no PR is created:\n%s", line)
	}
	if strings.Contains(line, "stops before the push") {
		t.Errorf("the Error line says the run stops before the push, but the branch is pushed:\n%s", line)
	}
}

// appendAcceptanceEvidenceCalls is the number of calls of
// appendAcceptanceEvidence in the non-test files of the package: the epic PR,
// the timeout salvage, the three PR-creation sites of Execute and the pre-PR
// hold.
const appendAcceptanceEvidenceCalls = 6

// TestMutationLeftChangedEveryAppendCallHandlesError reads the source of the
// package (GH-6617): each call of appendAcceptanceEvidence binds its error to a
// named variable and the next statement is an if on it, so no call site drops
// the leftChanged error of the collection.
func TestMutationLeftChangedEveryAppendCallHandlesError(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	isAppendCall := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "appendAcceptanceEvidence"
	}
	mentions := func(n ast.Node, name string) bool {
		found := false
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				found = true
			}
			return !found
		})
		return found
	}

	calls, handled := 0, 0
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok && isAppendCall(e) {
				calls++
			}
			block, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range block.List {
				as, ok := stmt.(*ast.AssignStmt)
				if !ok || len(as.Rhs) != 1 || !isAppendCall(as.Rhs[0]) {
					continue
				}
				pos := fset.Position(as.Pos())
				errID, ok := as.Lhs[len(as.Lhs)-1].(*ast.Ident)
				if len(as.Lhs) != 2 || !ok || errID.Name == "_" {
					t.Errorf("%s:%d: the error of appendAcceptanceEvidence is not bound to a variable", name, pos.Line)
					continue
				}
				var next ast.Stmt
				if i+1 < len(block.List) {
					next = block.List[i+1]
				}
				ifStmt, ok := next.(*ast.IfStmt)
				if !ok || !mentions(ifStmt.Cond, errID.Name) {
					t.Errorf("%s:%d: the statement after appendAcceptanceEvidence does not check %s", name, pos.Line, errID.Name)
					continue
				}
				handled++
			}
			return true
		})
	}
	if calls != appendAcceptanceEvidenceCalls {
		t.Errorf("found %d calls of appendAcceptanceEvidence, want %d: a new call must handle the error and update the count", calls, appendAcceptanceEvidenceCalls)
	}
	if handled != calls {
		t.Errorf("%d of %d calls of appendAcceptanceEvidence handle the error", handled, calls)
	}
}

// TestEvidenceCommandEnvAndDirAsAgent: with the mock sudo and the option set
// an evidence command gets no variable of the test process outside the
// allowlist (DAEMON_ONLY_VALUE, SSH_AUTH_SOCK), no model credential (GH-6638:
// ANTHROPIC_API_KEY, a name the allowlist of the model session keeps), and runs
// in the task directory.
func TestEvidenceCommandEnvAndDirAsAgent(t *testing.T) {
	t.Setenv("DAEMON_ONLY_VALUE", "daemon-only-6606")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-agent-6606.sock")
	t.Setenv("ANTHROPIC_API_KEY", testutil.FakeAnthropicKey)
	f := newAgentFilesFixture(t, "")
	f.lock(t)
	runner := shellAcceptanceCommandRunner{timeout: 10 * time.Second}

	out, err := runner.RunCommand(t.Context(), f.ws, "env")
	if err != nil {
		t.Fatalf("env: %v: %s", err, out)
	}
	for _, bad := range []string{"DAEMON_ONLY_VALUE", "daemon-only-6606", "SSH_AUTH_SOCK", "fake-agent-6606", "ANTHROPIC_API_KEY", testutil.FakeAnthropicKey} {
		if strings.Contains(out, bad) {
			t.Errorf("the environment of the evidence command has %s:\n%s", bad, out)
		}
	}

	out, err = runner.RunCommand(t.Context(), f.ws, "pwd")
	if err != nil {
		t.Fatalf("pwd: %v: %s", err, out)
	}
	if got := strings.TrimSpace(out); got != f.ws {
		t.Errorf("working directory = %q, want the task directory %q", got, f.ws)
	}
}
