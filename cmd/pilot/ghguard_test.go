// GH-4671: e2e-style tests for the gh-guard shim target. These exercise
// runGhGuard end-to-end (env -> Classify -> journal/exec) against a fake
// `gh` script rather than testing ghguard.Classify's rule table directly
// (that's already covered exhaustively in internal/executor/ghguard).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeGh writes an executable shell script standing in for the real
// `gh` binary: it records its argv (one arg per line) to recordPath, then
// exits 0. Used to verify runGhGuard execs the real binary with the exact
// argv it was given, without needing gh installed in the test environment.
func writeFakeGh(t *testing.T, dir, recordPath string) string {
	t.Helper()
	ghPath := filepath.Join(dir, "gh")
	script := "#!/bin/sh\n" +
		"rm -f " + recordPath + "\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + recordPath + "; done\n" +
		"exit 0\n"
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake gh: %v", err)
	}
	return ghPath
}

func envLookup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRunGhGuard_AllowsReadAndExecsRealGh(t *testing.T) {
	tmpDir := t.TempDir()
	recordPath := filepath.Join(tmpDir, "record.txt")
	fakeGh := writeFakeGh(t, tmpDir, recordPath)
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	env := envLookup(map[string]string{
		"PILOT_TASK_ISSUE":       "123",
		"PILOT_TASK_REPO":        "qf-studio/pilot",
		"PILOT_TASK_BRANCH":      "pilot/GH-123",
		"PILOT_GH_REAL":          fakeGh,
		"PILOT_GH_GUARD_JOURNAL": journalPath,
	})

	var stdout, stderr bytes.Buffer
	code := runGhGuard([]string{"issue", "view", "123"}, env, strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", code, stderr.String())
	}

	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("expected fake gh to have run and recorded argv: %v", err)
	}
	got := strings.Fields(string(recorded))
	want := []string{"issue", "view", "123"}
	if len(got) != len(want) {
		t.Fatalf("argv mismatch: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if _, err := os.Stat(journalPath); err == nil {
		t.Error("expected no journal file to be written on an allow verdict")
	}
}

func TestRunGhGuard_DeniesMutationAndNeverExecs(t *testing.T) {
	tmpDir := t.TempDir()
	recordPath := filepath.Join(tmpDir, "record.txt")
	fakeGh := writeFakeGh(t, tmpDir, recordPath)
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	env := envLookup(map[string]string{
		"PILOT_TASK_ISSUE":       "123",
		"PILOT_TASK_REPO":        "qf-studio/pilot",
		"PILOT_TASK_BRANCH":      "pilot/GH-123",
		"PILOT_GH_REAL":          fakeGh,
		"PILOT_GH_GUARD_JOURNAL": journalPath,
	})

	var stdout, stderr bytes.Buffer
	code := runGhGuard([]string{"issue", "close", "999"}, env, strings.NewReader(""), &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected non-zero exit code for a denied mutation")
	}

	if _, err := os.Stat(recordPath); err == nil {
		t.Error("expected fake gh to never be invoked on a deny verdict")
	}

	if !strings.Contains(stderr.String(), "denied") {
		t.Errorf("expected stderr to explain the denial, got: %s", stderr.String())
	}

	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("expected a journal entry to be written on deny: %v", err)
	}
	journalStr := string(journalBytes)
	if !strings.Contains(journalStr, `"issue"`) || !strings.Contains(journalStr, `"close"`) {
		t.Errorf("expected journal entry to record the denied args, got: %s", journalStr)
	}
	if !strings.Contains(journalStr, `"task_issue":"123"`) {
		t.Errorf("expected journal entry to record task identity, got: %s", journalStr)
	}
}

func TestRunGhGuard_NoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runGhGuard(nil, envLookup(nil), strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2 for empty argv, got %d", code)
	}
}

func TestRunGhGuard_AllowFallsBackWhenRealGhUnset(t *testing.T) {
	shimDir := t.TempDir()
	realDir := t.TempDir()
	recordPath := filepath.Join(realDir, "record.txt")
	writeFakeGh(t, realDir, recordPath)

	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	_ = os.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)

	env := envLookup(map[string]string{
		"PILOT_TASK_ISSUE":        "123",
		"PILOT_TASK_REPO":         "qf-studio/pilot",
		"PILOT_TASK_BRANCH":       "pilot/GH-123",
		"PILOT_GH_GUARD_SHIM_DIR": shimDir,
		"PILOT_GH_GUARD_JOURNAL":  filepath.Join(shimDir, "journal.jsonl"),
	})

	var stdout, stderr bytes.Buffer
	code := runGhGuard([]string{"pr", "list"}, env, strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0 via fallback gh, got %d (stderr: %s)", code, stderr.String())
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Errorf("expected fallback-resolved gh to have run: %v", err)
	}
}
