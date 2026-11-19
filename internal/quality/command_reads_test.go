package quality

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestTaskDirectoryReadsUseTheBuilder: every read of the task directory goes
// through the builder (GH-6555). The builder reads a shadow directory whose
// content differs from the task directory, so a read by another route shows
// in the result.
func TestTaskDirectoryReadsUseTheBuilder(t *testing.T) {
	t.Run("gate run reads the Makefile, the markers and the directory through the builder", func(t *testing.T) {
		// On disk the Makefile has the target and an include: read from disk,
		// the configured command stays.
		dir := writeProjectFiles(t, map[string]string{
			"Makefile": "include other.mk\ncheck:\n\ttrue\ntest:\n\ttrue\n",
		})
		// Through the builder there is neither, and a Python file.
		shadow := writeProjectFiles(t, map[string]string{
			"Makefile": "build:\n\tgo build ./...\n",
			"main.py":  "print('x')\n",
		})
		b := useRecordingBuilder(t)
		b.from, b.to = dir, shadow

		gate := &Gate{Name: "test", Type: GateTest, Command: "make check", Timeout: 10 * time.Second}
		r, _ := newTestRunnerWithLog(&Config{Enabled: true, Gates: []*Gate{gate}}, dir, true)
		res, err := r.RunGate(context.Background(), "test")
		if err != nil {
			t.Fatalf("RunGate: %v", err)
		}
		if res.Command != "pytest -v 2>&1" {
			t.Errorf("Command = %q, want the fallback the builder's content gives (pytest -v 2>&1)", res.Command)
		}

		makefile := filepath.Join(dir, "Makefile")
		if !slices.Contains(b.reads, makefile) {
			t.Errorf("ReadFile calls = %v, want %s", b.reads, makefile)
		}
		if !slices.Contains(b.stats, makefile) || !slices.Contains(b.stats, filepath.Join(dir, "pyproject.toml")) {
			t.Errorf("Stat calls = %v, want the Makefile and pyproject.toml", b.stats)
		}
		if !slices.Contains(b.readDirs, dir) {
			t.Errorf("ReadDir calls = %v, want %s", b.readDirs, dir)
		}
	})

	t.Run("scope run checks go.mod and resolves paths through the builder", func(t *testing.T) {
		dir := newScopeRepo(t)
		writeFiles(t, dir, map[string]string{"mem/store.go": "package mem\n\nvar X = 1\n"})
		git(t, dir, "commit", "-q", "-am", "mem")
		b := useRecordingBuilder(t)

		if _, full, reason := ChangedGoPackages(context.Background(), dir, "main"); full {
			t.Fatalf("scope is full (%s), want a scoped diff", reason)
		}
		if !slices.Contains(b.stats, filepath.Join(dir, "go.mod")) {
			t.Errorf("Stat calls = %v, want go.mod", b.stats)
		}
		if !slices.Contains(b.evals, dir) {
			t.Errorf("EvalSymlinks calls = %v, want %s", b.evals, dir)
		}
	})

	t.Run("scope run sees the go.mod of the builder", func(t *testing.T) {
		dir := newScopeRepo(t)
		b := useRecordingBuilder(t)
		b.from, b.to = dir, t.TempDir()

		_, full, reason := ChangedGoPackages(context.Background(), dir, "main")
		if !full || reason != "not a Go module root" {
			t.Errorf("ChangedGoPackages = (full %v, %q), want the builder's missing go.mod", full, reason)
		}
	})
}

func TestScopeCommandsAreConfigured(t *testing.T) {
	dir := newScopeRepo(t)
	writeFiles(t, dir, map[string]string{"mem/store.go": "package mem\n\nvar X = 1\n"})
	git(t, dir, "commit", "-q", "-am", "mem")
	b := useRecordingBuilder(t)

	if _, full, reason := ChangedGoPackages(context.Background(), dir, "main"); full {
		t.Fatalf("scope is full (%s), want a scoped diff", reason)
	}
	names := b.builtNames()
	if !slices.Contains(names, "git") || !slices.Contains(names, "go") {
		t.Fatalf("Build names = %v, want git and go", names)
	}
	if len(b.configured) != len(b.builds) {
		t.Fatalf("Configure called %d times for %d commands, want one each", len(b.configured), len(b.builds))
	}
	for i, d := range b.configured {
		if d != gateWaitDelay {
			t.Errorf("Configure %d wait delay = %v, want %v", i, d, gateWaitDelay)
		}
		if b.startedAtConfigure[i] {
			t.Errorf("Configure %d came after the start of the command", i)
		}
	}
}

// nilCommandBuilder returns neither a command nor an error.
type nilCommandBuilder struct{ defaultCommandBuilder }

func (nilCommandBuilder) Build(context.Context, string, []string, string, ...string) (*exec.Cmd, error) {
	return nil, nil
}

func TestNilCommandIsAnError(t *testing.T) {
	t.Run("gate", func(t *testing.T) {
		SetCommandBuilder(nilCommandBuilder{})
		t.Cleanup(func() { SetCommandBuilder(nil) })

		gate := &Gate{Name: "custom", Type: GateCustom, Command: "true", Timeout: 5 * time.Second}
		runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, t.TempDir())
		res, err := runner.RunGate(context.Background(), "custom")
		if err != nil {
			t.Fatalf("RunGate: %v", err)
		}
		if !errors.Is(res.Err, errNoCommand) || res.Status != StatusFailed {
			t.Errorf("result = (%s, %v), want failed with %v", res.Status, res.Err, errNoCommand)
		}
	})

	t.Run("scope", func(t *testing.T) {
		dir := newScopeRepo(t)
		SetCommandBuilder(nilCommandBuilder{})
		t.Cleanup(func() { SetCommandBuilder(nil) })

		_, full, reason := ChangedGoPackages(context.Background(), dir, "main")
		if !full || !strings.Contains(reason, errNoCommand.Error()) {
			t.Errorf("ChangedGoPackages = (full %v, %q), want full with %q", full, reason, errNoCommand)
		}
	})

	t.Run("nil builder value", func(t *testing.T) {
		commandBuilderMu.Lock()
		commandBuilder = nil
		commandBuilderMu.Unlock()
		t.Cleanup(func() { SetCommandBuilder(nil) })

		gate := &Gate{Name: "custom", Type: GateCustom, Timeout: 5 * time.Second}
		runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, t.TempDir())
		if _, _, err := runner.executeCommand(context.Background(), gate, "true", &changeScope{full: true}); !errors.Is(err, errNoCommand) {
			t.Errorf("executeCommand err = %v, want %v", err, errNoCommand)
		}
		if _, full, _ := ChangedGoPackages(context.Background(), t.TempDir(), "main"); !full {
			t.Error("ChangedGoPackages with a nil builder is not full")
		}
	})

	t.Run("typed nil sets the default", func(t *testing.T) {
		SetCommandBuilder((*recordingBuilder)(nil))
		t.Cleanup(func() { SetCommandBuilder(nil) })
		if _, ok := CurrentCommandBuilder().(defaultCommandBuilder); !ok {
			t.Errorf("after a typed nil the builder is %T, want defaultCommandBuilder", CurrentCommandBuilder())
		}
	})
}

func TestOuterCancelIsAnError(t *testing.T) {
	gate := &Gate{Name: "slow", Type: GateCustom, Command: "sleep 5", Timeout: 10 * time.Second, MaxRetries: 1}
	runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, t.TempDir())

	t.Run("executeCommand", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(200*time.Millisecond, cancel)
		code, _, err := runner.executeCommand(ctx, gate, gate.Command, &changeScope{full: true})
		if code != -1 || !errors.Is(err, context.Canceled) {
			t.Errorf("executeCommand = (%d, %v), want (-1, context.Canceled)", code, err)
		}
	})

	t.Run("gate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(200*time.Millisecond, cancel)
		res, err := runner.RunGate(ctx, "slow")
		if err != nil {
			t.Fatalf("RunGate: %v", err)
		}
		if res.ExitCode != -1 || res.Status != StatusFailed || res.Error != "context cancelled" {
			t.Errorf("result = (exit %d, %s, %q), want (-1, failed, context cancelled)", res.ExitCode, res.Status, res.Error)
		}
		if !errors.Is(res.Err, context.Canceled) {
			t.Errorf("Err = %v, want one that wraps context.Canceled", res.Err)
		}
	})
}
