package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSetGHGuard(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	backend.SetGHGuard(false)
	if backend.ghGuardEnabled {
		t.Error("ghGuardEnabled should be false after SetGHGuard(false)")
	}
	if backend.ghRealPath != "" {
		t.Errorf("ghRealPath should be empty when disabled, got %q", backend.ghRealPath)
	}

	backend.SetGHGuard(true)
	if !backend.ghGuardEnabled {
		t.Error("ghGuardEnabled should be true after SetGHGuard(true)")
	}
	want, lookErr := exec.LookPath("gh")
	if lookErr == nil {
		if backend.ghRealPath != want {
			t.Errorf("ghRealPath = %q, want %q (exec.LookPath result)", backend.ghRealPath, want)
		}
	} else if backend.ghRealPath != "" {
		t.Errorf("expected empty ghRealPath when gh isn't on PATH, got %q", backend.ghRealPath)
	}
}

func TestSetupGHGuardShim_DisabledIsNoOp(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)
	dir, cleanup, err := backend.setupGHGuardShim(ExecuteOptions{TaskID: "GH-4671"})
	if err != nil {
		t.Fatalf("setupGHGuardShim() error: %v", err)
	}
	if dir != "" {
		t.Errorf("expected no shim dir when guard is disabled, got %q", dir)
	}
	cleanup() // must be safe to call even as a no-op
}

func TestSetupGHGuardShim_NoRealGHIsNoOp(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)
	backend.ghGuardEnabled = true
	backend.ghRealPath = "" // simulates gh not found on the host (SetGHGuard's LookPath failure path)

	dir, cleanup, err := backend.setupGHGuardShim(ExecuteOptions{TaskID: "GH-4671"})
	if err != nil {
		t.Fatalf("setupGHGuardShim() error: %v", err)
	}
	if dir != "" {
		t.Errorf("expected no shim dir when real gh wasn't resolved, got %q", dir)
	}
	cleanup()
}

func TestSetupGHGuardShim_CreatesExecutableShimAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shim is a POSIX shell script")
	}
	backend := NewClaudeCodeBackend(nil)
	backend.ghGuardEnabled = true
	backend.ghRealPath = "/usr/bin/gh" // needn't exist for this unit-level check

	dir, cleanup, err := backend.setupGHGuardShim(ExecuteOptions{TaskID: "GH-4671"})
	if err != nil {
		t.Fatalf("setupGHGuardShim() error: %v", err)
	}
	if dir == "" {
		t.Fatal("expected a non-empty shim dir")
	}

	ghPath := filepath.Join(dir, "gh")
	info, statErr := os.Stat(ghPath)
	if statErr != nil {
		t.Fatalf("stat shim gh script: %v", statErr)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("shim gh script not executable: mode %v", info.Mode())
	}
	content, readErr := os.ReadFile(ghPath)
	if readErr != nil {
		t.Fatalf("read shim gh script: %v", readErr)
	}
	if !strings.Contains(string(content), "gh-guard") {
		t.Errorf("shim script doesn't reference gh-guard: %q", content)
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected shim dir removed after cleanup, stat err = %v", err)
	}
}

// TestClaudeCodeBackend_GHGuardSpawnWiring is the GH-4671 acceptance
// criterion 6 spawn-layer regression test: with gh-guard enabled, Execute
// must prepend the shim dir to the child's PATH (ahead of the real gh),
// set PILOT_GH_REAL/PILOT_TASK_*/PILOT_GH_GUARD_JOURNAL completely and
// correctly, and clean the shim dir up once the run completes.
func TestClaudeCodeBackend_GHGuardSpawnWiring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "env.log")
	script := filepath.Join(tmpDir, "fake-claude")
	body := `#!/bin/sh
{
  echo "PILOT_GH_REAL=$PILOT_GH_REAL"
  echo "PILOT_TASK_ISSUE=$PILOT_TASK_ISSUE"
  echo "PILOT_TASK_REPO=$PILOT_TASK_REPO"
  echo "PILOT_TASK_BRANCH=$PILOT_TASK_BRANCH"
  echo "PILOT_GH_GUARD_JOURNAL=$PILOT_GH_GUARD_JOURNAL"
  echo "WHICH_GH=$(command -v gh)"
  echo "SHIM_CONTENT=$(cat "$(command -v gh)" 2>/dev/null | tr '\n' ' ')"
} > ` + logFile + `
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	backend.SetGHGuard(true)
	if backend.ghRealPath == "" {
		t.Skip("real gh not found on test host PATH; can't exercise gh-guard spawn wiring")
	}

	opts := ExecuteOptions{
		Prompt:        "hello",
		ProjectPath:   tmpDir,
		TaskID:        "GH-4671",
		GHGuardIssue:  "4671",
		GHGuardRepo:   "qf-studio/pilot",
		GHGuardBranch: "pilot/GH-4671",
		EventHandler:  func(BackendEvent) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := backend.Execute(ctx, opts); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read env log: %v", readErr)
	}
	env := string(data)
	lines := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(env, "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			lines[k] = v
		}
	}

	if lines["PILOT_GH_REAL"] != backend.ghRealPath {
		t.Errorf("PILOT_GH_REAL = %q, want %q", lines["PILOT_GH_REAL"], backend.ghRealPath)
	}
	if lines["PILOT_TASK_ISSUE"] != "4671" {
		t.Errorf("PILOT_TASK_ISSUE = %q, want 4671", lines["PILOT_TASK_ISSUE"])
	}
	if lines["PILOT_TASK_REPO"] != "qf-studio/pilot" {
		t.Errorf("PILOT_TASK_REPO = %q, want qf-studio/pilot", lines["PILOT_TASK_REPO"])
	}
	if lines["PILOT_TASK_BRANCH"] != "pilot/GH-4671" {
		t.Errorf("PILOT_TASK_BRANCH = %q, want pilot/GH-4671", lines["PILOT_TASK_BRANCH"])
	}
	wantJournal := ghGuardJournalPath("GH-4671")
	if lines["PILOT_GH_GUARD_JOURNAL"] != wantJournal {
		t.Errorf("PILOT_GH_GUARD_JOURNAL = %q, want %q", lines["PILOT_GH_GUARD_JOURNAL"], wantJournal)
	}

	whichGH := lines["WHICH_GH"]
	if whichGH == "" {
		t.Fatal("child process could not resolve `gh` on PATH at all")
	}
	if whichGH == backend.ghRealPath {
		t.Errorf("child's `gh` resolved directly to the real binary (%q) — shim dir wasn't prepended to PATH first", whichGH)
	}
	if !strings.Contains(lines["SHIM_CONTENT"], "gh-guard") {
		t.Errorf("resolved `gh` doesn't look like the gh-guard shim: %q", lines["SHIM_CONTENT"])
	}

	shimDir := filepath.Dir(whichGH)
	if _, statErr := os.Stat(shimDir); !os.IsNotExist(statErr) {
		t.Errorf("shim dir %q should have been cleaned up after Execute returned, stat err = %v", shimDir, statErr)
	}
}
