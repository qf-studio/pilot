package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/workflow"
)

func writeWorkflow(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".pilot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pilot", "workflow.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoad_FileAbsent(t *testing.T) {
	dir := t.TempDir()
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if w != nil {
		t.Fatal("expected nil Workflow when file is absent")
	}
}

func TestLoad_FullFile(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, `---
version: 1
agent:
  max_turns: 25
  reasoning_effort: high
policy:
  commit_format: conventional
  branch_prefix: pilot/GH-
  pr_template: .github/pull_request_template.md
hooks:
  after_create: null
  before_run: null
---

# Workflow

Project-specific instructions go here.
`)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil Workflow")
	}
	if w.Version != 1 {
		t.Errorf("Version: got %d, want 1", w.Version)
	}
	if w.Agent.MaxTurns != 25 {
		t.Errorf("Agent.MaxTurns: got %d, want 25", w.Agent.MaxTurns)
	}
	if w.Agent.ReasoningEffort != "high" {
		t.Errorf("Agent.ReasoningEffort: got %q, want %q", w.Agent.ReasoningEffort, "high")
	}
	if w.Policy.CommitFormat != "conventional" {
		t.Errorf("Policy.CommitFormat: got %q, want %q", w.Policy.CommitFormat, "conventional")
	}
	if w.Policy.BranchPrefix != "pilot/GH-" {
		t.Errorf("Policy.BranchPrefix: got %q, want %q", w.Policy.BranchPrefix, "pilot/GH-")
	}
	if w.Policy.PRTemplate != ".github/pull_request_template.md" {
		t.Errorf("Policy.PRTemplate: got %q", w.Policy.PRTemplate)
	}
	wantBody := "# Workflow\n\nProject-specific instructions go here."
	if w.PromptAppendix != wantBody {
		t.Errorf("PromptAppendix:\ngot  %q\nwant %q", w.PromptAppendix, wantBody)
	}
}

func TestLoad_MinimalFile(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, `---
version: 1
---
`)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil Workflow")
	}
	if w.Agent.MaxTurns != 0 {
		t.Errorf("expected zero MaxTurns, got %d", w.Agent.MaxTurns)
	}
	if w.PromptAppendix != "" {
		t.Errorf("expected empty PromptAppendix, got %q", w.PromptAppendix)
	}
}

func TestLoad_UnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, `---
version: 99
---
`)
	_, err := workflow.Load(dir)
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestLoad_MissingOpeningDelimiter(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "version: 1\n")
	_, err := workflow.Load(dir)
	if err == nil {
		t.Fatal("expected error for missing opening delimiter")
	}
}

func TestLoad_MissingClosingDelimiter(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "---\nversion: 1\n")
	_, err := workflow.Load(dir)
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestLoad_UnknownKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, `---
version: 1
future_feature: some_value
---
`)
	// Should load without error; unknown keys are warned but not fatal.
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil Workflow")
	}
}

func TestLoad_PromptAppendixOnly(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, `---
version: 1
---

## Project Conventions

- Always run tests before committing.
- Use conventional commits.
`)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil Workflow")
	}
	if w.PromptAppendix == "" {
		t.Error("expected non-empty PromptAppendix")
	}
}
