// GH-4671: regression tests for the spawn-side gh-guard wiring — shim
// directory creation, PATH ordering, and env-var completeness. The policy
// classification itself is tested exhaustively in internal/executor/ghguard;
// this file only covers the process-boundary glue in ghguard_spawn.go.
package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

func TestSetupGhGuardShim_CreatesExecutableShim(t *testing.T) {
	shimDir, journalPath, cleanup, err := setupGhGuardShim("/usr/bin/gh")
	if err != nil {
		t.Fatalf("setupGhGuardShim() error = %v", err)
	}
	defer cleanup()

	shimPath := filepath.Join(shimDir, "gh")
	info, err := os.Stat(shimPath)
	if err != nil {
		t.Fatalf("expected shim script at %s: %v", shimPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("expected shim script to be executable, mode = %v", info.Mode())
	}

	content, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("failed to read shim script: %v", err)
	}
	if !strings.Contains(string(content), "gh-guard --") {
		t.Errorf("expected shim script to re-exec via gh-guard, got: %s", content)
	}

	wantJournal := filepath.Join(shimDir, "journal.jsonl")
	if journalPath != wantJournal {
		t.Errorf("journalPath = %q, want %q", journalPath, wantJournal)
	}

	cleanup()
	if _, err := os.Stat(shimDir); !os.IsNotExist(err) {
		t.Errorf("expected shim dir to be removed after cleanup, stat err = %v", err)
	}
}

func TestSetupGhGuardShim_AllowsEmptyRealGh(t *testing.T) {
	// realGh may be empty at daemon startup if `gh` wasn't resolvable on the
	// daemon's own PATH — the shim must still be installed so mutations stay
	// blocked (fail closed), even though reads will need a fallback PATH
	// search at gh-guard invocation time.
	shimDir, _, cleanup, err := setupGhGuardShim("")
	if err != nil {
		t.Fatalf("setupGhGuardShim(\"\") error = %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(shimDir, "gh")); err != nil {
		t.Errorf("expected shim to be installed even with empty realGh: %v", err)
	}
}

func TestPrependPathEnv_RewritesExistingPathInPlace(t *testing.T) {
	env := []string{"HOME=/home/x", "PATH=/usr/bin:/bin", "LANG=C"}
	got := prependPathEnv(env, "/tmp/shim")

	var pathEntries []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") {
			pathEntries = append(pathEntries, kv)
		}
	}
	if len(pathEntries) != 1 {
		t.Fatalf("expected exactly one PATH= entry, got %d: %v", len(pathEntries), pathEntries)
	}

	want := "PATH=/tmp/shim" + string(os.PathListSeparator) + "/usr/bin:/bin"
	if pathEntries[0] != want {
		t.Errorf("PATH entry = %q, want %q", pathEntries[0], want)
	}

	// Shim dir must come first so it's found before the real gh on PATH.
	if !strings.HasPrefix(pathEntries[0], "PATH=/tmp/shim"+string(os.PathListSeparator)) {
		t.Errorf("expected shim dir to be prepended (first) in PATH, got %q", pathEntries[0])
	}
}

func TestPrependPathEnv_AppendsWhenNoExistingPath(t *testing.T) {
	env := []string{"HOME=/home/x"}
	got := prependPathEnv(env, "/tmp/shim")

	found := false
	for _, kv := range got {
		if kv == "PATH=/tmp/shim" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected PATH=/tmp/shim to be appended, got %v", got)
	}
}

func TestGhGuardTaskEnv_SetsAllRequiredVars(t *testing.T) {
	opts := ExecuteOptions{
		SourceIssueID: "42",
		SourceRepo:    "qf-studio/pilot",
		Branch:        "pilot/GH-42",
	}
	env := ghGuardTaskEnv(opts, "/usr/bin/gh", "/tmp/shimdir", "/tmp/shimdir/journal.jsonl")

	want := map[string]string{
		ghguard.EnvTaskIssue:   "42",
		ghguard.EnvTaskRepo:    "qf-studio/pilot",
		ghguard.EnvTaskBranch:  "pilot/GH-42",
		ghguard.EnvRealGh:      "/usr/bin/gh",
		ghguard.EnvShimDir:     "/tmp/shimdir",
		ghguard.EnvJournalPath: "/tmp/shimdir/journal.jsonl",
	}

	got := map[string]string{}
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			got[parts[0]] = parts[1]
		}
	}

	for k, wantV := range want {
		if got[k] != wantV {
			t.Errorf("env[%s] = %q, want %q", k, got[k], wantV)
		}
	}
	if len(env) != len(want) {
		t.Errorf("expected exactly %d env entries, got %d: %v", len(want), len(env), env)
	}
}

func TestReadGhGuardJournal_EmptyPathReturnsNil(t *testing.T) {
	if entries := readGhGuardJournal(""); entries != nil {
		t.Errorf("expected nil for empty journal path, got %v", entries)
	}
}

func TestReadGhGuardJournal_MissingFileFailsOpen(t *testing.T) {
	tmpDir := t.TempDir()
	entries := readGhGuardJournal(filepath.Join(tmpDir, "does-not-exist.jsonl"))
	if entries != nil {
		t.Errorf("expected nil for a missing journal file (fail open), got %v", entries)
	}
}

func TestReadGhGuardJournal_ReadsAppendedEntries(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	entry := ghguard.JournalEntry{
		Verdict:   ghguard.VerdictDeny,
		Reason:    "issue lifecycle mutation",
		Args:      []string{"issue", "close", "1"},
		TaskIssue: "1",
		TaskRepo:  "qf-studio/pilot",
	}
	if err := ghguard.AppendJournal(journalPath, entry); err != nil {
		t.Fatalf("AppendJournal() error = %v", err)
	}

	entries := readGhGuardJournal(journalPath)
	if len(entries) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(entries))
	}
	if entries[0].Reason != entry.Reason {
		t.Errorf("Reason = %q, want %q", entries[0].Reason, entry.Reason)
	}
}
