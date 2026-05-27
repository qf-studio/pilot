package workflow_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/workflow"
)

// writeFile creates a file with content under dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
	return p
}

func TestRunHook_Nil(t *testing.T) {
	err := workflow.RunHook(context.Background(), "after_create", nil, t.TempDir(), os.Environ(), 0, nil)
	if err != nil {
		t.Fatalf("nil scripts: unexpected error %v", err)
	}
}

func TestRunHook_Empty(t *testing.T) {
	err := workflow.RunHook(context.Background(), "after_create", workflow.HookValue{}, t.TempDir(), os.Environ(), 0, nil)
	if err != nil {
		t.Fatalf("empty scripts: unexpected error %v", err)
	}
}

func TestRunHook_Sequential(t *testing.T) {
	dir := t.TempDir()
	log := writeFile(t, dir, "log.txt", "")
	scripts := workflow.HookValue{
		fmt.Sprintf("echo first >> %s", log),
		fmt.Sprintf("echo second >> %s", log),
		fmt.Sprintf("echo third >> %s", log),
	}
	if err := workflow.RunHook(context.Background(), "test", scripts, dir, os.Environ(), 0, nil); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if want := "first\nsecond\nthird"; got != want {
		t.Errorf("execution order:\ngot  %q\nwant %q", got, want)
	}
}

func TestRunHook_Timeout(t *testing.T) {
	dir := t.TempDir()
	err := workflow.RunHook(context.Background(), "test", workflow.HookValue{"sleep 30"}, dir, os.Environ(), 50*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestRunHook_EnvPassthrough(t *testing.T) {
	dir := t.TempDir()
	out := writeFile(t, dir, "env.txt", "")
	env := append(os.Environ(), "PILOT_HOOK_TEST=hello_hooks")
	scripts := workflow.HookValue{fmt.Sprintf("echo $PILOT_HOOK_TEST > %s", out)}
	if err := workflow.RunHook(context.Background(), "test", scripts, dir, env, 0, nil); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "hello_hooks" {
		t.Errorf("env var: got %q, want %q", got, "hello_hooks")
	}
}

func TestRunHook_FirstFailureStopsRest(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	scripts := workflow.HookValue{
		"exit 1",
		fmt.Sprintf("touch %s", sentinel),
	}
	if err := workflow.RunHook(context.Background(), "test", scripts, dir, os.Environ(), 0, nil); err == nil {
		t.Fatal("expected error from failing script")
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("second script ran after first failure")
	}
}

func TestRunHook_LogFn(t *testing.T) {
	dir := t.TempDir()
	var captured []string
	logFn := func(s string) { captured = append(captured, s) }
	if err := workflow.RunHook(context.Background(), "test", workflow.HookValue{"echo pilot_hook_output"}, dir, os.Environ(), 0, logFn); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("logFn was never called")
	}
	if combined := strings.Join(captured, ""); !strings.Contains(combined, "pilot_hook_output") {
		t.Errorf("logFn output %q missing expected string", combined)
	}
}

func TestHookValue_YAML(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, `---
version: 1
hooks:
  after_create: echo hello
  before_run:
    - echo step1
    - echo step2
  after_run: null
---
`)
	w, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil Workflow")
	}
	if got := len(w.Hooks.AfterCreate); got != 1 {
		t.Errorf("AfterCreate: len=%d, want 1", got)
	} else if w.Hooks.AfterCreate[0] != "echo hello" {
		t.Errorf("AfterCreate[0]: got %q, want %q", w.Hooks.AfterCreate[0], "echo hello")
	}
	if got := len(w.Hooks.BeforeRun); got != 2 {
		t.Errorf("BeforeRun: len=%d, want 2", got)
	}
	if w.Hooks.AfterRun != nil {
		t.Errorf("AfterRun: expected nil, got %v", w.Hooks.AfterRun)
	}
}
