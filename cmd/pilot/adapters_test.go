package main

import (
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
