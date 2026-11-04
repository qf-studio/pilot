package executor

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// GH-6048: runner-level pins for the GH-6037 evidence wiring. The unit tests in
// pre_pr_evidence_test.go call collectAcceptanceEvidenceForReview,
// appendAcceptanceEvidence and evidenceAfterFixRound themselves, so they cannot
// notice a call site in Execute that stops passing the cached result or stops
// storing the rerun. These drive Execute end to end on a real repo and read the
// body that reaches the real PR-creation step.

// bodyCapturingPRCreator is a PRCreator that records the body it was handed.
type bodyCapturingPRCreator struct {
	mu     sync.Mutex
	bodies []string
}

func (c *bodyCapturingPRCreator) CreatePR(_ context.Context, _, _, _, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
	return "https://example.test/pr/1", nil
}

func (c *bodyCapturingPRCreator) only(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) != 1 {
		t.Fatalf("CreatePR called %d times, want 1", len(c.bodies))
	}
	return c.bodies[0]
}

// setUpBodyCapturingGh puts a fake gh on PATH that answers `pr list` with no
// PRs and writes the --body of `pr create` to the returned file.
func setUpBodyCapturingGh(t *testing.T) (bodyFile string) {
	t.Helper()
	fakeBin := t.TempDir()
	bodyFile = filepath.Join(t.TempDir(), "body.txt")
	script := `#!/bin/sh
if [ "$1 $2" = "pr create" ]; then
  while [ $# -gt 0 ]; do
    if [ "$1" = "--body" ]; then printf '%s' "$2" > "` + bodyFile + `"; fi
    shift
  done
  echo https://github.com/o/r/pull/1
else
  echo "[]"
fi
`
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return bodyFile
}

// evidenceExecuteEnv is one Execute run with the pre-PR review on, a scripted
// evidence command and a task that asks for its output in the PR body.
type evidenceExecuteEnv struct {
	runner  *Runner
	task    *Task
	cmd     *sequenceEvidenceRunner
	backend *mockGH4964Backend
}

func newEvidenceExecuteEnv(t *testing.T, mode string, review *PostMergeReview, cmd *sequenceEvidenceRunner) *evidenceExecuteEnv {
	t.Helper()
	// No testing.Short() skip: make test-short and test-changed run with -short
	// and would never reach these pins (TestEvidenceExecutePinsRunUnderShort).
	const branch = "pilot/GH-6048-evidence"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			if strings.Contains(opts.Prompt, "REVIEW_FIXED") {
				writeUncommittedFile(t, dir, "review-fix.go")
				runGit(t, dir, "add", "review-fix.go")
				runGit(t, dir, "commit", "-m", "fix: address review")
				return &BackendResult{Success: true, Output: "REVIEW_FIXED: feature.go:3 wrong constant"}
			}
			writeUncommittedFile(t, dir, "real-work.go")
			runGit(t, dir, "add", "real-work.go")
			runGit(t, dir, "commit", "-m", "real work")
			return &BackendResult{Success: true, Output: "done"}
		},
	}
	runner := newGH4964Runner(backend)
	runner.config.PrePRReview = &PrePRReviewConfig{Mode: mode, SkipComplexity: []string{}}
	runner.prePRReviewer = &fakePrePRReviewer{review: review}
	runner.acceptanceRunner = cmd

	task := newGH4964Task("GH-6048", branch, dir)
	task.Title = "Add multi-component feature with config wiring"
	task.Description = "Implement the feature across the config, the runner and the adapters, with tests."
	task.SkipQualityGates = true
	task.AcceptanceCriteria = []string{"`" + evidenceTestCmd + "` passes; paste the output into the PR body"}

	return &evidenceExecuteEnv{runner: runner, task: task, cmd: cmd, backend: backend}
}

func (e *evidenceExecuteEnv) execute(t *testing.T) {
	t.Helper()
	result, err := e.runner.Execute(context.Background(), e.task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success || result.PRUrl == "" {
		t.Fatalf("expected a created PR, got success=%v url=%q error=%q", result.Success, result.PRUrl, result.Error)
	}
}

// TestExecute_EvidenceRunsOnceAcrossReviewAndEveryCreatePRSite fails when any
// of the three PR-creation sites in Execute stops passing the evidence the
// review already read: the command would then run a second time at PR creation.
func TestExecute_EvidenceRunsOnceAcrossReviewAndEveryCreatePRSite(t *testing.T) {
	const output = "ok  \tgithub.com/example/pkg\t0.01s"
	tests := []struct {
		name string
		// wire installs the PR creator for the site and returns the PR body
		// as the creator saw it.
		wire func(t *testing.T, e *evidenceExecuteEnv) (body func() string)
	}{
		{"github sdk site", func(t *testing.T, e *evidenceExecuteEnv) func() string {
			c := &bodyCapturingPRCreator{}
			// The cross-project guard wants the repo name to match the project directory.
			repo := "o/" + filepath.Base(e.task.ProjectPath)
			e.task.SourceAdapter, e.task.SourceRepo = "github", repo
			e.runner.RegisterPRCreator("github:"+repo, c)
			return func() string { return c.only(t) }
		}},
		{"project or shared creator site", func(t *testing.T, e *evidenceExecuteEnv) func() string {
			c := &bodyCapturingPRCreator{}
			e.task.SourceAdapter = "linear"
			e.runner.SetPRCreator(c)
			return func() string { return c.only(t) }
		}},
		{"gh cli site", func(t *testing.T, e *evidenceExecuteEnv) func() string {
			file := setUpBodyCapturingGh(t)
			return func() string {
				b, err := os.ReadFile(file)
				if err != nil {
					t.Fatalf("gh pr create never ran: %v", err)
				}
				return string(b)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &sequenceEvidenceRunner{outputs: []string{output + "\n"}}
			e := newEvidenceExecuteEnv(t, PrePRReviewModeShadow, approveWithNotes(), cmd)
			body := tt.wire(t, e)

			e.execute(t)

			if n := cmd.count(); n != 1 {
				t.Errorf("paste-output command ran %d times across the review and PR creation, want 1", n)
			}
			if b := body(); !strings.Contains(b, "## Evidence") || !strings.Contains(b, output) {
				t.Errorf("PR body lacks the evidence:\n%s", b)
			}
			fake := e.runner.prePRReviewer.(*fakePrePRReviewer)
			if fake.calls() != 1 || !strings.Contains(fake.inputs[0].PRBody, output) {
				t.Errorf("the review did not read the evidence as its PR body: %d calls", fake.calls())
			}
		})
	}
}

// TestExecute_FixRoundSwapsEvidenceIntoPRBody fails when Execute stops storing
// the rerun after a ReviewFixFixed round: the PR body would keep the first,
// failing result for a tree that is no longer the one pushed. Quality gates are
// off here, so the round ends at the no-re-gate return in runReviewFix.
func TestExecute_FixRoundSwapsEvidenceIntoPRBody(t *testing.T) {
	runFixRoundSwapsEvidence(t, nil)
}

// TestExecute_FixRoundAfterReGateSwapsEvidenceIntoPRBody is the same pin for a
// fix that passes the quality-gate re-check (the second ReviewFixFixed return
// in runReviewFix, pre_pr_fix.go): with gates configured the round does not
// take the no-re-gate early return. Setting task.SkipQualityGates back to true
// leaves the re-gate unreached and fails the checker-call assertion.
func TestExecute_FixRoundAfterReGateSwapsEvidenceIntoPRBody(t *testing.T) {
	checker := &fakeRegateChecker{passed: true}
	runFixRoundSwapsEvidence(t, checker)
	// One check for the task's own gate run, one for the fix's re-gate.
	if n := checker.callCount(); n != 2 {
		t.Errorf("quality checker ran %d times, want 2 (the gate run, then the re-gate after the fix)", n)
	}
}

// runFixRoundSwapsEvidence drives one enforce-mode fix round. A non-nil checker
// turns the quality gates on, so the fix is re-gated.
func runFixRoundSwapsEvidence(t *testing.T, checker *fakeRegateChecker) {
	t.Helper()
	cmd := &sequenceEvidenceRunner{
		outputs: []string{evidenceFailOutput, evidencePassOutput},
		errs:    []error{exitOneError(t), nil},
	}
	review := &PostMergeReview{
		Verdict: PostMergeRequestChanges,
		Defects: []PostMergeDefect{
			{Severity: PostMergeSeverityBlocking, Location: "feature.go:3", Claim: "the evidence command fails", Evidence: "--- FAIL: TestX"},
		},
		FilesRead: 4,
	}
	e := newEvidenceExecuteEnv(t, PrePRReviewModeEnforce, review, cmd)
	if checker != nil {
		e.task.SkipQualityGates = false
		e.runner.qualityCheckerFactory = func(string, string, string) QualityChecker { return checker }
	}
	creator := &bodyCapturingPRCreator{}
	e.task.SourceAdapter = "linear"
	e.runner.SetPRCreator(creator)

	e.execute(t)

	if !strings.Contains(e.runner.prePRReviewer.(*fakePrePRReviewer).inputs[0].PRBody, "--- FAIL: TestX") {
		t.Fatal("test setup: the review should have read the failing first run")
	}
	if e.backend.callCount() != 2 {
		t.Fatalf("backend called %d times, want the task run plus one fix round", e.backend.callCount())
	}
	if n := cmd.count(); n != 2 {
		t.Errorf("command ran %d times, want 2 (before the review, then after the fix)", n)
	}
	body := creator.only(t)
	if strings.Contains(body, "--- FAIL: TestX") || strings.Contains(body, "FAILS") {
		t.Errorf("PR body still carries the first, failing result:\n%s", body)
	}
	if !strings.Contains(body, "ok  \tgithub.com/example/pkg") {
		t.Errorf("PR body lacks the rerun after the fix:\n%s", body)
	}
}

// TestEvidenceExecutePinsRunUnderShort fails when this file calls
// testing.Short(): make test-short and make test-changed pass -short, so a skip
// would leave the runner-level evidence pins to CI alone.
func TestEvidenceExecutePinsRunUnderShort(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pre_pr_evidence_execute_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Short" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "testing" {
			t.Errorf("%s: testing.Short() skips these pins under make test-short/test-changed", fset.Position(call.Pos()))
		}
		return true
	})
}
