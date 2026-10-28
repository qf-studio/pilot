package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// selfHealCase describes one dirty direct-mode clone scenario.
type selfHealCase struct {
	name          string
	branch        string // branch checked out in the clone ("main" = default)
	pushed        bool   // push the branch's HEAD to the remote
	unpushedExtra bool   // add a local commit after the push
	ledger        []memory.Execution
	wantHealed    bool
	wantErrSubs   []string
}

// newSelfHealClone builds a clone with a real bare origin, checks out the
// scenario's branch, and leaves one unstaged change to a tracked file.
func newSelfHealClone(t *testing.T, c selfHealCase) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	gitOut(t, dir, "branch", "-M", "main")

	origin := filepath.Join(t.TempDir(), "origin.git")
	gitOut(t, dir, "init", "-q", "--bare", origin)
	gitOut(t, dir, "remote", "add", "origin", origin)
	gitOut(t, dir, "push", "-q", "-u", "origin", "main")

	if c.branch != "main" {
		gitOut(t, dir, "checkout", "-q", "-b", c.branch)
		if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitOut(t, dir, "add", "feature.go")
		gitOut(t, dir, "commit", "-q", "-m", "feat: work")
		if c.pushed {
			gitOut(t, dir, "push", "-q", "-u", "origin", c.branch)
		}
		if c.unpushedExtra {
			if err := os.WriteFile(filepath.Join(dir, "more.go"), []byte("package x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitOut(t, dir, "add", "more.go")
			gitOut(t, dir, "commit", "-q", "-m", "feat: unpushed")
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("dirty edit left by a finished run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runSelfHealCase(t *testing.T, c selfHealCase) {
	t.Helper()
	dir := newSelfHealClone(t, c)

	store, cleanup := setupTestStore(t)
	defer cleanup()
	// The task's own row is "running" while its pre-flight executes; it must
	// never count as another active run.
	own := &memory.Execution{ID: "exec-own", TaskID: "GH-5901", ProjectPath: dir, Status: "running"}
	if err := store.SaveExecution(own); err != nil {
		t.Fatalf("SaveExecution own: %v", err)
	}
	for i := range c.ledger {
		row := c.ledger[i]
		row.ProjectPath = dir
		if err := store.SaveExecution(&row); err != nil {
			t.Fatalf("SaveExecution %s: %v", row.ID, err)
		}
	}

	runner := NewRunnerWithBackend(&mockFixedBackend{result: &BackendResult{Success: true}})
	runner.SetLogStore(store)
	var logBuf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	var notes []string
	runner.OnProgress(func(_, phase string, _ int, message string) {
		if phase == "Pre-flight Self-Heal" {
			notes = append(notes, message)
		}
	})

	task := &Task{ID: "GH-5901", ExecutionID: own.ID, ProjectPath: dir}
	// "mock" is an unknown backend, so the CLI-availability check is skipped.
	err := runner.runPreflight(context.Background(), task, dir, PreflightOptions{BackendType: "mock"})

	status := strings.TrimSpace(gitOut(t, dir, "status", "--porcelain"))
	logs := logBuf.String()
	wantLine := "pre-flight self-heal: discarded 1 uncommitted change(s) left by finished run on branch " + c.branch

	if c.wantHealed {
		if err != nil {
			t.Fatalf("pre-flight failed after heal: %v", err)
		}
		if status != "" {
			t.Errorf("tree still dirty after heal:\n%s", status)
		}
		if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, wantLine) {
			t.Errorf("missing Warn line %q in:\n%s", wantLine, logs)
		}
		if len(notes) != 1 || notes[0] != wantLine {
			t.Errorf("lifecycle notifications = %q, want [%q]", notes, wantLine)
		}
		events, evErr := store.ListExecutionEvents(own.ID)
		if evErr != nil {
			t.Fatalf("ListExecutionEvents: %v", evErr)
		}
		found := false
		for _, ev := range events {
			if ev.Stage == memory.StagePreflightSelfHeal {
				found = strings.Contains(ev.Detail, c.branch) && strings.Contains(ev.Detail, `"files":1`)
			}
		}
		if !found {
			t.Errorf("no %s execution event naming branch and file count: %+v", memory.StagePreflightSelfHeal, events)
		}
		return
	}

	if err == nil {
		t.Fatal("pre-flight passed; heal must not have run")
	}
	var pfErr *PreflightError
	if !errors.As(err, &pfErr) || pfErr.CheckName != "git_clean" {
		t.Errorf("error = %v, want a git_clean PreflightError", err)
	}
	for _, sub := range c.wantErrSubs {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q lacks %q", err.Error(), sub)
		}
	}
	if status != "M README.md" {
		t.Errorf("tree was touched; status = %q, want %q", status, "M README.md")
	}
	if strings.Contains(logs, "pre-flight self-heal") || len(notes) != 0 {
		t.Errorf("heal reported although it did not run: logs=%q notes=%q", logs, notes)
	}
}

var selfHealCases = map[string]selfHealCase{
	"pushed": {
		name:       "dirty clone on a pushed pilot/ branch heals",
		branch:     "pilot/GH-5800",
		pushed:     true,
		wantHealed: true,
	},
	"default": {
		name:        "dirty clone on the default branch is not healed",
		branch:      "main",
		wantErrSubs: []string{"uncommitted change(s)", `branch "main"`, "pilot/ branch=false"},
	},
	"unpushed": {
		name:          "dirty clone with an unpushed commit is not healed",
		branch:        "pilot/GH-5801",
		pushed:        true,
		unpushedExtra: true,
		wantErrSubs:   []string{"pilot/ branch=true", "pushed to remote=false", "no other run active=true"},
	},
	"never-pushed": {
		name:        "dirty clone on a never-pushed pilot/ branch is not healed",
		branch:      "pilot/GH-5802",
		wantErrSubs: []string{"pushed to remote=false"},
	},
	"running": {
		name:        "a running row for the same project blocks the heal",
		branch:      "pilot/GH-5803",
		pushed:      true,
		ledger:      []memory.Execution{{ID: "exec-other", TaskID: "GH-9999", Status: "running"}},
		wantErrSubs: []string{"pushed to remote=true", "no other run active=false", "exec-other"},
	},
	"queued": {
		name:        "a queued row for the same project blocks the heal",
		branch:      "pilot/GH-5804",
		pushed:      true,
		ledger:      []memory.Execution{{ID: "exec-queued", TaskID: "GH-9998", Status: "queued"}},
		wantErrSubs: []string{"no other run active=false"},
	},
}

func TestPreflightSelfHeal(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end: real git/gh, skipped under -short")
	}
	for key, c := range selfHealCases {
		t.Run(key, func(t *testing.T) {
			t.Log(c.name)
			runSelfHealCase(t, c)
		})
	}
}

func TestPreflightSelfHeal_PushedPilotBranchHeals(t *testing.T) {
	runSelfHealCase(t, selfHealCases["pushed"])
}

func TestPreflightSelfHeal_DefaultBranchNotHealed(t *testing.T) {
	runSelfHealCase(t, selfHealCases["default"])
}

func TestPreflightSelfHeal_UnpushedCommitNotHealed(t *testing.T) {
	runSelfHealCase(t, selfHealCases["unpushed"])
}

func TestPreflightSelfHeal_RunningRowBlocksHeal(t *testing.T) {
	runSelfHealCase(t, selfHealCases["running"])
}

// Worktree mode skips git_clean entirely, so the heal never applies there.
func TestPreflightSelfHeal_SkippedWhenGitCleanSkipped(t *testing.T) {
	dir := newSelfHealClone(t, selfHealCases["pushed"])
	runner := NewRunnerWithBackend(&mockFixedBackend{result: &BackendResult{Success: true}})
	task := &Task{ID: "GH-5901", ProjectPath: dir}
	if err := runner.runPreflight(context.Background(), task, dir, PreflightOptions{BackendType: "mock", SkipGitClean: true}); err != nil {
		t.Fatalf("pre-flight: %v", err)
	}
	if status := strings.TrimSpace(gitOut(t, dir, "status", "--porcelain")); status != "M README.md" {
		t.Errorf("status = %q, tree must be untouched", status)
	}
}
