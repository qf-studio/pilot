package workflow

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunHook_Empty(t *testing.T) {
	err := RunHook(context.Background(), "after_create", nil, t.TempDir(), nil, 0, nil)
	if err != nil {
		t.Fatalf("expected nil for empty scripts, got %v", err)
	}
}

func TestRunHook_SingleScript(t *testing.T) {
	dir := t.TempDir()
	var logs []string
	err := RunHook(context.Background(), "after_create",
		HookValue{"echo hello"},
		dir, nil, DefaultHookTimeout,
		func(s string) { logs = append(logs, s) },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "hello") {
		t.Errorf("expected log with 'hello', got %v", logs)
	}
}

func TestRunHook_ListScripts(t *testing.T) {
	dir := t.TempDir()
	var logs []string
	err := RunHook(context.Background(), "before_run",
		HookValue{"echo first", "echo second"},
		dir, nil, DefaultHookTimeout,
		func(s string) { logs = append(logs, s) },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	combined := strings.Join(logs, "\n")
	if !strings.Contains(combined, "first") || !strings.Contains(combined, "second") {
		t.Errorf("expected both outputs, got %v", logs)
	}
}

func TestRunHook_FailureAbortsEarly(t *testing.T) {
	dir := t.TempDir()
	ran := false
	err := RunHook(context.Background(), "after_create",
		HookValue{"exit 1", "echo should-not-run"},
		dir, nil, DefaultHookTimeout,
		func(s string) { ran = ran || strings.Contains(s, "should-not-run") },
	)
	if err == nil {
		t.Fatal("expected error for exit 1, got nil")
	}
	if ran {
		t.Error("second script should not have run after first failed")
	}
}

func TestRunHook_EnvVars(t *testing.T) {
	dir := t.TempDir()
	var logs []string
	env := BuildHookEnv("GH-42", "pilot/GH-42", "https://example.com/issues/42", dir)
	err := RunHook(context.Background(), "before_run",
		HookValue{"echo TASK=$PILOT_TASK_ID BRANCH=$PILOT_BRANCH"},
		dir, env, DefaultHookTimeout,
		func(s string) { logs = append(logs, s) },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	combined := strings.Join(logs, "\n")
	if !strings.Contains(combined, "GH-42") || !strings.Contains(combined, "pilot/GH-42") {
		t.Errorf("expected env vars in output, got %v", logs)
	}
}

func TestRunHook_Timeout(t *testing.T) {
	dir := t.TempDir()
	err := RunHook(context.Background(), "after_create",
		HookValue{"sleep 10"},
		dir, nil, 50*time.Millisecond, nil,
	)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestHookValue_UnmarshalYAML_String(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".pilot/workflow.yaml", `---
version: 1
hooks:
  after_create: "mise install"
---
`)
	wf, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(wf.Hooks.AfterCreate) != 1 || wf.Hooks.AfterCreate[0] != "mise install" {
		t.Errorf("expected [mise install], got %v", wf.Hooks.AfterCreate)
	}
}

func TestHookValue_UnmarshalYAML_List(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".pilot/workflow.yaml", `---
version: 1
hooks:
  before_run:
    - mise install
    - npm ci
---
`)
	wf, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(wf.Hooks.BeforeRun) != 2 {
		t.Fatalf("expected 2 scripts, got %d: %v", len(wf.Hooks.BeforeRun), wf.Hooks.BeforeRun)
	}
	if wf.Hooks.BeforeRun[0] != "mise install" || wf.Hooks.BeforeRun[1] != "npm ci" {
		t.Errorf("unexpected scripts: %v", wf.Hooks.BeforeRun)
	}
}
