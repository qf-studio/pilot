package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// fakePrePRReviewer records every input it is called with.
type fakePrePRReviewer struct {
	mu       sync.Mutex
	inputs   []PostMergeReviewInput
	hadDL    []bool
	review   *PostMergeReview
	err      error
	callback func(in PostMergeReviewInput)
}

func (f *fakePrePRReviewer) Review(ctx context.Context, in PostMergeReviewInput) (*PostMergeReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	_, ok := ctx.Deadline()
	f.hadDL = append(f.hadDL, ok)
	if f.callback != nil {
		f.callback(in)
	}
	return f.review, f.err
}

func (f *fakePrePRReviewer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

// prePRRepo builds a repo on branch pilot/GH-5949 one commit ahead of main and
// returns its path and HEAD SHA.
func prePRRepo(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	initGitRepo(t, dir)
	gitOut(t, dir, "branch", "-M", "main")
	gitOut(t, dir, "checkout", "-q", "-b", "pilot/GH-5949")
	writeFileT(t, dir, "feature.go", "package feature\n\nfunc Feature() int { return 1 }\n")
	gitOut(t, dir, "add", "feature.go")
	gitOut(t, dir, "commit", "-q", "-m", "feat: add feature")
	return dir, strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
}

func writeFileT(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func prePRTask(projectPath string) *Task {
	return &Task{
		ID:          "GH-5949",
		Title:       "Add multi-component feature with config wiring",
		Description: "Implement the feature across the config, the runner and the adapters, with tests.",
		ProjectPath: projectPath,
		Branch:      "pilot/GH-5949",
		BaseBranch:  "main",
		CreatePR:    true,
	}
}

func prePRGates() *QualityGatesResult {
	return &QualityGatesResult{
		Enabled:   true,
		AllPassed: true,
		Gates: []QualityGateResult{
			{Name: "build", Passed: true, Duration: 12 * time.Second},
			{Name: "test", Passed: true, Duration: 48 * time.Second},
		},
	}
}

func prePRRunner(mode string, reviewer PrePRReviewer) *Runner {
	r := NewRunnerWithBackend(&guardRecordingBackend{})
	r.config = &BackendConfig{UseWorktree: boolPtr(false), PrePRReview: &PrePRReviewConfig{Mode: mode}}
	r.prePRReviewer = reviewer
	return r
}

func approveWithNotes() *PostMergeReview {
	return &PostMergeReview{
		Verdict: PostMergeApproveWithNotes,
		Defects: []PostMergeDefect{
			{Severity: PostMergeSeverityMinor, Location: "feature.go:3", Claim: "no doc comment"},
		},
		FilesRead: 4,
	}
}

func TestRunPrePRReview_OffIsNoop(t *testing.T) {
	for _, mode := range []string{"", PrePRReviewModeOff} {
		t.Run("mode="+mode, func(t *testing.T) {
			dir, _ := prePRRepo(t)
			fake := &fakePrePRReviewer{review: approveWithNotes()}
			r := prePRRunner(mode, fake)

			out := r.runPrePRReview(context.Background(), prePRTask(dir), dir, NewGitOperations(dir), &progressState{}, prePRGates())

			if out != nil {
				t.Errorf("outcome = %+v, want nil when the mode is off", out)
			}
			if n := fake.calls(); n != 0 {
				t.Errorf("reviewer called %d times, want 0 when the mode is off", n)
			}
		})
	}

	t.Run("nil config", func(t *testing.T) {
		dir, _ := prePRRepo(t)
		fake := &fakePrePRReviewer{review: approveWithNotes()}
		r := prePRRunner(PrePRReviewModeShadow, fake)
		r.config = nil
		if out := r.runPrePRReview(context.Background(), prePRTask(dir), dir, NewGitOperations(dir), &progressState{}, prePRGates()); out != nil || fake.calls() != 0 {
			t.Errorf("nil config must be off: out=%v calls=%d", out, fake.calls())
		}
	})
}

func TestRunPrePRReview_Shadow_RecordsOutcomeNoFix(t *testing.T) {
	dir, head := prePRRepo(t)
	otherRoot := t.TempDir() // the daemon's shared root: must never reach the reviewer
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const execID = "exec-pre-pr-review"
	if err := store.SaveExecution(&memory.Execution{ID: execID, TaskID: "GH-5949", ProjectPath: dir, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	fake := &fakePrePRReviewer{review: approveWithNotes()}
	r := prePRRunner(PrePRReviewModeShadow, fake)
	r.SetLogStore(store)
	task := prePRTask(otherRoot)
	task.ExecutionID = execID

	out := r.runPrePRReview(context.Background(), task, dir, NewGitOperations(dir), &progressState{}, prePRGates())

	if n := fake.calls(); n != 1 {
		t.Fatalf("reviewer called %d times, want exactly 1 (shadow never starts a fix round)", n)
	}
	in := fake.inputs[0]
	if !in.PreMerge {
		t.Error("PreMerge = false, want true")
	}
	if in.MergeCommitSHA != head {
		t.Errorf("MergeCommitSHA = %q, want the worktree HEAD %q", in.MergeCommitSHA, head)
	}
	if in.ProjectPath != dir {
		t.Errorf("ProjectPath = %q, want the execution path %q (not task.ProjectPath %q)", in.ProjectPath, dir, otherRoot)
	}
	for _, gate := range []string{"build", "test"} {
		if !strings.Contains(in.CIResult, gate) {
			t.Errorf("CIResult %q does not name gate %q", in.CIResult, gate)
		}
	}
	if in.IssueTitle != task.Title || in.IssueBody != task.Description {
		t.Errorf("issue = %q / %q, want the task title and description", in.IssueTitle, in.IssueBody)
	}
	if !strings.Contains(in.Diff, "feature.go") || !strings.Contains(in.Diff, "func Feature()") {
		t.Errorf("diff does not carry the change: %q", in.Diff)
	}
	if !fake.hadDL[0] {
		t.Error("reviewer ran on a context without the configured timeout")
	}

	if out == nil || out.Mode != PrePRReviewModeShadow || out.Verdict != PostMergeApproveWithNotes || out.Skipped != "" || out.Err != nil || out.FilesRead != 4 || out.Review == nil {
		t.Fatalf("outcome = %+v, want a shadow APPROVE-WITH-NOTES with 4 files read", out)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	var reviewEvents int
	for _, e := range events {
		switch e.Stage {
		case memory.StagePrePRReview:
			reviewEvents++
			var d struct {
				Mode      string `json:"mode"`
				Verdict   string `json:"verdict"`
				Blocking  int    `json:"blocking"`
				Minor     int    `json:"minor"`
				FilesRead int    `json:"files_read"`
			}
			if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
				t.Fatalf("event detail %q: %v", e.Detail, err)
			}
			if d.Mode != "shadow" || d.Verdict != "APPROVE-WITH-NOTES" || d.Blocking != 0 || d.Minor != 1 || d.FilesRead != 4 {
				t.Errorf("event detail = %+v", d)
			}
		case memory.StagePrePRFix:
			t.Error("shadow mode recorded a fix round")
		}
	}
	if reviewEvents != 1 {
		t.Errorf("pre_pr_review events = %d, want 1", reviewEvents)
	}
}

func TestRunPrePRReview_Shadow_SkipsSelfReview(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		wantCalled bool
	}{
		{name: "off: self-review still runs", mode: PrePRReviewModeOff, wantCalled: true},
		{name: "shadow: self-review does not run", mode: PrePRReviewModeShadow, wantCalled: false},
		{name: "enforce: self-review does not run", mode: PrePRReviewModeEnforce, wantCalled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := prePRRepo(t)
			backend := &guardRecordingBackend{}
			r := NewRunnerWithBackend(backend)
			r.skipPreflightChecks = true
			r.config = &BackendConfig{UseWorktree: boolPtr(false), PrePRReview: &PrePRReviewConfig{Mode: tt.mode}}

			if err := r.runSelfReview(context.Background(), prePRTask(dir), dir, &progressState{}); err != nil {
				t.Fatalf("runSelfReview: %v", err)
			}
			if backend.called != tt.wantCalled {
				t.Errorf("self-review backend called = %v, want %v", backend.called, tt.wantCalled)
			}
			if tt.wantCalled && backend.gotProjectPath != dir {
				t.Errorf("self-review ran in %q, want %q", backend.gotProjectPath, dir)
			}
		})
	}
}

func TestRunPrePRReview_Skips(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(t *testing.T, task *Task, r *Runner, dir string) (context.Context, *QualityGatesResult)
		wantReason string
	}{
		{
			name: "trivial task",
			mutate: func(_ *testing.T, task *Task, _ *Runner, _ string) (context.Context, *QualityGatesResult) {
				task.Title = "Fix typo in README"
				task.Description = "Fix a typo."
				return context.Background(), prePRGates()
			},
			wantReason: "complexity trivial",
		},
		{
			name: "create_pr false",
			mutate: func(_ *testing.T, task *Task, _ *Runner, _ string) (context.Context, *QualityGatesResult) {
				task.CreatePR = false
				return context.Background(), prePRGates()
			},
			wantReason: "create_pr=false",
		},
		{
			name: "direct commit",
			mutate: func(_ *testing.T, task *Task, _ *Runner, _ string) (context.Context, *QualityGatesResult) {
				task.DirectCommit = true
				return context.Background(), prePRGates()
			},
			wantReason: "direct commit",
		},
		{
			name: "no branch",
			mutate: func(_ *testing.T, task *Task, _ *Runner, _ string) (context.Context, *QualityGatesResult) {
				task.Branch = ""
				return context.Background(), prePRGates()
			},
			wantReason: "no branch",
		},
		{
			name: "gates failed with a checker configured",
			mutate: func(_ *testing.T, _ *Task, r *Runner, _ string) (context.Context, *QualityGatesResult) {
				r.qualityCheckerFactory = func(string, string, string) QualityChecker { return nil }
				g := prePRGates()
				g.AllPassed = false
				return context.Background(), g
			},
			wantReason: "quality gates did not pass",
		},
		{
			name: "empty diff",
			mutate: func(t *testing.T, task *Task, _ *Runner, dir string) (context.Context, *QualityGatesResult) {
				gitOut(t, dir, "checkout", "-q", "-b", "pilot/empty", "main")
				task.Branch = "pilot/empty"
				return context.Background(), prePRGates()
			},
			wantReason: "empty diff",
		},
		{
			name: "under two minutes left",
			mutate: func(t *testing.T, _ *Task, _ *Runner, _ string) (context.Context, *QualityGatesResult) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				t.Cleanup(cancel)
				return ctx, prePRGates()
			},
			wantReason: "less than 2m left",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := prePRRepo(t)
			fake := &fakePrePRReviewer{review: approveWithNotes()}
			r := prePRRunner(PrePRReviewModeShadow, fake)
			task := prePRTask(dir)
			ctx, gates := tt.mutate(t, task, r, dir)

			out := r.runPrePRReview(ctx, task, dir, NewGitOperations(dir), &progressState{}, gates)

			if n := fake.calls(); n != 0 {
				t.Errorf("reviewer called %d times, want 0", n)
			}
			if out == nil || out.Mode != PrePRReviewModeShadow || out.Verdict != "" || out.Review != nil {
				t.Fatalf("outcome = %+v, want a skipped shadow outcome with no verdict", out)
			}
			if !strings.Contains(out.Skipped, tt.wantReason) {
				t.Errorf("Skipped = %q, want it to contain %q", out.Skipped, tt.wantReason)
			}
		})
	}
}

func TestRunPrePRReview_NoVerdictOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		review  *PostMergeReview
		err     error
		wantErr bool
	}{
		{name: "unknown verdict", review: &PostMergeReview{Verdict: PostMergeUnknown}},
		{name: "exhausted verdict", review: &PostMergeReview{Verdict: PostMergeExhausted}},
		{name: "reviewer error", err: errors.New("subprocess died"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := prePRRepo(t)
			fake := &fakePrePRReviewer{review: tt.review, err: tt.err}
			r := prePRRunner(PrePRReviewModeShadow, fake)

			out := r.runPrePRReview(context.Background(), prePRTask(dir), dir, NewGitOperations(dir), &progressState{}, prePRGates())

			if fake.calls() != 1 {
				t.Fatalf("reviewer called %d times, want 1", fake.calls())
			}
			if out == nil || out.Verdict != "" {
				t.Fatalf("outcome = %+v, want no verdict", out)
			}
			if (out.Err != nil) != tt.wantErr {
				t.Errorf("Err = %v, wantErr %v", out.Err, tt.wantErr)
			}
		})
	}
}

func TestPrePRReviewConfig_Effective(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *BackendConfig
		wantMode     string
		wantSkipSelf bool
	}{
		{name: "nil config", cfg: nil, wantMode: "off"},
		{name: "no block", cfg: &BackendConfig{}, wantMode: "off"},
		{name: "default config", cfg: DefaultBackendConfig(), wantMode: "off"},
		{name: "explicit skip_self_review", cfg: &BackendConfig{SkipSelfReview: true}, wantMode: "off", wantSkipSelf: true},
		{name: "shadow", cfg: &BackendConfig{PrePRReview: &PrePRReviewConfig{Mode: "shadow"}}, wantMode: "shadow", wantSkipSelf: true},
		{name: "enforce", cfg: &BackendConfig{PrePRReview: &PrePRReviewConfig{Mode: "enforce"}}, wantMode: "enforce", wantSkipSelf: true},
		{name: "empty mode", cfg: &BackendConfig{PrePRReview: &PrePRReviewConfig{}}, wantMode: "off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.PrePRReviewMode(); got != tt.wantMode {
				t.Errorf("PrePRReviewMode = %q, want %q", got, tt.wantMode)
			}
			if got := tt.cfg.EffectiveSkipSelfReview(); got != tt.wantSkipSelf {
				t.Errorf("EffectiveSkipSelfReview = %v, want %v", got, tt.wantSkipSelf)
			}
		})
	}

	t.Run("defaults", func(t *testing.T) {
		var c *PrePRReviewConfig
		if c.EffectiveTimeout() != 6*time.Minute || c.EffectiveFixTimeout() != 10*time.Minute || c.EffectiveMaxFixRounds() != 1 {
			t.Errorf("defaults = %s / %s / %d", c.EffectiveTimeout(), c.EffectiveFixTimeout(), c.EffectiveMaxFixRounds())
		}
		if !c.SkipsComplexity(ComplexityTrivial) || c.SkipsComplexity(ComplexitySimple) {
			t.Error("default skip_complexity must be exactly [trivial]")
		}
		none := &PrePRReviewConfig{SkipComplexity: []string{}}
		if none.SkipsComplexity(ComplexityTrivial) {
			t.Error("an explicit empty skip_complexity skips nothing")
		}
	})
}
