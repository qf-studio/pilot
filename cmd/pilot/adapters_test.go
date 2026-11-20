package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/quality"
)

// TestQualityCheckerFactory_OverrideLookupUsesProjectPath guards GH-5577: the
// per-project `quality:` override must be resolved from the task's project
// root, while the gates execute in the (different) execution path — an
// isolated worktree that matches no configured project.
func TestQualityCheckerFactory_OverrideLookupUsesProjectPath(t *testing.T) {
	projectPath := t.TempDir()
	worktreePath := t.TempDir()

	pnpmBuild := &quality.Gate{Name: "build", Type: quality.GateBuild, Command: "pnpm build", Required: true}
	cfg := &config.Config{
		Quality: &quality.Config{
			Enabled: true,
			Gates:   []*quality.Gate{{Name: "build", Type: quality.GateBuild, Command: "make build", Required: true}},
		},
		Projects: []*config.ProjectConfig{
			{
				Name:    "nextjs-project",
				Path:    projectPath,
				Quality: &quality.Config{Enabled: true, Gates: []*quality.Gate{pnpmBuild}},
			},
		},
	}

	checker := newProjectQualityCheckerFactory(cfg)("GH-1", projectPath, worktreePath)

	wrapper, ok := checker.(*qualityCheckerWrapper)
	if !ok {
		t.Fatalf("checker type = %T, want *qualityCheckerWrapper", checker)
	}
	gates := wrapper.executor.Config().Gates
	if len(gates) != 1 || gates[0].Command != "pnpm build" {
		t.Errorf("gates = %+v, want the project's single pnpm build gate (global make build must not apply)", gates)
	}
	if got := wrapper.executor.ProjectDir(); got != worktreePath {
		t.Errorf("gate working dir = %q, want the execution path %q", got, worktreePath)
	}
}

// TestQualityCheckerFactory_WorktreePathDoesNotMatchProject documents the
// pre-GH-5577 failure mode: looking up by the worktree path finds no project,
// so only the global gates remain.
func TestQualityCheckerFactory_WorktreePathDoesNotMatchProject(t *testing.T) {
	projectPath := t.TempDir()
	worktreePath := t.TempDir()

	cfg := &config.Config{
		Quality: &quality.Config{
			Enabled: true,
			Gates:   []*quality.Gate{{Name: "build", Type: quality.GateBuild, Command: "make build", Required: true}},
		},
		Projects: []*config.ProjectConfig{
			{
				Name: "nextjs-project",
				Path: projectPath,
				Quality: &quality.Config{Enabled: true, Gates: []*quality.Gate{
					{Name: "build", Type: quality.GateBuild, Command: "pnpm build", Required: true},
				}},
			},
		},
	}

	checker := newProjectQualityCheckerFactory(cfg)("GH-1", worktreePath, worktreePath)
	wrapper := checker.(*qualityCheckerWrapper)
	if got := wrapper.executor.Config().Gates[0].Command; got != "make build" {
		t.Errorf("command = %q, want global %q when the lookup path matches no project", got, "make build")
	}
}

// TestQualityCheckerFactory_BaseBranchFromProject guards GH-5926: the
// changed-package test scope is computed against the project's base branch
// (branch_from, then default_branch); an unregistered path gets none, which
// makes every gate see a full-suite scope.
func TestQualityCheckerFactory_BaseBranchFromProject(t *testing.T) {
	projectPath := t.TempDir()
	worktreePath := t.TempDir()
	cfg := &config.Config{
		Quality: &quality.Config{Enabled: true},
		Projects: []*config.ProjectConfig{
			{Name: "p", Path: projectPath, DefaultBranch: "main", BranchFrom: "develop"},
		},
	}
	factory := newProjectQualityCheckerFactory(cfg)

	tests := []struct {
		name        string
		projectPath string
		want        string
	}{
		{"branch_from wins", projectPath, "develop"},
		{"unregistered path has no base", worktreePath, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapper := factory("GH-1", tt.projectPath, worktreePath).(*qualityCheckerWrapper)
			if got := wrapper.executor.BaseBranch(); got != tt.want {
				t.Errorf("BaseBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}

// recordingQualityBuilder is the default builder of internal/quality that
// records every read of the task directory.
type recordingQualityBuilder struct {
	quality.CommandBuilder
	reads []string
}

func (b *recordingQualityBuilder) ReadFile(p string) ([]byte, error) {
	b.reads = append(b.reads, "ReadFile "+p)
	return b.CommandBuilder.ReadFile(p)
}

func (b *recordingQualityBuilder) Stat(p string) (os.FileInfo, error) {
	b.reads = append(b.reads, "Stat "+p)
	return b.CommandBuilder.Stat(p)
}

func (b *recordingQualityBuilder) ReadDir(p string) ([]os.DirEntry, error) {
	b.reads = append(b.reads, "ReadDir "+p)
	return b.CommandBuilder.ReadDir(p)
}

// TestRunAsUserGateDetectionRefusedInFactory (GH-6614, TASK-651): with the
// check of the gate builder failing (run_as_user set, the wrong builder) the
// factory reads no file of the workspace to detect the build and test command
// and its checker returns the error. A configured quality block needs no
// detection and is used as it is; with the check passing the detection runs.
func TestRunAsUserGateDetectionRefusedInFactory(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "Makefile"), []byte("build:\n\ttrue\ntest:\n\ttrue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevBuilder := quality.CurrentCommandBuilder()
	quality.SetCommandBuilder(nil)
	rec := &recordingQualityBuilder{CommandBuilder: quality.CurrentCommandBuilder()}
	quality.SetCommandBuilder(rec)
	t.Cleanup(func() { quality.SetCommandBuilder(prevBuilder) })
	refusal := errors.New("run_as_user: the quality gates have no agent command builder")
	prevCheck := gateBuilderError
	gateBuilderError = func() error { return refusal }
	t.Cleanup(func() { gateBuilderError = prevCheck })

	checker := newProjectQualityCheckerFactory(&config.Config{})("GH-1", ws, ws)
	if _, err := checker.Check(context.Background()); !errors.Is(err, refusal) {
		t.Errorf("Check = %v, want the refusal", err)
	}
	if len(rec.reads) != 0 {
		t.Errorf("the detection read the workspace: %q", rec.reads)
	}

	configured := &config.Config{Quality: &quality.Config{Enabled: true, Gates: []*quality.Gate{{Name: "build", Type: quality.GateBuild, Command: "make build"}}}}
	if _, ok := newProjectQualityCheckerFactory(configured)("GH-1", ws, ws).(*qualityCheckerWrapper); !ok {
		t.Error("a configured quality block was refused")
	}

	gateBuilderError = func() error { return nil }
	wrapper, ok := newProjectQualityCheckerFactory(&config.Config{})("GH-1", ws, ws).(*qualityCheckerWrapper)
	if !ok || len(wrapper.executor.Config().Gates) != 2 {
		t.Fatalf("with the check passing: %T, want the detected build and test gates", wrapper)
	}
	if len(rec.reads) == 0 {
		t.Error("the detection read nothing with the check passing")
	}
}
