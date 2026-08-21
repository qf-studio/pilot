package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestQualityGatesRespectCreatePR is the GH-4876 regression guard for the
// primary defect: quality gates used to run unconditionally, including for
// read-only question tasks (CreatePR=false, no branch, no files written by
// design). Running gates against those failed deterministically and
// triggered a doomed retry cycle against a working tree never set up for
// code work. Gates must now be skipped entirely when task.CreatePR is
// false, and behavior for CreatePR=true tasks must be unchanged.
func TestQualityGatesRespectCreatePR(t *testing.T) {
	tests := []struct {
		name        string
		createPR    bool
		wantGateRun bool
	}{
		{name: "question task (CreatePR=false) skips quality gates", createPR: false, wantGateRun: false},
		{name: "code task (CreatePR=true) still runs quality gates", createPR: true, wantGateRun: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := t.TempDir()

			backend := &mockSelfReviewBackend{output: "done"}
			runner := NewRunnerWithBackend(backend)
			runner.config = &BackendConfig{}
			runner.SetRecordingEnabled(false)
			runner.skipPreflightChecks = true

			gateCalled := false
			runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
				gateCalled = true
				return &mockQualityChecker{outcome: &QualityOutcome{Passed: true}}
			})

			task := &Task{
				ID:          "GH-4876-CREATEPR",
				Title:       "quality gate CreatePR guard test",
				Description: "test",
				ProjectPath: projectDir,
				LocalMode:   true,
				CreatePR:    tt.createPR,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := runner.Execute(ctx, task)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if !result.Success {
				t.Fatalf("Execute() not successful: %s", result.Error)
			}
			if gateCalled != tt.wantGateRun {
				t.Errorf("quality checker factory called = %v, want %v (CreatePR=%v)", gateCalled, tt.wantGateRun, tt.createPR)
			}
		})
	}
}

// TestQuestionTaskDoesNotAutoEnableQualityGates covers the GH-363
// auto-enable path specifically (a distinct code path from an explicitly
// configured QualityCheckerFactory): a question task's project directory
// may contain detectable build/test commands (here, a go.mod with no .go
// files, so a real `go build ./...` would fail with "no Go files in .").
// For a CreatePR=false task, gates must never be auto-enabled or run, so
// the answer succeeds and the factory stays nil.
func TestQuestionTaskDoesNotAutoEnableQualityGates(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module gh4876question\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}

	backend := &mockSelfReviewBackend{output: "the answer is 42"}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	// Deliberately no SetQualityCheckerFactory call: this exercises the
	// GH-363 auto-enable path, which would otherwise detect "go build ./..."
	// from the go.mod in projectDir and wire up a build gate on its own.

	task := &Task{
		ID:          "Q-GH-4876-AUTOGATE",
		Title:       "Question: what does this module do?",
		Description: "Answer this question about the codebase. DO NOT make any changes, only read and analyze.",
		ProjectPath: projectDir,
		LocalMode:   true,
		CreatePR:    false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s — a question task must succeed on its answer alone; quality gates must not auto-enable and run `go build ./...` against a project with no .go files", result.Error)
	}
	if runner.qualityCheckerFactory != nil {
		t.Errorf("GH-363 auto-enable wired a quality checker factory for a CreatePR=false question task; it must stay nil")
	}
}
