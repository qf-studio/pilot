package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// GH-6037: the acceptance evidence runs ahead of the pre-PR review, the review
// reads it as the PR body, and the PR body reuses that one run.

const evidenceTestCmd = "go test -run TestX ./pkg/"

// sequenceEvidenceRunner answers the n-th call with the n-th scripted output; a
// non-nil err entry is returned with it. Calls past the script repeat the last.
type sequenceEvidenceRunner struct {
	mu      sync.Mutex
	outputs []string
	errs    []error
	calls   int
}

func (s *sequenceEvidenceRunner) RunCommand(_ context.Context, _ string, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.calls, len(s.outputs)-1)
	s.calls++
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return s.outputs[i], err
}

func (s *sequenceEvidenceRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// exitOneError is the *exec.ExitError a command that exits 1 returns.
func exitOneError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("sh -c 'exit 1' succeeded")
	}
	return err
}

const (
	evidenceFailOutput = "=== RUN   TestX\n--- FAIL: TestX (0.00s)\nFAIL\nFAIL\tgithub.com/example/pkg\t0.01s\n"
	evidencePassOutput = "ok  \tgithub.com/example/pkg\t0.01s\n"
)

func evidenceTask(dir string) *Task {
	task := prePRTask(dir)
	task.AcceptanceCriteria = []string{"`" + evidenceTestCmd + "` passes; paste the output into the PR body"}
	return task
}

func evidenceRunner(mode string, reviewer PrePRReviewer, cmd AcceptanceCommandRunner) *Runner {
	r := prePRRunner(mode, reviewer)
	r.acceptanceRunner = cmd
	return r
}

func TestPrePRReview_ReadsAcceptanceEvidence(t *testing.T) {
	dir, _ := prePRRepo(t)
	fake := &fakePrePRReviewer{review: approveWithNotes()}
	cmd := &sequenceEvidenceRunner{outputs: []string{evidenceFailOutput}, errs: []error{exitOneError(t)}}
	r := evidenceRunner(PrePRReviewModeShadow, fake, cmd)
	var buf bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&buf, nil))
	task := evidenceTask(dir)

	ev := r.collectAcceptanceEvidenceForReview(context.Background(), task, dir, prePRGates())
	if ev == nil {
		t.Fatal("no evidence collected ahead of the review")
	}
	r.runPrePRReview(context.Background(), task, dir, NewGitOperations(dir), &progressState{}, prePRGates(), ev)

	if fake.calls() != 1 {
		t.Fatalf("reviewer called %d times, want 1", fake.calls())
	}
	body := fake.inputs[0].PRBody
	if !strings.Contains(body, "--- FAIL: TestX") {
		t.Errorf("review PRBody has no FAIL line:\n%s", body)
	}
	if !strings.Contains(body, "FAILS") || strings.Contains(body, "### `"+evidenceTestCmd+"` passes") {
		t.Errorf("review PRBody heading does not read FAILS:\n%s", body)
	}
	if body != ev.Section {
		t.Errorf("review PRBody differs from the collected section:\n%s", body)
	}
	if got := ev.Results[0].Commands; len(got) != 1 || !got[0].Failed || got[0].ExitCode != 1 {
		t.Errorf("per-command result = %+v, want one failed command with exit code 1", got)
	}
	if !strings.Contains(buf.String(), "evidence_s=") {
		t.Errorf("Pre-PR review log line has no evidence_s: %s", buf.String())
	}
}

func TestPrePRReview_NoEvidenceKeepsPRBodyEmpty(t *testing.T) {
	dir, _ := prePRRepo(t)
	fake := &fakePrePRReviewer{review: approveWithNotes()}
	r := prePRRunner(PrePRReviewModeShadow, fake)
	r.runPrePRReview(context.Background(), prePRTask(dir), dir, NewGitOperations(dir), &progressState{}, prePRGates(), nil)
	if body := fake.inputs[0].PRBody; body != "" {
		t.Errorf("PRBody = %q, want empty when no evidence was collected", body)
	}
}

func TestAcceptanceEvidence_RunsOnceAcrossReviewAndPR(t *testing.T) {
	dir, _ := prePRRepo(t)
	fake := &fakePrePRReviewer{review: approveWithNotes()}
	cmd := &sequenceEvidenceRunner{outputs: []string{evidencePassOutput}}
	r := evidenceRunner(PrePRReviewModeShadow, fake, cmd)
	task := evidenceTask(dir)

	ev := r.collectAcceptanceEvidenceForReview(context.Background(), task, dir, prePRGates())
	r.runPrePRReview(context.Background(), task, dir, NewGitOperations(dir), &progressState{}, prePRGates(), ev)
	prBody := r.appendAcceptanceEvidence(context.Background(), task, dir, "## Summary", prePRGates(), ev)

	if n := cmd.count(); n != 1 {
		t.Errorf("paste-output command ran %d times across review and PR body, want 1", n)
	}
	if !strings.Contains(prBody, "## Evidence") || !strings.Contains(prBody, "ok  \tgithub.com/example/pkg") {
		t.Errorf("PR body lacks the cached evidence:\n%s", prBody)
	}
	if got := fake.inputs[0].PRBody; !strings.Contains(prBody, got) {
		t.Errorf("PR body does not carry the section the review read:\n%s", prBody)
	}

	// Without a cached result the PR body assembly still collects on its own.
	r.appendAcceptanceEvidence(context.Background(), task, dir, "## Summary", prePRGates())
	if n := cmd.count(); n != 2 {
		t.Errorf("uncached append ran the command %d times in total, want 2", n)
	}
}

func TestPrePRReview_FixRoundRerunsEvidence(t *testing.T) {
	tests := []struct {
		status    ReviewFixStatus
		wantCalls int
		wantFail  bool
	}{
		{ReviewFixFixed, 2, false},
		{ReviewFixReverted, 1, true},
		{ReviewFixNoChange, 1, true},
		{ReviewFixFailed, 1, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			dir, _ := prePRRepo(t)
			cmd := &sequenceEvidenceRunner{
				outputs: []string{evidenceFailOutput, evidencePassOutput},
				errs:    []error{exitOneError(t), nil},
			}
			r := evidenceRunner(PrePRReviewModeEnforce, &fakePrePRReviewer{review: approveWithNotes()}, cmd)
			task := evidenceTask(dir)

			first := r.collectAcceptanceEvidenceForReview(context.Background(), task, dir, prePRGates())
			if first == nil || !strings.Contains(first.Section, "--- FAIL: TestX") {
				t.Fatalf("first evidence = %+v, want the failing run", first)
			}
			got := r.evidenceAfterFixRound(context.Background(), task, dir, &ReviewFixOutcome{Status: tt.status}, first)
			prBody := r.appendAcceptanceEvidence(context.Background(), task, dir, "## Summary", prePRGates(), got)

			if n := cmd.count(); n != tt.wantCalls {
				t.Errorf("command ran %d times, want %d", n, tt.wantCalls)
			}
			if has := strings.Contains(prBody, "--- FAIL: TestX"); has != tt.wantFail {
				t.Errorf("PR body carries the first (failing) result = %v, want %v:\n%s", has, tt.wantFail, prBody)
			}
			if !tt.wantFail && (!strings.Contains(prBody, "ok  \tgithub.com/example/pkg") || strings.Contains(prBody, "FAILS")) {
				t.Errorf("PR body does not carry the second result:\n%s", prBody)
			}
		})
	}

	t.Run("no first result leaves the evidence to PR creation", func(t *testing.T) {
		dir, _ := prePRRepo(t)
		cmd := &sequenceEvidenceRunner{outputs: []string{evidencePassOutput}}
		r := evidenceRunner(PrePRReviewModeEnforce, &fakePrePRReviewer{review: approveWithNotes()}, cmd)
		if got := r.evidenceAfterFixRound(context.Background(), evidenceTask(dir), dir, &ReviewFixOutcome{Status: ReviewFixFixed}, nil); got != nil || cmd.count() != 0 {
			t.Errorf("got %+v after %d runs, want nil and no run", got, cmd.count())
		}
	})
}

func TestCollectAcceptanceEvidenceForReview_Skips(t *testing.T) {
	t.Run("not enough time for evidence plus review", func(t *testing.T) {
		dir, _ := prePRRepo(t)
		cmd := &sequenceEvidenceRunner{outputs: []string{evidencePassOutput}}
		r := evidenceRunner(PrePRReviewModeShadow, &fakePrePRReviewer{review: approveWithNotes()}, cmd)
		var buf bytes.Buffer
		r.log = slog.New(slog.NewTextHandler(&buf, nil))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if ev := r.collectAcceptanceEvidenceForReview(ctx, evidenceTask(dir), dir, prePRGates()); ev != nil {
			t.Errorf("evidence = %+v, want nil", ev)
		}
		if cmd.count() != 0 {
			t.Errorf("command ran %d times, want 0", cmd.count())
		}
		if !strings.Contains(buf.String(), "Pre-PR review: evidence not ready") {
			t.Errorf("log = %q, want the evidence-not-ready line", buf.String())
		}
		// The evidence still runs once at PR creation.
		r.appendAcceptanceEvidence(ctx, evidenceTask(dir), dir, "## Summary", prePRGates(), nil)
		if cmd.count() != 1 {
			t.Errorf("command ran %d times after PR creation, want 1", cmd.count())
		}
	})

	t.Run("review off or not creating a PR", func(t *testing.T) {
		dir, _ := prePRRepo(t)
		for name, mutate := range map[string]func(*Runner, *Task){
			"mode off":        func(r *Runner, _ *Task) { r.config.PrePRReview.Mode = PrePRReviewModeOff },
			"no reviewer":     func(r *Runner, _ *Task) { r.prePRReviewer = nil },
			"create_pr false": func(_ *Runner, task *Task) { task.CreatePR = false },
		} {
			cmd := &sequenceEvidenceRunner{outputs: []string{evidencePassOutput}}
			r := evidenceRunner(PrePRReviewModeShadow, &fakePrePRReviewer{review: approveWithNotes()}, cmd)
			task := evidenceTask(dir)
			mutate(r, task)
			if ev := r.collectAcceptanceEvidenceForReview(context.Background(), task, dir, prePRGates()); ev != nil || cmd.count() != 0 {
				t.Errorf("%s: evidence = %+v after %d runs, want nil and no run", name, ev, cmd.count())
			}
		}
	})
}
