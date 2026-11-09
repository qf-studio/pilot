package executor

import (
	"context"
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

func TestReadGuardReachesHelperSpawns(t *testing.T) {
	cli, cliDir, capture := fakeGuardCLI(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", cliDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { SetReadGuardConfig(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	spawns := map[string]func(){
		"research subagent": func() {
			NewParallelRunner(nil, nil).executeSubagent(ctx, t.TempDir(), researchTask{Type: "research", Prompt: "p", Model: "haiku"})
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
			r := NewRunner()
			r.config = &BackendConfig{ClaudeCode: &ClaudeCodeConfig{Command: cli}}
			_, _ = r.getPostExecutionSummary(ctx, t.TempDir())
		},
		"contract evidence": func() {
			r := NewRunner()
			r.config = &BackendConfig{ClaudeCode: &ClaudeCodeConfig{Command: cli}}
			_, _ = r.getContractEvidence(ctx, t.TempDir(), []string{"field"})
		},
	}

	off := false
	for name, spawn := range spawns {
		t.Run(name, func(t *testing.T) {
			SetReadGuardConfig(nil)
			spawn()
			got := capturedSettings(t, capture)
			if !strings.Contains(got, readGuardName) || !strings.Contains(got, readGuardMatcher) {
				t.Errorf("default spawn has no read guard hook in --settings: %q", got)
			}

			if name == "post-execution summary" || name == "contract evidence" {
				// These follow the Runner's own config, not the daemon-wide one.
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
