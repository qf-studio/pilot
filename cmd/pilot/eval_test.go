package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/memory"
)

func TestNewEvalCmd(t *testing.T) {
	cmd := newEvalCmd()
	if cmd.Use != "eval" {
		t.Errorf("expected Use=eval, got %s", cmd.Use)
	}

	// Verify check subcommand exists
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "check" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'check' subcommand to be registered")
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
