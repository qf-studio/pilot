package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAcceptanceCommandRunner is a scripted AcceptanceCommandRunner double
// for unit-testing the paste-output and mutation harnesses without a real
// shell. Commands are matched by exact string; unmatched commands return an
// error.
type fakeAcceptanceCommandRunner struct {
	responses map[string]fakeCommandResponse
	calls     []string
}

type fakeCommandResponse struct {
	output string
	err    error
}

func (f *fakeAcceptanceCommandRunner) RunCommand(_ context.Context, _ string, command string) (string, error) {
	f.calls = append(f.calls, command)
	resp, ok := f.responses[command]
	if !ok {
		return "", errors.New("fakeAcceptanceCommandRunner: no scripted response for " + command)
	}
	return resp.output, resp.err
}

func TestRunPasteOutputItem(t *testing.T) {
	t.Run("captures output of every parsed command", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run TestX ./pkg/": {output: "--- FAIL: TestX\nFAIL"},
		}}
		item := ClassifyAcceptanceItem("paste the output of `go test -run TestX ./pkg/`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if len(result.Commands) != 1 {
			t.Fatalf("expected 1 command result, got %d", len(result.Commands))
		}
		if result.Commands[0].Command != "go test -run TestX ./pkg/" {
			t.Errorf("unexpected command: %q", result.Commands[0].Command)
		}
		if !strings.Contains(result.Commands[0].Output, "FAIL") {
			t.Errorf("expected output to contain FAIL, got %q", result.Commands[0].Output)
		}
	})

	t.Run("a command's non-zero exit is captured as output, not an error", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"false": {output: "", err: &exec.ExitError{}},
		}}
		item := ClassifyAcceptanceItem("paste the output of `false`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item)
		if result.NotVerifiedReason != "" {
			t.Fatalf("a non-zero exit should not be Not-verified, got reason: %s", result.NotVerifiedReason)
		}
		if result.Commands[0].Err != "" {
			t.Errorf("expected no Err for an ExitError, got %q", result.Commands[0].Err)
		}
	})

	t.Run("zero parsed commands is Not-verified", func(t *testing.T) {
		item := ClassifyAcceptanceItem("paste the output of the failing command")
		result := runPasteOutputItem(context.Background(), &fakeAcceptanceCommandRunner{}, "/tmp/whatever", item)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason when no commands were parsed")
		}
	})

	t.Run("a real execution error is Not-verified", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{} // no scripted response -> execution error
		item := ClassifyAcceptanceItem("paste the output of `nonexistent-binary`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason when the command could not execute at all")
		}
	})
}

func TestRunMutationItem_DeleteLineMutation(t *testing.T) {
	dir := t.TempDir()
	targetFile := "main.go"
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(dir, targetFile), []byte(original), 0o600); err != nil {
		t.Fatalf("failed to seed test file: %v", err)
	}

	t.Run("failing mutation records the failing test name and reverts the file", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run '^TestMain$' ./...": {
				output: "--- FAIL: TestMain (0.00s)\nFAIL",
				err:    &exec.ExitError{},
			},
		}}
		item := ClassifyAcceptanceItem("delete line 4 in main.go -> TestMain fails")
		result := runMutationItem(context.Background(), runner, dir, item)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if result.MutationOutcome == nil {
			t.Fatal("expected a MutationOutcome")
		}
		if result.MutationOutcome.NoTestFailed {
			t.Error("expected NoTestFailed=false since a test failed")
		}
		if len(result.MutationOutcome.FailingTests) != 1 || result.MutationOutcome.FailingTests[0] != "TestMain" {
			t.Errorf("expected failing test TestMain, got %v", result.MutationOutcome.FailingTests)
		}

		// The mutated line must be gone during the run: verify the command
		// was invoked (proves the harness reached the run step) and that
		// the file on disk is now back to its original content.
		if len(runner.calls) != 1 {
			t.Fatalf("expected exactly 1 command invocation, got %d: %v", len(runner.calls), runner.calls)
		}
		got, err := os.ReadFile(filepath.Join(dir, targetFile))
		if err != nil {
			t.Fatalf("failed to read file after revert: %v", err)
		}
		if string(got) != original {
			t.Fatalf("file was not reverted to its original content: got %q, want %q", got, original)
		}
	})

	t.Run("a mutation that fails no test is reported, not omitted", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run '^TestMain$' ./...": {output: "ok  \tpkg\t0.01s"},
		}}
		item := ClassifyAcceptanceItem("delete line 4 in main.go -> TestMain fails")
		result := runMutationItem(context.Background(), runner, dir, item)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if !result.MutationOutcome.NoTestFailed {
			t.Error("expected NoTestFailed=true, this is a real finding that must be reported")
		}
		if len(result.MutationOutcome.FailingTests) != 0 {
			t.Errorf("expected no failing tests, got %v", result.MutationOutcome.FailingTests)
		}

		got, err := os.ReadFile(filepath.Join(dir, targetFile))
		if err != nil {
			t.Fatalf("failed to read file after revert: %v", err)
		}
		if string(got) != original {
			t.Fatalf("file was not reverted to its original content: got %q, want %q", got, original)
		}
	})

	t.Run("unparseable freeform mutation is Not-verified and leaves the worktree untouched", func(t *testing.T) {
		item := ClassifyAcceptanceItem("change the retry backoff to exponential -> TestBackoff fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, dir, item)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason for a freeform mutation description")
		}
		got, err := os.ReadFile(filepath.Join(dir, targetFile))
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(got) != original {
			t.Fatal("worktree file should not have been touched for an unparseable mutation")
		}
	})

	t.Run("mutation naming a nonexistent file is Not-verified", func(t *testing.T) {
		item := ClassifyAcceptanceItem("delete line 1 in does-not-exist.go -> TestFoo fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, dir, item)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason for a missing file")
		}
	})
}

func TestRunAcceptanceEvidence_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("failed to seed test file: %v", err)
	}

	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go test -run TestX ./pkg/":       {output: "ok\tpkg\t0.01s"},
		"go test -run '^TestMain$' ./...": {output: "--- FAIL: TestMain\nFAIL", err: &exec.ExitError{}},
	}}

	criteria := []string{
		"paste the output of `go test -run TestX ./pkg/`",
		"delete line 3 in main.go -> TestMain fails",
		"a plain acceptance item with no evidence requirement",
	}

	results := RunAcceptanceEvidence(context.Background(), runner, dir, criteria)
	if len(results) != 2 {
		t.Fatalf("expected 2 evidence-requiring results (paste + mutation), got %d", len(results))
	}
	if results[0].Item.Kind != AcceptanceItemPasteOutput {
		t.Errorf("expected first result to be paste_output, got %q", results[0].Item.Kind)
	}
	if results[1].Item.Kind != AcceptanceItemMutation {
		t.Errorf("expected second result to be mutation, got %q", results[1].Item.Kind)
	}

	section := RenderAcceptanceEvidenceSections(results)
	if !strings.Contains(section, "## Evidence") {
		t.Fatalf("expected an Evidence section, got %q", section)
	}
	if !strings.Contains(section, "go test -run TestX ./pkg/") {
		t.Errorf("expected the paste-output command in the rendered section, got %q", section)
	}
	if !strings.Contains(section, "TestMain") {
		t.Errorf("expected the failing mutation test name in the rendered section, got %q", section)
	}
}

func TestAppendAcceptanceEvidence_FlagOffIsByteIdenticalNoOp(t *testing.T) {
	dir := t.TempDir()
	disabled := false
	r := &Runner{config: &BackendConfig{AcceptanceEvidence: &AcceptanceEvidenceConfig{Enabled: &disabled}}}
	task := &Task{
		ID:                 "GH-9999",
		AcceptanceCriteria: []string{"paste the output of `go test ./...`"},
	}
	prBody := "## Summary\n\noriginal body\n"

	got := r.appendAcceptanceEvidence(context.Background(), task, dir, prBody)
	if got != prBody {
		t.Fatalf("expected byte-identical PR body when disabled, got %q", got)
	}
}

func TestAppendAcceptanceEvidence_NoAcceptanceCriteriaIsNoOp(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{config: DefaultBackendConfig()}
	task := &Task{ID: "GH-9999"}
	prBody := "## Summary\n\noriginal body\n"

	got := r.appendAcceptanceEvidence(context.Background(), task, dir, prBody)
	if got != prBody {
		t.Fatalf("expected byte-identical PR body with no acceptance criteria, got %q", got)
	}
}

func TestAppendAcceptanceEvidence_NilConfigDefaultsToEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("noop"), 0o600); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	r := &Runner{} // r.config is nil
	task := &Task{
		ID:                 "GH-9999",
		AcceptanceCriteria: []string{"a plain acceptance item"},
	}
	prBody := "## Summary\n\noriginal body\n"

	// No evidence-requiring items, so this should still be a no-op, but it
	// must not panic on a nil r.config.
	got := r.appendAcceptanceEvidence(context.Background(), task, dir, prBody)
	if got != prBody {
		t.Fatalf("expected byte-identical PR body, got %q", got)
	}
}

func TestAppendAcceptanceEvidence_EnabledAppendsSection(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{config: DefaultBackendConfig()}
	task := &Task{
		ID:                 "GH-9999",
		AcceptanceCriteria: []string{"paste the output of `echo evidence-marker`"},
	}
	prBody := "## Summary\n\noriginal body\n"

	got := r.appendAcceptanceEvidence(context.Background(), task, dir, prBody)
	if !strings.Contains(got, "original body") {
		t.Fatalf("expected original body preserved, got %q", got)
	}
	if !strings.Contains(got, "## Evidence") {
		t.Fatalf("expected an Evidence section appended, got %q", got)
	}
	if !strings.Contains(got, "evidence-marker") {
		t.Fatalf("expected the real command's output in the PR body, got %q", got)
	}
}
