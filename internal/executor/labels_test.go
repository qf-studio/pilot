package executor

import (
	"context"
	"os"
	"strings"
	"testing"
)

// writeFakeGhLabelCreate installs a fake "gh" on PATH that logs every
// invocation's args to logFile (one line per call, space-joined) and exits
// with failExitCode for any label name in failFor (simulating a transient
// per-label failure), 0 otherwise. Used by the GH-4526 EnsureRepoLabels
// tests below.
func writeFakeGhLabelCreate(t *testing.T, logFile string, failFor map[string]bool) string {
	t.Helper()
	fakeBin := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> ` + logFile + `
`
	for name := range failFor {
		script += `if [ "$3" = "` + name + `" ]; then echo "fail" 1>&2; exit 1; fi
`
	}
	script += `exit 0
`
	if err := os.WriteFile(fakeBin+"/gh", []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return fakeBin
}

// TestEnsureRepoLabels_CreatesEveryPilotLabel is the GH-4526 regression
// guard for the secondary finding: a hosted tenant repo onboarded with zero
// pre-existing pilot-* labels must have all of them created (idempotently,
// via `gh label create --force`) so stalled-issue surfacing's
// `gh issue edit --add-label pilot-blocked` (and every sibling label write)
// never again fails with "label ... not found".
func TestEnsureRepoLabels_CreatesEveryPilotLabel(t *testing.T) {
	logFile := t.TempDir() + "/gh-calls.log"
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatalf("failed to seed log file: %v", err)
	}
	fakeBin := writeFakeGhLabelCreate(t, logFile, nil)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	errs := EnsureRepoLabels(context.Background(), "")
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}

	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read call log: %v", err)
	}
	calls := string(logged)
	for _, spec := range PilotLabels {
		want := "label create " + spec.Name
		if !strings.Contains(calls, want) {
			t.Errorf("expected a call creating label %q, log:\n%s", spec.Name, calls)
		}
		if !strings.Contains(calls, "--force") {
			t.Errorf("expected --force for idempotent creation, log:\n%s", calls)
		}
	}
}

// TestEnsureRepoLabels_OneFailureDoesNotBlockOthers guards the best-effort
// contract: a single label failing to create (transient error) must not
// stop the rest from being attempted, and must be reported back to the
// caller rather than silently swallowed.
func TestEnsureRepoLabels_OneFailureDoesNotBlockOthers(t *testing.T) {
	logFile := t.TempDir() + "/gh-calls.log"
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatalf("failed to seed log file: %v", err)
	}
	fakeBin := writeFakeGhLabelCreate(t, logFile, map[string]bool{"pilot-blocked": true})
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	errs := EnsureRepoLabels(context.Background(), "")
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "pilot-blocked") {
		t.Errorf("expected error to name the failing label, got: %v", errs[0])
	}

	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read call log: %v", err)
	}
	calls := string(logged)
	// Every label, including ones after the failing one, must still have
	// been attempted.
	for _, spec := range PilotLabels {
		want := "label create " + spec.Name
		if !strings.Contains(calls, want) {
			t.Errorf("expected label %q to still be attempted despite an earlier failure, log:\n%s", spec.Name, calls)
		}
	}
}
