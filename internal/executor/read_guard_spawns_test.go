package executor

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// GH-6258: the read guard must reach every Claude Code spawn, not only the
// main backend and epic planning. Each case below runs the real spawn code
// against a fake claude CLI that copies the --settings file it was handed into
// a capture file while the run is still live (the guard dir is removed on
// exit).

// fakeGuardCLI writes a fake claude into a fresh dir and returns the CLI path,
// the dir (for PATH) and the capture file.
func fakeGuardCLI(t *testing.T) (cli, dir, capture string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts")
	}
	dir = t.TempDir()
	capture = filepath.Join(t.TempDir(), "settings-captured")
	cli = filepath.Join(dir, "claude")
	body := `#!/bin/sh
while [ $# -gt 0 ]; do
  if [ "$1" = "--settings" ]; then cat "$2" > '` + capture + `'; fi
  shift
done
: > '` + capture + `.ran'
printf '{}'
exit 0
`
	if err := os.WriteFile(cli, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	return cli, dir, capture
}

// disableReadGuardForTest turns the helper-spawn read guard off for tests that
// run a non-claude binary as the "CLI": it would reject the --settings flag.
func disableReadGuardForTest(t *testing.T) {
	t.Helper()
	off := false
	SetReadGuardConfig(&ClaudeCodeConfig{ReadGuard: &off})
	t.Cleanup(func() { SetReadGuardConfig(nil) })
}

func capturedSettings(t *testing.T, capture string) string {
	t.Helper()
	raw, err := os.ReadFile(capture)
	if err != nil {
		return ""
	}
	_ = os.Remove(capture)
	return string(raw)
}

// guardSpawns returns one closure per Claude Code spawn site that takes the
// read guard. The Runner-config spawns follow runnerPaths (PilotHome and
// ConfigPath), the rest follow the daemon-wide SetReadGuardConfig. proj is a
// dir created up front so a test may break TMPDIR before calling a spawn.
func guardSpawns(ctx context.Context, cli, proj string, runnerPaths ClaudeCodeConfig) map[string]func() {
	runner := func() *Runner {
		r := NewRunner()
		cc := runnerPaths
		cc.Command = cli
		r.config = &BackendConfig{ClaudeCode: &cc}
		return r
	}
	return map[string]func(){
		"research subagent": func() {
			NewParallelRunner(nil, nil).executeSubagent(ctx, proj, researchTask{Type: "research", Prompt: "p", Model: "haiku"})
		},
		"complexity classifier": func() {
			_, _ = NewComplexityClassifier().defaultCmdRunner(ctx, "--print", "-p", "x")
		},
		"effort classifier": func() {
			_, _ = NewEffortClassifier().defaultCmdRunner(ctx, "--print", "-p", "x")
		},
		"subtask parser": func() {
			_, _ = NewSubtaskParser(cli, nil).defaultRunner(ctx, "--print", "-p", "x")
		},
		"intent judge": func() {
			_, _ = NewIntentJudge(cli).defaultCmdRunner(ctx, "--print", "-p", "x")
		},
		"post-merge reviewer": func() {
			_, _ = NewPostMergeReviewer(cli, "", 0).cmdRunner(ctx, "", "", "--print", "-p", "x")
		},
		"post-execution summary": func() {
			_, _ = runner().getPostExecutionSummary(ctx, proj)
		},
		"contract evidence": func() {
			_, _ = runner().getContractEvidence(ctx, proj, []string{"field"})
		},
	}
}

// followsRunnerConfig names the spawns that take their guard config from the
// Runner's own config, not the daemon-wide one.
func followsRunnerConfig(name string) bool {
	return name == "post-execution summary" || name == "contract evidence"
}

func guardTestEnv(t *testing.T) (cli, capture string) {
	t.Helper()
	cli, cliDir, capture := fakeGuardCLI(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", cliDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { SetReadGuardConfig(nil) })
	return cli, capture
}

func TestReadGuardReachesHelperSpawns(t *testing.T) {
	cli, capture := guardTestEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	off := false
	for name, spawn := range guardSpawns(ctx, cli, t.TempDir(), ClaudeCodeConfig{}) {
		t.Run(name, func(t *testing.T) {
			SetReadGuardConfig(nil)
			spawn()
			got := capturedSettings(t, capture)
			if !strings.Contains(got, readGuardName) || !strings.Contains(got, readGuardMatcher) {
				t.Errorf("default spawn has no read guard hook in --settings: %q", got)
			}

			if followsRunnerConfig(name) {
				return
			}
			SetReadGuardConfig(&ClaudeCodeConfig{ReadGuard: &off})
			spawn()
			if got := capturedSettings(t, capture); got != "" {
				t.Errorf("read_guard: false spawn still has --settings: %q", got)
			}
		})
	}
}

// mutation pin: a configured PilotHome/ConfigPath that does not reach the hook command of a helper spawn (the daemon config dropped, defaults used) -> this test fails
func TestReadGuardHelperSpawnsCarryConfiguredPaths(t *testing.T) {
	cli, capture := guardTestEnv(t)
	home := filepath.Join(t.TempDir(), "custom-home")
	cfgPath := filepath.Join(t.TempDir(), "elsewhere", "my-config.yaml")
	paths := ClaudeCodeConfig{PilotHome: home, ConfigPath: cfgPath}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for name, spawn := range guardSpawns(ctx, cli, t.TempDir(), paths) {
		t.Run(name, func(t *testing.T) {
			SetReadGuardConfig(&paths)
			spawn()
			got := capturedSettings(t, capture)
			for _, want := range []string{"'" + home + "'", "'" + cfgPath + "'"} {
				if !strings.Contains(got, want) {
					t.Errorf("hook command lacks configured path %s: %q", want, got)
				}
			}
		})
	}
}

// mutation pin: a setup error (guard dir cannot be created) lets a spawn run unguarded instead of stopping it, at any of the 8 call sites -> this test fails
func TestReadGuardSetupErrorStopsEverySpawn(t *testing.T) {
	cli, capture := guardTestEnv(t)
	proj := t.TempDir()
	spawns := guardSpawns(context.Background(), cli, proj, ClaudeCodeConfig{})
	// Break the guard dir creation (os.MkdirTemp("", ...) follows TMPDIR).
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	for name, spawn := range spawns {
		t.Run(name, func(t *testing.T) {
			SetReadGuardConfig(nil)
			spawn()
			if _, err := os.Stat(capture + ".ran"); err == nil {
				t.Fatal("claude ran although the read guard could not be set up")
			}
		})
	}
}

// mutation pin: the guard dir (pilot-runguard-*) is left behind after a spawn -> this test fails
func TestReadGuardDirRemovedAfterEverySpawn(t *testing.T) {
	cli, capture := guardTestEnv(t)
	proj := t.TempDir()
	spawns := guardSpawns(context.Background(), cli, proj, ClaudeCodeConfig{})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	for name, spawn := range spawns {
		t.Run(name, func(t *testing.T) {
			SetReadGuardConfig(nil)
			spawn()
			if _, err := os.Stat(capture + ".ran"); err != nil {
				t.Fatalf("claude did not run, nothing to check: %v", err)
			}
			_ = os.Remove(capture + ".ran")
			left, _ := filepath.Glob(filepath.Join(tmp, "pilot-runguard-*"))
			if len(left) != 0 {
				t.Errorf("guard dir left behind after the spawn: %v", left)
			}
		})
	}
}

// mutation pin: the early-return SubagentResult on a guard error drops the task type the normal path records -> this test fails
func TestExecuteSubagentGuardErrorKeepsTaskType(t *testing.T) {
	cli, _ := guardTestEnv(t)
	proj := t.TempDir()
	task := researchTask{Type: SubagentAnalysis, Prompt: "p"}

	ok := NewParallelRunner(nil, nil).executeSubagent(context.Background(), proj, task)
	if ok.Type != SubagentAnalysis {
		t.Errorf("normal path Type = %q, want %q (cli %s)", ok.Type, SubagentAnalysis, cli)
	}

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	res := NewParallelRunner(nil, nil).executeSubagent(context.Background(), proj, task)
	if res.Error == "" {
		t.Fatal("guard setup error did not surface as SubagentResult.Error")
	}
	if res.Type != SubagentAnalysis {
		t.Errorf("guard-error Type = %q, want %q", res.Type, SubagentAnalysis)
	}
}

// mutation pin: withReadGuard returns the args unguarded with no error when the Pilot home cannot be resolved (HOME unset, guard enabled) -> this test fails
func TestWithReadGuardFailsClosedWhenHomeUnresolvable(t *testing.T) {
	cli, capture := guardTestEnv(t)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if h, err := os.UserHomeDir(); err == nil {
		t.Skipf("os.UserHomeDir resolves to %q on this platform", h)
	}
	args := []string{"--print"}

	if _, _, err := withReadGuard(nil, args); err == nil {
		t.Error("guard enabled, home unresolvable: want an error, got none (spawn would run unguarded)")
	}
	off := false
	if _, _, err := withReadGuard(&ClaudeCodeConfig{ReadGuard: &off}, args); err != nil {
		t.Errorf("guard switched off must not error: %v", err)
	}
	got, cleanup, err := withReadGuard(&ClaudeCodeConfig{PilotHome: t.TempDir()}, args)
	if err != nil {
		t.Fatalf("explicit PilotHome must not error: %v", err)
	}
	cleanup()
	if len(got) != 3 || got[1] != "--settings" {
		t.Errorf("args = %v, want --settings appended", got)
	}

	// A spawn site must stop too.
	_, _ = NewIntentJudge(cli).defaultCmdRunner(context.Background(), "--print", "-p", "x")
	if _, err := os.Stat(capture + ".ran"); err == nil {
		t.Error("claude ran unguarded although the Pilot home could not be resolved")
	}
}

// mutation pin: a t.Parallel test calls a real Claude Code spawn or sets the global read guard config -> this test fails
func TestNoParallelTestUsesGlobalReadGuardConfig(t *testing.T) {
	// The read guard tests above change processClaudeCodeConfig, which the
	// real spawn runners read; they are safe only while no parallel test
	// calls such a runner. Direct references only: a helper hiding the call
	// is not seen.
	global := map[string]bool{
		"defaultCmdRunner": true, "defaultRunner": true, "executeSubagent": true,
		"SetReadGuardConfig": true, "disableReadGuardForTest": true,
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files found: %v", err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			parallel, used := false, ""
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if x.Sel.Name == "Parallel" {
						parallel = true
					}
					if global[x.Sel.Name] {
						used = x.Sel.Name
					}
				case *ast.Ident:
					if global[x.Name] {
						used = x.Name
					}
				}
				return true
			})
			if parallel && used != "" {
				t.Errorf("%s: %s is t.Parallel but uses %s, which reads the process-wide read guard config", file, fn.Name.Name, used)
			}
		}
	}
}

func TestReadGuardRunnerSpawnsHonorReadGuardOff(t *testing.T) {
	cli, _, capture := fakeGuardCLI(t)
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	off := false
	r := NewRunner()
	r.config = &BackendConfig{ClaudeCode: &ClaudeCodeConfig{Command: cli, ReadGuard: &off}}

	_, _ = r.getPostExecutionSummary(ctx, t.TempDir())
	_, _ = r.getContractEvidence(ctx, t.TempDir(), []string{"field"})
	if got := capturedSettings(t, capture); got != "" {
		t.Errorf("read_guard: false still passes --settings: %q", got)
	}
}
