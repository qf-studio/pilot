package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// fakeGhArgvLog puts a gh on PATH that appends its argv to a file, one line per
// call, and prints an issue URL.
func fakeGhArgvLog(t *testing.T) string {
	t.Helper()
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logFile + "\"\ncase \"$*\" in\n  *\"issue create\"*) echo https://github.com/o/r/issues/12 ;;\n  *\"issue view\"*) echo OPEN ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return logFile
}

// GH-6272: the gh CLI paths that post issue text mask the title and body.
func TestSecretGate_GhCLIIssueTextRedacted(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	const secret = "hunter2-not-a-known-shape"
	secretpatterns.RegisterSecretValues(secret)
	dirty := "see " + secret

	tests := []struct {
		name string
		call func(t *testing.T, r *Runner) error
	}{
		{"CreateSubIssues title and body", func(t *testing.T, r *Runner) error {
			plan := &EpicPlan{
				ParentTask: &Task{ID: "GH-100", Title: "feat(epic): parent", ProjectPath: t.TempDir()},
				Subtasks:   []PlannedSubtask{{Title: "feat(epic): " + dirty, Description: dirty, Order: 1}},
			}
			_, err := r.CreateSubIssues(context.Background(), plan, "")
			return err
		}},
		{"UpdateIssueProgress", func(t *testing.T, r *Runner) error {
			return r.UpdateIssueProgress(context.Background(), "", "100", dirty)
		}},
		{"CloseIssueWithComment", func(t *testing.T, r *Runner) error {
			return r.CloseIssueWithComment(context.Background(), "", "100", dirty)
		}},
		{"ghIssueComment", func(t *testing.T, r *Runner) error {
			return ghIssueComment(context.Background(), "", "100", dirty)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := fakeGhArgvLog(t)
			r := newSilentRunnerTask359()
			if err := tt.call(t, r); err != nil {
				t.Fatalf("call: %v", err)
			}
			b, err := os.ReadFile(logFile)
			if err != nil || len(b) == 0 {
				t.Fatalf("gh was not called: %v", err)
			}
			got := string(b)
			if strings.Contains(got, secret) {
				t.Errorf("gh argv carries the value:\n%s", got)
			}
			if !strings.Contains(got, "<redacted>") {
				t.Errorf("gh argv was not masked:\n%s", got)
			}
		})
	}
}

// The project's gate settings follow the repository into its worktrees: the
// worktree's push uses the configured default branch and allow_paths.
func TestSecretGate_ProjectSettingsReachWorktrees(t *testing.T) {
	f := newSecretGateFixture(t)
	t.Cleanup(ResetSecretGateConfigForTest)
	tok := "1234567890" + ":AA" + strings.Repeat("c", 33)
	if err := os.MkdirAll(filepath.Join(f.local, "fixtures", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.commit("fixtures/deep/token.txt", "bot "+tok+"\n", "test: fixture")

	wt := filepath.Join(t.TempDir(), "wt")
	f.run("worktree", "add", "--detach", wt, "pilot/GH-9")

	if err := CheckSecretGate(context.Background(), wt, "HEAD", "GH-9"); !IsPushRefused(err) {
		t.Fatalf("without allow_paths: got %v, want a refusal", err)
	}
	SetSecretGateProject(f.local, SecretGateProject{AllowPaths: []string{"fixtures/**"}})
	if err := CheckSecretGate(context.Background(), wt, "HEAD", "GH-9"); err != nil {
		t.Fatalf("project allow_paths, checked from a worktree: %v", err)
	}
	ResetSecretGateConfigForTest()
	SetSecretGateAllowPaths([]string{"fixtures/**"})
	if err := CheckSecretGate(context.Background(), wt, "HEAD", "GH-9"); err != nil {
		t.Fatalf("global allow_paths: %v", err)
	}
}
