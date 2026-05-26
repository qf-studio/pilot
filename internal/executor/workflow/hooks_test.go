package workflow_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/workflow"
)

func TestRunHook_Success(t *testing.T) {
	dir := t.TempDir()
	hooks := &workflow.WorkflowHooks{}
	hooks.AfterCreate = workflow.StringOrSlice([]string{"echo hello"})

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err := workflow.RunHook(context.Background(), "after_create", hooks, workflow.HookEnv{
		TaskID:  "T-1",
		Branch:  "pilot/GH-1",
		Worktree: dir,
	}, dir, log)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestRunHook_Failure(t *testing.T) {
	dir := t.TempDir()
	hooks := &workflow.WorkflowHooks{}
	hooks.BeforeRun = workflow.StringOrSlice([]string{"exit 1"})

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err := workflow.RunHook(context.Background(), "before_run", hooks, workflow.HookEnv{}, dir, log)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "before_run[0] failed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRunHook_Timeout(t *testing.T) {
	dir := t.TempDir()
	hooks := &workflow.WorkflowHooks{TimeoutSec: 1}
	hooks.AfterCreate = workflow.StringOrSlice([]string{"sleep 10"})

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	start := time.Now()
	err := workflow.RunHook(context.Background(), "after_create", hooks, workflow.HookEnv{}, dir, log)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout in error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestRunHook_EnvVars(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "env_out.txt")

	hooks := &workflow.WorkflowHooks{}
	hooks.AfterCreate = workflow.StringOrSlice([]string{
		"echo $PILOT_TASK_ID $PILOT_BRANCH $PILOT_WORKTREE > " + outFile,
	})

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	env := workflow.HookEnv{
		TaskID:   "GH-42",
		Branch:   "pilot/GH-42",
		Worktree: dir,
	}
	if err := workflow.RunHook(context.Background(), "after_create", hooks, env, dir, log); err != nil {
		t.Fatalf("hook failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}
	output := strings.TrimSpace(string(data))
	if !strings.Contains(output, "GH-42") {
		t.Errorf("PILOT_TASK_ID not found in output: %q", output)
	}
	if !strings.Contains(output, "pilot/GH-42") {
		t.Errorf("PILOT_BRANCH not found in output: %q", output)
	}
}

func TestRunHook_MultipleScripts(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "order.txt")

	hooks := &workflow.WorkflowHooks{}
	hooks.AfterCreate = workflow.StringOrSlice([]string{
		"echo first >> " + outFile,
		"echo second >> " + outFile,
	})

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := workflow.RunHook(context.Background(), "after_create", hooks, workflow.HookEnv{}, dir, log); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
		t.Errorf("unexpected output order: %v", lines)
	}
}

func TestRunHook_StopsOnFirstFailure(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "reached.txt")

	hooks := &workflow.WorkflowHooks{}
	hooks.BeforeRun = workflow.StringOrSlice([]string{
		"exit 1",
		"echo reached > " + outFile,
	})

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err := workflow.RunHook(context.Background(), "before_run", hooks, workflow.HookEnv{}, dir, log)
	if err == nil {
		t.Fatal("expected error from first script")
	}

	if _, statErr := os.Stat(outFile); !os.IsNotExist(statErr) {
		t.Error("second script ran despite first failure")
	}
}

func TestRunHook_NoScripts(t *testing.T) {
	dir := t.TempDir()
	hooks := &workflow.WorkflowHooks{} // no hooks configured

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := workflow.RunHook(context.Background(), "after_create", hooks, workflow.HookEnv{}, dir, log); err != nil {
		t.Fatalf("expected nil for empty hooks, got: %v", err)
	}
}

func TestRunHook_NilHooks(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := workflow.RunHook(context.Background(), "after_create", nil, workflow.HookEnv{}, dir, log); err != nil {
		t.Fatalf("expected nil for nil hooks, got: %v", err)
	}
}

func TestLoad_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	hooks, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if hooks != nil {
		t.Fatalf("expected nil hooks for missing file, got: %+v", hooks)
	}
}

func TestLoad_ParseHooks(t *testing.T) {
	dir := t.TempDir()
	pilotDir := filepath.Join(dir, ".pilot")
	if err := os.MkdirAll(pilotDir, 0755); err != nil {
		t.Fatal(err)
	}

	yaml := `
hooks:
  after_create:
    - "mise install"
    - "npm ci"
  before_run: "echo warming"
  timeout_sec: 120
`
	if err := os.WriteFile(filepath.Join(pilotDir, "workflow.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if hooks == nil {
		t.Fatal("expected non-nil hooks")
	}

	ac := hooks.Scripts("after_create")
	if len(ac) != 2 || ac[0] != "mise install" || ac[1] != "npm ci" {
		t.Errorf("after_create: want [mise install, npm ci], got %v", ac)
	}

	br := hooks.Scripts("before_run")
	if len(br) != 1 || br[0] != "echo warming" {
		t.Errorf("before_run: want [echo warming], got %v", br)
	}

	if hooks.Timeout() != 120*time.Second {
		t.Errorf("timeout: want 120s, got %v", hooks.Timeout())
	}
}

func TestLoad_NoHooksBlock(t *testing.T) {
	dir := t.TempDir()
	pilotDir := filepath.Join(dir, ".pilot")
	if err := os.MkdirAll(pilotDir, 0755); err != nil {
		t.Fatal(err)
	}

	// workflow.yaml with no hooks block (future TASK-304 fields only)
	yaml := `
version: 1
agent:
  max_turns: 20
`
	if err := os.WriteFile(filepath.Join(pilotDir, "workflow.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if hooks != nil {
		t.Fatalf("expected nil hooks when block absent, got: %+v", hooks)
	}
}

func TestStringOrSlice_UnmarshalSingle(t *testing.T) {
	dir := t.TempDir()
	pilotDir := filepath.Join(dir, ".pilot")
	if err := os.MkdirAll(pilotDir, 0755); err != nil {
		t.Fatal(err)
	}

	yaml := "hooks:\n  after_create: \"mise install\"\n"
	if err := os.WriteFile(filepath.Join(pilotDir, "workflow.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if hooks == nil {
		t.Fatal("expected hooks")
	}
	ac := hooks.Scripts("after_create")
	if len(ac) != 1 || ac[0] != "mise install" {
		t.Errorf("want [mise install], got %v", ac)
	}
}
