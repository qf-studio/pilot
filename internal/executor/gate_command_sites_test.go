package executor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/qf-studio/pilot/internal/quality"
)

// GH-6614 (TASK-651): the three gate starts of the executor refuse the
// default builder with the option set, the command detection reads nothing
// with the wrong builder, a gate gets no gh guard name, and the preflight
// probes the tools and the temporary directory of a gate as the agent user.

// useDefaultQualityBuilderForTest puts the default builder of internal/quality
// in place for one test.
func useDefaultQualityBuilderForTest(t *testing.T) {
	t.Helper()
	prev := quality.CurrentCommandBuilder()
	quality.SetCommandBuilder(nil)
	t.Cleanup(func() { quality.SetCommandBuilder(prev) })
}

// touchGateFactory is a factory whose checker runs a gate that writes marker
// in the execution path.
func touchGateFactory(marker string) QualityCheckerFactory {
	return func(_, _, executionPath string) QualityChecker {
		gate := &quality.Gate{Name: "touch", Type: quality.GateCustom, Command: "touch " + marker, Timeout: time.Minute}
		return &gateTestChecker{runner: quality.NewRunner(gateConfig(gate), executionPath)}
	}
}

// recordingBuilder is the default builder of internal/quality that records
// every read of the task directory: a builder that is not the gate builder.
type recordingBuilder struct {
	quality.CommandBuilder
	mu    sync.Mutex
	reads []string
}

func (b *recordingBuilder) record(op, path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads = append(b.reads, op+" "+path)
}

func (b *recordingBuilder) ReadFile(p string) ([]byte, error) {
	b.record("ReadFile", p)
	return b.CommandBuilder.ReadFile(p)
}

func (b *recordingBuilder) Stat(p string) (os.FileInfo, error) {
	b.record("Stat", p)
	return b.CommandBuilder.Stat(p)
}

func (b *recordingBuilder) ReadDir(p string) ([]os.DirEntry, error) {
	b.record("ReadDir", p)
	return b.CommandBuilder.ReadDir(p)
}

func (b *recordingBuilder) EvalSymlinks(p string) (string, error) {
	b.record("EvalSymlinks", p)
	return b.CommandBuilder.EvalSymlinks(p)
}

// TestRunAsUserGateStartSitesRefuseTheDefaultBuilder: with the option set and
// the default builder of internal/quality in place, each of the three gate
// starts (the gate run after the backend, the timeout salvage, the re-gate of
// the pre-PR fix) returns the refusal of the guard and the gate command does
// not run. The detection of the build and test command reads no file of the
// workspace with the wrong builder; a gate environment holds no gh guard name.
//
// mutation pin: the re-gate of the pre-PR fix (runReviewFix, pre_pr_fix.go) starts the gates without guardGateChecker -> this test fails
func TestRunAsUserGateStartSitesRefuseTheDefaultBuilder(t *testing.T) {
	const refusal = "run_as_user: the quality gates have no agent command builder"

	// optionSet sets the option for a workspace root that does not hold the
	// test repository: the git commands of the run take the route of today.
	optionSet := func(t *testing.T) {
		t.Helper()
		stubRunAsGOOS(t)
		_ = mockSudoAgent(t, "", "")
		setRunAsStateForTest(t, gitRouteSettings(t, t.TempDir()), "")
		useDefaultQualityBuilderForTest(t)
	}
	// optionSetLater is optionSet for a run of Execute, which refuses to start
	// a task without an agent workspace with the option set: the returned
	// func sets the option, from the fake backend, before the gates start.
	optionSetLater := func(t *testing.T) func() {
		t.Helper()
		stubRunAsGOOS(t)
		_ = mockSudoAgent(t, "", "")
		s := gitRouteSettings(t, t.TempDir())
		setRunAsStateForTest(t, nil, "")
		useDefaultQualityBuilderForTest(t)
		return func() {
			runAsMu.Lock()
			runAsCur = runAsState{settings: s, checked: true}
			runAsMu.Unlock()
		}
	}

	t.Run("gate run after the backend", func(t *testing.T) {
		_, _ = setUpFakeGhPRCreateAndLabelPATH(t)
		setOption := optionSetLater(t)
		const branch = "pilot/GH-6614-gate-run"
		dir, _ := setupFreshnessRepo(t)
		runGit(t, dir, "checkout", "-b", branch)
		backend := &mockGH4964Backend{
			perCall: func(_ int, _ ExecuteOptions) *BackendResult {
				writeUncommittedFile(t, dir, "work.go")
				runGit(t, dir, "add", "work.go")
				runGit(t, dir, "commit", "-m", "real work")
				setOption()
				return &BackendResult{Success: true}
			},
		}
		runner := newGH4964Runner(backend)
		marker := filepath.Join(t.TempDir(), "gate-ran")
		runner.qualityCheckerFactory = touchGateFactory(marker)

		result, err := runner.Execute(context.Background(), newGH4964Task("GH-6614", branch, dir))
		if err != nil {
			t.Fatalf("Execute() returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, refusal) {
			t.Errorf("result success=%v error=%q, want the refusal of the guard", result.Success, result.Error)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("the gate command ran")
		}
	})

	t.Run("timeout salvage", func(t *testing.T) {
		_, _ = setUpFakeGhPRCreateAndLabelPATH(t)
		setOption := optionSetLater(t)
		const branch = "pilot/GH-6614-salvage"
		dir, _ := setupFreshnessRepo(t)
		runGit(t, dir, "checkout", "-b", branch)
		taskCtx := newManualDeadlineCtx()
		defer taskCtx.expire()
		backend := &ctxRespectingBackend{
			run: func(ctx context.Context, _ int, _ ExecuteOptions) (*BackendResult, error) {
				writeUncommittedFile(t, dir, "salvaged.go")
				runGit(t, dir, "add", "salvaged.go")
				runGit(t, dir, "commit", "-m", "real work before the deadline hit")
				setOption()
				taskCtx.expire()
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		runner := newGH4964Runner(backend)
		marker := filepath.Join(t.TempDir(), "gate-ran")
		runner.qualityCheckerFactory = touchGateFactory(marker)

		result, err := runner.Execute(taskCtx, newGH4964Task("GH-6614", branch, dir))
		if err != nil {
			t.Fatalf("Execute() returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "timeout salvage: quality gate error: "+refusal) {
			t.Errorf("result success=%v error=%q, want the refusal of the guard", result.Success, result.Error)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("the gate command ran")
		}
	})

	t.Run("re-gate of the pre-PR fix", func(t *testing.T) {
		optionSet(t)
		e := newReviewFixEnv(t)
		e.backend.commit = true
		marker := filepath.Join(t.TempDir(), "gate-ran")
		e.runner.qualityCheckerFactory = touchGateFactory(marker)
		buf := &syncBuffer{}
		e.runner.log = slog.New(slog.NewTextHandler(buf, nil))

		fix := e.run(ctxWithin(t, 30*time.Minute), false)
		if fix == nil || fix.Status != ReviewFixRegateError {
			t.Fatalf("outcome = %+v, want %s", fix, ReviewFixRegateError)
		}
		if !strings.Contains(buf.String(), refusal) {
			t.Errorf("the log has no refusal of the guard:\n%s", buf.String())
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("the gate command ran")
		}
	})

	t.Run("detection reads nothing with the wrong builder", func(t *testing.T) {
		f := newGateFixture(t, "")
		const makefileContent = "makefile-content-6614"
		writeTestFile(t, filepath.Join(f.ws, "Makefile"), "build:\n\ttrue\ntest:\n\techo "+makefileContent+"\n")
		writeTestFile(t, filepath.Join(f.ws, "go.mod"), "module example.com/gate\n\ngo 1.21\n")
		quality.SetCommandBuilder(nil) // the fixture restores
		rec := &recordingBuilder{CommandBuilder: quality.CurrentCommandBuilder()}
		quality.SetCommandBuilder(rec)

		runner := newGH4964Runner(&mockGH4964Backend{})
		log := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
		runner.ensureQualityCheckerFactory(f.ws, log)
		if runner.qualityCheckerFactory == nil {
			t.Fatal("no checker factory: the gates would be skipped")
		}
		checker := runner.qualityCheckerFactory("GH-6614", f.ws, f.ws)
		if sc, ok := checker.(*simpleQualityChecker); ok {
			t.Fatalf("the detection ran: gates %+v", sc.config.Gates)
		}
		if _, err := checker.Check(context.Background()); err == nil || err.Error() != refusal {
			t.Errorf("Check = %v, want the refusal", err)
		}
		if len(rec.reads) != 0 {
			t.Errorf("the detection read the workspace with the wrong builder: %q", rec.reads)
		}
		if raw := f.log(t); raw != "" {
			t.Errorf("sudo ran (the Makefile holds %s):\n%s", makefileContent, raw)
		}

		// With the gate builder the next call detects, as the agent user.
		quality.SetCommandBuilder(gateCommandBuilder{})
		runner.ensureQualityCheckerFactory(f.ws, log)
		sc, ok := runner.qualityCheckerFactory("GH-6614", f.ws, f.ws).(*simpleQualityChecker)
		if !ok || len(sc.config.Gates) != 2 || sc.config.Gates[1].Command != "make test" {
			t.Fatalf("with the gate builder: checker %T, want the detected build and test gates", runner.qualityCheckerFactory("GH-6614", f.ws, f.ws))
		}
		if raw := f.log(t); !strings.Contains(raw, filepath.Join(f.ws, "Makefile")) {
			t.Errorf("the Makefile was not read through sudo:\n%s", raw)
		}
	})

	t.Run("gate environment holds no gh guard name", func(t *testing.T) {
		f := newGateFixture(t, "")
		for name := range runAsGhGuardEnv {
			t.Setenv(name, "daemon-"+name)
		}
		var appended []string
		for name := range runAsGhGuardEnv {
			appended = append(appended, name+"=caller-"+name)
		}
		for _, env := range [][]string{nil, append(os.Environ(), appended...), appended} {
			cmd, err := gateCommandBuilder{}.Build(context.Background(), f.ws, env, "sh", "-c", "true")
			if err != nil {
				t.Fatal(err)
			}
			for name := range runAsGhGuardEnv {
				if v, ok := gateCmdEnv(cmd, name); ok {
					t.Errorf("gate environment has %s=%q", name, v)
				}
				if strings.Contains(strings.Join(cmd.Args, " "), name) {
					t.Errorf("the preserve list of the gate names %s: %q", name, cmd.Args)
				}
			}
		}
		// The model session keeps them: the guard runs inside it.
		cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
		if err != nil {
			t.Fatal(err)
		}
		setAgentEnv(cmd, os.Environ())
		if _, ok := gateCmdEnv(cmd, ghguard.EnvShimDir); !ok {
			t.Errorf("the model session lost %s", ghguard.EnvShimDir)
		}
	})
}

// gateProbeHost is a PATH for the gate tool probe: bin holds sh, git and rm of
// the host, extra holds a fake make (and go when asked); the mock sudo is
// first. agentPath is the PATH the mock gives the agent side with
// agentLacksExtra.
type gateProbeHost struct {
	bin, extra, sudoLog string
}

func newGateProbeHost(t *testing.T, withGo, agentLacksExtra bool) *gateProbeHost {
	t.Helper()
	h := &gateProbeHost{bin: t.TempDir(), extra: t.TempDir()}
	for _, tool := range []string{"sh", "git", "rm"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not installed", tool)
		}
		if err := os.Symlink(p, filepath.Join(h.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	fakes := []string{"make"}
	if withGo {
		fakes = append(fakes, "go")
	}
	for _, tool := range fakes {
		if err := os.WriteFile(filepath.Join(h.extra, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // a fake tool must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", h.bin+string(os.PathListSeparator)+h.extra)
	extra := ""
	if agentLacksExtra {
		extra = "PATH='" + h.bin + "'; export PATH"
	}
	h.sudoLog = mockSudoAgent(t, "", extra)
	return h
}

// probeCall returns the call of the mock sudo that ran the gate tool probe.
func (h *gateProbeHost) probeCall(t *testing.T) string {
	t.Helper()
	for _, c := range sudoCalls(t, h.sudoLog) {
		if strings.Contains(c, ".pilot-gate-probe-") {
			return c
		}
	}
	return ""
}

// TestRunAsUserPreflightGateToolProbe: the preflight runs the gate tool probe
// as the agent user with the environment of a gate. A tool the agent user does
// not find and a temporary directory it cannot write refuse the start with a
// message that names the tool or the directory and the agent user; make and
// go are asked only when the daemon finds them.
//
// mutation pin: PreflightRunAsUser does not call runAsGateToolsCheck -> this test fails
func TestRunAsUserPreflightGateToolProbe(t *testing.T) {
	preflight := func(t *testing.T) error {
		t.Helper()
		setRunAsStateForTest(t, nil, "")
		stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
		runAsGateToolsCheck = checkRunAsGateTools // stubRunAsHost restores
		PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}, "", true, 0)
		return RunAsUserPreflightError()
	}

	t.Run("make missing for the agent user", func(t *testing.T) {
		h := newGateProbeHost(t, false, true)
		err := preflight(t)
		if err == nil || !strings.Contains(err.Error(), "finds no make") || !strings.Contains(err.Error(), "pilot-agent") {
			t.Errorf("preflight = %v, want the refusal naming make and the agent user", err)
		}
		if h.probeCall(t) == "" {
			t.Errorf("no probe through sudo: %q", sudoCalls(t, h.sudoLog))
		}
	})

	t.Run("temporary directory not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes a directory of mode 0500")
		}
		ro := t.TempDir()
		_ = newGateProbeHost(t, false, false)
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		t.Setenv("TMPDIR", ro)
		err := preflight(t)
		if err == nil || !strings.Contains(err.Error(), "temporary directory "+ro) || !strings.Contains(err.Error(), "pilot-agent") {
			t.Errorf("preflight = %v, want the refusal naming %s and the agent user", err, ro)
		}
	})

	t.Run("the daemon has no go", func(t *testing.T) {
		h := newGateProbeHost(t, false, false)
		if err := preflight(t); err != nil {
			t.Fatalf("preflight = %v", err)
		}
		call := h.probeCall(t)
		if !strings.HasSuffix(call, " sh sh git make") {
			t.Errorf("probe call = %q, want the tools sh git make and no go", call)
		}
	})

	t.Run("the daemon has go", func(t *testing.T) {
		h := newGateProbeHost(t, true, false)
		if err := preflight(t); err != nil {
			t.Fatalf("preflight = %v", err)
		}
		if call := h.probeCall(t); !strings.HasSuffix(call, " sh sh git make go") {
			t.Errorf("probe call = %q, want the tools sh git make go", call)
		}
	})
}
