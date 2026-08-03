package ghguard

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRun_AllowInvokesExecer(t *testing.T) {
	var called bool
	var gotRealGH string
	var stdout, stderr bytes.Buffer
	cfg := RunConfig{
		Args:    []string{"issue", "view", "4671"},
		RealGH:  "/usr/bin/gh",
		TaskCtx: TaskContext{Issue: "4671", Repo: "qf-studio/pilot", Branch: "pilot/GH-4671"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Exec: func(realGH string, args []string, out, errW io.Writer) int {
			called = true
			gotRealGH = realGH
			return 0
		},
	}
	code := Run(cfg)
	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if !called {
		t.Fatal("expected Execer to be called for an allowed command")
	}
	if gotRealGH != "/usr/bin/gh" {
		t.Errorf("Execer realGH = %q, want /usr/bin/gh", gotRealGH)
	}
}

func TestRun_DenyDoesNotInvokeExecer(t *testing.T) {
	called := false
	var stdout, stderr bytes.Buffer
	journalPath := filepath.Join(t.TempDir(), "GH-4671.jsonl")
	cfg := RunConfig{
		Args:        []string{"issue", "close", "4649"},
		RealGH:      "/usr/bin/gh",
		TaskCtx:     TaskContext{Issue: "4671", Repo: "qf-studio/pilot", Branch: "pilot/GH-4671"},
		JournalPath: journalPath,
		Stdout:      &stdout,
		Stderr:      &stderr,
		Exec: func(realGH string, args []string, out, errW io.Writer) int {
			called = true
			return 0
		},
	}
	code := Run(cfg)
	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if called {
		t.Fatal("Execer must not be called for a denied command")
	}
	if !strings.Contains(stderr.String(), "blocked") {
		t.Errorf("stderr = %q, want a blocked message", stderr.String())
	}

	entries, err := ReadJournal(journalPath)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(entries))
	}
}

func TestRun_NoRealGHFailsClosed(t *testing.T) {
	called := false
	var stderr bytes.Buffer
	cfg := RunConfig{
		Args:    []string{"issue", "view", "1"}, // read-only — would otherwise be allowed
		RealGH:  "",
		TaskCtx: TaskContext{Issue: "1", Repo: "qf-studio/pilot", Branch: "b"},
		Stderr:  &stderr,
		Exec: func(realGH string, args []string, out, errW io.Writer) int {
			called = true
			return 0
		},
	}
	code := Run(cfg)
	if code != 126 {
		t.Fatalf("Run() = %d, want 126", code)
	}
	if called {
		t.Fatal("Execer must not be called when RealGH can't be resolved")
	}
}

// TestRun_DefaultExecer_E2E exercises DefaultExecer (the production path)
// against a fake `gh` script — one allow path (script runs, exit 0, stdout
// captured) and one deny path (script never runs), satisfying GH-4671
// acceptance criterion 6's e2e-style requirement.
func TestRun_DefaultExecer_E2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh script uses a shell shebang")
	}

	dir := t.TempDir()
	fakeGH := filepath.Join(dir, "fake-gh.sh")
	script := "#!/bin/sh\necho \"real-gh-called: $@\"\nexit 0\n"
	if err := os.WriteFile(fakeGH, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh script: %v", err)
	}

	ctx := TaskContext{Issue: "4671", Repo: "qf-studio/pilot", Branch: "pilot/GH-4671"}

	t.Run("allow", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(RunConfig{
			Args:    []string{"issue", "view", "4671"},
			RealGH:  fakeGH,
			TaskCtx: ctx,
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
		if code != 0 {
			t.Fatalf("Run() = %d, want 0; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "real-gh-called: issue view 4671") {
			t.Errorf("stdout = %q, want it to show the real gh script ran", stdout.String())
		}
	})

	t.Run("deny", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		journalPath := filepath.Join(dir, "journal.jsonl")
		code := Run(RunConfig{
			Args:        []string{"issue", "close", "4649"},
			RealGH:      fakeGH,
			TaskCtx:     ctx,
			JournalPath: journalPath,
			Stdout:      &stdout,
			Stderr:      &stderr,
		})
		if code != 1 {
			t.Fatalf("Run() = %d, want 1", code)
		}
		if strings.Contains(stdout.String(), "real-gh-called") {
			t.Errorf("stdout = %q, the real gh script must not have run", stdout.String())
		}
		entries, err := ReadJournal(journalPath)
		if err != nil {
			t.Fatalf("ReadJournal: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 journal entry, got %d", len(entries))
		}
	})
}
