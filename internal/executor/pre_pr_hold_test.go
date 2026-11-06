package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/replay"
)

// GH-6138: runner-level pins for the pre-PR on_unfixed hold. Each drives Execute
// on a real repo with an enforce-mode review that comes back REQUEST-CHANGES, a
// fake gh on PATH that records issue comments and label edits, and a fake PR
// creator, and reads what reached them.

// holdGateChecker answers the quality gates by call: the first is the task's own
// gate run, the next one is the fix's re-gate.
type holdGateChecker struct {
	mu       sync.Mutex
	calls    int
	regate   *QualityOutcome
	regateOK bool
}

func (c *holdGateChecker) Check(context.Context) (*QualityOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 || c.regateOK {
		return &QualityOutcome{Passed: true}, nil
	}
	return c.regate, nil
}

// holdGh puts a fake gh on PATH that answers `pr list` with no PRs, appends
// every `issue comment` body and every `issue edit` argument list to files, and
// leaves a marker file if `pr create` ever runs.
func holdGh(t *testing.T) (comments, edits, prCreated string) {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	comments = filepath.Join(out, "comments.txt")
	edits = filepath.Join(out, "edits.txt")
	prCreated = filepath.Join(out, "pr-created")
	script := `#!/bin/sh
case "$1 $2" in
  "issue comment")
    while [ $# -gt 0 ]; do
      if [ "$1" = "--body" ]; then printf '%s\n=====\n' "$2" >> "` + comments + `"; fi
      shift
    done ;;
  "issue edit") echo "$*" >> "` + edits + `" ;;
  "pr create") touch "` + prCreated + `"; echo https://github.com/o/r/pull/1 ;;
  "label create") ;;
  *) echo "[]" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return comments, edits, prCreated
}

func readOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

type holdEnv struct {
	runner         *Runner
	task           *Task
	dir, branch    string
	creator        *bodyCapturingPRCreator
	sink           *recordingAlertProcessor
	logs           *bytes.Buffer
	recordingsPath string
	comments       string
	edits          string
	prCreated      string
}

// newHoldEnv builds one run whose review is REQUEST-CHANGES with the given
// defects. fixCommits is whether the fix session commits; regate is the gate
// outcome of the re-check after it (nil = pass).
func newHoldEnv(t *testing.T, defects []PostMergeDefect, fixCommits bool, regate *QualityOutcome) *holdEnv {
	t.Helper()
	e := &holdEnv{branch: "pilot/GH-6138-hold", logs: &bytes.Buffer{}, recordingsPath: t.TempDir()}
	e.comments, e.edits, e.prCreated = holdGh(t)
	dir, _ := setupFreshnessRepo(t)
	e.dir = dir
	runGit(t, dir, "checkout", "-b", e.branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			if strings.Contains(opts.Prompt, "REVIEW_FIXED") {
				if fixCommits {
					writeUncommittedFile(t, dir, "review-fix.go")
					runGit(t, dir, "add", "review-fix.go")
					runGit(t, dir, "commit", "-m", "fix: address review")
				}
				return &BackendResult{Success: true, Output: "REVIEW_FIXED: feature.go:3 wrong constant"}
			}
			writeUncommittedFile(t, dir, "real-work.go")
			runGit(t, dir, "add", "real-work.go")
			runGit(t, dir, "commit", "-m", "real work")
			return &BackendResult{Success: true, Output: "done"}
		},
	}
	review := &PostMergeReview{Verdict: PostMergeRequestChanges, Defects: defects, FilesRead: 4}
	if len(review.BlockingDefects()) == 0 {
		review.Verdict = PostMergeApproveWithNotes
	}
	e.runner = newGH4964Runner(backend)
	e.runner.config.PrePRReview = &PrePRReviewConfig{Mode: PrePRReviewModeEnforce, SkipComplexity: []string{}}
	e.runner.prePRReviewer = &fakePrePRReviewer{review: review}
	e.runner.acceptanceRunner = &sequenceEvidenceRunner{outputs: []string{evidencePassOutput}}
	checker := &holdGateChecker{regate: regate, regateOK: regate == nil}
	e.runner.qualityCheckerFactory = func(string, string, string) QualityChecker { return checker }
	e.runner.SetRecordingEnabled(true)
	e.runner.SetRecordingsPath(e.recordingsPath)
	e.runner.log = slog.New(slog.NewTextHandler(e.logs, nil))
	e.sink = &recordingAlertProcessor{}
	e.runner.SetAlertProcessor(e.sink)

	e.task = newGH4964Task("GH-6138", e.branch, dir)
	e.task.Title = "Add multi-component feature with config wiring"
	e.task.Description = "Implement the feature across the config, the runner and the adapters, with tests."
	e.task.AcceptanceCriteria = []string{"`" + evidenceTestCmd + "` passes; paste the output into the PR body"}
	// The cross-project guard wants the repo name to match the project directory.
	repo := "o/" + filepath.Base(dir)
	e.task.SourceAdapter, e.task.SourceRepo = "github", repo
	e.creator = &bodyCapturingPRCreator{}
	e.runner.RegisterPRCreator("github:"+repo, e.creator)
	return e
}

func (e *holdEnv) run(t *testing.T) *ExecutionResult {
	t.Helper()
	result, err := e.runner.Execute(context.Background(), e.task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	return result
}

func (e *holdEnv) prCount() int {
	e.creator.mu.Lock()
	defer e.creator.mu.Unlock()
	return len(e.creator.bodies)
}

// recordingStatus is the status the one recording was finished with; "" when it
// was never finished.
func (e *holdEnv) recordingStatus(t *testing.T) string {
	t.Helper()
	list, err := replay.ListRecordings(e.recordingsPath, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("recordings = %d, err = %v, want exactly 1", len(list), err)
	}
	rec, err := replay.LoadRecording(e.recordingsPath, list[0].ID)
	if err != nil {
		t.Fatalf("load recording: %v", err)
	}
	return rec.Status
}

var holdDefects = []PostMergeDefect{
	{Severity: PostMergeSeverityBlocking, Location: "feature.go:3", Claim: "the constant is wrong", Evidence: "the issue asks for 2"},
	{Severity: PostMergeSeverityMinor, Location: "feature.go:1", Claim: "no package doc"},
}

var revertedGate = &QualityOutcome{Passed: false}

func TestPrePRHold_NoPRWhenBlockingUnfixed(t *testing.T) {
	e := newHoldEnv(t, holdDefects, true, revertedGate) // fix commits, re-gate fails: reverted
	holdProject := e.task.ProjectPath
	before := PrePRHoldsTotal()[holdProject]
	result := e.run(t)

	if e.prCount() != 0 || fileExists(e.prCreated) {
		t.Errorf("a PR was created (creator calls %d); a hold must open none", e.prCount())
	}
	if result.PRUrl != "" {
		t.Errorf("PRUrl = %q, want none", result.PRUrl)
	}
	if result.Success || result.Outcome != "needs_human" || TerminalStatus(result) != "needs_human" {
		t.Errorf("success=%v outcome=%q status=%q error=%q, want a needs_human run", result.Success, result.Outcome, TerminalStatus(result), result.Error)
	}
	if result.Error != prePRHoldReasonText {
		t.Errorf("error = %q, want %q", result.Error, prePRHoldReasonText)
	}
	if result.PrePRReview == nil || result.PrePRReview.Fix == nil || result.PrePRReview.Fix.Status != ReviewFixReverted {
		t.Fatalf("test setup: fix = %+v, want a reverted fix round", result.PrePRReview)
	}
	if result.BranchPushed == nil || !*result.BranchPushed {
		t.Errorf("BranchPushed = %v, want true", result.BranchPushed)
	}
	if out := strings.TrimSpace(gitOutput(t, e.dir, "ls-remote", "origin", e.branch)); out == "" {
		t.Errorf("branch %s was not pushed", e.branch)
	}

	comments := readOrEmpty(t, e.comments)
	for _, want := range []string{"## Pre-PR review", "Verdict: REQUEST-CHANGES", "the constant is wrong", "## Evidence", evidencePassOutput[:2], e.branch, "remove the `pilot-needs-human` label"} {
		if !strings.Contains(comments, want) {
			t.Errorf("issue comment lacks %q:\n%s", want, comments)
		}
	}
	head := strings.TrimSpace(gitOutput(t, e.dir, "rev-parse", "HEAD"))
	if !strings.Contains(comments, head) {
		t.Errorf("issue comment lacks HEAD %s:\n%s", head, comments)
	}

	edits := readOrEmpty(t, e.edits)
	if !strings.Contains(edits, "--add-label "+labelPilotNeedsHuman) {
		t.Errorf("labels not added: want %s in\n%s", labelPilotNeedsHuman, edits)
	}
	if !strings.Contains(edits, "--remove-label pilot-in-progress") {
		t.Errorf("pilot-in-progress not removed:\n%s", edits)
	}
	if strings.Contains(edits, "pilot-failed") {
		t.Errorf("a hold must not add pilot-failed:\n%s", edits)
	}
	if got := e.sink.countByType(AlertEventTypeTaskFailed); got != 0 {
		t.Errorf("TaskFailed alerts = %d, want none for a needs_human hold", got)
	}
	if got := e.sink.countByType(AlertEventTypeTaskCompleted); got != 0 {
		t.Errorf("TaskCompleted alerts = %d, want none", got)
	}
	if st := e.recordingStatus(t); st != "needs_human" {
		t.Errorf("recording status = %q, want it finished as needs_human", st)
	}
	logs := e.logs.String()
	for _, want := range []string{"Pre-PR review hold", "blocking=1", "fix_status=reverted", "sha=" + head} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if PrePRFixesTotal()["hold"] != 0 {
		t.Error(`a hold must not be counted as a pre-PR fix outcome`)
	}
	if PrePRHoldsTotal()[holdProject] != before+1 {
		t.Errorf("pre-PR holds for %q = %d, want %d", holdProject, PrePRHoldsTotal()[holdProject], before+1)
	}
}

func TestPrePRHold_NilFixWithBlockingHolds(t *testing.T) {
	// A fix round that does not apply leaves Fix nil (max_fix_rounds spent, no
	// session). The review's blocking defect is then still unfixed and must hold.
	review := &PrePRReviewOutcome{
		Mode:    PrePRReviewModeEnforce,
		Verdict: PostMergeRequestChanges,
		Review:  &PostMergeReview{Verdict: PostMergeRequestChanges, Defects: holdDefects},
	}
	reason, hold := prePRHoldReason(review, nil, PrePRReviewConfig{})
	if !hold || reason != prePRHoldReasonText {
		t.Errorf("prePRHoldReason(nil fix) = %q, %v, want the hold", reason, hold)
	}
	comment := renderPrePRHoldComment(&Task{Branch: "pilot/GH-1"}, review, &ExecutionResult{}, "abc1234", "")
	if !strings.Contains(comment, "No fix round ran") || !strings.Contains(comment, "## Pre-PR review") {
		t.Errorf("hold comment for a nil fix:\n%s", comment)
	}

	// The same through the fix statuses that leave the tree unfixed.
	for _, status := range []ReviewFixStatus{ReviewFixReverted, ReviewFixRegateError, ReviewFixRegateTimeout, ReviewFixFailed, ReviewFixNoChange, ReviewFixSkippedCtx, ReviewFixSkippedBudget} {
		if _, hold := prePRHoldReason(review, &ReviewFixOutcome{Status: status}, PrePRReviewConfig{}); !hold {
			t.Errorf("fix status %s: no hold, want one", status)
		}
	}
	if _, hold := prePRHoldReason(review, &ReviewFixOutcome{Status: ReviewFixFixed}, PrePRReviewConfig{}); hold {
		t.Error("fixed: hold, want none")
	}
}

func TestPrePRHold_RegateTimeoutRendersNotVerified(t *testing.T) {
	timedOut := &QualityOutcome{Passed: false, GateDetails: []QualityGateDetail{{Name: "test", Passed: false, Error: "context cancelled"}}}
	e := newHoldEnv(t, holdDefects, true, timedOut)
	result := e.run(t)

	if result.PrePRReview == nil || result.PrePRReview.Fix == nil || result.PrePRReview.Fix.Status != ReviewFixRegateTimeout {
		t.Fatalf("test setup: fix = %+v, want regate_timeout", result.PrePRReview)
	}
	if result.Outcome != "needs_human" || e.prCount() != 0 {
		t.Fatalf("outcome = %q, PRs = %d, want a hold", result.Outcome, e.prCount())
	}
	comments := readOrEmpty(t, e.comments)
	if !strings.Contains(comments, "not verified") {
		t.Errorf("comment does not say the fix is not verified:\n%s", comments)
	}
	if result.Error != prePRHoldReasonText {
		t.Errorf("reason = %q, want it unchanged by the fix status", result.Error)
	}
}

func TestPrePRHold_FixedProceedsToPR(t *testing.T) {
	e := newHoldEnv(t, holdDefects, true, nil) // fix commits, re-gate passes
	result := e.run(t)

	if result.PrePRReview == nil || result.PrePRReview.Fix == nil || result.PrePRReview.Fix.Status != ReviewFixFixed {
		t.Fatalf("test setup: fix = %+v, want fixed", result.PrePRReview)
	}
	if !result.Success || result.PRUrl == "" || e.prCount() != 1 {
		t.Errorf("success=%v url=%q PRs=%d error=%q, want one created PR", result.Success, result.PRUrl, e.prCount(), result.Error)
	}
	if strings.Contains(readOrEmpty(t, e.edits), labelPilotNeedsHuman) {
		t.Error("a fixed review must not park the issue")
	}
}

func TestPrePRHold_OpenPRKeepsToday(t *testing.T) {
	e := newHoldEnv(t, holdDefects, true, revertedGate)
	e.runner.config.PrePRReview.OnUnfixed = PrePRReviewOnUnfixedOpenPR
	result := e.run(t)

	if !result.Success || result.PRUrl == "" || e.prCount() != 1 {
		t.Fatalf("success=%v url=%q PRs=%d error=%q, want one created PR", result.Success, result.PRUrl, e.prCount(), result.Error)
	}
	if body := e.creator.only(t); !strings.Contains(body, "Verdict: REQUEST-CHANGES") {
		t.Errorf("PR body lacks the review section:\n%s", body)
	}
	if result.Outcome == "needs_human" {
		t.Error("open_pr must not end needs_human")
	}
}

func TestPrePRHold_MinorOnlyProceeds(t *testing.T) {
	minors := []PostMergeDefect{{Severity: PostMergeSeverityMinor, Location: "feature.go:1", Claim: "no package doc"}}
	e := newHoldEnv(t, minors, false, nil)
	result := e.run(t)

	if !result.Success || result.PRUrl == "" || e.prCount() != 1 {
		t.Errorf("success=%v url=%q PRs=%d error=%q, want one created PR", result.Success, result.PRUrl, e.prCount(), result.Error)
	}
	if strings.Contains(readOrEmpty(t, e.edits), labelPilotNeedsHuman) {
		t.Error("minor defects must not park the issue")
	}
}

func TestPrePRHold_BorrowedBranchNeverHolds(t *testing.T) {
	e := newHoldEnv(t, holdDefects, true, revertedGate)
	e.task.FromPR = 77
	e.runner.RecordBorrowedBranch(e.task.ID, e.branch, 77)
	result := e.run(t)

	if result.PrePRReview == nil || result.PrePRReview.Fix == nil || result.PrePRReview.Fix.Status != ReviewFixReverted {
		t.Fatalf("test setup: fix = %+v, want a reverted fix round", result.PrePRReview)
	}
	if result.Outcome == "needs_human" || !result.Success || e.prCount() != 1 {
		t.Errorf("outcome=%q success=%v PRs=%d error=%q, want today's PR path", result.Outcome, result.Success, e.prCount(), result.Error)
	}
	if strings.Contains(readOrEmpty(t, e.edits), labelPilotNeedsHuman) {
		t.Error("a borrowed branch must not be parked under pilot-needs-human")
	}
	if logs := e.logs.String(); strings.Count(logs, "Pre-PR review hold skipped: borrowed branch") != 1 {
		t.Errorf("want exactly one borrowed-branch WARN:\n%s", logs)
	}
}

func TestPrePRHoldReason_Table(t *testing.T) {
	rc := func(sev PostMergeSeverity) *PrePRReviewOutcome {
		return &PrePRReviewOutcome{
			Mode:    PrePRReviewModeEnforce,
			Verdict: PostMergeRequestChanges,
			Review:  &PostMergeReview{Verdict: PostMergeRequestChanges, Defects: []PostMergeDefect{{Severity: sev, Location: "a.go:1", Claim: "x"}}},
		}
	}
	shadow := rc(PostMergeSeverityBlocking)
	shadow.Mode = PrePRReviewModeShadow
	approve := rc(PostMergeSeverityBlocking)
	approve.Verdict = PostMergeApprove
	tests := []struct {
		name string
		rev  *PrePRReviewOutcome
		fix  *ReviewFixOutcome
		cfg  PrePRReviewConfig
		want bool
	}{
		{"nil review", nil, nil, PrePRReviewConfig{}, false},
		{"unset on_unfixed holds", rc(PostMergeSeverityBlocking), nil, PrePRReviewConfig{}, true},
		{"explicit hold", rc(PostMergeSeverityBlocking), nil, PrePRReviewConfig{OnUnfixed: PrePRReviewOnUnfixedHold}, true},
		{"open_pr", rc(PostMergeSeverityBlocking), nil, PrePRReviewConfig{OnUnfixed: PrePRReviewOnUnfixedOpenPR}, false},
		{"shadow", shadow, nil, PrePRReviewConfig{}, false},
		{"approve verdict", approve, nil, PrePRReviewConfig{}, false},
		{"minor only", rc(PostMergeSeverityMinor), nil, PrePRReviewConfig{}, false},
		{"fixed", rc(PostMergeSeverityBlocking), &ReviewFixOutcome{Status: ReviewFixFixed}, PrePRReviewConfig{}, false},
		{"reverted", rc(PostMergeSeverityBlocking), &ReviewFixOutcome{Status: ReviewFixReverted}, PrePRReviewConfig{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := prePRHoldReason(tt.rev, tt.fix, tt.cfg); got != tt.want {
				t.Errorf("hold = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNeedsHumanHold_NotRetriedByDispatcher: the hold's needs_human row grants
// no retry generation while it stands (the poller also skips the issue while
// pilot-needs-human is on); once the operator clears the label, the re-arm
// (cmd/pilot/rearm_needs_human.go) reclassifies the row and the dispatcher
// grants the next generation.
func TestNeedsHumanHold_NotRetriedByDispatcher(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-6138-HOLD", ProjectPath: "/project-hold"}
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := store.UpdateExecutionStatusIfNotTerminal(execID, "needs_human", prePRHoldReasonText); err != nil {
		t.Fatalf("needs_human: %v", err)
	}
	d := NewDispatcher(store, NewRunner(), nil)

	for i := 0; i < 3; i++ {
		gen, retry, err := d.nextRetryGeneration(task.ID, task.ProjectPath)
		if err != nil || retry || gen != 0 {
			t.Fatalf("held, cycle %d: gen=%d retry=%v err=%v, want no retry", i, gen, retry, err)
		}
	}

	if err := store.ReclassifyNeedsHumanForRearm(task.ID, task.ProjectPath, "re-armed: pilot-needs-human removed"); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	gen, retry, err := d.nextRetryGeneration(task.ID, task.ProjectPath)
	if err != nil || !retry || gen != 1 {
		t.Errorf("after re-arm: gen=%d retry=%v err=%v, want generation 1 and a retry", gen, retry, err)
	}
}

// PrePRHoldReasonForTest lets the external test package (which can import
// internal/briefs, an import cycle for this one) read the hold reason.
var PrePRHoldReasonForTest = prePRHoldReason
