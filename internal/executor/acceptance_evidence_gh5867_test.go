package executor

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sleepingEvidenceRunner is an AcceptanceCommandRunner whose every command
// takes a fixed wall-clock time and ignores ctx, like a subprocess that is
// slow to die.
type sleepingEvidenceRunner struct {
	delay time.Duration
	calls atomic.Int32
}

func (s *sleepingEvidenceRunner) RunCommand(_ context.Context, _, command string) (string, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return "ran " + command, nil
}

func newEvidenceTestRunner(cfg *BackendConfig) *Runner {
	return &Runner{config: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// TestAppendAcceptanceEvidence_BudgetExhausted pins GH-5867: the evidence step
// has its own budget; once it is spent every remaining item is recorded as
// Not-verified instead of being run, and the evidence block is still returned.
func TestAppendAcceptanceEvidence_BudgetExhausted(t *testing.T) {
	cfg := DefaultBackendConfig()
	cfg.AcceptanceEvidenceBudget = time.Second
	r := newEvidenceTestRunner(cfg)
	cmdRunner := &sleepingEvidenceRunner{delay: 2 * time.Second}
	r.acceptanceRunner = cmdRunner

	task := &Task{ID: "GH-5867", AcceptanceCriteria: []string{
		"paste the output of `go test ./a/` into the PR body",
		"paste the output of `go test ./b/` into the PR body",
		"paste the output of `go test ./c/` into the PR body",
	}}

	start := time.Now()
	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), "## Summary", nil)
	elapsed := time.Since(start)

	if n := cmdRunner.calls.Load(); n != 1 {
		t.Errorf("expected exactly one executed item, runner called %d times", n)
	}
	if n := strings.Count(got, "Reason: "+evidenceBudgetExhaustedReason); n != 2 {
		t.Errorf("expected 2 %q lines, got %d in:\n%s", evidenceBudgetExhaustedReason, n, got)
	}
	if !strings.Contains(got, "## Evidence") || !strings.Contains(got, "ran go test ./a/") {
		t.Errorf("expected the executed item's evidence in the PR body, got:\n%s", got)
	}
	// budget (1s) plus the one command that was already running (2s).
	if elapsed > 3500*time.Millisecond {
		t.Errorf("appendAcceptanceEvidence took %v, want within budget plus one command", elapsed)
	}
}

// TestAppendAcceptanceEvidence_BudgetKillsRunningCommand: a command still
// running when the budget ends is reported as budget-exhausted, not as a
// per-command timeout.
func TestAppendAcceptanceEvidence_BudgetKillsRunningCommand(t *testing.T) {
	cfg := DefaultBackendConfig()
	cfg.AcceptanceEvidenceBudget = 200 * time.Millisecond
	r := newEvidenceTestRunner(cfg)
	r.acceptanceRunner = blockingEvidenceRunner{}

	task := &Task{ID: "GH-5867", AcceptanceCriteria: []string{"paste the output of `go test ./a/` into the PR body"}}
	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), "## Summary", nil)

	if !strings.Contains(got, "Reason: "+evidenceBudgetExhaustedReason) {
		t.Errorf("expected budget-exhausted reason, got:\n%s", got)
	}
	if strings.Contains(got, "timed out after") {
		t.Errorf("budget kill must not be blamed on the command timeout:\n%s", got)
	}
}

// blockingEvidenceRunner blocks until ctx is done, then fails like the real
// runner does when its parent ctx expires.
type blockingEvidenceRunner struct{}

func (blockingEvidenceRunner) RunCommand(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", &acceptanceCommandTimeoutError{timeout: 10 * time.Minute}
}

// TestAppendAcceptanceEvidence_ReusesQualityGateOutput pins GH-5867: a
// paste-output command whose argv equals a passed quality gate renders the
// gate's captured output without running; anything else still runs.
func TestAppendAcceptanceEvidence_ReusesQualityGateOutput(t *testing.T) {
	r := newEvidenceTestRunner(DefaultBackendConfig())
	fake := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go vet ./...":  {output: "vet ran for real"},
		"make  lint":    {output: "lint ran for real"},
		"make test -j2": {output: "test -j2 ran for real"},
	}}
	r.acceptanceRunner = fake

	gates := &QualityGatesResult{Enabled: true, Gates: []QualityGateResult{
		{Name: "test", Passed: true, Command: "make test", Output: "gate output: ok pkg 1.2s\n"},
		{Name: "lint", Passed: false, ExitCode: 2, Command: "make lint", Output: "gate lint failed"},
	}}
	task := &Task{ID: "GH-5867", AcceptanceCriteria: []string{
		"paste the output of `make   test` into the PR body",
		"paste the output of `go vet ./...` into the PR body",
		"paste the output of `make  lint` into the PR body",
		"paste the output of `make test -j2` into the PR body",
	}}

	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), "## Summary", gates)

	if !strings.Contains(got, "gate output: ok pkg 1.2s") || !strings.Contains(got, evidenceFromQualityGateMarker) {
		t.Errorf("expected the passed gate's output marked %q, got:\n%s", evidenceFromQualityGateMarker, got)
	}
	for _, c := range fake.calls {
		if strings.TrimSpace(strings.Join(strings.Fields(c), " ")) == "make test" {
			t.Errorf("runner was invoked for a command equal to a passed gate: %q", c)
		}
	}
	// different argv, a failed gate, and a longer argv all still run.
	for _, want := range []string{"vet ran for real", "lint ran for real", "test -j2 ran for real"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q to run for real, got:\n%s", want, got)
		}
	}
	if n := strings.Count(got, evidenceFromQualityGateMarker); n != 1 {
		t.Errorf("expected exactly one reused line, got %d:\n%s", n, got)
	}
}

// TestAppendAcceptanceEvidence_ReadsBudgetFromConfig: the runner takes the
// evidence budget from executor.acceptance_evidence_budget.
func TestAppendAcceptanceEvidence_ReadsBudgetFromConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *BackendConfig
		want time.Duration
	}{
		{"nil config", nil, 15 * time.Minute},
		{"unset", &BackendConfig{}, 15 * time.Minute},
		{"default config", DefaultBackendConfig(), 15 * time.Minute},
		{"configured", &BackendConfig{AcceptanceEvidenceBudget: 42 * time.Second}, 42 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newEvidenceTestRunner(tt.cfg)
			ctx, cancel := r.evidenceBudgetCtx(context.Background())
			defer cancel()
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("evidence ctx has no deadline")
			}
			if got := time.Until(deadline); got > tt.want || got < tt.want-5*time.Second {
				t.Errorf("budget = %v, want ~%v", got, tt.want)
			}
		})
	}
}

// TestEvidenceBudgetCtx_DetachesDeadlineButKeepsCancel: an expired task
// deadline does not shorten the evidence budget, a real cancellation still
// stops it (GH-5342).
func TestEvidenceBudgetCtx_DetachesDeadlineButKeepsCancel(t *testing.T) {
	r := newEvidenceTestRunner(DefaultBackendConfig())

	expired, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer expiredCancel()
	ctx, cancel := r.evidenceBudgetCtx(expired)
	defer cancel()
	if ctx.Err() != nil {
		t.Errorf("an expired task deadline must not expire the evidence budget, got %v", ctx.Err())
	}

	parent, parentCancel := context.WithCancel(context.Background())
	ctx2, cancel2 := r.evidenceBudgetCtx(parent)
	defer cancel2()
	parentCancel()
	select {
	case <-ctx2.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a real cancellation of the task ctx must stop the evidence step")
	}
}

// initEvidenceGitRepo makes dir a git repo with one commit and returns HEAD.
func initEvidenceGitRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sha, err := NewGitOperations(dir).GetCurrentCommitSHA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

// TestAppendAcceptanceEvidence_NoReuseAfterHeadMoved pins GH-5890: gate output
// is reused only while the pushed HEAD is the tree the gates ran on.
func TestAppendAcceptanceEvidence_NoReuseAfterHeadMoved(t *testing.T) {
	dir := t.TempDir()
	head := initEvidenceGitRepo(t, dir)

	tests := []struct {
		name      string
		gateSHA   string
		wantReuse bool
	}{
		{"same HEAD reuses", head, true},
		{"moved HEAD runs the command", "0123456789abcdef0123456789abcdef01234567", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logBuf strings.Builder
			r := &Runner{config: DefaultBackendConfig(), log: slog.New(slog.NewTextHandler(&logBuf, nil))}
			fake := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
				"make test": {output: "ran for real"},
			}}
			r.acceptanceRunner = fake
			gates := &QualityGatesResult{Enabled: true, HeadSHA: tt.gateSHA, Gates: []QualityGateResult{
				{Name: "test", Passed: true, Command: "make test", Output: "gate output"},
			}}
			task := &Task{ID: "GH-5890", AcceptanceCriteria: []string{"paste the output of `make test` into the PR body"}}

			got := r.appendAcceptanceEvidence(context.Background(), task, dir, "## Summary", gates)

			if reused := strings.Contains(got, evidenceFromQualityGateMarker); reused != tt.wantReuse {
				t.Errorf("reused = %v, want %v:\n%s", reused, tt.wantReuse, got)
			}
			if ran := len(fake.calls) == 1; ran == tt.wantReuse {
				t.Errorf("runner calls = %v, want ran=%v", fake.calls, !tt.wantReuse)
			}
			logged := strings.Contains(logBuf.String(), "HEAD moved after the gates ran") &&
				strings.Contains(logBuf.String(), tt.gateSHA) && strings.Contains(logBuf.String(), head)
			if logged == tt.wantReuse {
				t.Errorf("Info line with both SHAs logged = %v, want %v; log:\n%s", logged, !tt.wantReuse, logBuf.String())
			}
		})
	}
}

// TestRunPasteOutputItem_BudgetKillMarksCommandErr: a command killed by the
// budget carries Err even though the run reports a plain ExitError, and the
// not-yet-run command after it is marked too.
func TestRunPasteOutputItem_BudgetKillMarksCommandErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &killOnSecondRunner{cancel: cancel}
	item := AcceptanceItem{Kind: AcceptanceItemPasteOutput, Text: "paste a, b and c",
		Commands: []string{"go test ./a/", "go test ./b/", "go test ./c/"}}

	res := runPasteOutputItemReusing(ctx, runner, t.TempDir(), item, defaultTestAllowedCommands, nil)

	if res.NotVerifiedReason != "" {
		t.Fatalf("a mixed item has no item-level reason, got %q", res.NotVerifiedReason)
	}
	if len(res.Commands) != 3 {
		t.Fatalf("want 3 command results, got %d", len(res.Commands))
	}
	if res.Commands[0].Err != "" {
		t.Errorf("A finished, got Err %q", res.Commands[0].Err)
	}
	for _, c := range res.Commands[1:] {
		if c.Err != evidenceBudgetExhaustedReason {
			t.Errorf("%s: Err = %q, want %q", c.Command, c.Err, evidenceBudgetExhaustedReason)
		}
	}
}

// killOnSecondRunner lets the first command finish, then ends the ctx during
// the second and returns the kill as a plain ExitError, as exec does.
type killOnSecondRunner struct {
	cancel context.CancelFunc
	n      int
}

func (k *killOnSecondRunner) RunCommand(_ context.Context, _, command string) (string, error) {
	k.n++
	if k.n == 1 {
		return "ok  \tpkg/a\t0.1s", nil
	}
	k.cancel()
	return "ok  \tpkg/b\t(partial)", &exec.ExitError{}
}
