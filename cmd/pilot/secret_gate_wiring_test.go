package main

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// GH-6284: the project settings of the config reach the push gate through the
// registry, for the project's own checkout and for a worktree of it.

func newGateWiringRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "trunk"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-b", "pilot/GH-1", filepath.Join(dir, "wt")},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func TestRegisterSecretGate_ProjectSettingsReachTheGate(t *testing.T) {
	executor.ResetSecretGateConfigForTest()
	t.Cleanup(executor.ResetSecretGateConfigForTest)
	repo := newGateWiringRepo(t)

	cfg := &config.Config{
		Security: &config.SecurityConfig{SecretGate: &config.SecretGateConfig{AllowPaths: []string{"global/**"}}},
		Projects: []*config.ProjectConfig{{
			Name:          "p",
			Path:          repo,
			DefaultBranch: "main",
			BranchFrom:    "trunk", // wins over default_branch
			Security:      &config.ProjectSecurityConfig{SecretGate: &config.SecretGateConfig{AllowPaths: []string{"fixtures/**"}}},
		}},
	}
	registerSecretGate(cfg)

	for _, dir := range []string{repo, filepath.Join(repo, "wt")} {
		got := executor.SecretGateOptionsFor(context.Background(), dir)
		if got.DefaultBranch != "trunk" {
			t.Errorf("%s: DefaultBranch = %q, want trunk (branch_from wins)", dir, got.DefaultBranch)
		}
		if want := []string{"global/**", "fixtures/**"}; !reflect.DeepEqual(got.AllowPaths, want) {
			t.Errorf("%s: AllowPaths = %v, want %v (global then project)", dir, got.AllowPaths, want)
		}
	}

	// A repository that is not configured sees only the global list.
	other := newGateWiringRepo(t)
	got := executor.SecretGateOptionsFor(context.Background(), other)
	if got.DefaultBranch != "" || !reflect.DeepEqual(got.AllowPaths, []string{"global/**"}) {
		t.Errorf("unconfigured repo got %+v, want no default branch and only the global allow_paths", got)
	}
}

func TestRegisterSecretGate_WarnsOnceForANonGitProjectPath(t *testing.T) {
	executor.ResetSecretGateConfigForTest()
	t.Cleanup(executor.ResetSecretGateConfigForTest)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	notRepo := t.TempDir()
	registerSecretGate(&config.Config{Projects: []*config.ProjectConfig{
		{Name: "plain-dir", Path: notRepo, DefaultBranch: "trunk"},
		{Name: "repo", Path: newGateWiringRepo(t), DefaultBranch: "trunk"},
	}})

	if n := strings.Count(buf.String(), "not a git repository"); n != 1 {
		t.Fatalf("got %d warnings, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "plain-dir") || !strings.Contains(buf.String(), notRepo) {
		t.Errorf("warning does not name the project and its path:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "name=repo ") || strings.Contains(buf.String(), "project=repo") {
		t.Errorf("a real repo was warned about:\n%s", buf.String())
	}
}
