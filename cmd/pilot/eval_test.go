package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

func TestNewEvalCmd(t *testing.T) {
	cmd := newEvalCmd()
	if cmd.Use != "eval" {
		t.Errorf("expected Use=eval, got %s", cmd.Use)
	}

	// Verify all subcommands are registered.
	want := map[string]bool{"run": false, "list": false, "stats": false, "check": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Use]; ok {
			want[sub.Use] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected %q subcommand to be registered", name)
		}
	}
}

func TestNewEvalCheckCmd_RequiresFlags(t *testing.T) {
	cmd := newEvalCheckCmd()

	// Running without required flags should fail
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when --baseline and --current are missing")
	}
	if err != nil && !strings.Contains(err.Error(), "--baseline") {
		t.Errorf("expected error about --baseline, got: %s", err)
	}
}

func TestNewEvalCheckCmd_Flags(t *testing.T) {
	cmd := newEvalCheckCmd()

	baselineFlag := cmd.Flags().Lookup("baseline")
	if baselineFlag == nil {
		t.Fatal("expected --baseline flag")
	}

	currentFlag := cmd.Flags().Lookup("current")
	if currentFlag == nil {
		t.Fatal("expected --current flag")
	}

	thresholdFlag := cmd.Flags().Lookup("threshold")
	if thresholdFlag == nil {
		t.Fatal("expected --threshold flag")
	}
	if thresholdFlag.DefValue != "5" {
		t.Errorf("expected threshold default=5, got %s", thresholdFlag.DefValue)
	}
}

func TestPrintEvalReport_Regression(t *testing.T) {
	report := &memory.RegressionReport{
		BaselinePassRate: 80.0,
		CurrentPassRate:  50.0,
		Delta:            -30.0,
		Regressed:        true,
		RegressedTaskIDs: []string{"eval-aaa", "eval-bbb"},
		ImprovedTaskIDs:  []string{"eval-ccc"},
		Recommendation:   "Pass rate dropped 30.0pp. Investigate 2 regressed task(s).",
	}

	output := captureStdout(func() {
		printEvalReport(report, "run-baseline", "run-current", 5.0)
	})

	checks := []string{
		"=== Eval Regression Report ===",
		"Baseline run:  run-baseline",
		"Current run:   run-current",
		"Threshold:     5.0pp",
		"Baseline pass@1: 80.0%",
		"Current pass@1:  50.0%",
		"Delta:           -30.0pp",
		"Regressed tasks (2):",
		"eval-aaa",
		"eval-bbb",
		"Improved tasks (1):",
		"eval-ccc",
		"REGRESSION DETECTED",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q\nGot:\n%s", check, output)
		}
	}
}

func TestPrintEvalReport_OK(t *testing.T) {
	report := &memory.RegressionReport{
		BaselinePassRate: 80.0,
		CurrentPassRate:  85.0,
		Delta:            5.0,
		Regressed:        false,
		ImprovedTaskIDs:  []string{"eval-abc"},
		Recommendation:   "Pass rate improved 5.0pp.",
	}

	output := captureStdout(func() {
		printEvalReport(report, "run-a", "run-b", 5.0)
	})

	if !strings.Contains(output, "Result: OK") {
		t.Errorf("expected 'Result: OK' in output, got:\n%s", output)
	}
	if strings.Contains(output, "REGRESSION") {
		t.Errorf("unexpected 'REGRESSION' in output:\n%s", output)
	}
}

func TestNewEvalRunCmd_RequiresRepo(t *testing.T) {
	cmd := newEvalRunCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when --repo is missing")
	}
	if err != nil && !strings.Contains(err.Error(), "--repo") {
		t.Errorf("expected error about --repo, got: %s", err)
	}
}

func TestNewEvalRunCmd_Flags(t *testing.T) {
	cmd := newEvalRunCmd()

	repoFlag := cmd.Flags().Lookup("repo")
	if repoFlag == nil {
		t.Fatal("expected --repo flag")
	}

	modelFlag := cmd.Flags().Lookup("model")
	if modelFlag == nil {
		t.Fatal("expected --model flag")
	}

	limitFlag := cmd.Flags().Lookup("limit")
	if limitFlag == nil {
		t.Fatal("expected --limit flag")
	}
	if limitFlag.DefValue != "100" {
		t.Errorf("expected limit default=100, got %s", limitFlag.DefValue)
	}
}

func TestNewEvalListCmd_Flags(t *testing.T) {
	cmd := newEvalListCmd()

	for _, name := range []string{"repo", "limit", "success", "failed"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected --%s flag", name)
		}
	}
}

func TestNewEvalStatsCmd_Flags(t *testing.T) {
	cmd := newEvalStatsCmd()

	if cmd.Flags().Lookup("repo") == nil {
		t.Fatal("expected --repo flag")
	}
}

func TestEvalPassRate(t *testing.T) {
	tests := []struct {
		name  string
		tasks []*memory.EvalTask
		want  float64
	}{
		{"empty", nil, 0},
		{"all pass", []*memory.EvalTask{
			{Success: true}, {Success: true},
		}, 100.0},
		{"all fail", []*memory.EvalTask{
			{Success: false}, {Success: false},
		}, 0.0},
		{"mixed", []*memory.EvalTask{
			{Success: true}, {Success: false}, {Success: true}, {Success: false},
		}, 50.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evalPassRate(tt.tasks)
			if got != tt.want {
				t.Errorf("evalPassRate() = %.1f, want %.1f", got, tt.want)
			}
		})
	}
}

func TestPrintEvalStats_Overall(t *testing.T) {
	tasks := []*memory.EvalTask{
		{ID: "eval-1", Repo: "org/a", Success: true, DurationMs: 3000,
			PassCriteria: []memory.PassCriteria{{Type: "build", Passed: true}, {Type: "test", Passed: true}}},
		{ID: "eval-2", Repo: "org/a", Success: false, DurationMs: 1000,
			PassCriteria: []memory.PassCriteria{{Type: "build", Passed: true}, {Type: "test", Passed: false}}},
		{ID: "eval-3", Repo: "org/b", Success: true, DurationMs: 2000},
	}

	output := captureStdout(func() {
		printEvalStats(tasks, "")
	})

	checks := []string{
		"=== Eval Statistics ===",
		"Total tasks:  3",
		"Passed:       2",
		"Failed:       1",
		"pass@1:       66.7%",
		"Per-repository breakdown:",
		"Quality gate pass rates:",
		"build",
		"test",
	}
	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q\nGot:\n%s", check, output)
		}
	}
}

func TestPrintEvalStats_SingleRepo(t *testing.T) {
	tasks := []*memory.EvalTask{
		{ID: "eval-1", Repo: "org/a", Success: true, DurationMs: 2000},
		{ID: "eval-2", Repo: "org/a", Success: false, DurationMs: 1000},
	}

	output := captureStdout(func() {
		printEvalStats(tasks, "org/a")
	})

	// When filtered to single repo, no per-repo breakdown.
	if strings.Contains(output, "Per-repository breakdown:") {
		t.Errorf("should not show per-repo breakdown when filtered to single repo:\n%s", output)
	}
	if !strings.Contains(output, "pass@1:       50.0%") {
		t.Errorf("expected pass@1 of 50.0%% in output:\n%s", output)
	}
}

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	_ = w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}
